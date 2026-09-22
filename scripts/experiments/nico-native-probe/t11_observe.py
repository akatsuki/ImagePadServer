"""Collect process, CPU20-job, GPU/VRAM, and VRChat observations for T11.

The observer is deliberately diagnostic.  It never treats an unavailable
counter as zero and it never turns VRChat metrics into an acceptance PASS:
frame-time telemetry is not available from the generic Windows process APIs,
so the VRChat gate remains explicitly pending.
"""

from __future__ import annotations

import csv
import json
import math
import os
import shutil
import subprocess
import threading
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from io import StringIO
from pathlib import Path
from typing import Any, Callable, Iterable


@dataclass(frozen=True)
class ProcessInfo:
    pid: int
    parent_pid: int
    name: str
    cpu_seconds: float | None
    working_set_bytes: int | None
    handles: int | None


ProcessProvider = Callable[[], tuple[dict[int, ProcessInfo], str | None]]
GpuProvider = Callable[[], tuple[list[dict[str, float]], str | None]]
_PROCESS_SNAPSHOT_BACKEND = "unknown"


def _number(value: Any) -> float | None:
    if value is None or isinstance(value, bool):
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _int(value: Any) -> int | None:
    number = _number(value)
    return int(number) if number is not None else None


def parse_vrchat_performance_stats(text: str) -> list[dict[str, float]]:
    """Extract VRChat ``[Performance Stats]`` frame metrics from a log.

    VRChat writes the stats object as JSON after a human-readable marker.  The
    object may be one line or pretty-printed, so parse JSON objects directly
    instead of relying on line boundaries.  In the log format,
    ``cpu_frame_time`` and ``gpu_frame_time`` are seconds per frame despite
    their historical ``f/s`` label; retain the raw FPS and expose milliseconds
    only as a derived diagnostic value.
    """
    decoder = json.JSONDecoder()
    rows: list[dict[str, float]] = []
    offset = 0
    while True:
        start = text.find('{"runningTime"', offset)
        if start < 0:
            break
        try:
            payload, consumed = decoder.raw_decode(text[start:])
        except json.JSONDecodeError:
            offset = start + 1
            continue
        offset = start + consumed
        if not isinstance(payload, dict) or not isinstance(payload.get("stats"), list):
            continue
        values: dict[str, dict[str, float]] = {}
        for item in payload["stats"]:
            if not isinstance(item, dict) or not isinstance(item.get("name"), str):
                continue
            mean = _number(item.get("mean"))
            if mean is None:
                continue
            values[item["name"]] = {
                "mean": mean,
                "tw_mean": _number(item.get("tw-mean")) or 0.0,
            }
        fps = values.get("fps", {}).get("mean")
        running_time = _number(payload.get("runningTime"))
        if fps is None or fps <= 0 or running_time is None:
            continue
        row: dict[str, float] = {
            "running_time_s": running_time,
            "fps_mean": fps,
            "frame_time_ms": 1000.0 / fps,
        }
        fps_tw_mean = values.get("fps", {}).get("tw_mean")
        if fps_tw_mean is not None and fps_tw_mean > 0:
            row["fps_tw_mean"] = fps_tw_mean
        cpu_frame_time = values.get("cpu_frame_time", {}).get("mean")
        if cpu_frame_time is not None:
            row["cpu_frame_time_ms"] = cpu_frame_time * 1000.0
        gpu_frame_time = values.get("gpu_frame_time", {}).get("mean")
        if gpu_frame_time is not None:
            row["gpu_frame_time_ms"] = gpu_frame_time * 1000.0
        rows.append(row)
    return rows


def _parse_presentmon_timestamp(value: Any) -> datetime | None:
    if value is None:
        return None
    raw = str(value).strip()
    if not raw:
        return None
    normalized = raw.replace(" ", "T", 1)
    if normalized.endswith("Z"):
        normalized = normalized[:-1] + "+00:00"
    try:
        parsed = datetime.fromisoformat(normalized)
    except ValueError:
        # PresentMon's --date_time output can contain a non-zero-padded date,
        # hour, and nanosecond fraction.  datetime accepts microseconds only.
        date_part, separator, time_part = raw.partition(" ")
        if not separator:
            return None
        time_head, dot, fraction = time_part.partition(".")
        if not dot:
            fraction = "0"
        fraction = (fraction + "000000")[:6]
        try:
            parsed = datetime.strptime(
                f"{date_part} {time_head}.{fraction}", "%Y-%m-%d %H:%M:%S.%f"
            )
        except ValueError:
            return None
    return parsed if parsed.tzinfo is not None else parsed.replace(tzinfo=timezone.utc)


def _presentmon_bool(value: Any) -> bool:
    return str(value).strip().lower() in {"1", "true", "yes", "y"}


def parse_presentmon_csv(text: str, *, process_id: int | None = None) -> list[dict[str, Any]]:
    """Parse the target-process rows emitted by PresentMon's CSV writer.

    The parser intentionally keeps only diagnostic fields needed for a frame
    time gate.  It does not treat a missing metric as zero and it does not
    interpret a dropped-present row as a complete rendered-frame count.
    """
    rows: list[dict[str, Any]] = []
    for raw in csv.DictReader(StringIO(text)):
        row_pid = _int(raw.get("ProcessID"))
        if process_id is not None and row_pid != process_id:
            continue
        timestamp = _parse_presentmon_timestamp(raw.get("TimeInSeconds"))
        rows.append(
            {
                "process_id": row_pid,
                "time": timestamp,
                "time_text": str(raw.get("TimeInSeconds") or ""),
                "dropped": _presentmon_bool(raw.get("Dropped")),
                "ms_between_presents": _number(raw.get("msBetweenPresents")),
                "ms_gpu_active": _number(raw.get("msGPUActive")),
            }
        )
    return rows


def _presentmon_percentile(values: list[float], quantile: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    # Use nearest-rank so a small sample cannot hide the upper-tail value.
    index = max(0, math.ceil(quantile * len(ordered)) - 1)
    return ordered[index]


def summarize_presentmon_rows(
    rows: list[dict[str, Any]],
    *,
    start_time: str | None = None,
    end_time: str | None = None,
) -> dict[str, Any]:
    """Summarize PresentMon rows, optionally limited to an export interval."""
    start = _parse_presentmon_timestamp(start_time) if start_time else None
    end = _parse_presentmon_timestamp(end_time) if end_time else None
    selected = [
        row
        for row in rows
        if (start is None or row.get("time") is not None and row["time"] >= start)
        and (end is None or row.get("time") is not None and row["time"] <= end)
    ]
    between = [
        float(row["ms_between_presents"])
        for row in selected
        if row.get("ms_between_presents") is not None and row["ms_between_presents"] > 0
    ]
    gpu_active = [
        float(row["ms_gpu_active"])
        for row in selected
        if row.get("ms_gpu_active") is not None and row["ms_gpu_active"] >= 0
    ]
    timestamps = sorted(row["time"] for row in selected if row.get("time") is not None)
    dropped = sum(1 for row in selected if row.get("dropped"))

    def metric(values: list[float]) -> dict[str, float | None] | None:
        if not values:
            return None
        return {
            "mean": sum(values) / len(values),
            "p50": _presentmon_percentile(values, 0.50),
            "p95": _presentmon_percentile(values, 0.95),
            # p99 of frame interval is the frame-time representation of the
            # 1% low gate defined by the T11 plan.
            "p99": _presentmon_percentile(values, 0.99),
            "min": min(values),
            "max": max(values),
        }

    return {
        "source": "presentmon_csv",
        "sample_count": len(selected),
        "valid_ms_between_presents": len(between),
        "dropped_present_rows": dropped,
        "dropped_present_ratio": dropped / len(selected) if selected else None,
        "ms_between_presents": metric(between),
        "ms_gpu_active": metric(gpu_active),
        "capture_first_time": timestamps[0].isoformat() if timestamps else None,
        "capture_last_time": timestamps[-1].isoformat() if timestamps else None,
    }


def _decode_process_rows(decoded: Any) -> dict[int, ProcessInfo]:
    rows = decoded if isinstance(decoded, list) else [decoded]
    result: dict[int, ProcessInfo] = {}
    for row in rows:
        if not isinstance(row, dict):
            continue
        pid = _int(row.get("ProcessId"))
        if pid is None or pid <= 0:
            continue
        cpu_seconds = _number(row.get("CpuSeconds"))
        if cpu_seconds is None:
            user_ns = _number(row.get("UserModeTime"))
            kernel_ns = _number(row.get("KernelModeTime"))
            if user_ns is not None and kernel_ns is not None:
                cpu_seconds = (user_ns + kernel_ns) / 10_000_000
        result[pid] = ProcessInfo(
            pid=pid,
            parent_pid=_int(row.get("ParentProcessId")) or 0,
            name=str(row.get("Name") or ""),
            cpu_seconds=cpu_seconds,
            working_set_bytes=_int(row.get("WorkingSetSize")),
            handles=_int(row.get("HandleCount")),
        )
    return result


def _run_powershell_json(command: str) -> tuple[Any, str | None]:
    try:
        completed = subprocess.run(
            ["powershell", "-NoProfile", "-NonInteractive", "-Command", command],
            capture_output=True,
            timeout=1.5,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return None, str(exc)
    if completed.returncode != 0:
        detail = completed.stderr.decode("utf-8", errors="replace").strip()
        return None, f"exit={completed.returncode}:{detail[:256]}"
    try:
        return json.loads(completed.stdout.decode("utf-8", errors="replace")), None
    except json.JSONDecodeError as exc:
        return None, f"json={exc}"


def windows_process_snapshot() -> tuple[dict[int, ProcessInfo], str | None]:
    """Read a point-in-time process table without requiring psutil.

    WMI's cumulative CPU times are useful for an audit trail, while the parent
    relationship lets us distinguish the budget runner's descendants from
    unrelated applications such as VRChat.
    """

    global _PROCESS_SNAPSHOT_BACKEND
    if os.name != "nt":
        _PROCESS_SNAPSHOT_BACKEND = "unavailable"
        return {}, "windows_process_snapshot_unavailable_on_non_windows"
    command = (
        "$ErrorActionPreference='Stop'; "
        "Get-CimInstance Win32_Process | "
        "Select-Object ProcessId,ParentProcessId,Name,UserModeTime,KernelModeTime,WorkingSetSize,HandleCount | "
        "ConvertTo-Json -Compress"
    )
    if _PROCESS_SNAPSHOT_BACKEND == "get-process-fallback":
        decoded, fallback_error = _run_powershell_json(_fallback_process_command())
        fallback = _decode_process_rows(decoded) if fallback_error is None else {}
        if fallback:
            return fallback, None
        return {}, f"process_snapshot_fallback={fallback_error}"

    decoded, primary_error = _run_powershell_json(command)
    if primary_error is None:
        _PROCESS_SNAPSHOT_BACKEND = "wmi"
        return _decode_process_rows(decoded), None

    # Some locked-down Windows installations deny Win32_Process/WMI while
    # still allowing ordinary process handles.  The fallback cannot recover
    # parent PIDs, so it is intentionally diagnostic and does not overclaim a
    # complete child-tree observation.
    fallback_decoded, fallback_error = _run_powershell_json(_fallback_process_command())
    fallback = _decode_process_rows(fallback_decoded) if fallback_error is None else {}
    if fallback:
        _PROCESS_SNAPSHOT_BACKEND = "get-process-fallback"
        return fallback, None
    return {}, f"process_snapshot_wmi={primary_error};fallback={fallback_error}"


def _fallback_process_command() -> str:
    return (
        "$ErrorActionPreference='SilentlyContinue'; "
        "Get-Process | ForEach-Object { "
        "$cpu=$null; try {$cpu=$_.TotalProcessorTime.TotalSeconds} catch {}; "
        "[pscustomobject]@{ProcessId=$_.Id;ParentProcessId=0;Name=$_.ProcessName;CpuSeconds=$cpu;WorkingSetSize=$_.WorkingSet64;HandleCount=$_.HandleCount} "
        "} | ConvertTo-Json -Compress"
    )


def nvidia_smi_snapshot() -> tuple[list[dict[str, float]], str | None]:
    executable = shutil.which("nvidia-smi")
    if not executable:
        return [], "nvidia_smi_unavailable"
    query = "index,utilization.gpu,utilization.encoder,memory.used,memory.total"
    try:
        completed = subprocess.run(
            [executable, f"--query-gpu={query}", "--format=csv,noheader,nounits"],
            capture_output=True,
            timeout=1.5,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return [], f"nvidia_smi_error={exc}"
    if completed.returncode != 0:
        detail = completed.stderr.decode("utf-8", errors="replace").strip()
        return [], f"nvidia_smi_exit={completed.returncode}:{detail[:256]}"
    rows: list[dict[str, float]] = []
    for values in csv.reader(completed.stdout.decode("utf-8", errors="replace").splitlines()):
        if len(values) < 5:
            continue
        parsed = [_number(value.strip()) for value in values[:5]]
        if any(value is None for value in parsed):
            continue
        rows.append(
            {
                "index": float(parsed[0]),
                "utilization_gpu_percent": float(parsed[1]),
                "utilization_encoder_percent": float(parsed[2]),
                "memory_used_mib": float(parsed[3]),
                "memory_total_mib": float(parsed[4]),
            }
        )
    return rows, None if rows else "nvidia_smi_no_parseable_rows"


def _descendants(processes: dict[int, ProcessInfo], root_pid: int) -> set[int]:
    found = {root_pid}
    changed = True
    while changed:
        changed = False
        for info in processes.values():
            if info.parent_pid in found and info.pid not in found:
                found.add(info.pid)
                changed = True
    return found


def _is_vrchat(name: str) -> bool:
    normalized = name.lower().replace(".exe", "")
    return normalized in {"vrchat", "vrchatunity"} or normalized.startswith("vrchat-")


def _aggregate_processes(processes: Iterable[ProcessInfo]) -> dict[str, Any]:
    values = list(processes)
    cpu = [item.cpu_seconds for item in values if item.cpu_seconds is not None]
    working_set = [item.working_set_bytes for item in values if item.working_set_bytes is not None]
    handles = [item.handles for item in values if item.handles is not None]
    return {
        "count": len(values),
        "pids": sorted(item.pid for item in values),
        "names": sorted({item.name for item in values if item.name}),
        "cpu_seconds": sum(cpu) if cpu else None,
        "working_set_bytes": sum(working_set) if working_set else None,
        "handles": sum(handles) if handles else None,
    }


def _process_details(processes: dict[int, ProcessInfo], pids: Iterable[int]) -> list[dict[str, Any]]:
    details: list[dict[str, Any]] = []
    for pid in sorted(pids):
        info = processes.get(pid)
        if info is None:
            continue
        details.append(
            {
                "pid": info.pid,
                "parent_pid": info.parent_pid,
                "name": info.name,
                "cpu_seconds": info.cpu_seconds,
                "working_set_bytes": info.working_set_bytes,
                "handles": info.handles,
            }
        )
    return details


def _gpu_summary(samples: list[list[dict[str, float]]], errors: list[str]) -> dict[str, Any]:
    flat = [row for sample in samples for row in sample]
    def values(key: str) -> list[float]:
        return [row[key] for row in flat if key in row]

    utilization = values("utilization_gpu_percent")
    encoder = values("utilization_encoder_percent")
    memory = values("memory_used_mib")
    return {
        "available": bool(flat),
        "sample_count": len(samples),
        "gpu_utilization_percent": {"avg": sum(utilization) / len(utilization), "max": max(utilization)} if utilization else None,
        "encoder_utilization_percent": {"avg": sum(encoder) / len(encoder), "max": max(encoder)} if encoder else None,
        "memory_used_mib": {"avg": sum(memory) / len(memory), "max": max(memory)} if memory else None,
        "unavailable_reasons": sorted(set(errors)),
    }


def summarize_observation(
    samples: list[dict[str, Any]],
    *,
    observed_pids: set[int],
    final_processes: dict[int, ProcessInfo],
    gpu_samples: list[list[dict[str, float]]],
    gpu_errors: list[str],
    timed_out: bool,
    exit_code: int | None,
    logical_cpus: int | None,
    vrchat_stats: list[dict[str, float]] | None = None,
) -> dict[str, Any]:
    per_pid_cpu: dict[int, float] = {}
    for sample in samples:
        for item in sample.get("processes", []):
            if not isinstance(item, dict):
                continue
            pid = _int(item.get("pid"))
            cpu = _number(item.get("cpu_seconds"))
            if pid is not None and cpu is not None:
                per_pid_cpu[pid] = max(per_pid_cpu.get(pid, 0.0), cpu)
    aggregate_cpu = [
        _number(sample.get("processes", {}).get("cpu_seconds"))
        for sample in samples
        if isinstance(sample.get("processes"), dict)
    ]
    aggregate_cpu = [value for value in aggregate_cpu if value is not None]
    observed_cpu = [*per_pid_cpu.values(), *aggregate_cpu]
    residue = sorted(pid for pid in observed_pids if pid in final_processes)
    residue_details = [_aggregate_processes(final_processes[pid] for pid in residue)] if residue else []
    vrchat_samples = []
    for sample in samples:
        vrchat = sample.get("vrchat")
        if isinstance(vrchat, dict) and vrchat.get("count", 0):
            vrchat_samples.append(vrchat)
    vrchat_pids = sorted({pid for sample in vrchat_samples for pid in sample.get("pids", [])})
    vrchat_stats = vrchat_stats or []
    vrchat_pending_reasons = [] if vrchat_stats else ["frame_time_unavailable"]
    if not vrchat_samples:
        vrchat_pending_reasons.append("vrchat_process_not_detected")
    latest_stats = vrchat_stats[-1] if vrchat_stats else {}
    frame_times = [row["frame_time_ms"] for row in vrchat_stats if "frame_time_ms" in row]
    return {
        "schema_version": 1,
        "sample_count": len(samples),
        "timed_out": timed_out,
        "exit_code": exit_code,
        "logical_cpus": logical_cpus,
        "observed_pids": sorted(observed_pids),
        "cpu_seconds_observed": max(observed_cpu) if observed_cpu else None,
        "peak_process_count": max((sample.get("processes", {}).get("count", 0) if isinstance(sample.get("processes"), dict) else 0 for sample in samples), default=0),
        "peak_working_set_bytes": max((sample.get("processes", {}).get("working_set_bytes") or 0 if isinstance(sample.get("processes"), dict) else 0 for sample in samples), default=0) or None,
        "peak_handles": max((sample.get("processes", {}).get("handles") or 0 if isinstance(sample.get("processes"), dict) else 0 for sample in samples), default=0) or None,
        "child_residue": {"pids": residue, "details": residue_details, "clear": not residue},
        "gpu": _gpu_summary(gpu_samples, gpu_errors),
        "vrchat": {
            "present": bool(vrchat_samples),
            "pids": vrchat_pids,
            "sample_count": len(vrchat_samples),
            "frame_time_ms": latest_stats.get("frame_time_ms"),
            "frame_time_ms_range": {
                "min": min(frame_times),
                "max": max(frame_times),
            } if frame_times else None,
            "performance_stats_count": len(vrchat_stats),
            "performance_stats_source": "vrchat_performance_log" if vrchat_stats else None,
            "acceptance_status": "measured" if vrchat_stats else "pending",
            "pending_reasons": vrchat_pending_reasons,
        },
        "vrc_acceptance_pending": True,
        "acceptance_note": (
            "VRChat performance-log telemetry was measured, but no acceptance threshold/pair policy is defined; gate remains pending."
            if vrchat_stats
            else "Generic process/GPU counters do not provide VRChat frame-time telemetry; real VRChat gate remains pending."
        ),
    }


def run_observed_command(
    command: list[str],
    *,
    cwd: Path,
    env: dict[str, str],
    timeout: float,
    observation_json: Path | None = None,
    observation_jsonl: Path | None = None,
    vrchat_log_path: Path | None = None,
    sample_interval: float = 1.0,
    process_provider: ProcessProvider = windows_process_snapshot,
    gpu_provider: GpuProvider = nvidia_smi_snapshot,
) -> dict[str, Any]:
    """Run one child, draining output while sampling it and its descendants."""

    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=str(cwd),
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    stdout_data = bytearray()
    stderr_data = bytearray()
    vrchat_log_initial_size: int | None = None
    if vrchat_log_path is not None:
        try:
            vrchat_log_initial_size = vrchat_log_path.stat().st_size
        except OSError:
            vrchat_log_initial_size = None

    def drain(stream: Any, target: bytearray) -> None:
        try:
            while True:
                chunk = stream.read(64 * 1024)
                if not chunk:
                    return
                target.extend(chunk)
        finally:
            stream.close()

    stdout_thread = threading.Thread(target=drain, args=(process.stdout, stdout_data), daemon=True)
    stderr_thread = threading.Thread(target=drain, args=(process.stderr, stderr_data), daemon=True)
    stdout_thread.start()
    stderr_thread.start()
    samples: list[dict[str, Any]] = []
    gpu_samples: list[list[dict[str, float]]] = []
    gpu_errors: list[str] = []
    observed_pids: set[int] = {int(process.pid)}
    first_processes, process_error = process_provider()
    logical_cpus = os.cpu_count()
    next_sample = 0.0
    sample_index = 0
    timed_out = False
    jsonl_stream = None
    if observation_jsonl is not None:
        observation_jsonl.parent.mkdir(parents=True, exist_ok=True)
        jsonl_stream = observation_jsonl.open("w", encoding="utf-8")
    try:
        while True:
            now = time.monotonic()
            if now - started >= timeout:
                timed_out = True
                process.kill()
                break
            if now - started >= next_sample:
                if sample_index == 0:
                    processes, error = first_processes, process_error
                else:
                    processes, error = process_provider()
                process_error = process_error or error
                tree_pids = _descendants(processes, int(process.pid))
                observed_pids.update(tree_pids)
                tree = _aggregate_processes(processes[pid] for pid in tree_pids if pid in processes)
                vrchat = _aggregate_processes(info for info in processes.values() if _is_vrchat(info.name))
                row = {
                    "at_s": now - started,
                    "processes": tree,
                    "process_details": _process_details(processes, tree_pids),
                    "vrchat": vrchat,
                }
                if process_error:
                    row["process_snapshot_error"] = process_error
                samples.append(row)
                if jsonl_stream is not None:
                    jsonl_stream.write(json.dumps(row, ensure_ascii=False) + "\n")
                    jsonl_stream.flush()
                gpu, gpu_error = gpu_provider()
                gpu_samples.append(gpu)
                if gpu_error:
                    gpu_errors.append(gpu_error)
                next_sample += max(0.1, sample_interval)
                sample_index += 1
            exit_code = process.poll()
            if exit_code is not None:
                break
            time.sleep(min(0.1, max(0.01, sample_interval / 4)))
    finally:
        if jsonl_stream is not None:
            jsonl_stream.close()
    exit_code = process.wait()
    stdout_thread.join(timeout=5)
    stderr_thread.join(timeout=5)
    final_processes, final_error = process_provider()
    if final_error:
        process_error = process_error or final_error
    vrchat_stats: list[dict[str, float]] = []
    vrchat_log_error: str | None = None
    vrchat_log_rotated = False
    vrchat_log_bytes_read = 0
    if vrchat_log_path is not None:
        try:
            raw_log = vrchat_log_path.read_bytes()
            if vrchat_log_initial_size is not None and len(raw_log) >= vrchat_log_initial_size:
                raw_log = raw_log[vrchat_log_initial_size:]
            else:
                vrchat_log_rotated = True
            vrchat_log_bytes_read = len(raw_log)
            vrchat_stats = parse_vrchat_performance_stats(raw_log.decode("utf-8", errors="replace"))
        except OSError as exc:
            vrchat_log_error = str(exc)
    summary = summarize_observation(
        samples,
        observed_pids=observed_pids,
        final_processes=final_processes,
        gpu_samples=gpu_samples,
        gpu_errors=gpu_errors,
        timed_out=timed_out,
        exit_code=exit_code,
        logical_cpus=logical_cpus,
        vrchat_stats=vrchat_stats,
    )
    if vrchat_log_path is not None:
        summary["vrchat_log"] = {
            "path": str(vrchat_log_path),
            "initial_size_bytes": vrchat_log_initial_size,
            "bytes_read": vrchat_log_bytes_read,
            "rotated": vrchat_log_rotated,
            "stats_count": len(vrchat_stats),
            "read_error": vrchat_log_error,
        }
    if process_error:
        summary["process_snapshot_error"] = process_error
    if process_provider is windows_process_snapshot:
        summary["process_snapshot_backend"] = _PROCESS_SNAPSHOT_BACKEND
        if _PROCESS_SNAPSHOT_BACKEND != "wmi":
            summary["child_residue"]["tree_complete"] = False
            summary["child_residue"]["clear"] = None
            summary["child_residue"]["status"] = "incomplete"
            summary["child_residue"]["note"] = "Get-Process fallback has no parent PID; runner_report PIDs remain authoritative for CPU20 job membership."
        else:
            summary["child_residue"]["tree_complete"] = True
    result = {
        "stdout": bytes(stdout_data),
        "stderr": bytes(stderr_data),
        "exit_code": exit_code,
        "timed_out": timed_out,
        "observation": summary,
    }
    if observation_json is not None:
        observation_json.parent.mkdir(parents=True, exist_ok=True)
        observation_json.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return result

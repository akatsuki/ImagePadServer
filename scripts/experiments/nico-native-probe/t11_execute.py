"""Execute persisted T11 production cases without overwriting recorded cases."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from t11_matrix import _persist_case_result, _read_json, _write_json
from t11_observe import run_observed_command


DURATION_MS = {"fixed-6s": 6000, "high-density-10s": 10000, "real-source-155s": 154955}
EXPECTED_FRAMES = {"fixed-6s": 180, "high-density-10s": 300, "real-source-155s": 4649}


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def backend_for_case(case: dict[str, Any], backend_a: str, backend_b: str) -> str:
    backend = backend_a if str(case.get("variant")) == "A" else backend_b
    if backend not in {"auto", "browser", "native"}:
        raise ValueError(f"unsupported backend: {backend!r}")
    return backend


def snapshot_for_case(case: dict[str, Any], default: Path, high_density: Path | None) -> Path:
    """Use the synthetic high-density snapshot only for its declared scenario."""
    if str(case.get("scenario")) == "high-density-10s" and high_density is not None:
        return high_density
    return default


def input_missing_reason(snapshot: Path, source: Path | None) -> str | None:
    if not snapshot.is_file():
        return "snapshot_missing"
    if source is None or not source.is_file():
        return "source_missing"
    return None


def _sha256(path: Path) -> str | None:
    if not path.is_file():
        return None
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _tail(data: bytes | None, limit: int = 64 * 1024) -> str:
    return (data or b"")[-limit:].decode("utf-8", errors="replace")


def classify_failure(stderr: bytes | str, invalid_reason: str | None = None) -> dict[str, Any] | None:
    """Classify persisted failure evidence without turning it into acceptance."""
    text = stderr.decode("utf-8", errors="replace") if isinstance(stderr, bytes) else str(stderr)
    lower = text.lower()
    if "gpu process isn't usable" in lower or "gpu process exited unexpectedly" in lower:
        return {
            "class": "browser_gpu_process_unusable",
            "phase": "browser_startup",
            "signals": ["gpu_process_exit", "gpu_process_unusable"],
        }
    if "cdp method=page.enable" in lower and "forcibly closed" in lower:
        return {
            "class": "cdp_page_enable_disconnect",
            "phase": "cdp_page_enable",
            "signals": ["page_enable_disconnect"],
        }
    if "cdp method=runtime.evaluate" in lower and ("timeout" in lower or "timed out" in lower):
        return {
            "class": "cdp_runtime_evaluate_timeout",
            "phase": "cdp_runtime_evaluate",
            "signals": ["runtime_evaluate_timeout"],
        }
    if "wsarecv" in lower and "forcibly closed" in lower:
        return {
            "class": "browser_websocket_forcibly_closed",
            "phase": "browser_transport",
            "signals": ["wsarecv", "connection_forcibly_closed"],
        }
    if "native runtime failed; regenerating with browser" in lower:
        return {
            "class": "native_compositor_runtime_failure",
            "phase": "native_render_encode",
            "signals": ["native_runtime_failure", "browser_fallback"],
        }
    if "truncated scene" in lower:
        return {
            "class": "native_scene_truncated",
            "phase": "native_scene_input",
            "signals": ["truncated_scene"],
        }
    if invalid_reason == "timeout":
        return {"class": "case_timeout", "phase": "case_timeout", "signals": ["timeout"]}
    return None


def build_case_environment(
    case: dict[str, Any],
    case_dir: Path,
    *,
    snapshot: Path,
    source: Path,
    backend_a: str,
    backend_b: str,
    gpu_mode: str = "disabled",
    capture_mode: str = "auto",
    compositor: Path | None = None,
    browser: Path | None = None,
    allow_native_fallback: bool = False,
) -> dict[str, str]:
    backend = backend_for_case(case, backend_a, backend_b)
    env = os.environ.copy()
    env.update(
        {
            "IMAGEPAD_NICO_PRODUCTION_TEST": "1",
            "IMAGEPAD_NICO_PRODUCTION_BACKEND": backend,
            "IMAGEPAD_NICO_PERF_SNAPSHOT": str(snapshot.resolve()),
            "IMAGEPAD_NICO_PERF_SOURCE": str(source.resolve()),
            "IMAGEPAD_NICO_PERF_FFMPEG": env.get("IMAGEPAD_NICO_PERF_FFMPEG", ""),
            "IMAGEPAD_NICO_PERF_DURATION_MS": str(DURATION_MS[str(case["scenario"])]),
            "IMAGEPAD_NICO_PRODUCTION_ARTIFACTS": str(case_dir.resolve()),
            "IMAGEPAD_NICONICO_RENDER_GPU": gpu_mode,
            "IMAGEPAD_NICONICO_RENDER_CAPTURE": capture_mode,
        }
    )
    if compositor is not None:
        env["IMAGEPAD_NICO_COMPOSITOR"] = str(compositor.resolve())
    if browser is not None:
        env["IMAGEPAD_NICONICO_RENDER_BROWSER"] = str(browser.resolve())
    env.pop("IMAGEPAD_NICO_ALLOW_NATIVE_FALLBACK", None)
    if allow_native_fallback:
        env["IMAGEPAD_NICO_ALLOW_NATIVE_FALLBACK"] = "1"
    return env


def build_runner_command(runner: Path, test_executable: Path, record: Path, root: Path) -> list[str]:
    return [
        str(runner),
        "-cpu-percent",
        "20",
        "-sample-ms",
        "250",
        "-record",
        str(record),
        "-dir",
        str(root.resolve()),
        "--",
        str(test_executable.resolve()),
        "-test.run=^TestNicoProductionPipeline$",
        "-test.count=1",
        "-test.v",
    ]


def _artifact(case_dir: Path, scenario: str) -> dict[str, Any] | None:
    output = case_dir / "out.mp4"
    playlist = case_dir / "hls" / "playlist.m3u8"
    if not output.is_file() or not playlist.is_file():
        return None
    value: dict[str, Any] = {
        "mp4": str(output.resolve()),
        "playlist": str(playlist.resolve()),
        "bytes": output.stat().st_size,
        "sha256": _sha256(output),
    }
    try:
        from validate import validate_artifacts

        value["validation"] = validate_artifacts(
            output,
            playlist=playlist,
            expected_frame_rate="30/1",
            expected_frames=EXPECTED_FRAMES[scenario],
        )
    except Exception as exc:  # validation is evidence; preserve its failure in the case
        value["validation_error"] = str(exc)
    result_path = case_dir / "result.json"
    if result_path.is_file():
        try:
            value["production_result"] = _read_json(result_path)
        except json.JSONDecodeError as exc:
            value["production_result_error"] = str(exc)
    return value


def _persist_existing_case_result(run_dir: Path, result: dict[str, Any]) -> None:
    case_id = str(result["case_id"])
    case_dir = Path(run_dir) / "cases" / case_id
    _write_json(case_dir / "result.json", result)
    manifest_path = Path(run_dir) / "manifest.json"
    manifest = _read_json(manifest_path)
    manifest.setdefault("cases", {})[case_id] = result
    _write_json(manifest_path, manifest)


def run_case(
    run_dir: Path,
    case: dict[str, Any],
    *,
    root: Path,
    runner: Path,
    test_executable: Path,
    snapshot: Path,
    source: Path,
    ffmpeg: Path,
    backend_a: str,
    backend_b: str,
    gpu_mode: str,
    capture_mode: str,
    compositor: Path | None = None,
    browser: Path | None = None,
    allow_native_fallback: bool = False,
    vrchat_log: Path | None = None,
) -> dict[str, Any]:
    case_id = str(case["case_id"])
    case_dir = Path(run_dir) / "cases" / case_id
    case_dir.mkdir(parents=True, exist_ok=False)
    record = case_dir / "runner-report.json"
    env = build_case_environment(
        case,
        case_dir,
        snapshot=snapshot,
        source=source,
        backend_a=backend_a,
        backend_b=backend_b,
        gpu_mode=gpu_mode,
        capture_mode=capture_mode,
        compositor=compositor,
        browser=browser,
        allow_native_fallback=allow_native_fallback,
    )
    env["IMAGEPAD_NICO_PERF_FFMPEG"] = str(ffmpeg.resolve())
    command = build_runner_command(runner, test_executable, record, root)
    started = time.monotonic()
    started_at = utc_now()
    exit_code: int | None = None
    invalid_reason: str | None = None
    stdout = b""
    stderr = b""
    observed: dict[str, Any] | None = None
    try:
        observed = run_observed_command(
            command,
            cwd=root,
            env=env,
            timeout=float(case["timeout_s"]),
            observation_json=case_dir / "observation.json",
            observation_jsonl=case_dir / "observation.jsonl",
            vrchat_log_path=vrchat_log,
            sample_interval=1.0,
        )
        stdout, stderr, exit_code = observed["stdout"], observed["stderr"], observed["exit_code"]
        status = "complete" if exit_code == 0 else "failed"
        if observed.get("timed_out"):
            invalid_reason = "timeout"
        elif exit_code != 0:
            invalid_reason = f"exit_code={exit_code}"
    except OSError as exc:
        stdout, stderr, exit_code = b"", str(exc).encode("utf-8"), None
        status, invalid_reason = "failed", f"spawn_error={exc}"

    runner_report = None
    if record.is_file():
        try:
            runner_report = _read_json(record)
        except json.JSONDecodeError as exc:
            invalid_reason = invalid_reason or f"invalid_runner_report={exc}"
            status = "failed"
    if status == "complete" and (not runner_report or not runner_report.get("report", {}).get("verified")):
        status = "failed"
        invalid_reason = "cpu_budget_unverified"
    artifact = _artifact(case_dir, str(case["scenario"])) if status == "complete" else None
    if status == "complete" and (not artifact or artifact.get("validation_error")):
        status = "failed"
        invalid_reason = "artifact_validation_failed"
    result: dict[str, Any] = {
        **case,
        "status": status,
        "invalid_reason": invalid_reason,
        "started_at": started_at,
        "wall_s": time.monotonic() - started,
        "exit_code": exit_code,
        "command": command,
        "variant_backend": backend_for_case(case, backend_a, backend_b),
        "inputs": {
            "snapshot": str(snapshot.resolve()),
            "source": str(source.resolve()),
            "duration_ms": DURATION_MS[str(case["scenario"])],
            "synthetic_snapshot": str(snapshot.name).endswith("snapshot-high-density.json"),
            "snapshot_sha256": _sha256(snapshot),
            "source_sha256": _sha256(source),
            "compositor": str(compositor.resolve()) if compositor is not None else None,
            "compositor_sha256": _sha256(compositor) if compositor is not None else None,
            "browser": str(browser.resolve()) if browser is not None else None,
            "browser_sha256": _sha256(browser) if browser is not None else None,
            "allow_native_fallback": allow_native_fallback,
            "vrchat_log": str(vrchat_log.resolve()) if vrchat_log is not None else None,
        },
        "tool_hashes": {
            "runner_sha256": _sha256(runner),
            "test_executable_sha256": _sha256(test_executable),
            "ffmpeg_sha256": _sha256(ffmpeg),
        },
        "stdout_tail": _tail(stdout),
        "stderr_tail": _tail(stderr),
    }
    if failure := classify_failure(stdout + b"\n" + stderr, invalid_reason):
        result["failure_classification"] = failure
    if runner_report is not None:
        result["runner_report"] = runner_report
    if observed is not None:
        result["observation"] = observed["observation"]
    if artifact is not None:
        result["artifact"] = artifact
    _persist_existing_case_result(run_dir, result)
    return result


def run_manifest(args: argparse.Namespace) -> list[dict[str, Any]]:
    run_dir = args.run_dir.resolve()
    manifest_path = run_dir / "manifest.json"
    manifest = _read_json(manifest_path)
    recorded = manifest.setdefault("cases", {})
    results: list[dict[str, Any]] = []
    for case in manifest["matrix"]:
        case_id = str(case["case_id"])
        if case_id in recorded:
            continue
        if args.scenario and str(case.get("scenario")) != args.scenario:
            continue
        if args.limit is not None and len(results) >= args.limit:
            break
        source = {
            "fixed-6s": args.source_fixed,
            "high-density-10s": args.source_high_density,
            "real-source-155s": args.source_real,
        }[str(case["scenario"])]
        snapshot = snapshot_for_case(case, args.snapshot, args.snapshot_high_density)
        missing_reason = input_missing_reason(snapshot, source)
        if missing_reason is not None:
            result = {**case, "status": "invalid", "invalid_reason": missing_reason, "command": []}
            _persist_case_result(run_dir, result)
        else:
            result = run_case(
                run_dir,
                case,
                root=args.root,
                runner=args.runner,
                test_executable=args.test_executable,
                snapshot=snapshot,
                source=source,
                ffmpeg=args.ffmpeg,
                backend_a=args.backend_a,
                backend_b=args.backend_b,
                gpu_mode=args.gpu_mode,
                capture_mode=args.capture_mode,
                compositor=args.compositor,
                browser=args.browser,
                allow_native_fallback=args.allow_native_fallback,
                vrchat_log=args.vrchat_log,
            )
        results.append(result)
        manifest = _read_json(manifest_path)
        recorded = manifest.setdefault("cases", {})
    manifest = _read_json(manifest_path)
    statuses = [manifest.get("cases", {}).get(str(case["case_id"]), {}).get("status") for case in manifest["matrix"]]
    manifest["status"] = "complete" if statuses and all(status == "complete" for status in statuses) else ("failed" if any(status in {"failed", "invalid"} for status in statuses) else "pending")
    manifest["finished_at"] = utc_now() if manifest["status"] != "pending" else None
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return results


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", type=Path, required=True)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[3])
    parser.add_argument("--runner", type=Path, required=True)
    parser.add_argument("--test-executable", type=Path, required=True)
    parser.add_argument("--snapshot", type=Path, required=True)
    parser.add_argument("--snapshot-high-density", type=Path, help="snapshot used only for the high-density-10s scenario")
    parser.add_argument("--source-fixed", type=Path, required=True)
    parser.add_argument("--source-high-density", type=Path)
    parser.add_argument("--source-real", type=Path, required=True)
    parser.add_argument("--ffmpeg", type=Path, required=True)
    parser.add_argument("--backend-a", choices=("auto", "browser", "native"), default="browser")
    parser.add_argument("--backend-b", choices=("auto", "browser", "native"), default="native")
    parser.add_argument("--gpu-mode", default="disabled")
    parser.add_argument("--capture-mode", choices=("auto", "2d", "none"), default="auto")
    parser.add_argument("--compositor", type=Path, help="diagnostic explicit native compositor; never changes the embedded default")
    parser.add_argument("--browser", type=Path, help="diagnostic explicit browser executable; inherited default is unchanged")
    parser.add_argument("--allow-native-fallback", action="store_true", help="diagnostic only: accept auto native runtime failure followed by browser regeneration")
    parser.add_argument("--vrchat-log", type=Path, help="optional VRChat output log used only for diagnostic performance stats")
    parser.add_argument("--scenario", choices=("fixed-6s", "high-density-10s", "real-source-155s"))
    parser.add_argument("--limit", type=int)
    args = parser.parse_args(argv)
    results = run_manifest(args)
    print(json.dumps({"run_dir": str(args.run_dir), "executed": len(results), "statuses": [r.get("status") for r in results]}, ensure_ascii=False))
    return 0 if all(result.get("status") == "complete" for result in results) else 1


if __name__ == "__main__":
    raise SystemExit(main())

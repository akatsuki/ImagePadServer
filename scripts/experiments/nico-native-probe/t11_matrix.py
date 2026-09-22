"""Reproducible T11 acceptance order and run-manifest writer."""

from __future__ import annotations

import argparse
import json
import subprocess
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


SCENARIOS = {
    "fixed-6s": {"duration_s": 6, "fixture": "snapshot-fixed", "long": False},
    "high-density-10s": {"duration_s": 10, "fixture": "snapshot-high-density", "long": False},
    "real-source-155s": {"duration_s": 155, "fixture": "snapshot-real-source", "long": True},
}
SHORT_TIMEOUT_S = 10 * 60
LONG_TIMEOUT_CAP_S = 30 * 60
SCHEMA_VERSION = 1
QUALITY_MATRIX = [
    "no-comments",
    "alpha",
    "multicolor-overlap",
    "clip",
    "negative-rect",
    "aspect-4x3",
    "aspect-1x1",
    "aspect-portrait",
    "fps-30",
    "fps-29.97",
    "fps-59.94",
    "vfr",
    "audio-none",
    "audio-delay",
    "short-tail",
]


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def timeout_seconds(scenario: str, initial_baseline_seconds: float | None) -> int:
    if scenario not in SCENARIOS:
        raise ValueError(f"unknown scenario: {scenario}")
    if not SCENARIOS[scenario]["long"]:
        return SHORT_TIMEOUT_S
    if initial_baseline_seconds is None:
        return LONG_TIMEOUT_CAP_S
    if initial_baseline_seconds <= 0:
        raise ValueError("initial baseline must be positive")
    return min(LONG_TIMEOUT_CAP_S, max(SHORT_TIMEOUT_S, int(initial_baseline_seconds * 3)))


def build_matrix(initial_baseline_seconds: float | None = None) -> list[dict[str, Any]]:
    cases: list[dict[str, Any]] = []
    for scenario, definition in SCENARIOS.items():
        cases.append(
            {
                "case_id": f"{scenario}-warmup-A",
                "scenario": scenario,
                "phase": "warmup",
                "repeat": 0,
                "variant": "A",
                "duration_s": definition["duration_s"],
                "fixture": definition["fixture"],
                "timeout_s": timeout_seconds(scenario, initial_baseline_seconds),
            }
        )
        for repeat in range(1, 6):
            order = ("A", "B") if repeat % 2 else ("B", "A")
            for position, variant in enumerate(order, start=1):
                cases.append(
                    {
                        "case_id": f"{scenario}-repeat-{repeat:02d}-{position}-{variant}",
                        "scenario": scenario,
                        "phase": "measured",
                        "repeat": repeat,
                        "position": position,
                        "variant": variant,
                        "duration_s": definition["duration_s"],
                        "fixture": definition["fixture"],
                        "timeout_s": timeout_seconds(scenario, initial_baseline_seconds),
                    }
                )
    return cases


def matrix_case_ids(matrix: list[dict[str, Any]]) -> list[str]:
    ids = [str(case["case_id"]) for case in matrix]
    if len(ids) != len(set(ids)):
        raise ValueError("matrix contains duplicate case ids")
    return ids


def _write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    data = (json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode("utf-8")
    with tempfile.NamedTemporaryFile("wb", dir=path.parent, prefix=f".{path.name}.", delete=False) as stream:
        temporary = Path(stream.name)
        stream.write(data)
        stream.flush()
    try:
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def create_matrix_run(run_root: Path, run_id: str, matrix: list[dict[str, Any]]) -> Path:
    if not run_id or run_id in {".", ".."} or "/" in run_id or "\\" in run_id:
        raise ValueError(f"invalid run id: {run_id!r}")
    matrix_case_ids(matrix)
    run_dir = Path(run_root) / run_id
    run_dir.mkdir(parents=True, exist_ok=False)
    _write_json(
        run_dir / "manifest.json",
        {
            "schema_version": SCHEMA_VERSION,
            "run_id": run_id,
            "status": "pending",
            "started_at": utc_now(),
            "finished_at": None,
            "timeout_is_failure": True,
            "matrix": matrix,
            "quality_matrix": QUALITY_MATRIX,
            "cases": {},
        },
    )
    return run_dir


def _read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def _tail_text(data: bytes | None, limit: int = 64 * 1024) -> str:
    if not data:
        return ""
    return data[-limit:].decode("utf-8", errors="replace")


def _persist_case_result(run_dir: Path, result: dict[str, Any]) -> None:
    case_id = str(result["case_id"])
    case_dir = Path(run_dir) / "cases" / case_id
    case_dir.mkdir(parents=True, exist_ok=False)
    _write_json(case_dir / "result.json", result)
    manifest_path = Path(run_dir) / "manifest.json"
    manifest = _read_json(manifest_path)
    manifest.setdefault("cases", {})[case_id] = result
    _write_json(manifest_path, manifest)


def run_command_case(
    run_dir: Path,
    case: dict[str, Any],
    command: list[str],
    *,
    timeout_override: float | None = None,
    cwd: Path | None = None,
) -> dict[str, Any]:
    """Run one finite case and persist timeout/exit evidence immediately."""
    run_dir = Path(run_dir)
    case_id = str(case["case_id"])
    timeout = float(timeout_override if timeout_override is not None else case["timeout_s"])
    started = time.monotonic()
    stdout = b""
    stderr = b""
    exit_code: int | None = None
    invalid_reason: str | None = None
    try:
        completed = subprocess.run(
            [str(item) for item in command],
            cwd=str(cwd or run_dir),
            capture_output=True,
            timeout=timeout,
            check=False,
        )
        stdout, stderr, exit_code = completed.stdout, completed.stderr, completed.returncode
        status = "complete" if exit_code == 0 else "failed"
        if exit_code != 0:
            invalid_reason = f"exit_code={exit_code}"
    except subprocess.TimeoutExpired as exc:
        stdout = exc.stdout or b""
        stderr = exc.stderr or b""
        status = "failed"
        invalid_reason = "timeout"
    result = {
        **case,
        "status": status,
        "invalid_reason": invalid_reason,
        "started_at_monotonic": started,
        "wall_s": time.monotonic() - started,
        "timeout_s": timeout,
        "exit_code": exit_code,
        "command": [str(item) for item in command],
        "stdout_tail": _tail_text(stdout),
        "stderr_tail": _tail_text(stderr),
    }
    _persist_case_result(run_dir, result)
    return result


def run_matrix(
    run_dir: Path,
    commands: dict[str, list[str]],
    *,
    cwd: Path | None = None,
) -> list[dict[str, Any]]:
    """Execute unrecorded cases in manifest order without overwriting cases."""
    run_dir = Path(run_dir)
    manifest = _read_json(run_dir / "manifest.json")
    recorded = manifest.setdefault("cases", {})
    results: list[dict[str, Any]] = []
    for case in manifest["matrix"]:
        case_id = str(case["case_id"])
        if case_id in recorded:
            continue
        command = commands.get(str(case["variant"]))
        if not command:
            result = {
                **case,
                "status": "invalid",
                "invalid_reason": "missing_command",
                "started_at_monotonic": time.monotonic(),
                "wall_s": 0.0,
                "timeout_s": case["timeout_s"],
                "exit_code": None,
                "command": [],
                "stdout_tail": "",
                "stderr_tail": "",
            }
            _persist_case_result(run_dir, result)
            results.append(result)
            continue
        results.append(run_command_case(run_dir, case, command, cwd=cwd))
        manifest = _read_json(run_dir / "manifest.json")
        recorded = manifest.setdefault("cases", {})
    manifest = _read_json(run_dir / "manifest.json")
    statuses = [manifest.get("cases", {}).get(str(case["case_id"]), {}).get("status") for case in manifest["matrix"]]
    manifest["status"] = "complete" if statuses and all(status == "complete" for status in statuses) else "failed"
    manifest["finished_at"] = utc_now()
    _write_json(run_dir / "manifest.json", manifest)
    return results


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-root", type=Path, default=Path("build/nico-cpu20"))
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--initial-baseline-seconds", type=float)
    args = parser.parse_args(argv)
    matrix = build_matrix(args.initial_baseline_seconds)
    run_dir = create_matrix_run(args.run_root, args.run_id, matrix)
    print(json.dumps({"run_dir": str(run_dir), "cases": len(matrix)}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Run native stability under the CPU20 Job with T11 process observation."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
from pathlib import Path

from t11_observe import run_observed_command


ROOT = Path(__file__).resolve().parents[3]


def _write_json(path: Path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def execute(args):
    run_dir = args.run_dir.resolve()
    run_dir.mkdir(parents=True, exist_ok=False)
    runner_report = run_dir / "runner-report.json"
    native_report = run_dir / "native-stability.json"
    observation = run_dir / "observation.json"
    observation_jsonl = run_dir / "observation.jsonl"
    stability_script = Path(__file__).with_name("native_stability.py").resolve()
    command = [
        str(args.runner.resolve()),
        "-cpu-percent",
        "20",
        "-sample-ms",
        str(args.sample_ms),
        "-record",
        str(runner_report),
        "--",
        str(args.python.resolve()),
        str(stability_script),
        "--compositor",
        str(args.compositor.resolve()),
        "--fixture",
        str(args.fixture.resolve()),
        "--duration-s",
        str(args.duration_s),
        "--min-iterations",
        str(args.min_iterations),
        "--per-run-timeout-s",
        str(args.per_run_timeout_s),
        "--output",
        str(native_report),
    ]
    observed = run_observed_command(
        command,
        cwd=ROOT,
        env=os.environ.copy(),
        timeout=max(60.0, args.duration_s + 120.0),
        observation_json=observation,
        observation_jsonl=observation_jsonl,
        vrchat_log_path=args.vrchat_log,
        sample_interval=max(0.25, args.sample_ms / 1000.0),
    )
    runner = None
    native = None
    if runner_report.is_file():
        runner = json.loads(runner_report.read_text(encoding="utf-8"))
    if native_report.is_file():
        native = json.loads(native_report.read_text(encoding="utf-8"))
    result = {
        "schema_version": 1,
        "command": command,
        "exit_code": observed["exit_code"],
        "timed_out": observed["timed_out"],
        "runner_verified": bool((runner or {}).get("report", {}).get("verified")),
        "native": native,
        "runner_report": runner,
        "observation": json.loads(observation.read_text(encoding="utf-8"))
        if observation.is_file()
        else None,
        "acceptance_complete": False,
        "acceptance_note": "native-layer stability only; browser/worker/VRChat acceptance remains pending",
    }
    _write_json(run_dir / "stability-report.json", result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", required=True, type=Path)
    parser.add_argument("--runner", required=True, type=Path)
    parser.add_argument("--python", required=True, type=Path)
    parser.add_argument("--compositor", required=True, type=Path)
    parser.add_argument("--fixture", required=True, type=Path)
    parser.add_argument("--duration-s", type=float, default=1800.0)
    parser.add_argument("--min-iterations", type=int, default=1)
    parser.add_argument("--per-run-timeout-s", type=float, default=120.0)
    parser.add_argument("--sample-ms", type=int, default=1000)
    parser.add_argument("--vrchat-log", type=Path)
    args = parser.parse_args()
    result = execute(args)
    print(json.dumps({"run_dir": str(args.run_dir), "exit_code": result["exit_code"]}, ensure_ascii=False))
    native_complete = bool(result.get("native") and result["native"].get("complete"))
    return 0 if result["exit_code"] == 0 and result["runner_verified"] and native_complete else 1


if __name__ == "__main__":
    raise SystemExit(main())

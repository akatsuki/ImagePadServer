"""Run worker stability under one CPU20 Job with T11 observation evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
from pathlib import Path

from t11_observe import run_observed_command


ROOT = Path(__file__).resolve().parents[3]
CPU_BUDGET_PERCENT = 20


def _observer_timeout_s(args: argparse.Namespace) -> float:
    """Allow finite min-iteration runs to finish before observer timeout."""
    finite_iterations_timeout = float(args.min_iterations) * float(args.per_run_timeout_s) + 60.0
    return max(60.0, float(args.duration_s) + 180.0, finite_iterations_timeout)


def _write_json(path: Path, value) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def _sha256(path: Path) -> str | None:
    if not path.is_file():
        return None
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def execute(args: argparse.Namespace) -> dict:
    run_dir = args.run_dir.resolve()
    run_dir.mkdir(parents=True, exist_ok=False)
    runner_report = run_dir / "runner-report.json"
    worker_dir = run_dir / "worker-loop"
    worker_report = worker_dir / "worker-stability.json"
    observation = run_dir / "observation.json"
    observation_jsonl = run_dir / "observation.jsonl"
    stability_script = Path(__file__).with_name("worker_stability.py").resolve()
    command = [
        str(args.runner.resolve()),
        "-cpu-percent",
        str(CPU_BUDGET_PERCENT),
        "-sample-ms",
        str(args.sample_ms),
        "-record",
        str(runner_report),
        "--",
        str(args.python.resolve()),
        str(stability_script),
        "--run-dir",
        str(worker_dir),
        "--root",
        str(ROOT),
        "--worker",
        str(args.worker.resolve()),
        "--ffmpeg",
        str(args.ffmpeg.resolve()),
        "--source",
        str(args.source.resolve()),
        "--snapshot",
        str(args.snapshot.resolve()),
        "--backend",
        args.backend,
        "--duration-s",
        str(args.duration_s),
        "--min-iterations",
        str(args.min_iterations),
        "--per-run-timeout-s",
        str(args.per_run_timeout_s),
    ]
    if args.browser:
        command += ["--browser", str(args.browser.resolve())]
    if args.compositor:
        command += ["--compositor", str(args.compositor.resolve())]
    if args.output_mode:
        command += ["--output-mode", args.output_mode]
    command += [
        "--width", str(args.width),
        "--height", str(args.height),
        "--duration-ms", str(args.duration_ms),
        "--fps-num", str(args.fps_num),
        "--fps-den", str(args.fps_den),
        "--crf", str(args.crf),
        "--audio-bitrate", args.audio_bitrate,
    ]
    observed = run_observed_command(
        command,
        cwd=ROOT,
        env=os.environ.copy(),
        timeout=_observer_timeout_s(args),
        observation_json=observation,
        observation_jsonl=observation_jsonl,
        vrchat_log_path=args.vrchat_log,
        sample_interval=max(0.25, args.sample_ms / 1000.0),
    )
    runner = json.loads(runner_report.read_text(encoding="utf-8")) if runner_report.is_file() else None
    worker = json.loads(worker_report.read_text(encoding="utf-8")) if worker_report.is_file() else None
    result = {
        "schema_version": 1,
        "cpu_budget_percent": CPU_BUDGET_PERCENT,
        "backend": args.backend,
        "output_mode": args.output_mode or "separate",
        "parameters": {
            "width": args.width,
            "height": args.height,
            "duration_ms": args.duration_ms,
            "fps_num": args.fps_num,
            "fps_den": args.fps_den,
            "crf": args.crf,
            "audio_bitrate": args.audio_bitrate,
        },
        "inputs": {
            "worker": str(args.worker.resolve()),
            "worker_sha256": _sha256(args.worker),
            "ffmpeg": str(args.ffmpeg.resolve()),
            "ffmpeg_sha256": _sha256(args.ffmpeg),
            "source": str(args.source.resolve()),
            "source_sha256": _sha256(args.source),
            "snapshot": str(args.snapshot.resolve()),
            "snapshot_sha256": _sha256(args.snapshot),
            "browser": str(args.browser.resolve()) if args.browser else None,
            "browser_sha256": _sha256(args.browser) if args.browser else None,
            "compositor": str(args.compositor.resolve()) if args.compositor else None,
            "compositor_sha256": _sha256(args.compositor) if args.compositor else None,
        },
        "command": command,
        "exit_code": observed["exit_code"],
        "timed_out": observed["timed_out"],
        "runner_verified": bool((runner or {}).get("report", {}).get("verified")),
        "worker": worker,
        "runner_report": runner,
        "observation": json.loads(observation.read_text(encoding="utf-8")) if observation.is_file() else None,
        "acceptance_complete": False,
        "acceptance_note": "worker stability evidence only; VRChat frame-time and public HTTP acceptance remain pending",
    }
    _write_json(run_dir / "stability-report.json", result)
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", required=True, type=Path)
    parser.add_argument("--runner", required=True, type=Path)
    parser.add_argument("--python", required=True, type=Path)
    parser.add_argument("--worker", required=True, type=Path)
    parser.add_argument("--ffmpeg", required=True, type=Path)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--snapshot", required=True, type=Path)
    parser.add_argument("--backend", choices=("native", "browser"), required=True)
    parser.add_argument("--browser", type=Path)
    parser.add_argument("--compositor", type=Path)
    parser.add_argument("--output-mode", choices=("separate", "tee"), default="")
    parser.add_argument("--duration-s", type=float, default=1800.0)
    parser.add_argument("--min-iterations", type=int, default=1)
    parser.add_argument("--per-run-timeout-s", type=float, default=120.0)
    parser.add_argument("--sample-ms", type=int, default=1000)
    parser.add_argument("--vrchat-log", type=Path)
    parser.add_argument("--width", type=int, default=1920)
    parser.add_argument("--height", type=int, default=1080)
    parser.add_argument("--duration-ms", type=int, default=6000)
    parser.add_argument("--fps-num", type=int, default=30)
    parser.add_argument("--fps-den", type=int, default=1)
    parser.add_argument("--crf", type=int, default=26)
    parser.add_argument("--audio-bitrate", default="160k")
    args = parser.parse_args()
    result = execute(args)
    print(json.dumps({"run_dir": str(args.run_dir), "exit_code": result["exit_code"]}, ensure_ascii=False))
    return 0 if result["exit_code"] == 0 and result["runner_verified"] and bool((result.get("worker") or {}).get("complete")) else 1


if __name__ == "__main__":
    raise SystemExit(main())

"""Repeat the native compositor without retaining raw output frames."""

from __future__ import annotations

import argparse
import json
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def summarize_runs(*, started_at, finished_at, duration_s, min_iterations, runs):
    failed_iterations = [
        int(run["iteration"])
        for run in runs
        if run.get("exit_code") != 0 or run.get("timed_out")
    ]
    reasons = []
    if len(runs) < min_iterations:
        reasons.append("minimum_iterations")
    if failed_iterations:
        reasons.append("child_failed")
        if any(run.get("timed_out") for run in runs if run.get("iteration") in failed_iterations):
            reasons.append("child_timeout")
    return {
        "schema_version": 1,
        "started_at": started_at,
        "finished_at": finished_at,
        "duration_s": float(duration_s),
        "min_iterations": int(min_iterations),
        "iterations": len(runs),
        "failed_iterations": failed_iterations,
        "incomplete_reasons": reasons,
        "complete": not reasons,
        "runs": runs,
    }


def run_stability(*, compositor, fixture, duration_s, min_iterations=1, per_run_timeout_s=120.0):
    compositor = Path(compositor).resolve()
    fixture = Path(fixture).resolve()
    if not compositor.is_file():
        raise FileNotFoundError(compositor)
    if not fixture.is_file():
        raise FileNotFoundError(fixture)
    if duration_s < 0 or min_iterations < 1:
        raise ValueError("duration_s must be non-negative and min_iterations must be positive")

    started_at = utc_now()
    started = time.monotonic()
    deadline = started + float(duration_s)
    runs = []
    iteration = 0
    while time.monotonic() < deadline or len(runs) < min_iterations:
        iteration += 1
        run_started = time.monotonic()
        timed_out = False
        exit_code = None
        stderr_tail = ""
        with fixture.open("rb") as input_stream:
            process = subprocess.Popen(
                [str(compositor), "--stdin"],
                stdin=input_stream,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
            )
            try:
                _, stderr = process.communicate(timeout=per_run_timeout_s)
                exit_code = process.returncode
                stderr_tail = stderr.decode("utf-8", errors="replace")[-4096:]
            except subprocess.TimeoutExpired as exc:
                timed_out = True
                process.kill()
                _, stderr = process.communicate()
                stderr_tail = ((exc.stderr or b"") + (stderr or b"")).decode(
                    "utf-8", errors="replace"
                )[-4096:]
        run = {
            "iteration": iteration,
            "elapsed_s": time.monotonic() - run_started,
            "exit_code": exit_code,
            "timed_out": timed_out,
            "stderr_tail": stderr_tail,
        }
        runs.append(run)
        if timed_out or exit_code != 0:
            break
    return summarize_runs(
        started_at=started_at,
        finished_at=utc_now(),
        duration_s=time.monotonic() - started,
        min_iterations=min_iterations,
        runs=runs,
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--compositor", required=True, type=Path)
    parser.add_argument("--fixture", required=True, type=Path)
    parser.add_argument("--duration-s", required=True, type=float)
    parser.add_argument("--min-iterations", type=int, default=1)
    parser.add_argument("--per-run-timeout-s", type=float, default=120.0)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    report = run_stability(
        compositor=args.compositor,
        fixture=args.fixture,
        duration_s=args.duration_s,
        min_iterations=args.min_iterations,
        per_run_timeout_s=args.per_run_timeout_s,
    )
    text = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(text, encoding="utf-8")
    print(text, end="")
    return 0 if report["complete"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

"""Repeat the finite Nico worker under one CPU20 Job without retaining all media."""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def _tail(data: bytes, limit: int = 8192) -> str:
    return data[-limit:].decode("utf-8", errors="replace")


def summarize_runs(*, started_at: str, finished_at: str, duration_s: float, min_iterations: int, runs: list[dict[str, Any]]) -> dict[str, Any]:
    failed_iterations = [
        int(run["iteration"])
        for run in runs
        if run.get("exit_code") != 0 or run.get("timed_out")
    ]
    reasons: list[str] = []
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


def _result_event(stdout: bytes) -> dict[str, Any] | None:
    result: dict[str, Any] | None = None
    for line in stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(event, dict) and event.get("type") == "result":
            result = event
    return result


def _request(args: argparse.Namespace, iteration: int, output: Path, hls: Path) -> dict[str, Any]:
    request: dict[str, Any] = {
        "version": 1,
        "run_id": f"worker-stability-{iteration:04d}",
        "media_id": f"nico-stability-{iteration:04d}",
        "source_path": str(args.source.resolve()),
        "snapshot_path": str(args.snapshot.resolve()),
        "output_path": str(output.resolve()),
        "hls_staging_dir": str(hls.resolve()),
        "ffmpeg": str(args.ffmpeg.resolve()),
        "backend": args.backend,
        "width": args.width,
        "height": args.height,
        "duration_ms": args.duration_ms,
        "fps_num": args.fps_num,
        "fps_den": args.fps_den,
        "crf": args.crf,
        "audio_bitrate": args.audio_bitrate,
    }
    output_mode = str(getattr(args, "output_mode", "")).strip()
    if output_mode:
        request["output_mode"] = output_mode
    if args.browser:
        request["browser_path"] = str(args.browser.resolve())
    if args.compositor:
        request["compositor"] = str(args.compositor.resolve())
    for key in ("filter_threads", "decoder_threads", "encoder_threads"):
        value = int(getattr(args, key, 0))
        if value > 0:
            request[key] = value
    return request


def run_worker_once(args: argparse.Namespace, iteration: int, case_dir: Path) -> dict[str, Any]:
    case_dir.mkdir(parents=True, exist_ok=True)
    output = case_dir / "out.mp4"
    hls = case_dir / "hls-staging"
    request = _request(args, iteration, output, hls)
    started = time.monotonic()
    timed_out = False
    exit_code: int | None = None
    stdout = b""
    stderr = b""
    try:
        completed = subprocess.run(
            [str(args.worker.resolve()), "nico-export-worker"],
            input=(json.dumps(request, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8"),
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=args.per_run_timeout_s,
            cwd=str(args.root.resolve()),
            check=False,
        )
        stdout, stderr, exit_code = completed.stdout, completed.stderr, completed.returncode
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        stdout = exc.stdout or b""
        stderr = exc.stderr or b""
    event = _result_event(stdout)
    playlist = Path(str(event.get("playlist"))) if event and event.get("playlist") else hls / "playlist.m3u8"
    result_ok = bool(
        event
        and event.get("ok")
        and event.get("run_id") == request["run_id"]
        and event.get("media_id") == request["media_id"]
        and output.is_file()
        and playlist.is_file()
    )
    result: dict[str, Any] = {
        "iteration": iteration,
        "elapsed_s": time.monotonic() - started,
        "exit_code": exit_code,
        "timed_out": timed_out,
        "result_ok": result_ok,
        "output_bytes": output.stat().st_size if output.is_file() else 0,
        "playlist": str(playlist),
        "playlist_present": playlist.is_file(),
        "stderr_tail": _tail(stderr),
    }
    _write_json(case_dir / "worker-result.json", result)
    return result


def run_stability(args: argparse.Namespace) -> dict[str, Any]:
    for path in (args.worker, args.ffmpeg, args.source, args.snapshot):
        if not path.is_file():
            raise FileNotFoundError(path)
    if args.backend == "browser" and (args.browser is None or not args.browser.is_file()):
        raise FileNotFoundError("browser path is required for browser stability")
    if args.backend == "native" and (args.compositor is None or not args.compositor.is_file()):
        raise FileNotFoundError("compositor path is required for native stability")
    run_dir = args.run_dir.resolve()
    run_dir.mkdir(parents=True, exist_ok=False)
    started_at = utc_now()
    started = time.monotonic()
    deadline = started + args.duration_s
    runs: list[dict[str, Any]] = []
    previous_case: Path | None = None
    iteration = 0
    while time.monotonic() < deadline or len(runs) < args.min_iterations:
        iteration += 1
        case_dir = run_dir / f"worker-{iteration:04d}"
        result = run_worker_once(args, iteration, case_dir)
        runs.append(result)
        if result["exit_code"] != 0 or result["timed_out"] or not result["result_ok"]:
            break
        if previous_case is not None:
            shutil.rmtree(previous_case, ignore_errors=True)
        previous_case = case_dir
    report = summarize_runs(
        started_at=started_at,
        finished_at=utc_now(),
        duration_s=time.monotonic() - started,
        min_iterations=args.min_iterations,
        runs=runs,
    )
    _write_json(run_dir / "worker-stability.json", report)
    return report


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", required=True, type=Path)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[3])
    parser.add_argument("--worker", required=True, type=Path)
    parser.add_argument("--ffmpeg", required=True, type=Path)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--snapshot", required=True, type=Path)
    parser.add_argument("--backend", choices=("native", "browser"), required=True)
    parser.add_argument("--browser", type=Path)
    parser.add_argument("--compositor", type=Path)
    parser.add_argument("--duration-s", type=float, default=1800.0)
    parser.add_argument("--min-iterations", type=int, default=1)
    parser.add_argument("--per-run-timeout-s", type=float, default=120.0)
    parser.add_argument("--width", type=int, default=1920)
    parser.add_argument("--height", type=int, default=1080)
    parser.add_argument("--duration-ms", type=int, default=6000)
    parser.add_argument("--fps-num", type=int, default=30)
    parser.add_argument("--fps-den", type=int, default=1)
    parser.add_argument("--crf", type=int, default=26)
    parser.add_argument("--audio-bitrate", default="160k")
    parser.add_argument("--output-mode", choices=("separate", "tee"), default="")
    parser.add_argument("--filter-threads", type=int, default=0)
    parser.add_argument("--decoder-threads", type=int, default=0)
    parser.add_argument("--encoder-threads", type=int, default=0)
    args = parser.parse_args()
    report = run_stability(args)
    print(json.dumps({"run_dir": str(args.run_dir), "iterations": report["iterations"], "complete": report["complete"]}, ensure_ascii=False))
    return 0 if report["complete"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

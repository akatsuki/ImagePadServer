"""Write an auditable baseline for NicoNico performance experiments."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


DEFAULT_TRACKED_PATHS = (
    "internal/video/niconico_encode.go",
    "internal/video/niconico_pipeline.go",
    "internal/video/niconico_native.go",
    "internal/video/niconico_timing.go",
    "internal/nicoexportworker/protocol.go",
    "internal/nicoexportworker/worker.go",
)


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _relative_path(root: Path, path: Path) -> str:
    try:
        return str(path.resolve().relative_to(root.resolve()))
    except ValueError:
        return str(path.resolve())


def file_identity(root: Path, path: Path | None) -> dict[str, Any]:
    if path is None:
        return {"path": None, "exists": False, "bytes": None, "sha256": None}
    path = Path(path)
    if not path.is_file():
        return {"path": _relative_path(root, path), "exists": False, "bytes": None, "sha256": None}
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return {
        "path": _relative_path(root, path),
        "exists": True,
        "bytes": path.stat().st_size,
        "sha256": digest.hexdigest(),
    }


def _git(root: Path, *args: str) -> str | None:
    try:
        result = subprocess.run(
            ["git", *args], cwd=root, capture_output=True, text=True, check=False
        )
    except OSError:
        return None
    if result.returncode != 0:
        return None
    return result.stdout.strip()


def _summary_valid(path: Path | None) -> bool:
    if path is None or not path.is_file():
        return False
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return False
    return bool(value.get("valid", True)) if isinstance(value, dict) else False


def build_manifest(
    root: Path,
    *,
    ffmpeg: Path | None = None,
    ffprobe: Path | None = None,
    source: Path | None = None,
    snapshot: Path | None = None,
    worker: Path | None = None,
    run_summary: Path | None = None,
    tracked_paths: tuple[str, ...] = DEFAULT_TRACKED_PATHS,
    effective_profile: dict[str, Any] | None = None,
) -> dict[str, Any]:
    root = Path(root).resolve()
    artifacts = {
        "ffmpeg": file_identity(root, ffmpeg),
        "ffprobe": file_identity(root, ffprobe),
        "source": file_identity(root, source),
        "snapshot": file_identity(root, snapshot),
        "worker": file_identity(root, worker),
        "run_summary": file_identity(root, run_summary),
    }
    invalid_reasons = []
    for name in ("ffmpeg", "source", "worker"):
        if not artifacts[name]["exists"]:
            invalid_reasons.append(f"{name}_missing")
    if not _summary_valid(run_summary):
        invalid_reasons.append("run_summary_missing_or_invalid")

    return {
        "schema_version": 1,
        "created_at": utc_now(),
        "scope": "NicoNico export baseline; current shared dirty checkout included",
        "root": str(root),
        "git": {
            "head": _git(root, "rev-parse", "HEAD"),
            "describe": _git(root, "describe", "--always", "--dirty"),
            "branch": _git(root, "branch", "--show-current"),
            "tracked_dirty": _git(root, "status", "--short", "--untracked-files=no"),
        },
        "host": {
            "platform": platform.platform(),
            "logical_cpus": os.cpu_count(),
            "python": platform.python_version(),
            "cpu_budget_percent": 20,
            "vrchat_required_for_acceptance": True,
        },
        "effective_profile": effective_profile
        or {
            "width": 1920,
            "height": 1080,
            "duration_ms": 6000,
            "fps_num": 30,
            "fps_den": 1,
            "encoder": "nvenc",
            "preset": "p4",
            "quality": "CQ29/HQ",
            "b_frames": 3,
            "audio_bitrate": "160k",
        },
        "stage_definition": [
            {"name": "frame_source_next", "meaning": "RGBAFrameSource.Next cumulative time"},
            {"name": "frame_pipe_write_blocking", "meaning": "stdin.Write cumulative blocking time"},
            {"name": "frame_pipe_write", "meaning": "legacy aggregate source plus pipe loop and close"},
            {"name": "ffmpeg_wait", "meaning": "FFmpeg drain and mux completion"},
        ],
        "artifacts": artifacts,
        "tracked_sources": [
            file_identity(root, root / relative_path) for relative_path in tracked_paths
        ],
        "measurement": {
            "run_summary_valid": _summary_valid(run_summary),
            "worker_artifact_present": artifacts["worker"]["exists"],
            "worker_completed": None,
            "vrchat_concurrent_evidence": False,
        },
        "valid": not invalid_reasons,
        "invalid_reasons": invalid_reasons,
    }


def write_manifest(path: Path, manifest: dict[str, Any]) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--ffmpeg", type=Path)
    parser.add_argument("--ffprobe", type=Path)
    parser.add_argument("--source", type=Path)
    parser.add_argument("--snapshot", type=Path)
    parser.add_argument("--worker", type=Path)
    parser.add_argument("--run-summary", type=Path)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[3])
    args = parser.parse_args(argv)
    manifest = build_manifest(
        args.root,
        ffmpeg=args.ffmpeg,
        ffprobe=args.ffprobe,
        source=args.source,
        snapshot=args.snapshot,
        worker=args.worker,
        run_summary=args.run_summary,
    )
    write_manifest(args.output, manifest)
    print(json.dumps({"output": str(args.output), "valid": manifest["valid"], "invalid_reasons": manifest["invalid_reasons"]}, ensure_ascii=False))
    return 0 if manifest["valid"] else 1


if __name__ == "__main__":
    raise SystemExit(main())

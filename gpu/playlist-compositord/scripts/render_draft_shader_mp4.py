#!/usr/bin/env python3
"""Render the Draft WGSL module without starting the ImagePadServer.

The Rust side executes the actual Draft shader through wgpu and writes diagnostic
YUV420P frames. This wrapper muxes those frames into MP4 and extracts PNG
snapshots plus a JSON manifest for human/AI review.
"""

from __future__ import annotations

import argparse
import json
import math
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


SCRIPT = Path(__file__).resolve()
REPO_ROOT = SCRIPT.parents[3]
MANIFEST = REPO_ROOT / "gpu" / "playlist-compositord" / "Cargo.toml"


def positive_int(value: str) -> int:
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def nonnegative_float(value: str) -> float:
    parsed = float(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Execute Draft WGSL, write a diagnostic MP4, and extract review frames."
    )
    parser.add_argument("--width", type=positive_int, default=640)
    parser.add_argument("--height", type=positive_int, default=360)
    parser.add_argument("--fps", type=positive_int, default=30)
    parser.add_argument("--duration", type=nonnegative_float, default=4.0)
    parser.add_argument("--frames", type=positive_int)
    parser.add_argument(
        "--output",
        type=Path,
        default=REPO_ROOT / "artifacts" / "draft-shader-preview.mp4",
        help="MP4 output path",
    )
    parser.add_argument(
        "--binary",
        type=Path,
        help="Use an existing playlist-compositord binary instead of cargo run",
    )
    parser.add_argument("--cargo", default=shutil.which("cargo") or "cargo")
    parser.add_argument("--crf", type=int, default=18)
    parser.add_argument("--preset", default="medium")
    parser.add_argument(
        "--pcm-samples", type=positive_int, default=4096, help="GPU PCM window size"
    )
    parser.add_argument(
        "--sample-rate", type=positive_int, default=44100, help="Input PCM sample rate"
    )
    parser.add_argument(
        "--pcm-input",
        type=Path,
        help="Mono little-endian f32 PCM input; omitted uses deterministic fixture PCM",
    )
    parser.add_argument("--title", help="Scene title metadata")
    parser.add_argument("--artist", help="Scene artist metadata")
    parser.add_argument("--album", help="Scene album metadata")
    parser.add_argument(
        "--artwork-input",
        type=Path,
        help="512x512 raw RGBA8 artwork input; omitted uses shader fallback artwork",
    )
    parser.add_argument(
        "--no-frames", action="store_true", help="Do not extract PNG review frames"
    )
    parser.add_argument(
        "--keep-yuv", action="store_true", help="Keep the intermediate raw YUV420P file"
    )
    return parser.parse_args()


def run(command: list[str], *, cwd: Path | None = None) -> None:
    print("$", " ".join(str(part) for part in command), flush=True)
    subprocess.run(command, cwd=cwd, check=True)


def main() -> int:
    args = parse_args()
    ffmpeg = shutil.which("ffmpeg")
    if not ffmpeg:
        raise SystemExit("ffmpeg is required but was not found on PATH")

    output = args.output.expanduser().resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    frames = args.frames or max(1, math.ceil(args.duration * args.fps))

    binary = args.binary.expanduser().resolve() if args.binary else None
    if binary is not None and not binary.is_file():
        raise SystemExit(f"shader exporter binary does not exist: {binary}")

    with tempfile.TemporaryDirectory(prefix="imagepad-draft-shader-") as temp_dir:
        yuv_path = Path(temp_dir) / "draft-shader.yuv420p"
        exporter_args = [
            "--export-draft-shader-yuv",
            "--width",
            str(args.width),
            "--height",
            str(args.height),
            "--fps",
            str(args.fps),
            "--frames",
            str(frames),
            "--pcm-samples",
            str(args.pcm_samples),
            "--sample-rate",
            str(args.sample_rate),
            "--output-yuv",
            str(yuv_path),
        ]
        if args.pcm_input:
            exporter_args.extend(["--pcm-input", str(args.pcm_input.expanduser().resolve())])
        if args.title is not None:
            exporter_args.extend(["--title", args.title])
        if args.artist is not None:
            exporter_args.extend(["--artist", args.artist])
        if args.album is not None:
            exporter_args.extend(["--album", args.album])
        if args.artwork_input:
            exporter_args.extend(
                ["--artwork-input", str(args.artwork_input.expanduser().resolve())]
            )
        if binary is None:
            exporter_command = [
                args.cargo,
                "run",
                "--manifest-path",
                str(MANIFEST),
                "--release",
                "--",
                *exporter_args,
            ]
            run(exporter_command, cwd=REPO_ROOT)
        else:
            run([str(binary), *exporter_args], cwd=REPO_ROOT)

        run(
            [
                ffmpeg,
                "-y",
                "-hide_banner",
                "-loglevel",
                "error",
                "-f",
                "rawvideo",
                "-pixel_format",
                "yuv420p",
                "-video_size",
                f"{args.width}x{args.height}",
                "-framerate",
                str(args.fps),
                "-i",
                str(yuv_path),
                "-an",
                "-c:v",
                "libx264",
                "-preset",
                args.preset,
                "-crf",
                str(args.crf),
                "-pix_fmt",
                "yuv420p",
                str(output),
            ]
        )

        if args.keep_yuv:
            kept_yuv = output.with_suffix(".yuv420p")
            shutil.copyfile(yuv_path, kept_yuv)
        else:
            kept_yuv = None

    frames_dir = output.parent / f"{output.stem}_frames"
    if not args.no_frames:
        frames_dir.mkdir(parents=True, exist_ok=True)
        run(
            [
                ffmpeg,
                "-y",
                "-hide_banner",
                "-loglevel",
                "error",
                "-i",
                str(output),
                "-vf",
                "fps=1",
                "-frames:v",
                str(max(1, math.ceil(frames / args.fps))),
                str(frames_dir / "frame-%03d.png"),
            ]
        )
    else:
        frames_dir = None

    ffprobe = shutil.which("ffprobe")
    probe = None
    if ffprobe:
        probe_command = [
            ffprobe,
            "-v",
            "error",
            "-count_frames",
            "-select_streams",
            "v:0",
            "-show_entries",
            "stream=width,height,avg_frame_rate,nb_read_frames,duration",
            "-of",
            "json",
            str(output),
        ]
        probe_result = subprocess.run(probe_command, check=True, capture_output=True, text=True)
        probe = json.loads(probe_result.stdout)

    manifest_path = output.with_suffix(".json")
    manifest = {
        "source": "gpu/playlist-compositord/src/music_v2_shader_draft.rs",
        "module": "DRAFT_SHADER_MODULE",
        "mode": "diagnostic_gpu_readback_only",
        "production_route_started": False,
        "shader_executed_by": "wgpu",
        "width": args.width,
        "height": args.height,
        "fps": args.fps,
        "frames_requested": frames,
        "pcm_samples": args.pcm_samples,
        "mp4": str(output),
        "review_frames": str(frames_dir) if frames_dir else None,
        "raw_yuv420p": str(kept_yuv) if kept_yuv else None,
        "ffprobe": probe,
    }
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")

    if not output.is_file() or output.stat().st_size == 0:
        raise SystemExit(f"FFmpeg did not create a non-empty MP4: {output}")
    print(f"draft_shader_mp4={output}")
    print(f"draft_shader_manifest={manifest_path}")
    if frames_dir:
        print(f"draft_shader_review_frames={frames_dir}")
    if kept_yuv:
        print(f"draft_shader_yuv={kept_yuv}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except subprocess.CalledProcessError as error:
        print(f"command failed with exit code {error.returncode}", file=sys.stderr)
        raise

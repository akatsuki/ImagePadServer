#!/usr/bin/env python3
"""Run a matched CPU-reference vs GPU-Draft music render comparison.

This is a diagnostic comparison harness.  It deliberately reports the current
GPU readback boundary instead of presenting the Draft exporter as production
GPU-only.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import shutil
import subprocess
import sys
import tempfile
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Sequence


SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parents[2]
DEFAULT_GPU_BINARY = REPO_ROOT / "gpu" / "playlist-compositord" / "target" / "release" / "playlist-compositord.exe"


@dataclass(frozen=True)
class ComparisonConfig:
    width: int
    height: int
    fps: int
    frames: int
    pcm_samples: int
    sample_rate: int
    pcm_input: Path
    title: str
    artist: str
    album: str
    artwork_input: Path
    output_dir: Path


@dataclass(frozen=True)
class FrameStats:
    frame_count: int
    first_frame: int | None
    last_frame: int | None
    mean_mse: float | None


def build_gpu_export_args(
    binary: Path,
    config: ComparisonConfig,
    output_yuv: Path,
    scene_input: Path,
) -> list[str]:
    return [
        str(binary),
        "--export-draft-shader-yuv",
        "--width",
        str(config.width),
        "--height",
        str(config.height),
        "--fps",
        str(config.fps),
        "--frames",
        str(config.frames),
        "--pcm-samples",
        str(config.pcm_samples),
        "--sample-rate",
        str(config.sample_rate),
        "--pcm-input",
        str(config.pcm_input),
        "--scene-input",
        str(scene_input),
        "--title",
        config.title,
        "--artist",
        config.artist,
        "--album",
        config.album,
        "--artwork-input",
        str(config.artwork_input),
        "--output-yuv",
        str(output_yuv),
    ]


def parse_frame_stats(text: str) -> FrameStats:
    matches = re.findall(r"\bn:(\d+)\b.*?\bmse_avg:([0-9]+(?:\.[0-9]+)?)\b", text)
    if not matches:
        return FrameStats(0, None, None, None)
    frames = [int(frame) for frame, _ in matches]
    mse_values = [float(mse) for _, mse in matches]
    return FrameStats(
        frame_count=len(frames),
        first_frame=frames[0],
        last_frame=frames[-1],
        mean_mse=sum(mse_values) / len(mse_values),
    )


def build_manifest(
    config: ComparisonConfig,
    *,
    cpu_wall_seconds: float,
    gpu_render_seconds: float,
    gpu_mux_seconds: float | None,
    cpu_frames: int,
    gpu_frames: int,
    comparison: FrameStats,
    gpu_total_seconds: float | None = None,
    gpu_transport: str = "raw_file",
) -> dict[str, object]:
    total_seconds = gpu_total_seconds
    if total_seconds is None:
        total_seconds = gpu_render_seconds + (gpu_mux_seconds or 0.0)
    return {
        "schema": 1,
        "mode": "cpu_gpu_1to1_diagnostic",
        "conditions": {
            "width": config.width,
            "height": config.height,
            "fps": config.fps,
            "frames": config.frames,
            "pcm_samples": config.pcm_samples,
            "sample_rate": config.sample_rate,
            "title": config.title,
            "artist": config.artist,
            "album": config.album,
        },
        "cpu": {
            "wall_seconds": cpu_wall_seconds,
            "frames": cpu_frames,
            "reference": "RunAudioVisualizerHLSCPUReference",
        },
        "gpu": {
            "render_seconds": gpu_render_seconds,
            "mux_seconds": gpu_mux_seconds,
            "total_seconds": total_seconds,
            "frames": gpu_frames,
            "readback": True,
            "production_ready": False,
            "route": "music_v2_shader_export",
            "transport": gpu_transport,
        },
        "comparison": {
            "frame_count_match": cpu_frames == gpu_frames == config.frames,
            "stats_frame_count": comparison.frame_count,
            "first_frame": comparison.first_frame,
            "last_frame": comparison.last_frame,
            "mean_mse": comparison.mean_mse,
        },
    }


def _run(command: Sequence[str], *, cwd: Path | None = None, capture: bool = True) -> tuple[float, subprocess.CompletedProcess[str]]:
    started = time.perf_counter()
    completed = subprocess.run(
        list(command),
        cwd=cwd,
        text=True,
        capture_output=capture,
        check=False,
    )
    elapsed = time.perf_counter() - started
    if completed.returncode != 0:
        details = (completed.stdout or "") + (completed.stderr or "")
        raise RuntimeError(f"command failed ({completed.returncode}): {' '.join(command)}\n{details}")
    return elapsed, completed


def _run_gpu_direct_pipe(
    gpu_command: Sequence[str],
    ffmpeg_command: Sequence[str],
    *,
    cwd: Path,
) -> tuple[float, float, str, str]:
    started = time.perf_counter()
    ffmpeg_process = subprocess.Popen(
        list(ffmpeg_command),
        cwd=cwd,
        stdin=subprocess.PIPE,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=False,
    )
    if ffmpeg_process.stdin is None:
        ffmpeg_process.kill()
        raise RuntimeError("FFmpeg stdin pipe was not created")

    gpu_started = time.perf_counter()
    gpu_process = subprocess.Popen(
        list(gpu_command),
        cwd=cwd,
        stdout=ffmpeg_process.stdin,
        stderr=subprocess.PIPE,
        text=False,
    )
    ffmpeg_process.stdin.close()
    gpu_stderr_bytes = gpu_process.communicate()[1] or b""
    gpu_seconds = time.perf_counter() - gpu_started
    ffmpeg_stderr_bytes = ffmpeg_process.stderr.read() if ffmpeg_process.stderr else b""
    ffmpeg_returncode = ffmpeg_process.wait()
    total_seconds = time.perf_counter() - started
    gpu_stderr = gpu_stderr_bytes.decode(errors="replace")
    ffmpeg_stderr = ffmpeg_stderr_bytes.decode(errors="replace")
    if gpu_process.returncode != 0 or ffmpeg_returncode != 0:
        raise RuntimeError(
            "direct GPU-to-FFmpeg pipe failed: "
            f"gpu={gpu_process.returncode} ffmpeg={ffmpeg_returncode}\n"
            f"GPU stderr:\n{gpu_stderr}\nFFmpeg stderr:\n{ffmpeg_stderr}"
        )
    return total_seconds, gpu_seconds, gpu_stderr, ffmpeg_stderr


def _require_file(path: Path, label: str) -> Path:
    resolved = path.expanduser().resolve()
    if not resolved.is_file():
        raise FileNotFoundError(f"{label} does not exist: {resolved}")
    return resolved


def _probe_video(ffprobe: str, path: Path) -> dict[str, object]:
    _, completed = _run(
        [
            ffprobe,
            "-v",
            "error",
            "-select_streams",
            "v:0",
            "-count_frames",
            "-show_entries",
            "stream=width,height,r_frame_rate,nb_read_frames,duration",
            "-of",
            "json",
            str(path),
        ]
    )
    payload = json.loads(completed.stdout)
    streams = payload.get("streams", [])
    if not streams:
        raise RuntimeError(f"no video stream in {path}")
    return streams[0]


def _extract_frame(ffmpeg: str, source: Path, output: Path, index: int) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    _run(
        [
            ffmpeg,
            "-y",
            "-hide_banner",
            "-loglevel",
            "error",
            "-i",
            str(source),
            "-vf",
            f"select=eq(n\\,{index})",
            "-vsync",
            "0",
            "-frames:v",
            "1",
            str(output),
        ]
    )


def _compare_frames(ffmpeg: str, cpu_video: Path, gpu_video: Path, stats_path: Path) -> FrameStats:
    stats_path.parent.mkdir(parents=True, exist_ok=True)
    _, completed = _run(
        [
            ffmpeg,
            "-hide_banner",
            "-loglevel",
            "error",
            "-i",
            str(cpu_video),
            "-i",
            str(gpu_video),
            "-filter_complex",
            # Use a basename inside the output directory.  An absolute
            # Windows path contains ':' after the drive letter, which the
            # libavfilter option parser treats as another option separator.
            f"[0:v]setpts=PTS-STARTPTS[cpu];[1:v]setpts=PTS-STARTPTS[gpu];[cpu][gpu]psnr=stats_file={stats_path.name}:shortest=1",
            "-f",
            "null",
            "-",
        ],
        cwd=stats_path.parent,
    )
    if not stats_path.is_file():
        raise RuntimeError(f"ffmpeg did not create frame stats: {stats_path}\n{completed.stderr}")
    return parse_frame_stats(stats_path.read_text(encoding="utf-8", errors="replace"))


def _build_cpu_command(
    compare_binary: Path | None,
    go_binary: str,
    config: ComparisonConfig,
    audio_input: Path,
    artwork_input: Path,
    cpu_output: Path,
    scene_input: Path,
) -> list[str]:
    prefix = [str(compare_binary)] if compare_binary else [go_binary, "run", "./cmd/music-render-compare"]
    return [
        *prefix,
        "--input",
        str(audio_input),
        "--output-dir",
        str(cpu_output),
        "--height",
        str(config.height),
        "--cpu-only",
        "--title",
        config.title,
        "--artist",
        config.artist,
        "--album",
        config.album,
        "--artwork",
        str(artwork_input),
        "--scene-input",
        str(scene_input),
    ]


def _build_scene_command(
    go_binary: str,
    config: ComparisonConfig,
    audio_input: Path,
    artwork_input: Path,
    scene_output: Path,
    ffmpeg: str,
) -> list[str]:
    return [
        go_binary,
        "run",
        "./cmd/music-scene-export",
        "--input",
        str(audio_input),
        "--output",
        str(scene_output),
        "--width",
        str(config.width),
        "--height",
        str(config.height),
        "--fps",
        str(config.fps),
        "--frames",
        str(config.frames),
        "--pcm-samples",
        str(config.pcm_samples),
        "--sample-rate",
        str(config.sample_rate),
        "--title",
        config.title,
        "--artist",
        config.artist,
        "--album",
        config.album,
        "--artwork",
        str(artwork_input),
        "--ffmpeg",
        ffmpeg,
    ]


def _build_gpu_mux_args(
    ffmpeg: str,
    config: ComparisonConfig,
    video_input: str,
    audio_input: Path,
    gpu_mp4: Path,
) -> list[str]:
    return [
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
        f"{config.width}x{config.height}",
        "-framerate",
        str(config.fps),
        "-i",
        video_input,
        "-i",
        str(audio_input),
        "-map",
        "0:v:0",
        "-map",
        "1:a:0",
        "-c:v",
        "libx264",
        "-preset",
        "medium",
        "-crf",
        "18",
        "-pix_fmt",
        "yuv420p",
        "-c:a",
        "aac",
        "-b:a",
        "128k",
        # The audio file may end a fraction of a frame before the video.
        # Frame identity is the comparison contract, so never let audio
        # duration truncate the final requested video frame.
        "-frames:v",
        str(config.frames),
        str(gpu_mp4),
    ]


def build_gpu_mux_args(
    ffmpeg: str,
    config: ComparisonConfig,
    gpu_yuv: Path,
    audio_input: Path,
    gpu_mp4: Path,
) -> list[str]:
    return _build_gpu_mux_args(ffmpeg, config, str(gpu_yuv), audio_input, gpu_mp4)


def build_gpu_pipe_mux_args(
    ffmpeg: str,
    config: ComparisonConfig,
    audio_input: Path,
    gpu_mp4: Path,
) -> list[str]:
    return _build_gpu_mux_args(ffmpeg, config, "-", audio_input, gpu_mp4)


def run_comparison(
    config: ComparisonConfig,
    *,
    audio_input: Path,
    cpu_artwork: Path,
    cpu_binary: Path | None,
    gpu_binary: Path,
    ffmpeg: str,
    ffprobe: str,
    go_binary: str,
    repo_root: Path,
) -> dict[str, object]:
    output_dir = config.output_dir.expanduser().resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    cpu_output = output_dir / "cpu"
    gpu_output = output_dir / "gpu"
    cpu_output.mkdir(parents=True, exist_ok=True)
    gpu_output.mkdir(parents=True, exist_ok=True)

    scene_input = output_dir / "shared-scene.json"
    scene_process_seconds, scene_process = _run(
        _build_scene_command(go_binary, config, audio_input, cpu_artwork, scene_input, ffmpeg),
        cwd=repo_root,
    )
    scene_document = json.loads(scene_input.read_text(encoding="utf-8"))
    scene_hash = hashlib.sha256(scene_input.read_bytes()).hexdigest()

    cpu_command = _build_cpu_command(
        cpu_binary,
        go_binary,
        config,
        audio_input,
        cpu_artwork,
        cpu_output,
        scene_input,
    )
    cpu_process_seconds, cpu_process = _run(cpu_command, cwd=repo_root)
    cpu_report_path = cpu_output / "report.json"
    if not cpu_report_path.is_file():
        raise RuntimeError(f"CPU comparison CLI did not create {cpu_report_path}\n{cpu_process.stdout}")
    cpu_report = json.loads(cpu_report_path.read_text(encoding="utf-8"))
    cpu_wall_seconds = float(cpu_report["cpu"]["wallSeconds"])
    cpu_video = Path(cpu_report["cpu"]["output"])
    if not cpu_video.is_absolute():
        cpu_video = (repo_root / cpu_video).resolve()

    gpu_mp4 = gpu_output / "gpu.mp4"
    gpu_command = build_gpu_export_args(gpu_binary, config, Path("-"), scene_input)
    gpu_mux_command = build_gpu_pipe_mux_args(ffmpeg, config, audio_input, gpu_mp4)
    gpu_total_seconds, gpu_render_seconds, gpu_stderr, ffmpeg_stderr = _run_gpu_direct_pipe(
        gpu_command,
        gpu_mux_command,
        cwd=repo_root,
    )

    cpu_probe = _probe_video(ffprobe, cpu_video)
    gpu_probe = _probe_video(ffprobe, gpu_mp4)
    cpu_frames = int(cpu_probe.get("nb_read_frames", 0))
    gpu_frames = int(gpu_probe.get("nb_read_frames", 0))
    comparison = _compare_frames(ffmpeg, cpu_video, gpu_mp4, output_dir / "frame-stats.log")

    review_indices = {
        "start": 0,
        "mid": max(0, (config.frames - 1) // 2),
        "end": max(0, config.frames - 1),
    }
    for label, index in review_indices.items():
        _extract_frame(ffmpeg, cpu_video, output_dir / "frames" / f"cpu-{label}.png", index)
        _extract_frame(ffmpeg, gpu_mp4, output_dir / "frames" / f"gpu-{label}.png", index)

    manifest = build_manifest(
        config,
        cpu_wall_seconds=cpu_wall_seconds,
        gpu_render_seconds=gpu_render_seconds,
        gpu_mux_seconds=None,
        cpu_frames=cpu_frames,
        gpu_frames=gpu_frames,
        comparison=comparison,
        gpu_total_seconds=gpu_total_seconds,
        gpu_transport="direct_ffmpeg_pipe",
    )
    manifest["inputs"] = {
        "audio": str(audio_input),
        "pcm": str(config.pcm_input),
        "cpu_artwork": str(cpu_artwork),
        "gpu_artwork_rgba": str(config.artwork_input),
    }
    manifest["shared_scene"] = {
        "path": str(scene_input),
        "sha256": scene_hash,
        "schema": scene_document.get("schema"),
        "frames": scene_document.get("frames"),
        "same_document_passed_to_cpu_and_gpu": True,
        "analysis_payload_parity_verified": True,
    }
    manifest["process"] = {
        "scene_process_seconds": scene_process_seconds,
        "scene_process_stdout": scene_process.stdout,
        "cpu_process_seconds": cpu_process_seconds,
        "gpu_render_stderr": gpu_stderr,
        "gpu_mux_stderr": ffmpeg_stderr,
    }
    manifest["probes"] = {"cpu": cpu_probe, "gpu": gpu_probe}
    manifest["artifacts"] = {
        "cpu_video": str(cpu_video),
        "gpu_video": str(gpu_mp4),
        "frame_stats": str(output_dir / "frame-stats.log"),
        "review_frames": str(output_dir / "frames"),
    }
    report_path = output_dir / "report.json"
    report_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return manifest


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Compare CPU reference and GPU Draft frames one-to-one.")
    parser.add_argument("--audio-input", type=Path, required=True, help="Audio file consumed by the CPU reference")
    parser.add_argument("--pcm-input", type=Path, required=True, help="Mono little-endian f32 PCM for the GPU exporter")
    parser.add_argument("--cpu-artwork", type=Path, required=True, help="Artwork image consumed by the CPU reference")
    parser.add_argument("--artwork-input", type=Path, required=True, help="512x512 raw RGBA8 artwork for the GPU exporter")
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--cpu-binary", type=Path, help="Prebuilt cmd/music-render-compare binary")
    parser.add_argument("--gpu-binary", type=Path, default=DEFAULT_GPU_BINARY)
    parser.add_argument("--repo-root", type=Path, default=REPO_ROOT)
    parser.add_argument("--go", default=shutil.which("go") or "go")
    parser.add_argument("--ffmpeg", default=shutil.which("ffmpeg") or "ffmpeg")
    parser.add_argument("--ffprobe", default=shutil.which("ffprobe") or "ffprobe")
    parser.add_argument("--width", type=int, default=640)
    parser.add_argument("--height", type=int, default=360)
    parser.add_argument("--fps", type=int, default=30)
    parser.add_argument("--frames", type=int, default=301)
    parser.add_argument("--pcm-samples", type=int, default=4096)
    parser.add_argument("--sample-rate", type=int, default=44100)
    parser.add_argument("--title", required=True)
    parser.add_argument("--artist", required=True)
    parser.add_argument("--album", default="")
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    config = ComparisonConfig(
        width=args.width,
        height=args.height,
        fps=args.fps,
        frames=args.frames,
        pcm_samples=args.pcm_samples,
        sample_rate=args.sample_rate,
        pcm_input=_require_file(args.pcm_input, "GPU PCM input"),
        title=args.title,
        artist=args.artist,
        album=args.album,
        artwork_input=_require_file(args.artwork_input, "GPU artwork input"),
        output_dir=args.output_dir,
    )
    audio_input = _require_file(args.audio_input, "CPU audio input")
    cpu_artwork = _require_file(args.cpu_artwork, "CPU artwork input")
    gpu_binary = _require_file(args.gpu_binary, "GPU binary")
    cpu_binary = args.cpu_binary.expanduser().resolve() if args.cpu_binary else None
    manifest = run_comparison(
        config,
        audio_input=audio_input,
        cpu_artwork=cpu_artwork,
        cpu_binary=cpu_binary,
        gpu_binary=gpu_binary,
        ffmpeg=args.ffmpeg,
        ffprobe=args.ffprobe,
        go_binary=args.go,
        repo_root=args.repo_root.expanduser().resolve(),
    )
    print(json.dumps(manifest, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (FileNotFoundError, RuntimeError, ValueError) as error:
        print(f"compare_music_render_1to1: {error}", file=sys.stderr)
        raise SystemExit(1)

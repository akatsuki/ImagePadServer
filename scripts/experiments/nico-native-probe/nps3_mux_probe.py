"""Run a saved NPS3 sprite stream through the real compositor and tee mux.

Diagnostic-only: this intentionally bypasses browser sprite capture so that
native compositor, FFmpeg, and MP4/HLS publication can be tested separately.
It is not a product acceptance result by itself.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import time
from pathlib import Path


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _nps3_header(path: Path) -> dict[str, int]:
    import struct

    raw = path.read_bytes()[:24]
    if len(raw) != 24:
        raise ValueError("NPS3 header is truncated")
    magic, width, height, frames, fps_num, fps_den = struct.unpack("<6I", raw)
    if magic != 0x3353504E or not all((width, height, frames, fps_num, fps_den)):
        raise ValueError("invalid NPS3 header")
    return {
        "width": width,
        "height": height,
        "frames": frames,
        "fps_num": fps_num,
        "fps_den": fps_den,
    }


def _tail(data: bytes, limit: int = 8192) -> str:
    return data[-limit:].decode("utf-8", errors="replace")


def _ffmpeg_args(
    *,
    ffmpeg: Path,
    source: Path,
    header: dict[str, int],
    audio_bitrate: str,
    crf: int,
) -> list[str]:
    width = header["width"]
    height = header["height"]
    frames = header["frames"]
    fps = f"{header['fps_num']}/{header['fps_den']}"
    geometry = f"{width}x{height}"
    filter_graph = (
        f"[0:v]scale={width}:{height}:force_original_aspect_ratio=decrease,"
        f"pad={width}:{height}:(ow-iw)/2:(oh-ih)/2:color=black,tpad=stop_mode=clone:"
        f"stop_duration=1[base];[1:v]format=rgba[overlay];"
        f"[base][overlay]overlay=0:0:format=auto,fps={fps},format=yuv420p[v]"
    )
    return [
        str(ffmpeg.resolve()),
        "-hide_banner",
        "-loglevel",
        "error",
        "-y",
        "-filter_complex_threads",
        "1",
        "-threads",
        "1",
        "-i",
        str(source.resolve()),
        "-f",
        "rawvideo",
        "-pix_fmt",
        "rgba",
        "-video_size",
        geometry,
        "-framerate",
        fps,
        "-i",
        "pipe:0",
        "-filter_complex",
        filter_graph,
        "-map",
        "[v]",
        "-map",
        "0:a?",
        "-frames:v",
        str(frames),
        "-c:v",
        "libx264",
        "-preset",
        "veryfast",
        "-crf",
        str(crf),
        "-x264-params",
        "sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0",
        "-g",
        str(max(1, round(4 * header["fps_num"] / header["fps_den"]))),
        "-keyint_min",
        str(max(1, round(4 * header["fps_num"] / header["fps_den"]))),
        "-sc_threshold",
        "0",
        "-force_key_frames",
        "expr:gte(t,n_forced*4)",
        "-pix_fmt",
        "yuv420p",
        "-c:a",
        "aac",
        "-b:a",
        audio_bitrate,
        "-flags:v",
        "+global_header",
        "-f",
        "tee",
        "[f=mp4:movflags=+faststart:onfail=abort]out.mp4|"
        "[f=hls:hls_time=4:hls_playlist_type=vod:hls_flags=independent_segments:"
        "start_number=0:hls_segment_filename=segment-%05d.ts:onfail=abort]playlist.m3u8",
    ]


def run(args: argparse.Namespace) -> dict:
    for path in (args.compositor, args.ffmpeg, args.fixture, args.source):
        if not path.is_file():
            raise FileNotFoundError(path)
    run_dir = args.run_dir.resolve()
    run_dir.mkdir(parents=True, exist_ok=False)
    header = _nps3_header(args.fixture)
    compositor = subprocess.Popen(
        [str(args.compositor.resolve()), "--stdin"],
        stdin=args.fixture.open("rb"),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        cwd=str(run_dir),
    )
    assert compositor.stdout is not None
    ffmpeg = subprocess.Popen(
        _ffmpeg_args(
            ffmpeg=args.ffmpeg,
            source=args.source,
            header=header,
            audio_bitrate=args.audio_bitrate,
            crf=args.crf,
        ),
        stdin=compositor.stdout,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        cwd=str(run_dir),
    )
    compositor.stdout.close()
    started = time.monotonic()
    timed_out = False
    try:
        ffmpeg_stdout, ffmpeg_stderr = ffmpeg.communicate(timeout=args.timeout_s)
        compositor_stderr = compositor.stderr.read() if compositor.stderr else b""
        compositor_code = compositor.wait(timeout=5)
    except subprocess.TimeoutExpired:
        timed_out = True
        ffmpeg.kill()
        compositor.kill()
        ffmpeg_stdout, ffmpeg_stderr = ffmpeg.communicate()
        compositor_stderr = compositor.stderr.read() if compositor.stderr else b""
        compositor_code = compositor.wait()
    output = run_dir / "out.mp4"
    playlist = run_dir / "playlist.m3u8"
    result = {
        "schema_version": 1,
        "status": "complete" if not timed_out and ffmpeg.returncode == 0 and compositor_code == 0 and output.is_file() and playlist.is_file() else "failed",
        "timed_out": timed_out,
        "elapsed_s": time.monotonic() - started,
        "compositor_exit_code": compositor_code,
        "ffmpeg_exit_code": ffmpeg.returncode,
        "fixture": str(args.fixture.resolve()),
        "fixture_sha256": _sha256(args.fixture),
        "source": str(args.source.resolve()),
        "source_sha256": _sha256(args.source),
        "header": header,
        "mp4_present": output.is_file(),
        "playlist_present": playlist.is_file(),
        "mp4_bytes": output.stat().st_size if output.is_file() else 0,
        "compositor_stderr_tail": _tail(compositor_stderr),
        "ffmpeg_stderr_tail": _tail(ffmpeg_stderr),
    }
    (run_dir / "result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", required=True, type=Path)
    parser.add_argument("--compositor", required=True, type=Path)
    parser.add_argument("--ffmpeg", required=True, type=Path)
    parser.add_argument("--fixture", required=True, type=Path)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--audio-bitrate", default="128k")
    parser.add_argument("--crf", type=int, default=26)
    parser.add_argument("--timeout-s", type=float, default=180.0)
    args = parser.parse_args()
    result = run(args)
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result["status"] == "complete" else 1


if __name__ == "__main__":
    raise SystemExit(main())

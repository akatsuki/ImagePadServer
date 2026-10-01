"""Full-frame RGBA8 comparison for Nico comment timeline artifacts."""

from __future__ import annotations

import argparse
import gzip
import hashlib
import json
from pathlib import Path
from typing import BinaryIO

import numpy as np
from PIL import Image


def _open_rgba(path: Path) -> BinaryIO:
    with path.open("rb") as stream:
        magic = stream.read(2)
    if magic == b"\x1f\x8b":
        return gzip.open(path, "rb")
    return path.open("rb")


def _write_difference_png(path: Path, channel_diff: np.ndarray) -> None:
    """Write a lossless, amplified RGBA diff preview; alpha-only changes are magenta."""
    preview = np.zeros((*channel_diff.shape[:2], 4), dtype=np.uint8)
    rgb = np.minimum(channel_diff[:, :, :3].astype(np.uint16) * 8, 255).astype(np.uint8)
    alpha_only = (channel_diff[:, :, 3] != 0) & (channel_diff[:, :, :3].max(axis=2) == 0)
    rgb[alpha_only] = (255, 0, 255)
    preview[:, :, :3] = rgb
    preview[:, :, 3] = 255
    with path.open("xb") as stream:
        Image.fromarray(preview).save(stream, format="PNG", optimize=False)


def compare_rgba(
    reference: Path,
    candidate: Path,
    width: int,
    height: int,
    frames: int,
    *,
    tolerance: int = 1,
    diff_dir: Path | None = None,
    max_diff_images: int = 2,
) -> dict:
    """Compare every premultiplied RGBA8 pixel in two raw or gzip streams.

    Pixel counts are per-frame pixel observations, accumulated across all frames.
    A pixel exceeds tolerance when any of its four channels differs by more than
    ``tolerance``. Difference previews are produced for every non-identical frame
    when ``diff_dir`` is supplied. To keep failed multi-minute 1080p runs
    reviewable without emitting hundreds of large PNGs, previews contain the
    first differing frame and the frame with the largest measured difference.
    """
    if width <= 0 or height <= 0 or frames <= 0:
        raise ValueError("dimensions and frame count must be positive")
    if tolerance < 0 or tolerance > 255:
        raise ValueError("tolerance must be between 0 and 255")
    if max_diff_images < 0:
        raise ValueError("max_diff_images must not be negative")

    reference = Path(reference)
    candidate = Path(candidate)
    frame_bytes = width * height * 4
    expected_length = frame_bytes * frames
    for label, path in (("reference", reference), ("candidate", candidate)):
        if not path.is_file():
            raise ValueError(f"{label} RGBA input is not a file: {path}")
        with path.open("rb") as stream:
            compressed = stream.read(2) == b"\x1f\x8b"
        if not compressed and path.stat().st_size != expected_length:
            raise ValueError(
                f"{label} RGBA length={path.stat().st_size}, expected {expected_length} bytes"
            )

    if diff_dir is not None:
        diff_dir = Path(diff_dir)
        diff_dir.mkdir(parents=True, exist_ok=True)

    reference_hash = hashlib.sha256()
    candidate_hash = hashlib.sha256()
    frame_metrics: list[dict] = []
    first_difference: tuple[int, np.ndarray] | None = None
    worst_difference: tuple[tuple[int, int, int], int, np.ndarray] | None = None
    difference_frames = 0
    max_channel_diff = 0
    total_pixels_over_tolerance = 0
    total_different_pixels = 0
    total_transparent_exterior_pixels = 0
    total_transparent_exterior_different_pixels = 0
    reference_stream = _open_rgba(reference)
    candidate_stream = _open_rgba(candidate)
    try:
        for frame_index in range(frames):
            reference_bytes = reference_stream.read(frame_bytes)
            candidate_bytes = candidate_stream.read(frame_bytes)
            if len(reference_bytes) != frame_bytes:
                actual = frame_index * frame_bytes + len(reference_bytes)
                raise ValueError(
                    f"reference RGBA length={actual}, expected {expected_length} bytes"
                )
            if len(candidate_bytes) != frame_bytes:
                actual = frame_index * frame_bytes + len(candidate_bytes)
                raise ValueError(
                    f"candidate RGBA length={actual}, expected {expected_length} bytes"
                )
            reference_hash.update(reference_bytes)
            candidate_hash.update(candidate_bytes)
            reference_pixels = np.frombuffer(reference_bytes, dtype=np.uint8).reshape(
                height, width, 4
            )
            candidate_pixels = np.frombuffer(candidate_bytes, dtype=np.uint8).reshape(
                height, width, 4
            )
            channel_diff = np.abs(
                reference_pixels.astype(np.int16) - candidate_pixels.astype(np.int16)
            ).astype(np.uint8)
            pixel_diff = channel_diff.max(axis=2)
            transparent_exterior = (reference_pixels[:, :, 3] == 0) & (
                candidate_pixels[:, :, 3] == 0
            )
            frame_max = int(channel_diff.max(initial=0))
            frame_over = int(np.count_nonzero(pixel_diff > tolerance))
            frame_different = int(np.count_nonzero(pixel_diff))
            frame_transparent_exterior = int(np.count_nonzero(transparent_exterior))
            frame_transparent_exterior_different = int(
                np.count_nonzero((pixel_diff != 0) & transparent_exterior)
            )
            max_channel_diff = max(max_channel_diff, frame_max)
            total_pixels_over_tolerance += frame_over
            total_different_pixels += frame_different
            total_transparent_exterior_pixels += frame_transparent_exterior
            total_transparent_exterior_different_pixels += (
                frame_transparent_exterior_different
            )
            if frame_different:
                difference_frames += 1
                if first_difference is None:
                    first_difference = (frame_index, channel_diff.copy())
                score = (frame_max, frame_over, frame_different)
                if worst_difference is None or score > worst_difference[0]:
                    worst_difference = (score, frame_index, channel_diff.copy())
            frame_result = {
                "frame": frame_index,
                "reference_sha256": hashlib.sha256(reference_bytes).hexdigest(),
                "candidate_sha256": hashlib.sha256(candidate_bytes).hexdigest(),
                "max_channel_diff": frame_max,
                "pixels_over_tolerance": frame_over,
                "different_pixels": frame_different,
                "transparent_exterior_pixels": frame_transparent_exterior,
                "transparent_exterior_different_pixels": frame_transparent_exterior_different,
            }
            frame_metrics.append(frame_result)

        for label, stream in (("reference", reference_stream), ("candidate", candidate_stream)):
            extra = stream.read(1)
            if extra:
                raise ValueError(f"{label} RGBA length exceeds expected {expected_length} bytes")
    finally:
        reference_stream.close()
        candidate_stream.close()

    difference_images: list[str] = []
    preview_candidates: list[tuple[int, np.ndarray]] = []
    if first_difference is not None and max_diff_images > 0:
        preview_candidates.append(first_difference)
    if (
        worst_difference is not None
        and max_diff_images > len(preview_candidates)
        and (first_difference is None or worst_difference[1] != first_difference[0])
    ):
        preview_candidates.append((worst_difference[1], worst_difference[2]))
    if diff_dir is not None:
        for frame_index, difference in preview_candidates:
            diff_path = diff_dir / f"frame-{frame_index:06d}-diff.png"
            _write_difference_png(diff_path, difference)
            difference_images.append(str(diff_path))

    return {
        "schema_version": 1,
        "width": width,
        "height": height,
        "frames": frames,
        "tolerance": tolerance,
        "expected_rgba_bytes": expected_length,
        "reference_rgba_sha256": reference_hash.hexdigest(),
        "candidate_rgba_sha256": candidate_hash.hexdigest(),
        "max_channel_diff": max_channel_diff,
        "pixels_over_tolerance": total_pixels_over_tolerance,
        "different_pixels": total_different_pixels,
        "transparent_exterior_pixels": total_transparent_exterior_pixels,
        "transparent_exterior_different_pixels": total_transparent_exterior_different_pixels,
        "transparent_exterior_exact": total_transparent_exterior_different_pixels == 0,
        "different_frames": difference_frames,
        "frame_metrics": frame_metrics,
        "difference_images": difference_images,
        "difference_images_truncated": diff_dir is not None and difference_frames > len(difference_images),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reference", type=Path)
    parser.add_argument("candidate", type=Path)
    parser.add_argument("--width", type=int, required=True)
    parser.add_argument("--height", type=int, required=True)
    parser.add_argument("--frames", type=int, required=True)
    parser.add_argument("--tolerance", type=int, default=1)
    parser.add_argument("--diff-dir", type=Path)
    parser.add_argument("--max-diff-images", type=int, default=2)
    parser.add_argument(
        "--require-transparent-exterior-exact",
        action="store_true",
        help="return failure when any pixel transparent in both inputs differs",
    )
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    result = compare_rgba(
        args.reference,
        args.candidate,
        args.width,
        args.height,
        args.frames,
        tolerance=args.tolerance,
        diff_dir=args.diff_dir,
        max_diff_images=args.max_diff_images,
    )
    serialized = json.dumps(result, indent=2)
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        with args.report.open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(serialized)
            stream.write("\n")
    print(serialized)
    within_tolerance = result["pixels_over_tolerance"] == 0
    exterior_matches = (
        not args.require_transparent_exterior_exact or result["transparent_exterior_exact"]
    )
    return 0 if within_tolerance and exterior_matches else 1


if __name__ == "__main__":
    raise SystemExit(main())

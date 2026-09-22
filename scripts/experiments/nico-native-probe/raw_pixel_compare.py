"""Compare saved browser RGBA frames with a contiguous native RGBA stream."""

import argparse
import hashlib
import json
from pathlib import Path


def _sha256(data):
    return hashlib.sha256(data).hexdigest()


def compare_raw_frames(
    *,
    browser_dir,
    native_path,
    width,
    height,
    frame_indices,
    browser_template="real.browser.frame-{index:03d}.rgba",
    total_frames=None,
):
    """Compare selected RGBA frames and return auditable pixel-diff metrics.

    ``native_path`` is expected to contain tightly packed RGBA frames. Missing
    or malformed inputs raise instead of being silently classified as a pass.
    ``scope`` is ``full`` only when every frame in ``range(total_frames)`` was
    requested; otherwise it is ``sparse`` and cannot satisfy full parity.
    """
    browser_dir = Path(browser_dir)
    native_path = Path(native_path)
    width = int(width)
    height = int(height)
    if width <= 0 or height <= 0:
        raise ValueError("width and height must be positive")

    indices = [int(index) for index in frame_indices]
    if not indices:
        raise ValueError("at least one frame index is required")
    if any(index < 0 for index in indices):
        raise ValueError("frame indices must be non-negative")
    if len(set(indices)) != len(indices):
        raise ValueError("frame indices must be unique")

    frame_bytes = width * height * 4
    native_size = native_path.stat().st_size
    if native_size % frame_bytes:
        raise ValueError(
            f"native stream size {native_size} is not a multiple of frame size {frame_bytes}"
        )
    native_frame_count = native_size // frame_bytes
    if max(indices) >= native_frame_count:
        raise ValueError(
            f"native stream has {native_frame_count} frames, cannot read frame {max(indices)}"
        )

    expected_indices = set(range(int(total_frames))) if total_frames is not None else None
    scope = "full" if expected_indices is not None and set(indices) == expected_indices else "sparse"
    missing_frames = []
    frame_results = []
    changed_frames = []
    identical_frames = 0
    changed_channels = 0
    max_channel_diff = 0
    total_abs_diff = 0
    compared_channels = 0

    with native_path.open("rb") as native_file:
        for frame_index in indices:
            browser_path = browser_dir / browser_template.format(index=frame_index)
            if not browser_path.is_file():
                missing_frames.append(frame_index)
                continue
            browser_data = browser_path.read_bytes()
            if len(browser_data) != frame_bytes:
                raise ValueError(
                    f"browser frame {browser_path} has {len(browser_data)} bytes, expected {frame_bytes}"
                )
            native_file.seek(frame_index * frame_bytes)
            native_data = native_file.read(frame_bytes)
            if len(native_data) != frame_bytes:
                raise ValueError(f"native frame {frame_index} is truncated")

            diffs = [abs(browser_byte - native_byte) for browser_byte, native_byte in zip(browser_data, native_data)]
            frame_changed_channels = sum(diff != 0 for diff in diffs)
            frame_max_diff = max(diffs, default=0)
            frame_abs_diff = sum(diffs)
            identical = frame_changed_channels == 0
            if identical:
                identical_frames += 1
            else:
                changed_frames.append(frame_index)
            changed_channels += frame_changed_channels
            max_channel_diff = max(max_channel_diff, frame_max_diff)
            total_abs_diff += frame_abs_diff
            compared_channels += len(diffs)
            frame_results.append(
                {
                    "frame_index": frame_index,
                    "identical": identical,
                    "changed_channels": frame_changed_channels,
                    "max_channel_diff": frame_max_diff,
                    "mean_abs_channel": frame_abs_diff / len(diffs),
                    "browser_sha256": _sha256(browser_data),
                    "native_sha256": _sha256(native_data),
                }
            )

    if missing_frames:
        raise FileNotFoundError(
            "missing browser frames: " + ", ".join(str(index) for index in missing_frames)
        )

    return {
        "schema_version": 1,
        "browser_dir": str(browser_dir),
        "native_path": str(native_path),
        "browser_template": browser_template,
        "scope": scope,
        "width": width,
        "height": height,
        "frame_bytes": frame_bytes,
        "native_frame_count": native_frame_count,
        "total_frames": total_frames,
        "frame_indices": indices,
        "frames_compared": len(frame_results),
        "missing_frames": missing_frames,
        "identical_frames": identical_frames,
        "changed_frames": changed_frames,
        "changed_channels": changed_channels,
        "max_channel_diff": max_channel_diff,
        "mean_abs_channel": total_abs_diff / compared_channels,
        "frames": frame_results,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--browser-dir", required=True, type=Path)
    parser.add_argument("--native", required=True, type=Path)
    parser.add_argument("--width", required=True, type=int)
    parser.add_argument("--height", required=True, type=int)
    parser.add_argument("--frames", required=True, help="comma-separated frame indices")
    parser.add_argument("--total-frames", type=int)
    parser.add_argument("--browser-template", default="real.browser.frame-{index:03d}.rgba")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()

    result = compare_raw_frames(
        browser_dir=args.browser_dir,
        native_path=args.native,
        width=args.width,
        height=args.height,
        frame_indices=[int(value) for value in args.frames.split(",") if value.strip()],
        browser_template=args.browser_template,
        total_frames=args.total_frames,
    )
    text = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(text, encoding="utf-8")
    print(text, end="")


if __name__ == "__main__":
    main()

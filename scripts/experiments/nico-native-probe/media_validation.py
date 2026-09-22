"""Deterministic media-output checks used by the T11 acceptance matrix."""

from __future__ import annotations

from fractions import Fraction
import math
from pathlib import Path
from typing import Any


class ValidationError(ValueError):
    """An output cannot be accepted by the media contract."""


def summarize_timestamped_durations(streams: list[dict[str, Any]]) -> dict[str, Any]:
    """Summarize stream durations without mistaking timestamp offsets for gaps.

    MPEG-TS/AAC segments commonly expose a small encoder/priming offset in
    ``start_time``.  Summing each segment's ``duration`` therefore measures
    payload duration, not the duration covered by the stream timeline.  When
    every measured stream has timestamps, use the enclosing timeline span for
    duration comparisons and retain the payload sum for diagnostics.
    """
    payload_duration = 0.0
    ranges: list[tuple[float, float]] = []
    measured = 0
    for stream in streams:
        raw_duration = stream.get("duration")
        if raw_duration is None:
            continue
        try:
            duration = float(raw_duration)
        except (TypeError, ValueError) as exc:
            raise ValidationError(f"invalid stream duration: {raw_duration!r}") from exc
        if not math.isfinite(duration) or duration < 0:
            raise ValidationError(f"invalid stream duration: {raw_duration!r}")
        payload_duration += duration
        measured += 1

        raw_start = stream.get("start_time")
        if raw_start is None:
            continue
        try:
            start = float(raw_start)
        except (TypeError, ValueError) as exc:
            raise ValidationError(f"invalid stream start_time: {raw_start!r}") from exc
        if not math.isfinite(start):
            raise ValidationError(f"invalid stream start_time: {raw_start!r}")
        ranges.append((start, start + duration))

    if measured == 0:
        return {
            "duration": None,
            "payload_duration": 0.0,
            "timeline_start": None,
            "timeline_end": None,
            "source": "unmeasured",
        }
    if len(ranges) == measured:
        timeline_start = min(start for start, _ in ranges)
        timeline_end = max(end for _, end in ranges)
        return {
            "duration": timeline_end - timeline_start,
            "payload_duration": payload_duration,
            "timeline_start": timeline_start,
            "timeline_end": timeline_end,
            "source": "timestamps",
        }
    return {
        "duration": payload_duration,
        "payload_duration": payload_duration,
        "timeline_start": None,
        "timeline_end": None,
        "source": "payload",
    }


def _rational(value: str, name: str) -> Fraction:
    try:
        result = Fraction(value)
    except (TypeError, ValueError, ZeroDivisionError) as exc:
        raise ValidationError(f"invalid {name} rational: {value!r}") from exc
    if result <= 0:
        raise ValidationError(f"{name} must be positive: {value!r}")
    return result


def check_frame_pts(
    frames: list[dict[str, Any]],
    time_base: str,
    frame_rate: str,
    expected_frames: int,
) -> int:
    """Require every decoded frame to land on the exact rational frame clock."""
    if expected_frames < 1:
        raise ValidationError(f"expected frame count must be positive: {expected_frames}")
    if len(frames) != expected_frames:
        raise ValidationError(f"frame count {len(frames)} != expected {expected_frames}")
    tick = _rational(time_base, "time_base")
    fps = _rational(frame_rate, "frame_rate")
    previous: int | None = None
    for index, frame in enumerate(frames):
        raw_pts = frame.get("best_effort_timestamp")
        if raw_pts is None:
            raise ValidationError(f"frame {index} has no best_effort_timestamp")
        try:
            pts = int(raw_pts)
        except (TypeError, ValueError) as exc:
            raise ValidationError(f"frame {index} has invalid PTS: {raw_pts!r}") from exc
        if previous is not None and pts <= previous:
            raise ValidationError(f"frame {index} PTS is not strictly increasing")
        previous = pts
        actual_seconds = pts * tick
        expected_seconds = Fraction(index, 1) / fps
        if actual_seconds != expected_seconds:
            raise ValidationError(
                f"frame {index} PTS {pts}*{tick} != {expected_seconds} at {fps} fps"
            )
    return len(frames)


def _annex_b_nals(data: bytes) -> list[bytes]:
    starts: list[tuple[int, int]] = []
    index = 0
    while index + 3 <= len(data):
        length = 0
        if data[index : index + 3] == b"\x00\x00\x01":
            length = 3
        elif index + 4 <= len(data) and data[index : index + 4] == b"\x00\x00\x00\x01":
            length = 4
        if length:
            starts.append((index, length))
            index += length
        else:
            index += 1
    nals: list[bytes] = []
    for position, prefix_length in starts:
        next_positions = [candidate for candidate, _ in starts if candidate > position]
        end = min(next_positions) if next_positions else len(data)
        nal = data[position + prefix_length : end]
        if nal:
            nals.append(nal)
    return nals


def h264_access_unit_slice_counts(data: bytes, *, require_single_slice: bool = True) -> list[int]:
    """Return the number of VCL NAL units grouped by AUD access unit."""
    counts: list[int] = []
    current: int | None = None
    for nal in _annex_b_nals(data):
        nal_type = nal[0] & 0x1F
        if nal_type == 9:  # access unit delimiter
            if current is not None:
                counts.append(current)
            current = 0
        elif nal_type in (1, 5):  # non-IDR/IDR VCL
            if current is None:
                raise ValidationError("H.264 VCL NAL appears before an AUD")
            current += 1
    if current is not None:
        counts.append(current)
    if not counts:
        raise ValidationError("H.264 stream has no AUD access units")
    if any(count == 0 for count in counts):
        raise ValidationError(f"H.264 access unit has no VCL NAL: {counts}")
    if require_single_slice and any(count != 1 for count in counts):
        raise ValidationError(f"H.264 access units are not one-slice: {counts}")
    return counts


def parse_hls_playlist(playlist: Path) -> dict[str, Any]:
    """Parse a local HLS playlist and resolve only safe, regular segment files."""
    playlist = Path(playlist)
    if not playlist.is_file():
        raise ValidationError(f"HLS playlist is missing: {playlist}")
    root = playlist.parent.resolve()
    lines = [line.strip() for line in playlist.read_text(encoding="utf-8-sig").splitlines()]
    if "#EXTM3U" not in lines:
        raise ValidationError("HLS playlist has no #EXTM3U header")
    endlist = "#EXT-X-ENDLIST" in lines
    if not endlist:
        raise ValidationError("HLS playlist has no #EXT-X-ENDLIST")
    segments: list[Path] = []
    durations: list[float] = []
    for index, line in enumerate(lines):
        if line.startswith("#EXTINF:"):
            try:
                durations.append(float(line.split(":", 1)[1].split(",", 1)[0]))
            except ValueError as exc:
                raise ValidationError(f"invalid HLS EXTINF at line {index + 1}") from exc
        elif line and not line.startswith("#"):
            if "://" in line or "\\" in line:
                raise ValidationError(f"HLS segment is not a local relative path: {line!r}")
            candidate = (root / line).resolve()
            if candidate != root and root not in candidate.parents:
                raise ValidationError(f"HLS segment escapes playlist directory: {line!r}")
            if not candidate.is_file() or candidate.stat().st_size == 0:
                raise ValidationError(f"HLS segment is missing or empty: {line!r}")
            segments.append(candidate)
    if not segments:
        raise ValidationError("HLS playlist contains no segments")
    if len(durations) != len(segments):
        raise ValidationError(
            f"HLS EXTINF count {len(durations)} != segment count {len(segments)}"
        )
    return {"playlist": playlist.resolve(), "segments": segments, "durations": durations, "endlist": True}

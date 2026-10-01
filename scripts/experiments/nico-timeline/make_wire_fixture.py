#!/usr/bin/env python3
"""Generate independent NCT1 protocol fixtures using Python struct.pack."""

from __future__ import annotations

import argparse
import hashlib
import struct
from pathlib import Path


HEADER = struct.Struct("<4s8I32sQI")
ASSET_PREFIX = struct.Struct("<I")
ASSET_META = struct.Struct("<III32s")
DRAW = struct.Struct("<IiiiIII4f16ffdd")
FLAGS = 3


def bundle_hash() -> bytes:
    return hashlib.sha256(b"nico-timeline-fixture-v1").digest()


def asset(asset_id: int, rgba: bytes) -> tuple[bytes, bytes]:
    digest = hashlib.sha256(rgba).digest()
    record_bytes = 44 + len(rgba)
    return ASSET_PREFIX.pack(record_bytes) + ASSET_META.pack(asset_id, 1, 1, digest) + rgba, digest


def draw(
    asset_id: int,
    start: int,
    end: int,
    anchor: int,
    owner: int,
    index: int,
    primitive: int,
    rect: tuple[float, float, float, float],
    alpha: float,
    anchor_x: float,
    speed_x: float,
    projection: tuple[float, ...] | None = None,
) -> bytes:
    if projection is None:
        projection = (
            1.0, 0.0, 0.0, 0.0,
            0.0, 1.0, 0.0, 0.0,
            0.0, 0.0, 1.0, 0.0,
            0.0, 0.0, 0.0, 1.0,
        )
    return DRAW.pack(
        asset_id,
        start,
        end,
        anchor,
        owner,
        index,
        primitive,
        *rect,
        *projection,
        alpha,
        anchor_x,
        speed_x,
    )


def make_multidraw_scene() -> bytes:
    first, _ = asset(1, bytes((255, 0, 0, 255)))
    second, _ = asset(2, bytes((0, 0, 128, 128)))
    first_draw = draw(1, -4, 100, 0, 0, 0, 0, (0.0, 0.0, 33.0, 19.0), 1.0, 0.0, 0.0)
    second_draw = draw(2, -1, 6, 2, 0, 1, 0, (4.0, 5.0, 10.0, 3.0), 0.75, 4.25, -0.125)
    assets = first + second
    draws = first_draw + second_draw
    header = HEADER.pack(
        b"NCT1",
        80,
        33,
        19,
        3,
        30,
        1,
        2,
        2,
        bundle_hash(),
        8,
        FLAGS,
    )
    result = header + assets + draws
    assert len(header) == 80
    assert len(first) == len(second) == 52
    assert len(first_draw) == len(second_draw) == 128
    assert len(result) == 80 + 2 * 52 + 2 * 128
    return result


def make_red_render_scene() -> bytes:
    pixels = bytes((255, 0, 0, 255))
    red_asset, _ = asset(1, pixels)
    pixel_to_clip = (
        2.0 / 33.0, 0.0, 0.0, 0.0,
        0.0, -2.0 / 19.0, 0.0, 0.0,
        0.0, 0.0, 1.0, 0.0,
        -1.0, 1.0, 0.0, 1.0,
    )
    full_canvas = draw(
        1, 0, 100, 0, 0, 0, 0,
        (0.0, 0.0, 33.0, 19.0),
        1.0, 0.0, 0.0,
        projection=pixel_to_clip,
    )
    header = HEADER.pack(
        b"NCT1",
        80,
        33,
        19,
        3,
        30,
        1,
        1,
        1,
        bundle_hash(),
        len(pixels),
        FLAGS,
    )
    result = header + red_asset + full_canvas
    assert len(red_asset) == 52
    assert len(full_canvas) == 128
    assert len(result) == 80 + 52 + 128
    return result


def write_fixtures(output_dir: Path) -> None:
    output_dir.mkdir(parents=True, exist_ok=True)
    valid = make_multidraw_scene()
    (output_dir / "wire-multidraw-33x19-3frames.nct").write_bytes(valid)
    (output_dir / "red-33x19-3frames.nct").write_bytes(make_red_render_scene())

    unknown_flags = bytearray(valid)
    struct.pack_into("<I", unknown_flags, 76, 0x80000003)
    (output_dir / "unknown-flags.nct").write_bytes(unknown_flags)

    missing_asset = bytearray(valid)
    draw_offset = 80 + 2 * 52
    struct.pack_into("<I", missing_asset, draw_offset, 0x7FFFFFFF)
    (output_dir / "missing-asset.nct").write_bytes(missing_asset)

    oversized_length = bytearray(valid)
    struct.pack_into("<I", oversized_length, 80, 0xFFFFFFFF)
    (output_dir / "oversized-length.nct").write_bytes(oversized_length)


def main() -> None:
    parser = argparse.ArgumentParser()
    default_dir = Path(__file__).resolve().parents[3] / "internal" / "nicorender" / "testdata" / "timeline" / "wire"
    parser.add_argument("--output-dir", type=Path, default=default_dir)
    args = parser.parse_args()
    write_fixtures(args.output_dir)
    print(f"wrote independent NCT1 fixtures to {args.output_dir}")


if __name__ == "__main__":
    main()

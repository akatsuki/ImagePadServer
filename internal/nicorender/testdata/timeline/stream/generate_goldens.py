#!/usr/bin/env python3
"""Independent, stdlib-only reference encoder for the checked-in NCT2 vectors.

Run with --write only when intentionally updating the protocol fixtures.
Default mode is read-only verification, including fixed scene commitments and
commitment sensitivity/invariance checks. This is testdata tooling, not app code.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import struct
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parent
LEGACY = ROOT.parent / "wire" / "wire-multidraw-33x19-3frames.nct"
HEADER_BYTES = 80
DRAW_BYTES = 128
FLAGS = 3
MAX_CHUNK = 4 << 20
SCENE_DIGEST_DOMAIN = b"NCT2-SCENE-DIGEST-v1\x00"

# Fixed protocol commitments. These are intentionally independent of the
# reference encoder's calculated values and updated only with a reviewed vector.
EXPECTED_SCENE_COMMITMENTS = {
    "empty-scene.nct2": "7cebd6ff85b74779fbd1e897e4eab1645607652310381797183d50d7a8875e18",
    "nct1-compat-multidraw.nct2": "c3f6fa5024466409879965d7cc0471aa33d5d3fb1b5842185bee5e923f785350",
    "owner-time-inversion.nct2": "531bbd57112e6da6e60c46cb96ebb1061b8af5fdefdeff450782bf945458a310",
}

REC_HEADER = 1
REC_DECLARATIONS = 2
REC_DECLARATIONS_COMPLETE = 3
REC_ASSET_BEGIN = 4
REC_ASSET_CHUNK = 5
REC_ASSET_END = 6
REC_ELEMENT_COMPLETE = 7
REC_WATERMARK = 8
REC_END = 9


def u32(value: int) -> bytes:
    return struct.pack("<I", value)


def sha(data: bytes) -> bytes:
    return hashlib.sha256(data).digest()


def envelope(kind: int, sequence: int, payload: bytes) -> bytes:
    return struct.pack("<IIQ", kind, len(payload), sequence) + payload


def end_commitment(stream: bytes) -> bytes:
    offset = 0
    while offset < len(stream):
        kind, payload_len, _sequence = struct.unpack_from("<IIQ", stream, offset)
        payload_start = offset + 16
        if kind == REC_END:
            if payload_len != 64 or payload_start + payload_len != len(stream):
                raise AssertionError("End must be the final exact 64-byte payload")
            return stream[payload_start + 32 : payload_start + 64]
        offset = payload_start + payload_len
    raise AssertionError("missing End record")


def verify_fixed_scene_commitments(
    fixtures: dict[str, tuple[bytes, bytes, dict[str, object]]]
) -> None:
    for name, expected in EXPECTED_SCENE_COMMITMENTS.items():
        actual = end_commitment(fixtures[name][0]).hex()
        if actual != expected:
            raise AssertionError(
                f"{name} scene commitment {actual}, expected fixed value {expected}"
            )


def verify_vectors_json() -> None:
    vectors = json.loads((ROOT / "vectors.json").read_text(encoding="utf-8"))
    if vectors.get("scene_commitments") != EXPECTED_SCENE_COMMITMENTS:
        raise AssertionError("vectors.json scene commitments differ from fixed protocol values")
    required_cases = {
        "empty_scene": "empty-scene.nct2",
        "nct1_parity": "nct1-compat-multidraw.nct2",
        "owner_order": "owner-time-inversion.nct2",
    }
    if vectors.get("scene_commitment_cases") != required_cases:
        raise AssertionError("vectors.json must identify empty, NCT1 parity, and owner-order cases")


def frame_clock(frame: int, fps_num: int, fps_den: int) -> int:
    return frame * fps_den * 100 // fps_num


def clipped(value: int, exclusive_end: int) -> int:
    return min(exclusive_end, max(0, value))


def encode_draw(draw: dict[str, object]) -> bytes:
    raw = struct.pack(
        "<IiiiIII4f16ffdd",
        int(draw["asset_id"]),
        int(draw["start"]),
        int(draw["end"]),
        int(draw["anchor_vpos"]),
        int(draw["owner"]),
        int(draw["index"]),
        int(draw["primitive"]),
        *[float(value) for value in draw["rect"]],  # type: ignore[arg-type]
        *[float(value) for value in draw["projection"]],  # type: ignore[arg-type]
        float(draw["alpha"]),
        float(draw["anchor_x"]),
        float(draw["speed_x"]),
    )
    if len(raw) != DRAW_BYTES:
        raise AssertionError(f"Draw encoding is {len(raw)} bytes, expected {DRAW_BYTES}")
    return raw


def nct1_bytes(
    header: dict[str, int | bytes],
    assets: list[dict[str, object]],
    draws: list[dict[str, object]],
) -> bytes:
    ordered_assets = sorted(assets, key=lambda item: int(item["id"]))
    raw_total = sum(len(bytes(item["rgba"])) for item in ordered_assets)
    out = bytearray(nct1_header_bytes(header, ordered_assets, draws, raw_total))
    for asset in ordered_assets:
        pixels = bytes(asset["rgba"])
        digest = sha(pixels)
        out += u32(44 + len(pixels))
        out += struct.pack(
            "<III", int(asset["id"]), int(asset["width"]), int(asset["height"])
        )
        out += digest
        out += pixels
    for draw in draws:
        out += encode_draw(draw)
    return bytes(out)


def nct1_header_bytes(
    header: dict[str, int | bytes],
    assets: list[dict[str, object]],
    draws: list[dict[str, object]],
    raw_total: int,
) -> bytes:
    out = bytearray(HEADER_BYTES)
    out[0:4] = b"NCT1"
    struct.pack_into(
        "<7I",
        out,
        4,
        HEADER_BYTES,
        int(header["width"]),
        int(header["height"]),
        int(header["frame_count"]),
        int(header["fps_num"]),
        int(header["fps_den"]),
        len(assets),
    )
    struct.pack_into("<I", out, 32, len(draws))
    out[36:68] = bytes(header["bundle_sha"])
    struct.pack_into("<Q", out, 68, raw_total)
    struct.pack_into("<I", out, 76, FLAGS)
    return bytes(out)


def asset_descriptor(asset: dict[str, object]) -> bytes:
    pixels = bytes(asset["rgba"])
    return struct.pack(
        "<IIIQ",
        int(asset["id"]),
        int(asset["width"]),
        int(asset["height"]),
        len(pixels),
    ) + sha(pixels)


def scene_commitment(
    header: dict[str, int | bytes],
    assets: list[dict[str, object]],
    draws: list[dict[str, object]],
) -> bytes:
    ordered_assets = sorted(assets, key=lambda item: int(item["id"]))
    raw_total = sum(len(bytes(asset["rgba"])) for asset in ordered_assets)
    canonical_header = nct1_header_bytes(header, ordered_assets, draws, raw_total)
    descriptors = b"".join(asset_descriptor(asset) for asset in ordered_assets)
    draw_bytes = b"".join(encode_draw(draw) for draw in draws)
    return sha(SCENE_DIGEST_DOMAIN + canonical_header + descriptors + draw_bytes)


def verify_commitment_properties(
    header: dict[str, int | bytes],
    assets: list[dict[str, object]],
    draws: list[dict[str, object]],
) -> None:
    baseline = scene_commitment(header, assets, draws)
    if scene_commitment(header, list(reversed(assets)), draws) != baseline:
        raise AssertionError("scene commitment depends on asset transfer order")

    changed_header = dict(header)
    changed_header["width"] = int(header["width"]) + 1
    if scene_commitment(changed_header, assets, draws) == baseline:
        raise AssertionError("scene commitment ignored canonical NCT1 header metadata")

    for field, value in (("id", 9), ("width", 2), ("height", 2)):
        changed_assets = [dict(asset) for asset in assets]
        changed_assets[0][field] = value
        if scene_commitment(header, changed_assets, draws) == baseline:
            raise AssertionError(f"scene commitment ignored asset descriptor {field}")

    changed_length = [dict(asset) for asset in assets]
    changed_length[0]["rgba"] = bytes(changed_length[0]["rgba"]) + b"\x00"
    if scene_commitment(header, changed_length, draws) == baseline:
        raise AssertionError("scene commitment ignored asset descriptor raw length")

    changed_pixels = [dict(asset) for asset in assets]
    pixel_bytes = bytearray(bytes(changed_pixels[0]["rgba"]))
    pixel_bytes[0] ^= 1
    changed_pixels[0]["rgba"] = bytes(pixel_bytes)
    if scene_commitment(header, changed_pixels, draws) == baseline:
        raise AssertionError("scene commitment ignored an updated verified pixel hash")

    changed_draws = [dict(draw) for draw in draws]
    changed_draws[0]["alpha"] = float(changed_draws[0]["alpha"]) / 2
    if scene_commitment(header, assets, changed_draws) == baseline:
        raise AssertionError("scene commitment ignored exact Draw bytes")


def nct2_header(
    header: dict[str, int | bytes], declaration_count: int
) -> bytes:
    out = bytearray(HEADER_BYTES)
    out[0:4] = b"NCT2"
    struct.pack_into(
        "<9I",
        out,
        4,
        HEADER_BYTES,
        2,
        int(header["width"]),
        int(header["height"]),
        int(header["frame_count"]),
        int(header["fps_num"]),
        int(header["fps_den"]),
        declaration_count,
        FLAGS,
    )
    out[40:72] = bytes(header["bundle_sha"])
    # Bytes 72..79 are reserved and remain zero.
    return bytes(out)


def encode_declarations(
    declarations: list[dict[str, int]], exclusive_end: int
) -> tuple[bytes, bytes]:
    entries = bytearray()
    previous: tuple[int, int] | None = None
    for ordinal, declaration in enumerate(declarations):
        if declaration["ordinal"] != ordinal:
            raise AssertionError("declaration ordinals must be contiguous")
        owner_index = (declaration["owner"], declaration["index"])
        if previous is not None and owner_index <= previous:
            raise AssertionError("declarations must be strictly owner/index ordered")
        previous = owner_index
        start = clipped(declaration["start"], exclusive_end)
        end = clipped(declaration["end"], exclusive_end)
        if start > end:
            raise AssertionError("clipped declaration interval is inverted")
        entries += struct.pack(
            "<IIIii", ordinal, declaration["owner"], declaration["index"], start, end
        )
    payload = u32(len(declarations)) + bytes(entries)
    completed = u32(len(declarations)) + sha(bytes(entries))
    return payload, completed


def ready_at(
    declarations: list[dict[str, int]], prefix: int, exclusive_end: int
) -> int:
    starts = [
        clipped(declaration["start"], exclusive_end)
        for declaration in declarations[prefix:]
    ]
    return min(starts) if starts else exclusive_end


def nct2_bytes(
    header: dict[str, int | bytes],
    declarations: list[dict[str, int]],
    assets: list[dict[str, object]],
    elements: list[list[dict[str, object]]],
    normalized_scene: bytes,
) -> bytes:
    if len(declarations) != len(elements):
        raise AssertionError("one explicit ElementComplete is required per declaration")
    exclusive_end = frame_clock(
        int(header["frame_count"]), int(header["fps_num"]), int(header["fps_den"])
    )
    declarations_payload, declarations_done = encode_declarations(
        declarations, exclusive_end
    )
    all_assets = {int(asset["id"]): asset for asset in assets}
    if len(all_assets) != len(assets):
        raise AssertionError("asset IDs must be unique")

    records: list[bytes] = []

    def add(kind: int, payload: bytes) -> None:
        records.append(envelope(kind, len(records), payload))

    add(REC_HEADER, nct2_header(header, len(declarations)))
    add(REC_DECLARATIONS, declarations_payload)
    add(REC_DECLARATIONS_COMPLETE, declarations_done)

    sent_assets: set[int] = set()
    draw_total = 0
    for ordinal, draws in enumerate(elements):
        if declarations[ordinal]["ordinal"] != ordinal:
            raise AssertionError("element ordinal mismatch")
        for draw in draws:
            if draw["owner"] != declarations[ordinal]["owner"] or draw["index"] != declarations[ordinal]["index"]:
                raise AssertionError("Draw owner/index differs from its declaration")
            asset_id = int(draw["asset_id"])
            if asset_id not in all_assets:
                raise AssertionError("Draw references an unknown asset")
            if asset_id in sent_assets:
                continue
            asset = all_assets[asset_id]
            pixels = bytes(asset["rgba"])
            digest = sha(pixels)
            add(
                REC_ASSET_BEGIN,
                struct.pack(
                    "<IIIQ", asset_id, int(asset["width"]), int(asset["height"]), len(pixels)
                )
                + digest,
            )
            offset = 0
            while offset < len(pixels):
                chunk = pixels[offset : offset + MAX_CHUNK]
                add(REC_ASSET_CHUNK, struct.pack("<IQ", asset_id, offset) + chunk)
                offset += len(chunk)
            add(REC_ASSET_END, struct.pack("<IQ", asset_id, len(pixels)) + digest)
            sent_assets.add(asset_id)

        payload = u32(ordinal) + u32(len(draws))
        payload += b"".join(encode_draw(draw) for draw in draws)
        add(REC_ELEMENT_COMPLETE, payload)
        draw_total += len(draws)
        add(
            REC_WATERMARK,
            struct.pack("<Iq", ordinal + 1, ready_at(declarations, ordinal + 1, exclusive_end)),
        )

    used_ids = {int(draw["asset_id"]) for element in elements for draw in element}
    if used_ids != sent_assets:
        raise AssertionError("all transferred assets must be referenced and verified")
    raw_total = sum(len(bytes(asset["rgba"])) for asset in assets)
    ordered_draws = [draw for element in elements for draw in element]
    expected_normalized = nct1_bytes(header, assets, ordered_draws)
    if normalized_scene != expected_normalized:
        raise AssertionError("normalized NCT1 scene differs from the declared assets and Draws")
    end = struct.pack(
        "<IIIQIq",
        len(declarations),
        len(assets),
        draw_total,
        raw_total,
        len(declarations),
        exclusive_end,
    ) + scene_commitment(
        header,
        assets,
        ordered_draws,
    )
    add(REC_END, end)
    return b"".join(records)


def fixture_scenes() -> dict[str, tuple[bytes, bytes, dict[str, object]]]:
    bundle_sha = sha(b"nico-timeline-fixture-v1")
    identity = [1.0, 0.0, 0.0, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0, 0.0, 1.0, 0.0, 0.0, 0.0, 0.0, 1.0]
    assets = [
        {"id": 1, "width": 1, "height": 1, "rgba": bytes([255, 0, 0, 255])},
        {"id": 2, "width": 1, "height": 1, "rgba": bytes([0, 0, 128, 128])},
    ]
    draws = [
        {
            "asset_id": 1, "start": -4, "end": 100, "anchor_vpos": 0,
            "owner": 0, "index": 0, "primitive": 0,
            "rect": [0.0, 0.0, 33.0, 19.0], "projection": identity,
            "alpha": 1.0, "anchor_x": 0.0, "speed_x": 0.0,
        },
        {
            "asset_id": 2, "start": -1, "end": 6, "anchor_vpos": 2,
            "owner": 0, "index": 1, "primitive": 0,
            "rect": [4.0, 5.0, 10.0, 3.0], "projection": identity,
            "alpha": 0.75, "anchor_x": 4.25, "speed_x": -0.125,
        },
    ]
    header = {
        "width": 33, "height": 19, "frame_count": 3,
        "fps_num": 30, "fps_den": 1, "bundle_sha": bundle_sha,
    }
    declarations = [
        {"ordinal": 0, "owner": 0, "index": 0, "start": -4, "end": 100},
        {"ordinal": 1, "owner": 0, "index": 1, "start": -1, "end": 6},
    ]
    legacy_scene = nct1_bytes(header, assets, draws)
    verify_commitment_properties(header, assets, draws)
    parity = nct2_bytes(
        header, declarations, assets, [[draws[0]], [draws[1]]], legacy_scene
    )

    empty_header = {
        "width": 33, "height": 19, "frame_count": 3,
        "fps_num": 30, "fps_den": 1, "bundle_sha": bundle_sha,
    }
    empty_scene = nct1_bytes(empty_header, [], [])
    empty = nct2_bytes(empty_header, [], [], [], empty_scene)

    inversion_header = {
        "width": 1, "height": 1, "frame_count": 3,
        "fps_num": 1, "fps_den": 1, "bundle_sha": sha(b"nct2-watermark-v1"),
    }
    inversion_declarations = [
        {"ordinal": 0, "owner": 0, "index": 0, "start": 0, "end": 300},
        {"ordinal": 1, "owner": 0, "index": 1, "start": 200, "end": 300},
        {"ordinal": 2, "owner": 0, "index": 2, "start": 0, "end": 300},
    ]
    inversion_scene = nct1_bytes(inversion_header, [], [])
    inversion = nct2_bytes(
        inversion_header,
        inversion_declarations,
        [],
        [[], [], []],
        inversion_scene,
    )
    return {
        "nct1-compat-multidraw.nct2": (
            parity,
            legacy_scene,
            {"declarations": declarations, "draws": draws},
        ),
        "owner-time-inversion.nct2": (
            inversion,
            inversion_scene,
            {"starts": [0, 200, 0], "ready_by_prefix": [0, 0, 0, 300]},
        ),
        "empty-scene.nct2": (
            empty,
            empty_scene,
            {"declarations": [], "ready_by_prefix": [10]},
        ),
    }


def manifest_for(
    fixtures: dict[str, tuple[bytes, bytes, dict[str, object]]], legacy: bytes
) -> bytes:
    entries = {}
    for name, (stream, normalized, details) in sorted(fixtures.items()):
        entries[name] = {
            "bytes": len(stream),
            "sha256": sha(stream).hex(),
            "scene_commitment_sha256": end_commitment(stream).hex(),
            "normalized_nct1_sha256": sha(normalized).hex(),
            "details": details,
        }
    manifest = {
        "format": "NCT2 v2",
        "legacy_nct1_fixture": {
            "path": "../wire/wire-multidraw-33x19-3frames.nct",
            "bytes": len(legacy),
            "sha256": sha(legacy).hex(),
        },
        "fixtures": entries,
    }
    return (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode("utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--write", action="store_true", help="explicitly regenerate checked-in vectors"
    )
    args = parser.parse_args()

    fixtures = fixture_scenes()
    verify_fixed_scene_commitments(fixtures)
    verify_vectors_json()
    expected_legacy = next(
        normalized for name, (_, normalized, _) in fixtures.items()
        if name == "nct1-compat-multidraw.nct2"
    )
    if not LEGACY.is_file():
        raise FileNotFoundError(f"missing NCT1 reference fixture: {LEGACY}")
    actual_legacy = LEGACY.read_bytes()
    if actual_legacy != expected_legacy:
        raise AssertionError("independent NCT1 reference bytes differ from the checked-in golden")

    expected_files = {name: value[0] for name, value in fixtures.items()}
    expected_files["goldens.json"] = manifest_for(fixtures, actual_legacy)
    mismatches = []
    for name, expected in expected_files.items():
        path = ROOT / name
        if args.write:
            path.write_bytes(expected)
        elif not path.is_file() or path.read_bytes() != expected:
            mismatches.append(name)
    if mismatches:
        raise AssertionError(
            "golden mismatch or missing file: " + ", ".join(mismatches) + "; rerun with --write only for an intentional update"
        )
    action = "wrote" if args.write else "verified"
    print(f"{action} {len(fixtures)} NCT2 streams and goldens.json")
    for name, (stream, normalized, _) in sorted(fixtures.items()):
        print(
            f"{name}: {len(stream)} bytes, stream sha256={sha(stream).hex()}, "
            f"scene commitment sha256={end_commitment(stream).hex()}, "
            f"normalized NCT1 sha256={sha(normalized).hex()}"
        )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)

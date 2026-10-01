#!/usr/bin/env python3
"""Compare paired Nico timeline separate-texture and Atlas benchmark runs."""

from __future__ import annotations

import argparse
import json
import math
import statistics
from collections import defaultdict
from pathlib import Path
from typing import Any

from optimization_summary import confirm_paired_improvement


def _load_runs(root: Path) -> dict[str, list[dict[str, Any]]]:
    variants: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for path in sorted(root.rglob("run.json")):
        try:
            run = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise ValueError(f"cannot read {path}: {error}") from error
        if run.get("schema_version") != 1:
            raise ValueError(f"unsupported benchmark manifest schema: {path}")
        run["_manifest_path"] = str(path.resolve())
        variants[str(run.get("variant"))].append(run)
    if not variants:
        raise ValueError(f"no run.json manifests under {root}")
    return variants


def _index_by_pair(runs: list[dict[str, Any]], variant: str) -> dict[str, dict[str, Any]]:
    indexed: dict[str, dict[str, Any]] = {}
    for run in runs:
        pair_id = run.get("pair_id")
        if not isinstance(pair_id, str) or not pair_id:
            raise ValueError(f"{variant} run has no pair_id: {run['_manifest_path']}")
        if pair_id in indexed:
            raise ValueError(f"duplicate {variant} pair_id {pair_id}")
        indexed[pair_id] = run
    return indexed


def _seconds(run: dict[str, Any]) -> float:
    value = run.get("convert_wall_seconds")
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"run time must be numeric: {run['_manifest_path']}")
    seconds = float(value)
    if not math.isfinite(seconds) or seconds <= 0:
        raise ValueError(f"run time must be finite and positive: {run['_manifest_path']}")
    return seconds


def _assert_layout_sample(run: dict[str, Any], layout: str) -> None:
    variant = f"timeline-{layout}"
    if run.get("variant") != variant:
        raise ValueError(f"expected {variant} sample, got {run.get('variant')!r}")
    if run.get("fallback_reason") or run.get("backend") != "timeline-wgpu":
        raise ValueError(f"fallback or non-WGPU sample is not eligible: {run['_manifest_path']}")
    if run.get("decoded_frames") != run.get("frame_count") or run.get("frame_count", 0) <= 0:
        raise ValueError(f"decoded frame count mismatch: {run['_manifest_path']}")
    if run.get("asset_layout_requested") != layout or run.get("asset_layout") != layout:
        raise ValueError(f"actual {layout} layout mismatch: {run['_manifest_path']}")
    if run.get("asset_layout_fallback_reason"):
        raise ValueError(f"layout fallback is not an eligible sample: {run['_manifest_path']}")
    if run.get("asset_page_count", 0) <= 0 or run.get("asset_source_bytes", 0) <= 0:
        raise ValueError(f"asset layout diagnostics are incomplete: {run['_manifest_path']}")
    slots = run.get("readback_slots")
    if isinstance(slots, bool) or not isinstance(slots, int) or not 1 <= slots <= 3:
        raise ValueError(f"invalid readback ring size: {run['_manifest_path']}")
    if not run.get("helper_sha256") or not run.get("gpu_backend") or not run.get("gpu_adapter") or not run.get("bundle_sha256"):
        raise ValueError(f"GPU/helper identity is incomplete: {run['_manifest_path']}")
    _seconds(run)


def _profile(run: dict[str, Any]) -> tuple[Any, ...]:
    return tuple(run.get(key) for key in (
        "encoder", "cpu_percent", "width", "height", "fps_num", "fps_den", "duration_ms",
        "source_sha256", "snapshot_sha256", "ffmpeg_sha256", "ffmpeg_version", "helper_sha256",
        "gpu_backend", "gpu_adapter", "readback_slots", "bundle_sha256", "logical_cpu_count",
        "processor_identifier",
    ))


def _input_profile(run: dict[str, Any]) -> tuple[Any, ...]:
    return tuple(run.get(key) for key in (
        "encoder", "cpu_percent", "width", "height", "fps_num", "fps_den", "duration_ms",
        "source_sha256", "snapshot_sha256", "ffmpeg_sha256", "ffmpeg_version",
    ))


def _pair_layouts(variants: dict[str, list[dict[str, Any]]], run_root: Path) -> dict[str, Any]:
    separate = _index_by_pair(variants.get("timeline-separate", []), "timeline-separate")
    atlas = _index_by_pair(variants.get("timeline-atlas", []), "timeline-atlas")
    if separate.keys() != atlas.keys():
        raise ValueError(f"separate/Atlas pair IDs differ under {run_root}")
    no_comments = _index_by_pair(variants.get("no-comments", []), "no-comments")
    if no_comments and no_comments.keys() != separate.keys():
        raise ValueError(f"no-comments pair IDs differ under {run_root}")

    pairs: list[dict[str, Any]] = []
    for pair_id in sorted(separate):
        separate_run, atlas_run = separate[pair_id], atlas[pair_id]
        _assert_layout_sample(separate_run, "separate")
        _assert_layout_sample(atlas_run, "atlas")
        if _profile(separate_run) != _profile(atlas_run):
            raise ValueError(f"separate/Atlas profile mismatch for pair {pair_id}")
        item = {
            "pair_id": pair_id,
            "separate_seconds": _seconds(separate_run),
            "atlas_seconds": _seconds(atlas_run),
            "separate_minus_atlas_seconds": _seconds(separate_run) - _seconds(atlas_run),
            "separate_pages": separate_run["asset_page_count"],
            "atlas_pages": atlas_run["asset_page_count"],
            "source_bytes": separate_run["asset_source_bytes"],
            "separate_allocated_bytes": separate_run["asset_allocated_bytes"],
            "atlas_allocated_bytes": atlas_run["asset_allocated_bytes"],
        }
        if no_comments:
            no_comment_run = no_comments[pair_id]
            if _input_profile(separate_run) != _input_profile(no_comment_run):
                raise ValueError(f"no-comments profile mismatch for pair {pair_id}")
            item["no_comments_seconds"] = _seconds(no_comment_run)
        pairs.append(item)
    if not pairs:
        raise ValueError(f"no paired separate/Atlas samples under {run_root}")
    deltas = [item["separate_minus_atlas_seconds"] for item in pairs]
    return {
        "root": str(run_root.resolve()),
        "pairs": pairs,
        "median_separate_seconds": statistics.median(item["separate_seconds"] for item in pairs),
        "median_atlas_seconds": statistics.median(item["atlas_seconds"] for item in pairs),
        "median_separate_minus_atlas_seconds": statistics.median(deltas),
    }


def compare_sessions(first_root: Path, second_root: Path | None = None, *, seed: int = 20260925) -> dict[str, Any]:
    first = _pair_layouts(_load_runs(first_root), first_root)
    confirmation: dict[str, Any]
    second: dict[str, Any] | None = None
    if second_root is None:
        confirmation = {"status": "screening_only", "pairs_per_session": len(first["pairs"])}
    else:
        second = _pair_layouts(_load_runs(second_root), second_root)
        first_run = _load_runs(first_root)
        second_run = _load_runs(second_root)
        first_sample = first_run["timeline-separate"][0]
        second_sample = second_run["timeline-separate"][0]
        if _profile(first_sample) != _profile(second_sample):
            raise ValueError("confirmation session profile mismatch")
        first_deltas = [item["separate_minus_atlas_seconds"] for item in first["pairs"]]
        second_deltas = [item["separate_minus_atlas_seconds"] for item in second["pairs"]]
        if len(first_deltas) != 10 or len(second_deltas) != 10:
            confirmation = {
                "status": "insufficient_pairs",
                "pairs_per_session": 10,
                "observed_pairs": {"first": len(first_deltas), "second": len(second_deltas)},
            }
        else:
            confirmation = confirm_paired_improvement(
                {first_root.name: first_deltas, second_root.name: second_deltas}, seed=seed
            )
    return {
        "schema_version": 1,
        "improvement_direction": "separate_minus_atlas_seconds; positive favors Atlas",
        "first_session": first,
        "second_session": second,
        "confirmation": confirmation,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--first", type=Path, required=True)
    parser.add_argument("--second", type=Path)
    parser.add_argument("--seed", type=int, default=20260925)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    result = compare_sessions(args.first, args.second, seed=args.seed)
    encoded = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded, encoding="utf-8")
        print(args.output.resolve())
    else:
        print(encoded, end="")


if __name__ == "__main__":
    main()

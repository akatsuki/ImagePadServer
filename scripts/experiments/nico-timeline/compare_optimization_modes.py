#!/usr/bin/env python3
"""Compare paired sync/PBO benchmark runs without imposing a gain threshold."""

from __future__ import annotations

import argparse
import json
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


def _assert_sample(run: dict[str, Any], requested_mode: str, variant: str) -> None:
    if run.get("variant") != variant:
        raise ValueError(f"expected {variant} sample, got {run.get('variant')!r}")
    if run.get("fallback_reason") or run.get("backend") != "timeline-wgpu":
        raise ValueError(f"fallback or non-WGPU sample is not eligible: {run['_manifest_path']}")
    if run.get("decoded_frames") != run.get("frame_count"):
        raise ValueError(f"decoded frame count mismatch: {run['_manifest_path']}")
    metrics = run.get("sprite_capture_metrics")
    if not isinstance(metrics, dict) or not metrics.get("valid"):
        raise ValueError(f"missing valid capture metrics: {run['_manifest_path']}")
    if metrics.get("requested_mode") != requested_mode or metrics.get("mode") != requested_mode:
        raise ValueError(f"readback mode mismatch: {run['_manifest_path']}")
    if requested_mode == "pbo" and (metrics.get("pbo_textures", 0) <= 0 or metrics.get("pbo_fallbacks", 0) != 0):
        raise ValueError(f"PBO sample had no PBO transfers or used sync fallback: {run['_manifest_path']}")


def _profile(run: dict[str, Any]) -> tuple[Any, ...]:
    return tuple(run.get(key) for key in (
        "encoder", "cpu_percent", "width", "height", "fps_num", "fps_den", "duration_ms",
        "source_sha256", "snapshot_sha256", "ffmpeg_sha256", "helper_sha256", "gpu_backend", "gpu_adapter",
    ))


def _input_profile(run: dict[str, Any]) -> tuple[Any, ...]:
    return tuple(run.get(key) for key in (
        "encoder", "cpu_percent", "width", "height", "fps_num", "fps_den", "duration_ms",
        "source_sha256", "snapshot_sha256", "ffmpeg_sha256",
    ))


def _pair_modes(variants: dict[str, list[dict[str, Any]]], run_root: Path) -> dict[str, Any]:
    baseline = _index_by_pair(variants.get("timeline-sync", []), "timeline-sync")
    candidate = _index_by_pair(variants.get("timeline-pbo", []), "timeline-pbo")
    if baseline.keys() != candidate.keys():
        raise ValueError(f"sync/PBO pair ids differ under {run_root}")
    no_comments = _index_by_pair(variants.get("no-comments", []), "no-comments")
    if no_comments and no_comments.keys() != baseline.keys():
        raise ValueError(f"no-comments pair ids differ under {run_root}")
    deltas = []
    for pair_id in sorted(baseline):
        sync_run, pbo_run = baseline[pair_id], candidate[pair_id]
        _assert_sample(sync_run, "sync", "timeline-sync")
        _assert_sample(pbo_run, "pbo", "timeline-pbo")
        if _profile(sync_run) != _profile(pbo_run):
            raise ValueError(f"sync/PBO profile mismatch for pair {pair_id}")
        if no_comments and _input_profile(sync_run) != _input_profile(no_comments[pair_id]):
            raise ValueError(f"no-comments profile mismatch for pair {pair_id}")
        sync_seconds = float(sync_run["convert_wall_seconds"])
        pbo_seconds = float(pbo_run["convert_wall_seconds"])
        item = {
            "pair_id": pair_id,
            "sync_seconds": sync_seconds,
            "pbo_seconds": pbo_seconds,
            "sync_minus_pbo_seconds": sync_seconds - pbo_seconds,
        }
        if no_comments:
            no_comment_seconds = float(no_comments[pair_id]["convert_wall_seconds"])
            item["no_comments_seconds"] = no_comment_seconds
            item["sync_overhead_seconds"] = sync_seconds - no_comment_seconds
            item["pbo_overhead_seconds"] = pbo_seconds - no_comment_seconds
        deltas.append(item)
    if not deltas:
        raise ValueError(f"no paired sync/PBO samples under {run_root}")
    return {
        "root": str(run_root.resolve()),
        "pairs": deltas,
        "median_sync_seconds": statistics.median(item["sync_seconds"] for item in deltas),
        "median_pbo_seconds": statistics.median(item["pbo_seconds"] for item in deltas),
        "median_sync_minus_pbo_seconds": statistics.median(item["sync_minus_pbo_seconds"] for item in deltas),
    }


def compare_sessions(first_root: Path, second_root: Path, *, seed: int) -> dict[str, Any]:
    first = _pair_modes(_load_runs(first_root), first_root)
    second = _pair_modes(_load_runs(second_root), second_root)
    first_deltas = [item["sync_minus_pbo_seconds"] for item in first["pairs"]]
    second_deltas = [item["sync_minus_pbo_seconds"] for item in second["pairs"]]
    if len(first_deltas) != 10 or len(second_deltas) != 10:
        confirmation: dict[str, Any] = {
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
        "improvement_direction": "sync_minus_pbo_seconds; positive favors PBO",
        "first_session": first,
        "second_session": second,
        "confirmation": confirmation,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--first", type=Path, required=True)
    parser.add_argument("--second", type=Path, required=True)
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

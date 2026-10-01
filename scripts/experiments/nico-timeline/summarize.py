#!/usr/bin/env python3
"""Summarize one-run Nico timeline performance manifests without hiding outliers."""

from __future__ import annotations

import argparse
import json
import math
import statistics
from collections import defaultdict
from pathlib import Path
from typing import Any


def _validated_times(values: list[float]) -> list[float]:
    result: list[float] = []
    for value in values:
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise ValueError(f"time must be numeric: {value!r}")
        number = float(value)
        if not math.isfinite(number) or number < 0:
            raise ValueError(f"time must be finite and non-negative: {value!r}")
        result.append(number)
    return result


def classify_times(seconds: list[float], limit: float = 4.0) -> dict[str, Any]:
    """Accept a profile when its five-run median is below the limit; retain outliers."""
    values = _validated_times(seconds)
    if isinstance(limit, bool) or not isinstance(limit, (int, float)) or not math.isfinite(limit) or limit <= 0:
        raise ValueError("limit must be finite and positive")
    ordered = sorted(values)
    count = len(ordered)
    if count < 5:
        return {
            "count": count,
            "insufficient": True,
            "limit_seconds": float(limit),
            "target_met": False,
            "median": statistics.median(ordered) if ordered else None,
            "maximum": max(ordered) if ordered else None,
            "p95": None,
        }
    p95_index = math.ceil(0.95 * count) - 1
    return {
        "count": count,
        "insufficient": False,
        "limit_seconds": float(limit),
        "target_met": statistics.median(ordered) < limit,
        "median": statistics.median(ordered),
        "maximum": ordered[-1],
        "p95": ordered[p95_index],
    }


def paired_comment_overhead(
    with_comments: dict[str, float], without_comments: dict[str, float]
) -> dict[str, Any]:
    """Return paired wall-time deltas; preserve negative values and require exact IDs."""
    with_ids = set(with_comments)
    without_ids = set(without_comments)
    if with_ids != without_ids:
        raise ValueError(
            "paired run IDs differ: "
            f"with_comments_only={sorted(with_ids - without_ids)}, "
            f"without_comments_only={sorted(without_ids - with_ids)}"
        )
    checked_with = {key: _validated_times([value])[0] for key, value in with_comments.items()}
    checked_without = {key: _validated_times([value])[0] for key, value in without_comments.items()}
    deltas = {key: checked_with[key] - checked_without[key] for key in sorted(with_ids)}
    ordered = list(deltas.values())
    return {
        "count": len(deltas),
        "run_ids": sorted(deltas),
        "seconds_by_run_id": deltas,
        "median_seconds": statistics.median(ordered) if ordered else None,
        "maximum_seconds": max(ordered) if ordered else None,
    }


def _profile_key(run: dict[str, Any]) -> tuple[Any, ...]:
    return (
        run.get("cpu_percent", "unlimited"),
        run.get("encoder"),
        run.get("width"),
        run.get("height"),
        run.get("fps_num"),
        run.get("fps_den"),
        run.get("duration_ms"),
    )


def summarize_runs(root: Path, limit: float = 4.0) -> dict[str, Any]:
    runs: list[dict[str, Any]] = []
    for manifest_path in sorted(root.rglob("run.json")):
        try:
            run = json.loads(manifest_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise ValueError(f"cannot read {manifest_path}: {error}") from error
        if run.get("schema_version") != 1:
            raise ValueError(f"unsupported benchmark manifest schema: {manifest_path}")
        run["manifest_path"] = str(manifest_path.resolve())
        run["convert_wall_seconds"] = _validated_times([run.get("convert_wall_seconds")])[0]
        if not run.get("pair_id"):
            raise ValueError(f"missing pair_id: {manifest_path}")
        runs.append(run)
    if not runs:
        raise ValueError(f"no run.json manifests under {root}")

    profiles: dict[tuple[Any, ...], list[dict[str, Any]]] = defaultdict(list)
    for run in runs:
        profiles[_profile_key(run)].append(run)

    summaries: list[dict[str, Any]] = []
    for profile, profile_runs in sorted(profiles.items(), key=lambda item: str(item[0])):
        input_hashes = {
            (run.get("source_sha256"), run.get("snapshot_sha256")) for run in profile_runs
        }
        if len(input_hashes) != 1:
            raise ValueError(f"source or snapshot hash differs within profile {profile}")
        by_variant: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for run in profile_runs:
            by_variant[str(run.get("variant"))].append(run)
        variants: dict[str, Any] = {}
        for variant, variant_runs in sorted(by_variant.items()):
            ids = [run["pair_id"] for run in variant_runs]
            if len(ids) != len(set(ids)):
                raise ValueError(f"duplicate pair_id for profile {profile}, variant {variant}")
            variants[variant] = {
                "runs": [
                    {
                        "pair_id": run["pair_id"],
                        "run_id": run.get("run_id"),
                        "seconds": run["convert_wall_seconds"],
                        "backend": run.get("backend"),
                        "gpu_backend": run.get("gpu_backend"),
                        "gpu_adapter": run.get("gpu_adapter"),
                        "readback_slots": run.get("readback_slots"),
                        "asset_layout_requested": run.get("asset_layout_requested"),
                        "asset_layout": run.get("asset_layout"),
                        "asset_layout_fallback_reason": run.get("asset_layout_fallback_reason"),
                        "asset_page_count": run.get("asset_page_count"),
                        "asset_source_bytes": run.get("asset_source_bytes"),
                        "asset_allocated_bytes": run.get("asset_allocated_bytes"),
                        "runner_record_path": run.get("runner_record_path"),
                        "manifest_path": run["manifest_path"],
                    }
                    for run in variant_runs
                ],
                "statistics": classify_times(
                    [run["convert_wall_seconds"] for run in variant_runs], limit
                ),
            }

        no_comment_by_id = {
            run["pair_id"]: run["convert_wall_seconds"]
            for run in by_variant.get("no-comments", [])
        }
        overheads: dict[str, Any] = {}
        for variant, variant_runs in by_variant.items():
            if variant == "no-comments" or not no_comment_by_id:
                continue
            with_comments = {
                run["pair_id"]: run["convert_wall_seconds"] for run in variant_runs
            }
            common = set(with_comments) & set(no_comment_by_id)
            if common != set(with_comments) or common != set(no_comment_by_id):
                overheads[variant] = {
                    "status": "unpaired",
                    "with_comments_ids": sorted(with_comments),
                    "without_comments_ids": sorted(no_comment_by_id),
                }
            else:
                overheads[variant] = {
                    "status": "paired",
                    **paired_comment_overhead(with_comments, no_comment_by_id),
                }

        candidates = [
            (name, value["statistics"]["median"])
            for name, value in variants.items()
            if name.startswith(("timeline-slots", "timeline-separate", "timeline-atlas"))
            and not value["statistics"]["insufficient"]
            and value["statistics"]["target_met"]
        ]
        selected = min(candidates, key=lambda item: item[1])[0] if candidates else None
        summaries.append(
            {
                "profile": {
                    "cpu_percent": profile[0],
                    "encoder": profile[1],
                    "width": profile[2],
                    "height": profile[3],
                    "fps_num": profile[4],
                    "fps_den": profile[5],
                    "duration_ms": profile[6],
                    "source_sha256": next(iter(input_hashes))[0],
                    "snapshot_sha256": next(iter(input_hashes))[1],
                },
                "variants": variants,
                "paired_comment_overhead": overheads,
                "fastest_target_passing_timeline_variant": selected,
            }
        )
    return {"schema_version": 1, "limit_seconds": limit, "profiles": summaries}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--limit-seconds", type=float, default=4.0)
    args = parser.parse_args()
    summary = summarize_runs(args.root, args.limit_seconds)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(args.output.resolve())


if __name__ == "__main__":
    main()

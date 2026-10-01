import math
import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent))
from summarize import classify_times, paired_comment_overhead, summarize_runs


def test_three_second_median_passes_and_keeps_slow_outlier_visible():
    result = classify_times([3.199, 3.4, 3.555, 3.8, 4.116])
    assert result["target_met"] is True
    assert result["limit_seconds"] == 4.0
    assert result["median"] == 3.555
    assert result["maximum"] == 4.116
    assert result["p95"] == 4.116


def test_four_second_median_does_not_meet_three_second_range():
    result = classify_times([3.7, 3.8, 4.0, 4.1, 4.2])
    assert result["target_met"] is False
    assert result["median"] == 4.0


def test_fewer_than_five_runs_is_insufficient():
    result = classify_times([0.5, 0.6, 0.7, 0.8])
    assert result["insufficient"] is True
    assert result["target_met"] is False
    assert result["p95"] is None


@pytest.mark.parametrize("values", [[-0.1], [math.nan], [math.inf], [-math.inf]])
def test_classification_rejects_invalid_times(values):
    with pytest.raises(ValueError, match="finite and non-negative"):
        classify_times(values)


def test_paired_overhead_requires_matching_ids():
    with pytest.raises(ValueError, match="paired run IDs differ"):
        paired_comment_overhead({"run-1": 1.0}, {"run-2": 0.5})


def test_paired_overhead_preserves_negative_deltas():
    result = paired_comment_overhead(
        {"run-1": 1.0, "run-2": 0.8},
        {"run-1": 0.9, "run-2": 1.0},
    )
    assert result["seconds_by_run_id"] == {"run-1": pytest.approx(0.1), "run-2": pytest.approx(-0.2)}
    assert result["median_seconds"] == pytest.approx(-0.05)


def test_summary_keeps_atlas_layout_evidence_and_selects_the_faster_measured_variant(tmp_path):
    for index in range(5):
        for variant, seconds, layout, pages in (
            ("timeline-separate", 2.0 + index / 100, "separate", 13),
            ("timeline-atlas", 1.995 + index / 100, "atlas", 1),
        ):
            run_dir = tmp_path / f"pair-{index}" / variant
            run_dir.mkdir(parents=True)
            run = {
                "schema_version": 1,
                "run_id": f"{variant}-{index}",
                "pair_id": f"pair-{index}",
                "variant": variant,
                "encoder": "nvenc",
                "cpu_percent": "unlimited",
                "width": 1920,
                "height": 1080,
                "fps_num": 30,
                "fps_den": 1,
                "duration_ms": 6000,
                "convert_wall_seconds": seconds,
                "source_sha256": "source",
                "snapshot_sha256": "snapshot",
                "backend": "timeline-wgpu",
                "asset_layout_requested": layout,
                "asset_layout": layout,
                "asset_layout_fallback_reason": "",
                "asset_page_count": pages,
                "asset_source_bytes": 1000,
                "asset_allocated_bytes": 1200,
            }
            (run_dir / "run.json").write_text(json.dumps(run), encoding="utf-8")

    report = summarize_runs(tmp_path)
    profile = report["profiles"][0]
    atlas_runs = profile["variants"]["timeline-atlas"]["runs"]
    assert all(run["asset_layout"] == "atlas" for run in atlas_runs)
    assert all(run["asset_page_count"] == 1 for run in atlas_runs)
    assert profile["fastest_target_passing_timeline_variant"] == "timeline-atlas"

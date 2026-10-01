import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent))
from compare_asset_layouts import compare_sessions


def _write_run(root: Path, pair: int, variant: str, seconds: float) -> None:
    layout = "atlas" if variant == "timeline-atlas" else "separate" if variant == "timeline-separate" else ""
    run = {
        "schema_version": 1,
        "run_id": f"{variant}-{pair:02d}",
        "pair_id": f"pair-{pair:02d}",
        "variant": variant,
        "encoder": "x264",
        "cpu_percent": "unlimited",
        "width": 1920,
        "height": 1080,
        "fps_num": 30,
        "fps_den": 1,
        "duration_ms": 6000,
        "source_sha256": "source-hash",
        "snapshot_sha256": "snapshot-hash",
        "ffmpeg_sha256": "ffmpeg-hash",
        "ffmpeg_version": "ffmpeg test build",
        "helper_sha256": "helper-hash" if layout else "",
        "gpu_backend": "dx12" if layout else "",
        "gpu_adapter": "RTX test" if layout else "",
        "readback_slots": 2 if layout else 0,
        "bundle_sha256": "bundle-hash" if layout else "",
        "convert_wall_seconds": seconds,
        "frame_count": 180,
        "decoded_frames": 180,
        "backend": "timeline-wgpu" if layout else "no-comments",
        "fallback_reason": "",
        "asset_layout_requested": layout,
        "asset_layout": layout,
        "asset_layout_fallback_reason": "",
        "asset_page_count": 13 if layout == "separate" else 1 if layout == "atlas" else 0,
        "asset_source_bytes": 1000 if layout else 0,
        "asset_allocated_bytes": 1200 if layout else 0,
    }
    path = root / f"r{pair:02d}" / variant / "run.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(run), encoding="utf-8")


def _make_session(root: Path, improvement: float, *, count: int = 10) -> None:
    for pair in range(count):
        no_comments = 0.7 + pair / 10_000
        separate = 1.8 + pair / 10_000
        atlas = separate - improvement
        _write_run(root, pair, "no-comments", no_comments)
        _write_run(root, pair, "timeline-separate", separate)
        _write_run(root, pair, "timeline-atlas", atlas)


def test_two_sessions_confirm_a_repeatable_millisecond_atlas_gain(tmp_path):
    first = tmp_path / "session-a"
    second = tmp_path / "session-b"
    _make_session(first, 0.001)
    _make_session(second, 0.0012)

    result = compare_sessions(first, second, seed=7)

    assert result["confirmation"]["status"] == "confirmed_improvement"
    assert result["improvement_direction"] == "separate_minus_atlas_seconds; positive favors Atlas"
    assert result["confirmation"]["median_seconds"] == pytest.approx(0.0011)
    assert result["confirmation"]["ci95_low_seconds"] > 0
    assert len(result["first_session"]["pairs"]) == 10
    assert result["first_session"]["pairs"][0]["separate_minus_atlas_seconds"] == pytest.approx(0.001)


def test_atlas_comparison_rejects_fallback_and_mismatched_ring(tmp_path):
    first = tmp_path / "first"
    second = tmp_path / "second"
    _make_session(first, 0.001)
    _make_session(second, 0.001)

    atlas_path = first / "r00" / "timeline-atlas" / "run.json"
    atlas = json.loads(atlas_path.read_text(encoding="utf-8"))
    atlas["asset_layout"] = "separate"
    atlas["asset_layout_fallback_reason"] = "atlas page limit"
    atlas_path.write_text(json.dumps(atlas), encoding="utf-8")
    with pytest.raises(ValueError, match="actual atlas layout"):
        compare_sessions(first, second, seed=1)

    _make_session(first, 0.001)
    atlas = json.loads(atlas_path.read_text(encoding="utf-8"))
    atlas["readback_slots"] = 3
    atlas_path.write_text(json.dumps(atlas), encoding="utf-8")
    with pytest.raises(ValueError, match="profile mismatch"):
        compare_sessions(first, second, seed=1)


def test_two_sessions_with_fewer_than_ten_pairs_are_not_confirmed(tmp_path):
    first = tmp_path / "first"
    second = tmp_path / "second"
    _make_session(first, 0.001, count=5)
    _make_session(second, 0.001, count=5)

    result = compare_sessions(first, second, seed=1)

    assert result["confirmation"]["status"] == "insufficient_pairs"

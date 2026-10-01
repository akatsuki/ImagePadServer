import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent))
from compare_optimization_modes import compare_sessions


def _write_run(root: Path, pair: int, variant: str, seconds: float) -> None:
    mode = "pbo" if variant == "timeline-pbo" else "sync" if variant == "timeline-sync" else None
    run = {
        "schema_version": 1,
        "pair_id": f"pair-{pair:02d}",
        "variant": variant,
        "encoder": "nvenc",
        "cpu_percent": "unlimited",
        "width": 1920,
        "height": 1080,
        "fps_num": 30,
        "fps_den": 1,
        "duration_ms": 6000,
        "source_sha256": "source-hash",
        "snapshot_sha256": "snapshot-hash",
        "ffmpeg_sha256": "ffmpeg-hash",
        "helper_sha256": "helper-hash" if mode else "",
        "gpu_backend": "vulkan" if mode else "",
        "gpu_adapter": "RTX test" if mode else "",
        "convert_wall_seconds": seconds,
        "frame_count": 180,
        "decoded_frames": 180,
        "backend": "timeline-wgpu" if mode else "no-comments",
        "sprite_capture_metrics": None if mode is None else {
            "valid": True,
            "requested_mode": mode,
            "mode": mode,
            "pbo_textures": 12 if mode == "pbo" else 0,
            "pbo_fallbacks": 0,
        },
    }
    path = root / f"r{pair:02d}" / variant / "run.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(run), encoding="utf-8")


def _make_session(root: Path, improvement: float) -> None:
    for pair in range(10):
        no_comments = 0.7 + pair / 10_000
        sync = 1.8 + pair / 10_000
        pbo = sync - improvement
        _write_run(root, pair, "no-comments", no_comments)
        _write_run(root, pair, "timeline-sync", sync)
        _write_run(root, pair, "timeline-pbo", pbo)


def test_two_session_comparison_confirms_small_repeatable_gain(tmp_path):
    first = tmp_path / "session-a"
    second = tmp_path / "session-b"
    _make_session(first, 0.001)
    _make_session(second, 0.0012)

    result = compare_sessions(first, second, seed=7)

    assert result["confirmation"]["status"] == "confirmed_improvement"
    assert result["confirmation"]["median_seconds"] == pytest.approx(0.0011)
    assert len(result["first_session"]["pairs"]) == 10
    assert result["first_session"]["pairs"][0]["pbo_overhead_seconds"] < result["first_session"]["pairs"][0]["sync_overhead_seconds"]


def test_mode_mismatch_fallback_and_pair_mismatch_are_rejected(tmp_path):
    first = tmp_path / "first"
    second = tmp_path / "second"
    _make_session(first, 0.001)
    _make_session(second, 0.001)
    candidate = next((first / "r00").rglob("timeline-pbo/run.json"))
    run = json.loads(candidate.read_text(encoding="utf-8"))
    run["sprite_capture_metrics"]["pbo_fallbacks"] = 1
    candidate.write_text(json.dumps(run), encoding="utf-8")

    with pytest.raises(ValueError, match="sync fallback"):
        compare_sessions(first, second, seed=1)

    _make_session(first, 0.001)
    missing = next((first / "r00").rglob("timeline-pbo/run.json"))
    missing.unlink()
    with pytest.raises(ValueError, match="pair ids differ"):
        compare_sessions(first, second, seed=1)


def test_not_enough_pairs_is_explicitly_unconfirmed(tmp_path):
    first = tmp_path / "first"
    second = tmp_path / "second"
    for pair in range(5):
        _write_run(first, pair, "no-comments", 0.7)
        _write_run(first, pair, "timeline-sync", 1.8)
        _write_run(first, pair, "timeline-pbo", 1.79)
        _write_run(second, pair, "no-comments", 0.7)
        _write_run(second, pair, "timeline-sync", 1.8)
        _write_run(second, pair, "timeline-pbo", 1.79)

    result = compare_sessions(first, second, seed=1)

    assert result["confirmation"]["status"] == "insufficient_pairs"

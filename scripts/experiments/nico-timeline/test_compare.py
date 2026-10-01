import gzip
import hashlib
from pathlib import Path

import pytest

from compare import compare_rgba, main


def test_rgba_comparison_rejects_missing_last_frame(tmp_path):
    reference = tmp_path / "reference.rgba"
    candidate = tmp_path / "candidate.rgba"
    reference.write_bytes(bytes([255, 0, 0, 255]) * 2)
    candidate.write_bytes(bytes([255, 0, 0, 255]))

    with pytest.raises(ValueError, match="length"):
        compare_rgba(reference, candidate, 1, 1, 2)


def test_rgba_comparison_rejects_trailing_bytes(tmp_path):
    reference = tmp_path / "reference.rgba"
    candidate = tmp_path / "candidate.rgba"
    reference.write_bytes(bytes([0, 0, 0, 0]))
    candidate.write_bytes(bytes([0, 0, 0, 0, 1]))

    with pytest.raises(ValueError, match="length"):
        compare_rgba(reference, candidate, 1, 1, 1)


def test_rgba_comparison_reports_every_frame_and_writes_difference_images(tmp_path):
    reference_bytes = bytes([10, 20, 30, 255, 40, 50, 60, 0])
    candidate_bytes = bytes([10, 20, 30, 255, 42, 50, 60, 1])
    reference = tmp_path / "reference.rgba.gz"
    candidate = tmp_path / "candidate.rgba"
    diff_dir = tmp_path / "diff"
    with gzip.open(reference, "wb") as stream:
        stream.write(reference_bytes)
    candidate.write_bytes(candidate_bytes)

    result = compare_rgba(
        reference,
        candidate,
        width=1,
        height=1,
        frames=2,
        tolerance=1,
        diff_dir=diff_dir,
    )

    assert result["reference_rgba_sha256"] == hashlib.sha256(reference_bytes).hexdigest()
    assert result["candidate_rgba_sha256"] == hashlib.sha256(candidate_bytes).hexdigest()
    assert result["max_channel_diff"] == 2
    assert result["pixels_over_tolerance"] == 1
    assert result["different_pixels"] == 1
    assert [frame["frame"] for frame in result["frame_metrics"]] == [0, 1]
    assert result["frame_metrics"][0]["max_channel_diff"] == 0
    assert result["frame_metrics"][1]["pixels_over_tolerance"] == 1
    assert len(result["difference_images"]) == 1
    assert (diff_dir / "frame-000001-diff.png").is_file()


def test_rgba_comparison_tolerance_does_not_hide_maximum_difference(tmp_path):
    reference = tmp_path / "reference.rgba"
    candidate = tmp_path / "candidate.rgba"
    reference.write_bytes(bytes([10, 20, 30, 40]))
    candidate.write_bytes(bytes([11, 19, 30, 40]))

    result = compare_rgba(reference, candidate, 1, 1, 1, tolerance=1)

    assert result["max_channel_diff"] == 1
    assert result["pixels_over_tolerance"] == 0
    assert result["different_pixels"] == 1


def test_rgba_comparison_reports_and_can_require_exact_transparent_exterior(
    tmp_path, monkeypatch
):
    reference = tmp_path / "reference.rgba"
    candidate = tmp_path / "candidate.rgba"
    reference_bytes = bytes([10, 20, 30, 255, 0, 0, 0, 0])
    candidate_bytes = bytes([11, 20, 30, 255, 1, 0, 0, 0])
    reference.write_bytes(reference_bytes)
    candidate.write_bytes(candidate_bytes)

    result = compare_rgba(reference, candidate, width=2, height=1, frames=1, tolerance=1)

    assert result["transparent_exterior_pixels"] == 1
    assert result["transparent_exterior_different_pixels"] == 1
    assert result["transparent_exterior_exact"] is False
    assert result["pixels_over_tolerance"] == 0

    monkeypatch.setattr(
        "sys.argv",
        [
            "compare.py",
            str(reference),
            str(candidate),
            "--width",
            "2",
            "--height",
            "1",
            "--frames",
            "1",
            "--tolerance",
            "1",
            "--require-transparent-exterior-exact",
        ],
    )
    assert main() == 1


def test_rgba_comparison_keeps_first_and_worst_diff_previews(tmp_path):
    reference = tmp_path / "reference.rgba"
    candidate = tmp_path / "candidate.rgba"
    reference.write_bytes(bytes([0, 0, 0, 0]) * 4)
    candidate.write_bytes(bytes([1, 0, 0, 0, 0, 0, 0, 0, 10, 0, 0, 0, 1, 0, 0, 0]))

    result = compare_rgba(
        reference,
        candidate,
        width=1,
        height=1,
        frames=4,
        tolerance=1,
        diff_dir=tmp_path / "diff",
    )

    assert result["different_frames"] == 3
    assert result["max_channel_diff"] == 10
    assert result["difference_images_truncated"] is True
    assert [Path(path).name for path in result["difference_images"]] == [
        "frame-000000-diff.png",
        "frame-000002-diff.png",
    ]


@pytest.mark.parametrize(
    ("width", "height", "frames", "tolerance"),
    [(0, 1, 1, 1), (1, 0, 1, 1), (1, 1, 0, 1), (1, 1, 1, -1), (1, 1, 1, 256)],
)
def test_rgba_comparison_rejects_invalid_dimensions_or_tolerance(
    tmp_path, width, height, frames, tolerance
):
    reference = tmp_path / "reference.rgba"
    candidate = tmp_path / "candidate.rgba"
    reference.write_bytes(b"")
    candidate.write_bytes(b"")

    with pytest.raises(ValueError, match="(dimensions|tolerance)"):
        compare_rgba(reference, candidate, width, height, frames, tolerance=tolerance)

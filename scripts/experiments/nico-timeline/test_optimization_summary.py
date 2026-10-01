import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent))
from optimization_summary import confirm_paired_improvement


def test_repeatable_one_millisecond_gain_is_confirmed_without_a_size_floor():
    session_deltas = {
        "morning": [0.0010] * 10,
        "afternoon": [0.0012] * 10,
    }

    result = confirm_paired_improvement(session_deltas, seed=7)

    assert result["status"] == "confirmed_improvement"
    assert result["median_seconds"] == pytest.approx(0.0011)
    assert result["ci95_low_seconds"] > 0
    assert result["session_medians_seconds"] == {
        "morning": pytest.approx(0.0010),
        "afternoon": pytest.approx(0.0012),
    }


def test_improvement_seen_in_only_one_session_is_not_confirmed():
    session_deltas = {
        "morning": [0.010] * 10,
        "afternoon": [-0.001] * 10,
    }

    result = confirm_paired_improvement(session_deltas, seed=7)

    assert result["status"] == "improvement_unconfirmed"
    assert result["session_medians_seconds"]["morning"] > 0
    assert result["session_medians_seconds"]["afternoon"] < 0


def test_repeatable_regression_is_reported_without_a_size_floor():
    session_deltas = {
        "morning": [-0.010] * 10,
        "afternoon": [-0.012] * 10,
    }

    result = confirm_paired_improvement(session_deltas, seed=7)

    assert result["status"] == "confirmed_regression"
    assert result["ci95_high_seconds"] < 0


def test_zero_effect_is_unconfirmed_and_bootstrap_is_reproducible():
    session_deltas = {
        "morning": [0.0] * 10,
        "afternoon": [0.0] * 10,
    }

    first = confirm_paired_improvement(session_deltas, seed=99)
    second = confirm_paired_improvement(session_deltas, seed=99)

    assert first == second
    assert first["status"] == "improvement_unconfirmed"
    assert first["ci95_low_seconds"] == 0
    assert first["ci95_high_seconds"] == 0


@pytest.mark.parametrize(
    "session_deltas",
    [
        {"morning": [0.001] * 10},
        {"morning": [0.001] * 10, "afternoon": [0.001] * 9},
        {"morning": [0.001] * 10, "afternoon": [float("nan")] * 10},
    ],
)
def test_confirmation_requires_two_valid_ten_pair_sessions(session_deltas):
    with pytest.raises(ValueError):
        confirm_paired_improvement(session_deltas, seed=3)

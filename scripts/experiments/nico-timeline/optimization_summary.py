#!/usr/bin/env python3
"""Judge small paired Nico optimization gains across two measurement sessions."""

from __future__ import annotations

import math
import random
import statistics
from collections.abc import Mapping, Sequence
from typing import Any


def confirm_paired_improvement(
    session_deltas: Mapping[str, Sequence[float]], *, seed: int
) -> dict[str, Any]:
    """Confirm positive baseline-minus-candidate deltas in both sessions.

    Each session must contain ten paired samples. The stratified bootstrap draws
    ten deltas with replacement from each session, then records the median of all
    twenty deltas. No minimum percentage or duration threshold is applied.
    """
    if len(session_deltas) != 2 or any(not isinstance(name, str) or not name for name in session_deltas):
        raise ValueError("exactly two named confirmation sessions are required")
    if isinstance(seed, bool) or not isinstance(seed, int):
        raise ValueError("seed must be an integer")

    checked: dict[str, list[float]] = {}
    for name, samples in session_deltas.items():
        if isinstance(samples, (str, bytes)) or len(samples) != 10:
            raise ValueError(f"session {name!r} must contain exactly ten paired deltas")
        values: list[float] = []
        for value in samples:
            if isinstance(value, bool) or not isinstance(value, (int, float)):
                raise ValueError(f"paired delta must be numeric: {value!r}")
            delta = float(value)
            if not math.isfinite(delta):
                raise ValueError(f"paired delta must be finite: {value!r}")
            values.append(delta)
        checked[name] = values

    rng = random.Random(seed)
    medians: list[float] = []
    for _ in range(10_000):
        resampled = [rng.choice(values) for values in checked.values() for _ in range(10)]
        medians.append(statistics.median(resampled))
    medians.sort()
    low = medians[math.ceil(0.025 * len(medians)) - 1]
    high = medians[math.ceil(0.975 * len(medians)) - 1]
    session_medians = {
        name: statistics.median(values) for name, values in checked.items()
    }
    confirmed_improvement = all(value > 0 for value in session_medians.values()) and low > 0
    confirmed_regression = all(value < 0 for value in session_medians.values()) and high < 0
    all_samples = [value for values in checked.values() for value in values]
    return {
        "status": (
            "confirmed_improvement"
            if confirmed_improvement
            else "confirmed_regression"
            if confirmed_regression
            else "improvement_unconfirmed"
        ),
        "sessions": len(checked),
        "pairs_per_session": 10,
        "pairs_total": len(all_samples),
        "median_seconds": statistics.median(all_samples),
        "ci95_low_seconds": low,
        "ci95_high_seconds": high,
        "session_medians_seconds": session_medians,
        "bootstrap_resamples": len(medians),
        "bootstrap_seed": seed,
    }

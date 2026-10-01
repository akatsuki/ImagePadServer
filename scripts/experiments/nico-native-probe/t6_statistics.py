"""Build fail-closed T6 paired timing statistics."""

from __future__ import annotations

import argparse
import json
import random
from pathlib import Path
from typing import Any


REQUIRED_SCENARIOS = ("fixed-6s", "high-density-10s", "real-source-155s")
DEFAULT_RESAMPLES = 10_000
DEFAULT_SEED = 20260922
MIN_PAIRS = 40
TEE_MIN_SPEEDUP_PERCENT = 5.0
TEE_MIN_SAVING_SECONDS = 0.25


def _percentile_type7(values: list[float], probability: float) -> float:
    if not values:
        raise ValueError("percentile requires at least one value")
    ordered = sorted(values)
    if len(ordered) == 1:
        return ordered[0]
    position = (len(ordered) - 1) * probability
    lower = int(position)
    upper = min(lower + 1, len(ordered) - 1)
    fraction = position - lower
    return ordered[lower] + (ordered[upper] - ordered[lower]) * fraction


def _worker_runs(report: dict[str, Any]) -> list[dict[str, Any]]:
    worker = report.get("worker")
    if not isinstance(worker, dict) or worker.get("complete") is not True:
        raise ValueError("worker stability report is incomplete")
    runs = worker.get("runs")
    if not isinstance(runs, list):
        raise ValueError("worker runs are missing")
    result: list[dict[str, Any]] = []
    for run in runs:
        if not isinstance(run, dict) or run.get("result_ok") is not True:
            raise ValueError("worker run artifact/event failed")
        elapsed = run.get("elapsed_s")
        if not isinstance(elapsed, (int, float)) or elapsed <= 0:
            raise ValueError("worker run elapsed_s is missing or invalid")
        result.append(run)
    return result


def _wall_values(reports: list[dict[str, Any]]) -> list[float]:
    values: list[float] = []
    for report in reports:
        values.extend(float(run["elapsed_s"]) for run in _worker_runs(report))
    return values


def _cpu_total(reports: list[dict[str, Any]]) -> float | None:
    total = 0.0
    for report in reports:
        runner = report.get("runner_report")
        nested = runner.get("report") if isinstance(runner, dict) else None
        value = nested.get("cpu_seconds") if isinstance(nested, dict) else None
        if not isinstance(value, (int, float)):
            return None
        total += float(value)
    return total


def _bootstrap(
    baseline: list[float],
    candidate: list[float],
    *,
    resamples: int,
    rng: random.Random,
) -> dict[str, float | int]:
    if len(baseline) != len(candidate) or not baseline:
        raise ValueError("paired timing arrays must have equal non-zero length")
    median_speedups: list[float] = []
    p95_deltas: list[float] = []
    size = len(baseline)
    for _ in range(resamples):
        indexes = [rng.randrange(size) for _ in range(size)]
        base_sample = [baseline[index] for index in indexes]
        candidate_sample = [candidate[index] for index in indexes]
        pair_speedups = [
            (base / new - 1.0) * 100.0
            for base, new in zip(base_sample, candidate_sample)
        ]
        median_speedups.append(_percentile_type7(pair_speedups, 0.5))
        base_p95 = _percentile_type7(base_sample, 0.95)
        candidate_p95 = _percentile_type7(candidate_sample, 0.95)
        p95_deltas.append((candidate_p95 / base_p95 - 1.0) * 100.0)
    return {
        "resamples": resamples,
        "median_speedup_ci_lower_percent": _percentile_type7(median_speedups, 0.025),
        "median_speedup_ci_upper_percent": _percentile_type7(median_speedups, 0.975),
        "p95_delta_ci_lower_percent": _percentile_type7(p95_deltas, 0.025),
        "p95_delta_ci_upper_percent": _percentile_type7(p95_deltas, 0.975),
    }


def build_statistics(
    scenarios: dict[str, dict[str, Any]],
    *,
    resamples: int = DEFAULT_RESAMPLES,
    seed: int = DEFAULT_SEED,
) -> dict[str, Any]:
    reasons: list[str] = []
    if resamples != DEFAULT_RESAMPLES:
        reasons.append(f"bootstrap_resamples_must_be_{DEFAULT_RESAMPLES}")
    pairs: dict[str, int] = {}
    scenario_results: dict[str, Any] = {}
    cpu_total_not_increased = True
    threshold_met = True
    global_median_lower: float | None = None
    global_p95_upper: float | None = None
    rng = random.Random(seed)

    for scenario in REQUIRED_SCENARIOS:
        spec = scenarios.get(scenario)
        if not isinstance(spec, dict):
            reasons.append(f"{scenario}:scenario_missing")
            continue
        baseline_reports = spec.get("baseline_reports")
        candidate_reports = spec.get("candidate_reports")
        if not isinstance(baseline_reports, list) or not isinstance(candidate_reports, list):
            reasons.append(f"{scenario}:reports_missing")
            continue
        try:
            baseline = _wall_values(baseline_reports)
            candidate = _wall_values(candidate_reports)
        except (TypeError, ValueError) as exc:
            reasons.append(f"{scenario}:invalid_report:{exc}")
            continue
        count = min(len(baseline), len(candidate))
        pairs[scenario] = count
        if len(baseline) != len(candidate):
            reasons.append(f"{scenario}:baseline_candidate_pair_count_mismatch")
        if count < MIN_PAIRS:
            reasons.append(f"{scenario}:under_{MIN_PAIRS}_pairs")
        if count == 0:
            continue
        baseline = baseline[:count]
        candidate = candidate[:count]
        try:
            bootstrap = _bootstrap(
                baseline,
                candidate,
                resamples=resamples,
                rng=rng,
            )
        except ValueError as exc:
            reasons.append(f"{scenario}:bootstrap_failed:{exc}")
            continue
        median_speedup = _percentile_type7(
            [(base / new - 1.0) * 100.0 for base, new in zip(baseline, candidate)],
            0.5,
        )
        median_saving = _percentile_type7(
            [base - new for base, new in zip(baseline, candidate)], 0.5
        )
        cpu_baseline = _cpu_total(baseline_reports)
        cpu_candidate = _cpu_total(candidate_reports)
        if cpu_baseline is None or cpu_candidate is None:
            reasons.append(f"{scenario}:cpu_total_missing")
            cpu_total_not_increased = False
        elif cpu_candidate > cpu_baseline:
            cpu_total_not_increased = False
        scenario_threshold = (
            median_speedup >= TEE_MIN_SPEEDUP_PERCENT
            or median_saving >= TEE_MIN_SAVING_SECONDS
        )
        threshold_met = threshold_met and scenario_threshold
        if not scenario_threshold:
            reasons.append(f"{scenario}:candidate_threshold_unmet")
        lower = float(bootstrap["median_speedup_ci_lower_percent"])
        upper = float(bootstrap["p95_delta_ci_upper_percent"])
        global_median_lower = lower if global_median_lower is None else min(global_median_lower, lower)
        global_p95_upper = upper if global_p95_upper is None else max(global_p95_upper, upper)
        scenario_results[scenario] = {
            "pairs": count,
            "baseline_wall_median_s": _percentile_type7(baseline, 0.5),
            "candidate_wall_median_s": _percentile_type7(candidate, 0.5),
            "baseline_wall_p95_s": _percentile_type7(baseline, 0.95),
            "candidate_wall_p95_s": _percentile_type7(candidate, 0.95),
            "median_speedup_percent": median_speedup,
            "median_wall_saving_s": median_saving,
            "bootstrap": bootstrap,
            "cpu_baseline_s": cpu_baseline,
            "cpu_candidate_s": cpu_candidate,
            "candidate_threshold_met": scenario_threshold,
        }

    status = "complete" if not reasons else "blocked"
    return {
        "schema_version": 1,
        "status": status,
        "reasons": reasons,
        "seed": seed,
        "pairs": pairs,
        "scenarios": scenario_results,
        "bootstrap": {
            "resamples": resamples,
            "median_speedup_ci_lower_percent": global_median_lower,
            "p95_delta_ci_upper_percent": global_p95_upper,
        },
        "cpu_total_not_increased": cpu_total_not_increased,
        "candidate_threshold_met": threshold_met,
    }


def _load_reports(paths: list[str]) -> list[dict[str, Any]]:
    return [json.loads(Path(path).read_text(encoding="utf-8")) for path in paths]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--seed", type=int, default=DEFAULT_SEED)
    parser.add_argument("--resamples", type=int, default=DEFAULT_RESAMPLES)
    args = parser.parse_args()
    manifest = json.loads(args.input.read_text(encoding="utf-8"))
    source = manifest.get("scenarios") if isinstance(manifest, dict) else None
    if not isinstance(source, dict):
        raise ValueError("input must contain scenarios object")
    scenarios: dict[str, dict[str, Any]] = {}
    for name, spec in source.items():
        if not isinstance(spec, dict):
            continue
        scenarios[name] = {
            "baseline_reports": _load_reports(list(spec.get("baseline_reports", []))),
            "candidate_reports": _load_reports(list(spec.get("candidate_reports", []))),
        }
    result = build_statistics(scenarios, resamples=args.resamples, seed=args.seed)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output), "status": result["status"], "reasons": result["reasons"]}, ensure_ascii=False))
    return 0 if result["status"] == "complete" else 1


if __name__ == "__main__":
    raise SystemExit(main())

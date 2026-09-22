"""Summarize a PresentMon CSV without turning telemetry into an acceptance PASS."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any

from t11_observe import parse_presentmon_csv, summarize_presentmon_rows


MAX_REGRESSION_RATIO = 0.10


def _frame_interval_metric(summary: dict[str, Any], percentile: str) -> float | None:
    if not isinstance(summary, dict):
        return None
    values = summary.get("ms_between_presents")
    if not isinstance(values, dict):
        return None
    value = values.get(percentile)
    if isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0:
        return float(value)
    return None


def evaluate_presentmon_acceptance(
    *,
    export_window: dict[str, Any],
    baseline_before: dict[str, Any],
    baseline_after: dict[str, Any] | None = None,
    continuous_hitching: bool | None = None,
    max_regression_ratio: float = MAX_REGRESSION_RATIO,
) -> dict[str, Any]:
    """Evaluate the declared T11 frame-time gate without inventing missing data.

    Frame interval p95 is the tail-latency gate. p99 is retained as the
    frame-time representation of the 1% low gate. The pre/post baseline is
    averaged only after their absolute drift is within the declared limit.
    Continuous hitching is an explicit observation; an omitted value stays
    pending rather than being inferred from PresentMon's Dropped column.
    """
    reasons: list[str] = []
    metrics: dict[str, float | None] = {}
    values: dict[str, float | None] = {}
    for name, summary in (
        ("export_p95", export_window),
        ("export_p99", export_window),
        ("baseline_before_p95", baseline_before),
        ("baseline_before_p99", baseline_before),
        ("baseline_after_p95", baseline_after),
        ("baseline_after_p99", baseline_after),
    ):
        percentile = "p99" if name.endswith("p99") else "p95"
        values[name] = _frame_interval_metric(summary, percentile)
        if values[name] is None:
            reasons.append(f"{name}_unavailable")

    if not reasons:
        before_p95 = float(values["baseline_before_p95"])
        after_p95 = float(values["baseline_after_p95"])
        before_p99 = float(values["baseline_before_p99"])
        after_p99 = float(values["baseline_after_p99"])
        reference_p95 = (before_p95 + after_p95) / 2.0
        reference_p99 = (before_p99 + after_p99) / 2.0
        baseline_p95_change = abs(after_p95 - before_p95) / before_p95
        baseline_p99_change = abs(after_p99 - before_p99) / before_p99
        export_p95_change = (float(values["export_p95"]) - reference_p95) / reference_p95
        export_p99_change = (float(values["export_p99"]) - reference_p99) / reference_p99
        metrics.update(
            {
                "baseline_p95_change_ratio": baseline_p95_change,
                "baseline_p99_change_ratio": baseline_p99_change,
                "baseline_p95_reference_ms": reference_p95,
                "baseline_p99_reference_ms": reference_p99,
                "export_p95_change_ratio": export_p95_change,
                "export_p99_change_ratio": export_p99_change,
            }
        )
        if baseline_p95_change > max_regression_ratio:
            reasons.append("baseline_p95_drift_over_10_percent")
        if baseline_p99_change > max_regression_ratio:
            reasons.append("baseline_p99_drift_over_10_percent")
        if export_p95_change > max_regression_ratio:
            reasons.append("export_p95_regression_over_10_percent")
        if export_p99_change > max_regression_ratio:
            reasons.append("export_p99_regression_over_10_percent")

    if continuous_hitching is None:
        reasons.append("continuous_hitching_unavailable")
    elif continuous_hitching:
        reasons.append("continuous_hitching_detected")

    hard_failures = [reason for reason in reasons if reason != "continuous_hitching_unavailable" and not reason.endswith("_unavailable")]
    status = "fail" if hard_failures else ("pending" if reasons else "pass")
    return {
        "status": status,
        "max_regression_ratio": max_regression_ratio,
        "reasons": reasons,
        "metrics": metrics,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--csv", required=True, type=Path)
    parser.add_argument("--process-id", required=True, type=int)
    parser.add_argument("--start-time")
    parser.add_argument("--end-time")
    parser.add_argument("--baseline-start-time")
    parser.add_argument("--baseline-end-time")
    parser.add_argument("--post-baseline-start-time")
    parser.add_argument("--post-baseline-end-time")
    parser.add_argument("--baseline-csv", type=Path)
    parser.add_argument("--continuous-hitching", choices=("true", "false"), help="explicit audit result; omit when not inspected")
    parser.add_argument("--output", type=Path, help="write the UTF-8 JSON summary to this path")
    args = parser.parse_args(argv)

    rows = parse_presentmon_csv(
        args.csv.read_text(encoding="utf-8"), process_id=args.process_id
    )
    result = {
        "source_csv": str(args.csv),
        "target_process_id": args.process_id,
        "export_window_csv_clock": {
            "start": args.start_time,
            "end": args.end_time,
        },
        "export_window": summarize_presentmon_rows(
            rows, start_time=args.start_time, end_time=args.end_time
        ),
        "full_capture": summarize_presentmon_rows(rows),
    }
    if args.baseline_csv is not None:
        baseline_rows = parse_presentmon_csv(
            args.baseline_csv.read_text(encoding="utf-8"), process_id=args.process_id
        )
        result["baseline_smoke_csv"] = str(args.baseline_csv)
        result["baseline_smoke"] = summarize_presentmon_rows(baseline_rows)
    if args.baseline_start_time or args.baseline_end_time:
        result["paired_baseline_same_csv"] = summarize_presentmon_rows(
            rows,
            start_time=args.baseline_start_time,
            end_time=args.baseline_end_time,
        )
    if args.post_baseline_start_time or args.post_baseline_end_time:
        result["paired_post_baseline_same_csv"] = summarize_presentmon_rows(
            rows,
            start_time=args.post_baseline_start_time,
            end_time=args.post_baseline_end_time,
        )
    result["acceptance"] = evaluate_presentmon_acceptance(
        export_window=result["export_window"],
        baseline_before=result.get("paired_baseline_same_csv", {}),
        baseline_after=result.get("paired_post_baseline_same_csv"),
        continuous_hitching=(
            None
            if args.continuous_hitching is None
            else args.continuous_hitching == "true"
        ),
    )
    serialized = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    if args.output is not None:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(serialized, encoding="utf-8")
    print(serialized, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Fail-closed T6 acceptance evaluator for the Nico comment exporter.

The runner is intentionally separate from the long-running benchmark. It
evaluates persisted baseline/candidate evidence and never converts a skipped,
failed, or missing VRChat observation into a pass.
"""

from __future__ import annotations

import argparse
import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


CPU_BUDGET_PERCENT = 20
REQUIRED_VRCHAT_SEGMENTS = ("baseline-before", "candidate", "baseline-after")
REQUIRED_SCENARIOS = ("fixed-6s", "high-density-10s", "real-source-155s")
REQUIRED_QUALITY_MATRIX = (
    "no-comments",
    "alpha",
    "multicolor-overlap",
    "clip",
    "negative-rect",
    "aspect-4x3",
    "aspect-1x1",
    "aspect-portrait",
    "fps-30",
    "fps-29.97",
    "fps-59.94",
    "vfr",
    "audio-none",
    "audio-delay",
    "short-tail",
)


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _read_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError(f"JSON object required: {path}")
    return value


def _reason(report: dict[str, Any], label: str) -> list[str]:
    reasons: list[str] = []
    if report.get("cpu_budget_percent") != CPU_BUDGET_PERCENT:
        reasons.append(f"{label}:cpu_budget_not_20_percent")
    if not report.get("runner_verified"):
        reasons.append(f"{label}:runner_cpu_budget_unverified")
    runner = report.get("runner_report")
    if not isinstance(runner, dict) or runner.get("budget", {}).get("Percent") != CPU_BUDGET_PERCENT:
        reasons.append(f"{label}:runner_budget_missing_or_mismatched")
    worker = report.get("worker")
    if not isinstance(worker, dict) or not worker.get("complete"):
        reasons.append(f"{label}:worker_stability_incomplete")
    runs = worker.get("runs", []) if isinstance(worker, dict) else []
    if not runs:
        reasons.append(f"{label}:no_worker_iterations")
    for run in runs:
        if not run.get("result_ok"):
            reasons.append(f"{label}:iteration_{run.get('iteration', '?')}_artifact_or_event_failed")
    if report.get("timed_out"):
        reasons.append(f"{label}:timed_out")
    if report.get("exit_code") != 0:
        reasons.append(f"{label}:process_exit_{report.get('exit_code')}")
    return reasons


def evaluate_worker_report(
    report: dict[str, Any],
    *,
    label: str,
    expected_output_mode: str,
) -> dict[str, Any]:
    reasons = _reason(report, label)
    actual_mode = str(report.get("output_mode") or "")
    if actual_mode != expected_output_mode:
        reasons.append(f"{label}:output_mode_{actual_mode or 'missing'}_expected_{expected_output_mode}")
    inputs = report.get("inputs")
    if not isinstance(inputs, dict):
        reasons.append(f"{label}:input_fingerprint_missing")
    else:
        for name in ("worker_sha256", "ffmpeg_sha256", "source_sha256", "snapshot_sha256"):
            if not inputs.get(name):
                reasons.append(f"{label}:{name}_missing")
    observation = report.get("observation")
    if not isinstance(observation, dict):
        reasons.append(f"{label}:observation_missing")
    if not isinstance(report.get("parameters"), dict):
        reasons.append(f"{label}:parameters_missing")
    return {
        "status": "pass" if not reasons else "blocked",
        "label": label,
        "backend": str(report.get("backend") or "") or None,
        "output_mode": actual_mode or None,
        "reasons": reasons,
        "worker_iterations": int((report.get("worker") or {}).get("iterations", 0)),
        "wall_s": float((report.get("worker") or {}).get("duration_s", 0.0) or 0.0),
    }


def _input_fingerprint(report: dict[str, Any]) -> dict[str, Any] | None:
    inputs = report.get("inputs")
    if not isinstance(inputs, dict):
        return None
    backend = str(report.get("backend") or "").strip()
    if backend not in {"browser", "native"}:
        return None
    renderer_key = "browser_sha256" if backend == "browser" else "compositor_sha256"
    keys = ("ffmpeg_sha256", "source_sha256", "snapshot_sha256", renderer_key)
    if any(not inputs.get(key) for key in keys):
        return None
    return {"backend": backend, **{key: inputs[key] for key in keys}}


def evaluate_vrchat_report(report: dict[str, Any] | None) -> dict[str, Any]:
    if report is None:
        return {
            "status": "pending",
            "reasons": ["vrchat_evidence_missing"],
            "segments": [],
        }
    segments = report.get("segments")
    if not isinstance(segments, list):
        return {"status": "blocked", "reasons": ["vrchat_segments_missing"], "segments": []}
    by_name = {str(item.get("name")): item for item in segments if isinstance(item, dict)}
    reasons: list[str] = []
    for name in REQUIRED_VRCHAT_SEGMENTS:
        item = by_name.get(name)
        if not item:
            reasons.append(f"vrchat_segment_{name}_missing")
            continue
        if float(item.get("duration_s", 0) or 0) < 60:
            reasons.append(f"vrchat_segment_{name}_under_60_seconds")
        frame = item.get("frame_time_ms")
        if not isinstance(frame, dict) or frame.get("p95") is None or frame.get("p99") is None:
            reasons.append(f"vrchat_segment_{name}_p95_p99_missing")
        if item.get("cpu_budget_percent") != CPU_BUDGET_PERCENT:
            reasons.append(f"vrchat_segment_{name}_cpu_budget_missing")
        if item.get("player_av_verified") is not True:
            reasons.append(f"vrchat_segment_{name}_player_av_unverified")
    if report.get("status") != "complete":
        reasons.append("vrchat_report_not_complete")
    return {
        "status": "pass" if not reasons else "blocked",
        "reasons": reasons,
        "segments": segments,
    }


def evaluate_matrix_report(report: dict[str, Any] | None, *, label: str) -> dict[str, Any]:
    if report is None:
        return {"status": "pending", "reasons": [f"{label}:matrix_evidence_missing"]}
    reasons: list[str] = []
    if report.get("matrix_complete") is not True:
        reasons.append(f"{label}:matrix_incomplete")
    scenarios = report.get("scenarios")
    if not isinstance(scenarios, dict):
        reasons.append(f"{label}:scenario_summary_missing")
    else:
        for scenario in REQUIRED_SCENARIOS:
            value = scenarios.get(scenario)
            if not isinstance(value, dict) or int(value.get("n", 0) or 0) <= 0:
                reasons.append(f"{label}:scenario_{scenario}_missing")
    quality = report.get("quality_matrix")
    if not isinstance(quality, list):
        reasons.append(f"{label}:quality_matrix_missing")
    else:
        missing = [item for item in REQUIRED_QUALITY_MATRIX if item not in quality]
        reasons.extend(f"{label}:quality_{item}_missing" for item in missing)
    counts = report.get("case_counts")
    if isinstance(counts, dict):
        for status in ("failed", "invalid", "missing"):
            if int(counts.get(status, 0) or 0) > 0:
                reasons.append(f"{label}:case_count_{status}_nonzero")
    return {"status": "pass" if not reasons else "blocked", "reasons": reasons}


def evaluate_operational_report(report: dict[str, Any] | None) -> dict[str, Any]:
    if report is None:
        return {"status": "pending", "reasons": ["operational_evidence_missing"]}
    reasons: list[str] = []
    if report.get("status") != "complete":
        reasons.append("operational_report_not_complete")
    for phase in ("cancel", "rerun"):
        value = report.get(phase)
        if not isinstance(value, dict) or value.get("verified") is not True:
            reasons.append(f"operational_{phase}_unverified")
    long_run = report.get("long_run")
    if not isinstance(long_run, dict):
        reasons.append("operational_long_run_missing")
    else:
        if float(long_run.get("duration_s", 0) or 0) < 1800:
            reasons.append("operational_long_run_under_30_minutes")
        for name in ("queue_stable", "vram_stable", "child_residue_clear"):
            if long_run.get(name) is not True:
                reasons.append(f"operational_{name}_unverified")
    return {"status": "pass" if not reasons else "blocked", "reasons": reasons}


def evaluate_statistics_report(report: dict[str, Any] | None) -> dict[str, Any]:
    if report is None:
        return {"status": "pending", "reasons": ["statistics_evidence_missing"]}
    reasons: list[str] = []
    if report.get("status") != "complete":
        reasons.append("statistics_report_not_complete")
    pairs = report.get("pairs")
    if not isinstance(pairs, dict):
        reasons.append("statistics_pairs_missing")
    else:
        for scenario in REQUIRED_SCENARIOS:
            if int(pairs.get(scenario, 0) or 0) < 40:
                reasons.append(f"statistics_{scenario}_under_40_pairs")
    bootstrap = report.get("bootstrap")
    if not isinstance(bootstrap, dict):
        reasons.append("statistics_bootstrap_missing")
    else:
        if int(bootstrap.get("resamples", 0) or 0) != 10000:
            reasons.append("statistics_bootstrap_not_10000")
        if bootstrap.get("median_speedup_ci_lower_percent") is None or float(bootstrap.get("median_speedup_ci_lower_percent")) <= 0:
            reasons.append("statistics_median_speedup_ci_lower_not_positive")
        if bootstrap.get("p95_delta_ci_upper_percent") is None or float(bootstrap.get("p95_delta_ci_upper_percent")) > 0:
            reasons.append("statistics_p95_delta_ci_upper_not_nonpositive")
    if report.get("cpu_total_not_increased") is not True:
        reasons.append("statistics_cpu_total_increased_or_unverified")
    if report.get("candidate_threshold_met") is not True:
        reasons.append("statistics_candidate_threshold_unmet")
    return {"status": "pass" if not reasons else "blocked", "reasons": reasons}


def evaluate_acceptance(
    baseline: dict[str, Any],
    candidate: dict[str, Any],
    *,
    vrchat: dict[str, Any] | None = None,
    baseline_matrix: dict[str, Any] | None = None,
    candidate_matrix: dict[str, Any] | None = None,
    operational: dict[str, Any] | None = None,
    statistics: dict[str, Any] | None = None,
    candidate_output_mode: str = "tee",
) -> dict[str, Any]:
    baseline_gate = evaluate_worker_report(
        baseline, label="baseline", expected_output_mode="separate"
    )
    candidate_gate = evaluate_worker_report(
        candidate, label="candidate", expected_output_mode=candidate_output_mode
    )
    reasons = [*baseline_gate["reasons"], *candidate_gate["reasons"]]
    baseline_inputs = _input_fingerprint(baseline)
    candidate_inputs = _input_fingerprint(candidate)
    if baseline_inputs is None or candidate_inputs is None:
        reasons.append("common_input_fingerprint_unavailable")
    elif baseline_inputs != candidate_inputs:
        reasons.append("baseline_candidate_input_mismatch")
    baseline_parameters = baseline.get("parameters")
    candidate_parameters = candidate.get("parameters")
    if not isinstance(baseline_parameters, dict) or not isinstance(candidate_parameters, dict):
        reasons.append("baseline_candidate_parameters_missing")
    elif baseline_parameters != candidate_parameters:
        reasons.append("baseline_candidate_parameters_mismatch")
    vrchat_gate = evaluate_vrchat_report(vrchat)
    reasons.extend(vrchat_gate["reasons"])
    baseline_matrix_gate = evaluate_matrix_report(baseline_matrix, label="baseline")
    candidate_matrix_gate = evaluate_matrix_report(candidate_matrix, label="candidate")
    reasons.extend(baseline_matrix_gate["reasons"])
    reasons.extend(candidate_matrix_gate["reasons"])
    operational_gate = evaluate_operational_report(operational)
    reasons.extend(operational_gate["reasons"])
    statistics_gate = evaluate_statistics_report(statistics)
    reasons.extend(statistics_gate["reasons"])
    status = "eligible" if not reasons else "blocked"
    speedup_percent: float | None = None
    if not reasons and candidate_gate["wall_s"] > 0:
        speedup_percent = (baseline_gate["wall_s"] / candidate_gate["wall_s"] - 1.0) * 100.0
    return {
        "schema_version": 1,
        "status": status,
        "reasons": reasons,
        "cpu_budget_percent": CPU_BUDGET_PERCENT,
        "baseline": baseline_gate,
        "candidate": candidate_gate,
        "vrchat": vrchat_gate,
        "baseline_matrix": baseline_matrix_gate,
        "candidate_matrix": candidate_matrix_gate,
        "operational": operational_gate,
        "statistics": statistics_gate,
        "common_inputs": baseline_inputs if baseline_inputs == candidate_inputs else None,
        "common_parameters": baseline_parameters if baseline_parameters == candidate_parameters else None,
        "speedup_percent": speedup_percent,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline-report", required=True, type=Path)
    parser.add_argument("--candidate-report", required=True, type=Path)
    parser.add_argument("--vrchat-report", type=Path)
    parser.add_argument("--baseline-matrix-report", type=Path)
    parser.add_argument("--candidate-matrix-report", type=Path)
    parser.add_argument("--operational-report", type=Path)
    parser.add_argument("--statistics-report", type=Path)
    parser.add_argument("--candidate-output-mode", choices=("separate", "tee"), default="tee")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args(argv)
    result = evaluate_acceptance(
        _read_json(args.baseline_report),
        _read_json(args.candidate_report),
        vrchat=_read_json(args.vrchat_report) if args.vrchat_report else None,
        baseline_matrix=_read_json(args.baseline_matrix_report) if args.baseline_matrix_report else None,
        candidate_matrix=_read_json(args.candidate_matrix_report) if args.candidate_matrix_report else None,
        operational=_read_json(args.operational_report) if args.operational_report else None,
        statistics=_read_json(args.statistics_report) if args.statistics_report else None,
        candidate_output_mode=args.candidate_output_mode,
    )
    result["generated_at"] = utc_now()
    result["evidence"] = {
        "baseline_report": str(args.baseline_report.resolve()),
        "candidate_report": str(args.candidate_report.resolve()),
        "vrchat_report": str(args.vrchat_report.resolve()) if args.vrchat_report else None,
        "baseline_matrix_report": str(args.baseline_matrix_report.resolve()) if args.baseline_matrix_report else None,
        "candidate_matrix_report": str(args.candidate_matrix_report.resolve()) if args.candidate_matrix_report else None,
        "operational_report": str(args.operational_report.resolve()) if args.operational_report else None,
        "statistics_report": str(args.statistics_report.resolve()) if args.statistics_report else None,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output), "status": result["status"], "reasons": result["reasons"]}, ensure_ascii=False))
    return 0 if result["status"] == "eligible" else 1


if __name__ == "__main__":
    raise SystemExit(main())

"""Build an auditable T11 report from a persisted matrix manifest."""

from __future__ import annotations

import argparse
import json
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any


OBSERVATION_RANGE_METRICS = (
    "observed_cpu_seconds",
    "peak_working_set_bytes",
    "peak_handles",
    "gpu_utilization_avg",
    "gpu_utilization_max",
    "gpu_memory_used_avg_mib",
    "gpu_memory_used_max_mib",
)


def _number(value: Any) -> float | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    return None


def _sources(case: dict[str, Any]) -> list[dict[str, Any]]:
    sources = [case]
    for name in ("metrics", "artifact", "runner_report", "report"):
        value = case.get(name)
        if isinstance(value, dict):
            sources.append(value)
            nested = value.get("report")
            if isinstance(nested, dict):
                sources.append(nested)
    return sources


def _first_number(case: dict[str, Any], *names: str) -> float | None:
    for source in _sources(case):
        for name in names:
            value = _number(source.get(name))
            if value is not None:
                return value
    return None


def _metric(case: dict[str, Any], name: str) -> float | str | None:
    if name == "wall_s":
        return _first_number(case, "wall_s", "elapsed_s")
    if name == "cpu_seconds":
        return _first_number(case, "cpu_seconds", "cpu_s")
    if name == "video_seconds":
        return _first_number(case, "video_seconds", "duration_s")
    if name == "cpu_time_ratio":
        cpu = _metric(case, "cpu_seconds")
        video = _metric(case, "video_seconds")
        if isinstance(cpu, (int, float)) and isinstance(video, (int, float)) and video > 0:
            return cpu / video
        return None
    if name == "output_bytes":
        return _first_number(case, "output_bytes", "bytes")
    if name == "sha256":
        for source in _sources(case):
            value = source.get("sha256") or source.get("hash")
            if isinstance(value, str) and value:
                return value
        return None
    observation = case.get("observation")
    if not isinstance(observation, dict):
        return None
    if name == "observed_cpu_seconds":
        return _number(observation.get("cpu_seconds_observed"))
    if name == "peak_working_set_bytes":
        return _number(observation.get("peak_working_set_bytes"))
    if name == "peak_handles":
        return _number(observation.get("peak_handles"))
    gpu = observation.get("gpu")
    if not isinstance(gpu, dict):
        return None
    if name == "gpu_utilization_avg":
        return _number((gpu.get("gpu_utilization_percent") or {}).get("avg"))
    if name == "gpu_utilization_max":
        return _number((gpu.get("gpu_utilization_percent") or {}).get("max"))
    if name == "gpu_memory_used_avg_mib":
        return _number((gpu.get("memory_used_mib") or {}).get("avg"))
    if name == "gpu_memory_used_max_mib":
        return _number((gpu.get("memory_used_mib") or {}).get("max"))
    return None


def _integrated_observation_reasons(case: dict[str, Any], target_pid: int | None) -> list[str]:
    """Return missing same-run evidence required beside PresentMon acceptance."""
    reasons: list[str] = []
    if case.get("status") != "complete":
        reasons.append("case_not_complete")
    runner = case.get("runner_report")
    runner_report = runner.get("report") if isinstance(runner, dict) else None
    if not isinstance(runner_report, dict) or runner_report.get("verified") is not True:
        reasons.append("cpu20_job_unverified")
    observation = case.get("observation")
    if not isinstance(observation, dict):
        return [*reasons, "observation_missing"]
    if not isinstance(observation.get("cpu_seconds_observed"), (int, float)):
        reasons.append("cpu_observation_missing")
    gpu = observation.get("gpu")
    if not isinstance(gpu, dict) or gpu.get("available") is not True:
        reasons.append("gpu_unavailable")
    else:
        memory = gpu.get("memory_used_mib")
        if not isinstance(memory, dict) or not all(
            isinstance(memory.get(name), (int, float)) for name in ("avg", "max")
        ):
            reasons.append("vram_observation_missing")
    vrchat = observation.get("vrchat")
    if not isinstance(vrchat, dict) or vrchat.get("present") is not True:
        reasons.append("vrchat_process_not_observed")
    else:
        pids = vrchat.get("pids")
        if not isinstance(pids, list) or not pids:
            reasons.append("vrchat_pid_missing")
        elif target_pid is not None and target_pid not in pids:
            reasons.append("vrchat_pid_mismatch")
    residue = observation.get("child_residue")
    if not isinstance(residue, dict) or residue.get("clear") is not True:
        reasons.append("child_residue_not_clear")
    if isinstance(residue, dict) and residue.get("tree_complete") is False:
        reasons.append("child_tree_incomplete")
    return reasons


def _metric_row(a: dict[str, Any], b: dict[str, Any], name: str) -> dict[str, Any] | None:
    baseline = _metric(a, name)
    candidate = _metric(b, name)
    if not isinstance(baseline, (int, float)) or not isinstance(candidate, (int, float)):
        return None
    row: dict[str, Any] = {"baseline": baseline, "candidate": candidate}
    if baseline != 0:
        row["reduction"] = 1.0 - candidate / baseline
    else:
        row["reduction"] = None
    return row


def _range(values: list[float]) -> dict[str, float] | None:
    if not values:
        return None
    return {"min": min(values), "max": max(values)}


def build_report(
    manifest: dict[str, Any], *, vrchat_evidence: dict[str, Any] | None = None
) -> dict[str, Any]:
    cases = manifest.get("cases") or {}
    ordered = []
    for spec in manifest.get("matrix", []):
        if not isinstance(spec, dict):
            continue
        case_id = str(spec.get("case_id"))
        result = cases.get(case_id)
        merged = dict(spec)
        if isinstance(result, dict):
            merged.update(result)
        else:
            merged.update({"status": "missing", "case_id": case_id})
        ordered.append(merged)
    if not ordered:
        ordered = [case for case in cases.values() if isinstance(case, dict)]
    statuses = Counter(str(case.get("status", "missing")) for case in ordered)
    failure_classes = Counter(
        str(classification.get("class"))
        for case in ordered
        if isinstance(classification := case.get("failure_classification"), dict)
        and classification.get("class")
    )
    invalid = sorted(
        str(case.get("case_id"))
        for case in ordered
        if case.get("status") != "complete"
    )
    grouped: dict[str, dict[int, dict[str, dict[str, Any]]]] = defaultdict(lambda: defaultdict(dict))
    for case in ordered:
        if case.get("phase") == "measured" and case.get("status") == "complete":
            grouped[str(case.get("scenario"))][int(case.get("repeat", 0))][str(case.get("variant"))] = case

    scenarios: dict[str, Any] = {}
    for scenario, repeats in grouped.items():
        paired: list[dict[str, Any]] = []
        for repeat, variants in sorted(repeats.items()):
            if "A" not in variants or "B" not in variants:
                continue
            row: dict[str, Any] = {"repeat": repeat}
            for name in ("wall_s", "cpu_seconds", "video_seconds", "cpu_time_ratio", "output_bytes"):
                metrics = _metric_row(variants["A"], variants["B"], name)
                if metrics is not None:
                    row[name] = metrics
            if row.keys() != {"repeat"}:
                paired.append(row)
        measured = [
            case
            for case in ordered
            if case.get("scenario") == scenario and case.get("phase") == "measured"
        ]
        ranges: dict[str, Any] = {}
        for name in ("wall_s", "cpu_seconds", "video_seconds", "cpu_time_ratio", "output_bytes", *OBSERVATION_RANGE_METRICS):
            values = [value for case in measured if (value := _metric(case, name)) is not None]
            numeric = [float(value) for value in values if isinstance(value, (int, float))]
            ranges[name] = _range(numeric)
        scenarios[scenario] = {"n": len(measured), "ranges": ranges, "paired": paired}

    observed_cases = [case for case in ordered if isinstance(case.get("observation"), dict)]
    observed = [case["observation"] for case in observed_cases]
    observation_ranges = {
        name: _range(
            [float(value) for case in observed_cases if (value := _metric(case, name)) is not None]
        )
        for name in OBSERVATION_RANGE_METRICS
    }
    verified_cases = [
        case
        for case in ordered
        if isinstance(case.get("runner_report"), dict)
        and isinstance(case["runner_report"].get("report"), dict)
        and case["runner_report"]["report"].get("verified") is True
    ]
    observation_gate = {
        "observed_cases": len(observed_cases),
        "observation_missing_cases": len(ordered) - len(observed_cases),
        "vrc_acceptance_pending_cases": sum(1 for value in observed if value.get("vrc_acceptance_pending") is True),
        "child_residue_cases": sum(
            1
            for value in observed
            if isinstance(value.get("child_residue"), dict)
            and value["child_residue"].get("clear") is False
        ),
        "child_residue_incomplete_cases": sum(
            1
            for value in observed
            if isinstance(value.get("child_residue"), dict)
            and value["child_residue"].get("tree_complete") is False
        ),
        "cpu20_verified_cases": len(verified_cases),
        "budget_unverified_cases": len(ordered) - len(verified_cases),
        "gpu_available_cases": sum(
            1
            for value in observed
            if isinstance(value.get("gpu"), dict) and value["gpu"].get("available") is True
        ),
        "vrc_gate": "pending",
    }
    if vrchat_evidence is not None:
        observation_gate["vrc_external_evidence"] = "measured"
        acceptance = vrchat_evidence.get("acceptance")
        if isinstance(acceptance, dict):
            external_status = acceptance.get("status")
            if external_status in {"pass", "fail", "pending"}:
                observation_gate["vrc_external_acceptance_status"] = external_status
            reasons = acceptance.get("reasons")
            if isinstance(reasons, list):
                observation_gate["vrc_external_acceptance_reasons"] = [
                    str(reason) for reason in reasons
                ]
    matrix_complete = manifest.get("status") == "complete" and not invalid
    external_status = observation_gate.get("vrc_external_acceptance_status")
    if external_status == "pass":
        raw_target_pid = vrchat_evidence.get("target_process_id") if vrchat_evidence else None
        target_pid = int(raw_target_pid) if isinstance(raw_target_pid, int) and not isinstance(raw_target_pid, bool) else None
        integrated_missing = {
            str(case.get("case_id")): _integrated_observation_reasons(case, target_pid)
            for case in ordered
        }
        integrated_missing = {
            case_id: reasons for case_id, reasons in integrated_missing.items() if reasons
        }
        observation_gate["vrc_integrated_gate"] = "pass" if not integrated_missing else "pending"
        if integrated_missing:
            observation_gate["vrc_integrated_pending_case_count"] = len(integrated_missing)
            observation_gate["vrc_integrated_pending_reasons"] = sorted(
                {reason for reasons in integrated_missing.values() for reason in reasons}
            )
        elif matrix_complete:
            observation_gate["vrc_gate"] = "pass"
    elif external_status == "fail":
        observation_gate["vrc_integrated_gate"] = "fail"
        observation_gate["vrc_gate"] = "fail"
    elif vrchat_evidence is None and observed and len(observed) == len(ordered):
        if all(
            value.get("vrc_acceptance_pending") is False
            and isinstance(value.get("vrchat"), dict)
            and value["vrchat"].get("acceptance_status") == "pass"
            for value in observed
        ):
            observation_gate["vrc_gate"] = "pass"
    acceptance_complete = matrix_complete and observation_gate["vrc_gate"] == "pass"

    return {
        "schema_version": 1,
        "run_id": manifest.get("run_id"),
        "status": manifest.get("status", "unknown"),
        "matrix_complete": matrix_complete,
        "complete": acceptance_complete,
        "acceptance_complete": acceptance_complete,
        "case_counts": dict(sorted(statuses.items())),
        "failure_classes": dict(sorted(failure_classes.items())),
        "quality_matrix": manifest.get("quality_matrix", []),
        "scenarios": scenarios,
        "observation_summary": {"n": len(observed_cases), "ranges": observation_ranges},
        "observation_gate": observation_gate,
        "invalid_runs": invalid,
        "vrchat_external_evidence": vrchat_evidence,
    }


def write_markdown(report: dict[str, Any], output: Path) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    complete = "完了" if report.get("complete") else "未完了（PASSへ読み替えない）"
    lines = [
        "# Nico CPU20 T11 結果レポート",
        "",
        f"- run_id: `{report.get('run_id')}`",
        f"- 判定: **{complete}**",
        f"- matrix_complete: `{report.get('matrix_complete')}` / acceptance_complete: `{report.get('acceptance_complete')}`",
        f"- case_counts: `{json.dumps(report.get('case_counts', {}), ensure_ascii=False, sort_keys=True)}`",
        f"- failure_classes: `{json.dumps(report.get('failure_classes', {}), ensure_ascii=False, sort_keys=True)}`",
        f"- invalid_runs: `{len(report.get('invalid_runs', []))}`",
        f"- 観測: `{json.dumps(report.get('observation_gate', {}), ensure_ascii=False, sort_keys=True)}`",
        f"- 外部VRChat計測: `{json.dumps(report.get('vrchat_external_evidence'), ensure_ascii=False, sort_keys=True)}`",
        f"- 観測範囲: `{json.dumps(report.get('observation_summary', {}), ensure_ascii=False, sort_keys=True)}`",
        "",
        "## シナリオ",
        "",
        "| scenario | n | wall range (s) | CPU range (s) | paired |",
        "|---|---:|---:|---:|---:|",
    ]
    for scenario, data in sorted(report.get("scenarios", {}).items()):
        wall = data["ranges"].get("wall_s") or {}
        cpu = data["ranges"].get("cpu_seconds") or {}
        lines.append(
            f"|{scenario}|{data['n']}|{wall.get('min', 'n/a')}–{wall.get('max', 'n/a')}|"
            f"{cpu.get('min', 'n/a')}–{cpu.get('max', 'n/a')}|{len(data['paired'])}|"
        )
    lines += ["", "## 無効run", "", "```json", json.dumps(report.get("invalid_runs", []), ensure_ascii=False, indent=2), "```", ""]
    output.write_text("\n".join(lines), encoding="utf-8")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--markdown-output", type=Path)
    parser.add_argument("--vrchat-presentmon", type=Path, help="optional external PresentMon summary")
    args = parser.parse_args(argv)
    evidence = (
        json.loads(args.vrchat_presentmon.read_text(encoding="utf-8"))
        if args.vrchat_presentmon is not None
        else None
    )
    report = build_report(
        json.loads(args.manifest.read_text(encoding="utf-8")),
        vrchat_evidence=evidence,
    )
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    if args.markdown_output:
        write_markdown(report, args.markdown_output)
    print(json.dumps(report, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

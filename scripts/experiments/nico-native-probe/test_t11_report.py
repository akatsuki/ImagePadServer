import json
import tempfile
import unittest
from pathlib import Path

from t11_report import build_report, write_markdown


class T11ReportTests(unittest.TestCase):
    def _manifest(self, cases):
        return {
            "schema_version": 1,
            "run_id": "report-fixture",
            "status": "failed" if any(c["status"] != "complete" for c in cases.values()) else "complete",
            "quality_matrix": ["fps-30"],
            "matrix": list(cases.values()),
            "cases": cases,
        }

    def _case(self, case_id, scenario, repeat, variant, status, wall_s, cpu_s, sha):
        return {
            "case_id": case_id,
            "scenario": scenario,
            "phase": "measured",
            "repeat": repeat,
            "variant": variant,
            "duration_s": 10,
            "status": status,
            "wall_s": wall_s,
            "runner_report": {"verified": True, "report": {"cpu_seconds": cpu_s}},
            "artifact": {"bytes": 1000 if variant == "A" else 900, "sha256": sha},
        }

    def test_report_keeps_invalid_runs_and_calculates_paired_reduction(self):
        cases = {
            "a": self._case("a", "fixed-6s", 1, "A", "complete", 10, 5, "a" * 64),
            "b": self._case("b", "fixed-6s", 1, "B", "complete", 8, 4, "b" * 64),
            "bad": self._case("bad", "fixed-6s", 2, "A", "failed", 0, 0, ""),
        }
        report = build_report(self._manifest(cases))
        self.assertEqual(report["case_counts"], {"complete": 2, "failed": 1})
        pair = report["scenarios"]["fixed-6s"]["paired"][0]
        self.assertEqual(pair["repeat"], 1)
        self.assertAlmostEqual(pair["wall_s"]["reduction"], 0.2)
        self.assertAlmostEqual(pair["cpu_time_ratio"]["candidate"], 0.4)
        self.assertEqual(report["invalid_runs"], ["bad"])

    def test_report_counts_machine_classified_failures(self):
        case = self._case("bad", "fixed-6s", 1, "A", "failed", 0, 0, "")
        case["failure_classification"] = {
            "class": "browser_gpu_process_unusable",
            "phase": "browser_startup",
            "signals": ["gpu_process_unusable"],
        }
        report = build_report(self._manifest({"bad": case}))
        self.assertEqual(report["failure_classes"], {"browser_gpu_process_unusable": 1})

    def test_markdown_is_explicit_about_incomplete_matrix(self):
        cases = {"a": self._case("a", "fixed-6s", 1, "A", "failed", 0, 0, "")}
        report = build_report(self._manifest(cases))
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "report.md"
            write_markdown(report, output)
            text = output.read_text(encoding="utf-8")
        self.assertIn("未完了", text)
        self.assertIn("invalid_runs", text)

    def test_report_merges_matrix_specs_with_persisted_case_results(self):
        spec = {
            "case_id": "spec-a",
            "scenario": "fixed-6s",
            "phase": "measured",
            "repeat": 1,
            "variant": "A",
            "duration_s": 6,
        }
        manifest = self._manifest({"spec-a": {**spec, "status": "complete", "wall_s": 2}})
        manifest["matrix"] = [spec]
        report = build_report(manifest)
        self.assertEqual(report["case_counts"], {"complete": 1})
        self.assertEqual(report["scenarios"]["fixed-6s"]["n"], 1)

    def test_report_exposes_observation_gate_without_turning_pending_into_pass(self):
        case = self._case("observed", "fixed-6s", 1, "A", "failed", 1, 0.5, "")
        case["observation"] = {
            "vrc_acceptance_pending": True,
            "child_residue": {"clear": False},
            "gpu": {"available": False},
        }
        report = build_report(self._manifest({"observed": case}))
        self.assertEqual(report["observation_gate"]["observed_cases"], 1)
        self.assertEqual(report["observation_gate"]["vrc_acceptance_pending_cases"], 1)
        self.assertEqual(report["observation_gate"]["child_residue_cases"], 1)
        self.assertEqual(report["observation_gate"]["vrc_gate"], "pending")
        self.assertFalse(report["complete"])

    def test_report_collects_observation_ranges_and_cpu20_verification(self):
        case = self._case("observed", "fixed-6s", 1, "A", "failed", 1, 0.5, "")
        case["runner_report"] = {"report": {"verified": True}}
        case["observation"] = {
            "cpu_seconds_observed": 0.25,
            "peak_working_set_bytes": 100,
            "peak_handles": 7,
            "gpu": {
                "available": True,
                "gpu_utilization_percent": {"avg": 12.0, "max": 20.0},
                "memory_used_mib": {"avg": 1000.0, "max": 1200.0},
            },
            "vrc_acceptance_pending": True,
            "child_residue": {"clear": True, "tree_complete": True},
        }
        report = build_report(self._manifest({"observed": case}))
        self.assertEqual(report["observation_summary"]["ranges"]["gpu_utilization_max"], {"min": 20.0, "max": 20.0})
        self.assertEqual(report["observation_gate"]["cpu20_verified_cases"], 1)
        self.assertEqual(report["observation_gate"]["budget_unverified_cases"], 0)

    def test_matrix_completion_does_not_bypass_vrchat_acceptance_gate(self):
        case = self._case("complete", "fixed-6s", 1, "A", "complete", 1, 0.5, "a" * 64)
        manifest = self._manifest({"complete": case})
        manifest["status"] = "complete"
        report = build_report(manifest)
        self.assertTrue(report["matrix_complete"])
        self.assertFalse(report["acceptance_complete"])
        self.assertFalse(report["complete"])

    def test_report_records_external_presentmon_without_bypassing_gate(self):
        case = self._case("complete", "real-source-155s", 1, "A", "complete", 1, 0.5, "a" * 64)
        manifest = self._manifest({"complete": case})
        manifest["status"] = "complete"
        evidence = {
            "target_process_id": 58216,
            "export_window": {"sample_count": 10},
            "acceptance": {"status": "pending"},
        }
        report = build_report(manifest, vrchat_evidence=evidence)
        self.assertEqual(report["vrchat_external_evidence"]["target_process_id"], 58216)
        self.assertEqual(report["observation_gate"]["vrc_external_evidence"], "measured")
        self.assertEqual(report["observation_gate"]["vrc_gate"], "pending")
        self.assertFalse(report["acceptance_complete"])

    def test_external_presentmon_fail_is_visible_and_fails_gate(self):
        case = self._case("complete", "real-source-155s", 1, "A", "complete", 1, 0.5, "a" * 64)
        manifest = self._manifest({"complete": case})
        manifest["status"] = "complete"
        evidence = {
            "target_process_id": 58216,
            "acceptance": {
                "status": "fail",
                "reasons": ["export_p95_regression_over_10_percent"],
            },
        }
        report = build_report(manifest, vrchat_evidence=evidence)
        self.assertEqual(report["observation_gate"]["vrc_external_acceptance_status"], "fail")
        self.assertEqual(
            report["observation_gate"]["vrc_external_acceptance_reasons"],
            ["export_p95_regression_over_10_percent"],
        )
        self.assertEqual(report["observation_gate"]["vrc_gate"], "fail")
        self.assertFalse(report["acceptance_complete"])

    def test_external_presentmon_pass_still_needs_integrated_observation(self):
        case = self._case("complete", "real-source-155s", 1, "A", "complete", 1, 0.5, "a" * 64)
        manifest = self._manifest({"complete": case})
        manifest["status"] = "complete"
        evidence = {
            "target_process_id": 58216,
            "acceptance": {"status": "pass", "reasons": []},
        }
        report = build_report(manifest, vrchat_evidence=evidence)
        self.assertEqual(report["observation_gate"]["vrc_external_acceptance_status"], "pass")
        self.assertEqual(report["observation_gate"]["vrc_gate"], "pending")
        self.assertFalse(report["acceptance_complete"])

    def test_external_pass_and_complete_observation_can_close_integrated_gate(self):
        case = self._case("complete", "real-source-155s", 1, "A", "complete", 1, 0.5, "a" * 64)
        case["runner_report"] = {"report": {"verified": True}}
        case["observation"] = {
            "sample_count": 10,
            "cpu_seconds_observed": 0.5,
            "gpu": {
                "available": True,
                "memory_used_mib": {"avg": 1000.0, "max": 1100.0},
            },
            "vrchat": {"present": True, "pids": [58216]},
            "child_residue": {"clear": True, "tree_complete": True},
            "vrc_acceptance_pending": True,
        }
        manifest = self._manifest({"complete": case})
        evidence = {
            "target_process_id": 58216,
            "acceptance": {"status": "pass", "reasons": []},
        }
        report = build_report(manifest, vrchat_evidence=evidence)
        self.assertEqual(report["observation_gate"]["vrc_integrated_gate"], "pass")
        self.assertEqual(report["observation_gate"]["vrc_gate"], "pass")
        self.assertTrue(report["acceptance_complete"])
        self.assertTrue(report["complete"])

    def test_integrated_gate_keeps_pending_when_gpu_or_pid_evidence_is_missing(self):
        case = self._case("complete", "real-source-155s", 1, "A", "complete", 1, 0.5, "a" * 64)
        case["runner_report"] = {"report": {"verified": True}}
        case["observation"] = {
            "sample_count": 10,
            "cpu_seconds_observed": 0.5,
            "gpu": {"available": False},
            "vrchat": {"present": True, "pids": [999]},
            "child_residue": {"clear": True, "tree_complete": True},
        }
        manifest = self._manifest({"complete": case})
        evidence = {
            "target_process_id": 58216,
            "acceptance": {"status": "pass", "reasons": []},
        }
        report = build_report(manifest, vrchat_evidence=evidence)
        self.assertEqual(report["observation_gate"]["vrc_integrated_gate"], "pending")
        self.assertIn("gpu_unavailable", report["observation_gate"]["vrc_integrated_pending_reasons"])
        self.assertIn("vrchat_pid_mismatch", report["observation_gate"]["vrc_integrated_pending_reasons"])
        self.assertEqual(report["observation_gate"]["vrc_gate"], "pending")
        self.assertFalse(report["acceptance_complete"])


if __name__ == "__main__":
    unittest.main()

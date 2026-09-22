import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from presentmon_report import evaluate_presentmon_acceptance, main


def _summary(p95, p99):
    return {"ms_between_presents": {"p95": p95, "p99": p99}}


class PresentMonReportTests(unittest.TestCase):
    def test_cli_output_is_utf8_json_for_t11_report_consumption(self):
        csv_text = """Application,ProcessID,TimeInSeconds,Dropped,msBetweenPresents,msGPUActive
VRChat,58216,2026-9-22 8:02:04.000000000,0,27.0,8.0
VRChat,58216,2026-9-22 8:02:04.030000000,0,30.0,10.0
"""
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            csv_path = root / "presentmon.csv"
            output_path = root / "summary.json"
            csv_path.write_text(csv_text, encoding="utf-8")
            with contextlib.redirect_stdout(io.StringIO()):
                with mock.patch(
                    "sys.argv",
                    [
                        "presentmon_report.py",
                        "--csv",
                        str(csv_path),
                        "--process-id",
                        "58216",
                        "--output",
                        str(output_path),
                    ],
                ):
                    self.assertEqual(main(), 0)
            saved = json.loads(output_path.read_text(encoding="utf-8"))
            self.assertEqual(saved["target_process_id"], 58216)
            self.assertEqual(saved["export_window"]["sample_count"], 2)

    def test_passes_when_baselines_are_stable_and_no_hitch_is_reported(self):
        result = evaluate_presentmon_acceptance(
            export_window=_summary(21.0, 22.0),
            baseline_before=_summary(20.0, 21.0),
            baseline_after=_summary(20.5, 21.5),
            continuous_hitching=False,
        )
        self.assertEqual(result["status"], "pass")
        self.assertEqual(result["reasons"], [])
        self.assertAlmostEqual(result["metrics"]["baseline_p95_change_ratio"], 0.025)
        self.assertAlmostEqual(result["metrics"]["export_p99_change_ratio"], 22.0 / 21.25 - 1.0)

    def test_baseline_drift_invalidates_comparison(self):
        result = evaluate_presentmon_acceptance(
            export_window=_summary(21.0, 22.0),
            baseline_before=_summary(20.0, 21.0),
            baseline_after=_summary(23.0, 24.0),
            continuous_hitching=False,
        )
        self.assertEqual(result["status"], "fail")
        self.assertIn("baseline_p95_drift_over_10_percent", result["reasons"])
        self.assertIn("baseline_p99_drift_over_10_percent", result["reasons"])

    def test_missing_hitch_evidence_remains_pending(self):
        result = evaluate_presentmon_acceptance(
            export_window=_summary(21.0, 22.0),
            baseline_before=_summary(20.0, 21.0),
            baseline_after=_summary(20.5, 21.5),
        )
        self.assertEqual(result["status"], "pending")
        self.assertIn("continuous_hitching_unavailable", result["reasons"])

    def test_large_export_regression_fails(self):
        result = evaluate_presentmon_acceptance(
            export_window=_summary(24.0, 26.0),
            baseline_before=_summary(20.0, 21.0),
            baseline_after=_summary(20.0, 21.0),
            continuous_hitching=False,
        )
        self.assertEqual(result["status"], "fail")
        self.assertIn("export_p95_regression_over_10_percent", result["reasons"])
        self.assertIn("export_p99_regression_over_10_percent", result["reasons"])


if __name__ == "__main__":
    unittest.main()

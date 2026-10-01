import unittest

from t6_statistics import build_statistics


def _stability_report(walls, cpu_seconds):
    return {
        "worker": {
            "complete": True,
            "runs": [
                {
                    "iteration": index + 1,
                    "elapsed_s": wall,
                    "result_ok": True,
                }
                for index, wall in enumerate(walls)
            ],
        },
        "runner_report": {"report": {"cpu_seconds": cpu_seconds}},
    }


class T6StatisticsTests(unittest.TestCase):
    def test_build_statistics_uses_paired_runs_and_10000_bootstrap(self):
        baseline = _stability_report([10.0] * 40, 400.0)
        candidate = _stability_report([8.0] * 40, 360.0)
        scenarios = {
            name: {
                "baseline_reports": [baseline],
                "candidate_reports": [candidate],
            }
            for name in ("fixed-6s", "high-density-10s", "real-source-155s")
        }

        result = build_statistics(scenarios, resamples=10_000, seed=20260922)

        self.assertEqual(result["status"], "complete")
        self.assertEqual(result["pairs"], {name: 40 for name in scenarios})
        self.assertEqual(result["bootstrap"]["resamples"], 10_000)
        self.assertGreater(result["bootstrap"]["median_speedup_ci_lower_percent"], 0)
        self.assertLessEqual(result["bootstrap"]["p95_delta_ci_upper_percent"], 0)
        self.assertTrue(result["cpu_total_not_increased"])
        self.assertTrue(result["candidate_threshold_met"])

    def test_incomplete_pair_is_blocked(self):
        report = _stability_report([10.0], 10.0)
        scenarios = {
            "fixed-6s": {
                "baseline_reports": [report],
                "candidate_reports": [report],
            }
        }

        result = build_statistics(scenarios, resamples=10_000, seed=20260922)

        self.assertEqual(result["status"], "blocked")
        self.assertIn("fixed-6s:under_40_pairs", result["reasons"])


if __name__ == "__main__":
    unittest.main()

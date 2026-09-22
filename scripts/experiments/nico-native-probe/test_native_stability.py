import unittest

from native_stability import summarize_runs


class NativeStabilityTests(unittest.TestCase):
    def test_complete_requires_minimum_successful_iterations(self):
        report = summarize_runs(
            started_at="2026-09-22T00:00:00Z",
            finished_at="2026-09-22T00:00:10Z",
            duration_s=10,
            min_iterations=2,
            runs=[
                {"iteration": 1, "exit_code": 0, "elapsed_s": 1.0},
                {"iteration": 2, "exit_code": 0, "elapsed_s": 1.1},
            ],
        )
        self.assertTrue(report["complete"])
        self.assertEqual(report["iterations"], 2)
        self.assertEqual(report["failed_iterations"], [])

    def test_nonzero_run_and_short_run_cannot_be_pass(self):
        report = summarize_runs(
            started_at="2026-09-22T00:00:00Z",
            finished_at="2026-09-22T00:00:01Z",
            duration_s=10,
            min_iterations=2,
            runs=[{"iteration": 1, "exit_code": 1, "elapsed_s": 1.0}],
        )
        self.assertFalse(report["complete"])
        self.assertEqual(report["failed_iterations"], [1])
        self.assertIn("minimum_iterations", report["incomplete_reasons"])
        self.assertIn("child_failed", report["incomplete_reasons"])

    def test_timeout_is_recorded_as_a_failed_iteration(self):
        report = summarize_runs(
            started_at="2026-09-22T00:00:00Z",
            finished_at="2026-09-22T00:00:02Z",
            duration_s=10,
            min_iterations=1,
            runs=[{"iteration": 1, "exit_code": None, "timed_out": True, "elapsed_s": 2.0}],
        )
        self.assertFalse(report["complete"])
        self.assertEqual(report["failed_iterations"], [1])
        self.assertIn("child_timeout", report["incomplete_reasons"])


if __name__ == "__main__":
    unittest.main()

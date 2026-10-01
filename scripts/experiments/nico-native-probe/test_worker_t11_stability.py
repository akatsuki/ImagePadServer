import argparse
import unittest

from worker_t11_stability import _observer_timeout_s


class WorkerT11StabilityTests(unittest.TestCase):
    def test_observer_timeout_covers_requested_finite_iterations(self):
        args = argparse.Namespace(
            duration_s=0.0,
            min_iterations=40,
            per_run_timeout_s=60.0,
        )

        self.assertEqual(_observer_timeout_s(args), 2460.0)

    def test_long_duration_remains_the_dominant_timeout(self):
        args = argparse.Namespace(
            duration_s=1800.0,
            min_iterations=1,
            per_run_timeout_s=120.0,
        )

        self.assertEqual(_observer_timeout_s(args), 1980.0)


if __name__ == "__main__":
    unittest.main()

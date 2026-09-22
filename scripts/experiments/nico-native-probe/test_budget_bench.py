import json
import tempfile
import unittest
from pathlib import Path

import budget_bench


class BudgetBenchTests(unittest.TestCase):
    def test_run_id_is_exclusive(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            budget_bench.create_run(root, "same-run", "production30")
            with self.assertRaises(FileExistsError):
                budget_bench.create_run(root, "same-run", "production30")

    def test_failed_case_is_persisted(self):
        with tempfile.TemporaryDirectory() as temp:
            run = budget_bench.create_run(Path(temp), "failed-run", "production30")
            case = budget_bench.record_case(run, "case-001", {"status": "failed", "error": "timeout"})
            self.assertEqual(json.loads((case / "result.json").read_text()), {"status": "failed", "error": "timeout"})
            budget_bench.finish_run(run, "failed", "case timeout")
            manifest = json.loads((run / "manifest.json").read_text())
            self.assertEqual(manifest["status"], "failed")
            self.assertEqual(manifest["invalid_reason"], "case timeout")

    def test_even_median_uses_saved_wall_seconds(self):
        self.assertEqual(budget_bench.median_wall_seconds([{"wall_s": 1.0}, {"wall_s": 3.0}]), 2.0)

    def test_profiles_are_separate(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            production = budget_bench.create_run(root, "production", "production30")
            legacy = budget_bench.create_run(root, "legacy", "legacy60")
            budget_bench.record_case(production, "case", {"status": "complete", "wall_s": 10.0})
            budget_bench.record_case(legacy, "case", {"status": "complete", "wall_s": 2.0})
            summary = budget_bench.summarize_runs(root)
            self.assertEqual(summary["production30"]["median_wall_s"], 10.0)
            self.assertEqual(summary["legacy60"]["median_wall_s"], 2.0)


if __name__ == "__main__":
    unittest.main()

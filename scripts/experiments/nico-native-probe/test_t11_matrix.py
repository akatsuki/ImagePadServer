import unittest
import sys
from pathlib import Path
from tempfile import TemporaryDirectory

from t11_matrix import (
    QUALITY_MATRIX,
    build_matrix,
    create_matrix_run,
    matrix_case_ids,
    run_matrix,
    run_command_case,
    timeout_seconds,
)


class T11MatrixTests(unittest.TestCase):
    def test_each_scenario_has_warmup_and_alternating_pairs(self):
        matrix = build_matrix()
        self.assertEqual(len(matrix), 33)
        for scenario in ("fixed-6s", "high-density-10s", "real-source-155s"):
            cases = [case for case in matrix if case["scenario"] == scenario]
            self.assertEqual(len(cases), 11)
            self.assertEqual(cases[0]["phase"], "warmup")
            measured = [case["variant"] for case in cases[1:]]
            self.assertEqual(measured, ["A", "B", "B", "A", "A", "B", "B", "A", "A", "B"])

    def test_long_timeout_is_three_times_baseline_but_never_over_30_minutes(self):
        self.assertEqual(timeout_seconds("fixed-6s", None), 600)
        self.assertEqual(timeout_seconds("real-source-155s", 400), 1200)
        self.assertEqual(timeout_seconds("real-source-155s", 700), 1800)

    def test_case_ids_are_unique_and_run_directory_is_exclusive(self):
        matrix = build_matrix()
        self.assertEqual(len(matrix_case_ids(matrix)), len(matrix))
        with TemporaryDirectory() as temp:
            root = Path(temp)
            create_matrix_run(root, "t11-example", matrix)
            with self.assertRaises(FileExistsError):
                create_matrix_run(root, "t11-example", matrix)

    def test_quality_matrix_covers_render_and_clock_boundaries(self):
        self.assertEqual(
            QUALITY_MATRIX,
            [
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
            ],
        )

    def test_timeout_is_persisted_as_failed_case(self):
        with TemporaryDirectory() as temp:
            root = Path(temp)
            case = build_matrix()[0]
            run = create_matrix_run(root, "t11-timeout", [case])
            result = run_command_case(
                run,
                case,
                [sys.executable, "-c", "import time; time.sleep(1)"],
                timeout_override=0.05,
            )
            self.assertEqual(result["status"], "failed")
            self.assertEqual(result["invalid_reason"], "timeout")
            manifest = __import__("json").loads((run / "manifest.json").read_text())
            self.assertEqual(manifest["cases"][case["case_id"]]["status"], "failed")

    def test_success_case_persists_exit_and_output(self):
        with TemporaryDirectory() as temp:
            root = Path(temp)
            case = build_matrix()[0]
            run = create_matrix_run(root, "t11-success", [case])
            result = run_command_case(
                run,
                case,
                [sys.executable, "-c", "print('matrix-ok')"],
                timeout_override=5,
            )
            self.assertEqual(result["status"], "complete")
            self.assertEqual(result["exit_code"], 0)
            self.assertIn("matrix-ok", result["stdout_tail"])
            self.assertTrue((run / "cases" / case["case_id"] / "result.json").is_file())

    def test_matrix_runner_does_not_overwrite_existing_case(self):
        with TemporaryDirectory() as temp:
            root = Path(temp)
            case = build_matrix()[0]
            run = create_matrix_run(root, "t11-resume", [case])
            first = run_matrix(run, {"A": [sys.executable, "-c", "print('first')"]})
            second = run_matrix(run, {"A": [sys.executable, "-c", "print('second')"]})
            self.assertEqual(first[0]["stdout_tail"].strip(), "first")
            self.assertEqual(second, [])
            self.assertEqual(
                __import__("json").loads((run / "manifest.json").read_text())["status"],
                "complete",
            )


if __name__ == "__main__":
    unittest.main()

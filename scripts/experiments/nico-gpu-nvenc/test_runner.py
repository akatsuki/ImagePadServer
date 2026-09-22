import os
import tempfile
import unittest
from pathlib import Path

from runner import EXIT_UNAVAILABLE, inspect_environment, run_experiment


class RunnerTests(unittest.TestCase):
    def test_missing_prerequisites_are_reported_without_path_mutation(self):
        original_path = os.environ.get("PATH")
        with tempfile.TemporaryDirectory() as directory:
            sdk_root = Path(directory) / "missing-sdk"
            environment = {"PATH": "sentinel-path"}
            inspection = inspect_environment(
                sdk_root,
                platform_name="Windows",
                environment=environment,
                command_lookup=lambda _name: None,
                path_exists=lambda _path: False,
            )

        self.assertEqual(os.environ.get("PATH"), original_path)
        self.assertFalse(inspection["ready"])
        self.assertIn("msvc_missing", inspection["missing"])
        self.assertIn("nvenc_header_missing", inspection["missing"])
        self.assertEqual(environment["PATH"], "sentinel-path")

    def test_run_experiment_writes_unavailable_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "result.json"
            code, result = run_experiment(
                output,
                sdk_root=root / "missing-sdk",
                platform_name="Windows",
                environment={"PATH": "sentinel-path"},
                command_lookup=lambda _name: None,
                path_exists=lambda _path: False,
            )

            self.assertEqual(code, EXIT_UNAVAILABLE)
            self.assertEqual(result["status"], "unavailable")
            self.assertTrue(output.is_file())
            self.assertIn("msvc_missing", result["unavailable_reasons"])

    def test_non_windows_is_unavailable_before_build(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "result.json"
            code, result = run_experiment(
                output,
                sdk_root=Path(directory) / "sdk",
                platform_name="Linux",
                environment={},
                command_lookup=lambda _name: "ignored",
                path_exists=lambda _path: True,
            )

        self.assertEqual(code, EXIT_UNAVAILABLE)
        self.assertEqual(result["status"], "unavailable")
        self.assertIn("windows_required", result["unavailable_reasons"])


if __name__ == "__main__":
    unittest.main()

import unittest
from pathlib import Path

from t11_execute import (
    backend_for_case,
    build_case_environment,
    build_runner_command,
    classify_failure,
    input_missing_reason,
    snapshot_for_case,
)


class T11ExecuteTests(unittest.TestCase):
    def _case(self, variant):
        return {"case_id": "fixed-6s-test", "scenario": "fixed-6s", "variant": variant, "timeout_s": 600}

    def test_variants_are_explicit_baseline_and_candidate(self):
        self.assertEqual(backend_for_case(self._case("A"), "browser", "native"), "browser")
        self.assertEqual(backend_for_case(self._case("B"), "browser", "native"), "native")

    def test_high_density_snapshot_is_selected_only_for_high_density_case(self):
        default = Path("snapshot.json")
        dense = Path("snapshot-high-density.json")
        self.assertEqual(snapshot_for_case(self._case("A"), default, dense), default)
        high = {**self._case("A"), "scenario": "high-density-10s"}
        self.assertEqual(snapshot_for_case(high, default, dense), dense)
        self.assertEqual(snapshot_for_case(high, default, None), default)

    def test_missing_snapshot_is_classified_before_launch(self):
        self.assertEqual(input_missing_reason(Path("missing.json"), Path("source.mp4")), "snapshot_missing")
        self.assertEqual(input_missing_reason(Path(__file__), Path("missing.mp4")), "source_missing")

    def test_environment_uses_case_artifact_directory_and_duration(self):
        env = build_case_environment(
            self._case("B"),
            Path("case-dir"),
            snapshot=Path("snapshot.json"),
            source=Path("source.mp4"),
            backend_a="browser",
            backend_b="native",
            gpu_mode="software-inprocess",
            capture_mode="2d",
        )
        self.assertEqual(env["IMAGEPAD_NICO_PRODUCTION_BACKEND"], "native")
        self.assertEqual(env["IMAGEPAD_NICO_PERF_DURATION_MS"], "6000")
        self.assertEqual(env["IMAGEPAD_NICONICO_RENDER_CAPTURE"], "2d")
        self.assertTrue(env["IMAGEPAD_NICO_PRODUCTION_ARTIFACTS"].endswith("case-dir"))

    def test_explicit_compositor_is_opt_in_diagnostic_environment(self):
        env = build_case_environment(
            self._case("B"),
            Path("case-dir"),
            snapshot=Path("snapshot.json"),
            source=Path("source.mp4"),
            backend_a="browser",
            backend_b="native",
            compositor=Path("compositor.exe"),
        )
        self.assertTrue(env["IMAGEPAD_NICO_COMPOSITOR"].endswith("compositor.exe"))

    def test_native_fallback_is_opt_in_diagnostic_environment(self):
        env = build_case_environment(
            self._case("B"),
            Path("case-dir"),
            snapshot=Path("snapshot.json"),
            source=Path("source.mp4"),
            backend_a="browser",
            backend_b="auto",
            allow_native_fallback=True,
        )
        self.assertEqual(env["IMAGEPAD_NICO_ALLOW_NATIVE_FALLBACK"], "1")

    def test_explicit_browser_is_opt_in_diagnostic_environment(self):
        env = build_case_environment(
            self._case("A"),
            Path("case-dir"),
            snapshot=Path("snapshot.json"),
            source=Path("source.mp4"),
            backend_a="browser",
            backend_b="native",
            browser=Path("chrome.exe"),
        )
        self.assertTrue(env["IMAGEPAD_NICONICO_RENDER_BROWSER"].endswith("chrome.exe"))

    def test_runner_command_preserves_cpu20_and_test_selector(self):
        command = build_runner_command(Path("runner.exe"), Path("video.test.exe"), Path("report.json"), Path("."))
        self.assertIn("-cpu-percent", command)
        self.assertIn("20", command)
        self.assertIn("-test.run=^TestNicoProductionPipeline$", command)
        self.assertIn("--", command)

    def test_classifies_browser_gpu_failure_before_cdp_disconnect(self):
        evidence = "GPU process exited unexpectedly: exit_code=-1073741790\nGPU process isn't usable. Goodbye.\ncdp method=Page.enable error=forcibly closed"
        self.assertEqual(
            classify_failure(evidence),
            {
                "class": "browser_gpu_process_unusable",
                "phase": "browser_startup",
                "signals": ["gpu_process_exit", "gpu_process_unusable"],
            },
        )

    def test_classifies_runtime_timeout_and_native_truncation(self):
        self.assertEqual(classify_failure("cdp method=Runtime.evaluate error=timeout"), {"class": "cdp_runtime_evaluate_timeout", "phase": "cdp_runtime_evaluate", "signals": ["runtime_evaluate_timeout"]})
        self.assertEqual(classify_failure("truncated scene"), {"class": "native_scene_truncated", "phase": "native_scene_input", "signals": ["truncated_scene"]})

    def test_classifies_native_compositor_runtime_failure(self):
        self.assertEqual(
            classify_failure("renderer backend=native-warp\nnative runtime failed; regenerating with browser (compositor: exit status 42)"),
            {
                "class": "native_compositor_runtime_failure",
                "phase": "native_render_encode",
                "signals": ["native_runtime_failure", "browser_fallback"],
            },
        )

    def test_classifies_forcibly_closed_browser_transport(self):
        self.assertEqual(
            classify_failure("read tcp wsarecv: An existing connection was forcibly closed by the remote host"),
            {
                "class": "browser_websocket_forcibly_closed",
                "phase": "browser_transport",
                "signals": ["wsarecv", "connection_forcibly_closed"],
            },
        )


if __name__ == "__main__":
    unittest.main()

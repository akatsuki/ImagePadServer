import unittest
from argparse import Namespace
from pathlib import Path

from worker_stability import _request, summarize_runs


class WorkerStabilityTests(unittest.TestCase):
    def test_successful_duration_with_minimum_iterations_is_complete(self):
        runs = [
            {"iteration": 1, "exit_code": 0, "timed_out": False},
            {"iteration": 2, "exit_code": 0, "timed_out": False},
        ]
        report = summarize_runs(
            started_at="start",
            finished_at="finish",
            duration_s=1800,
            min_iterations=2,
            runs=runs,
        )
        self.assertTrue(report["complete"])
        self.assertEqual(report["failed_iterations"], [])

    def test_failed_iteration_is_persisted_and_blocks_completion(self):
        runs = [
            {"iteration": 1, "exit_code": 0, "timed_out": False},
            {"iteration": 2, "exit_code": 1, "timed_out": False},
        ]
        report = summarize_runs(
            started_at="start",
            finished_at="finish",
            duration_s=1800,
            min_iterations=1,
            runs=runs,
        )
        self.assertFalse(report["complete"])
        self.assertEqual(report["failed_iterations"], [2])
        self.assertIn("child_failed", report["incomplete_reasons"])

    def test_timeout_is_recorded_separately(self):
        runs = [{"iteration": 1, "exit_code": None, "timed_out": True}]
        report = summarize_runs(
            started_at="start",
            finished_at="finish",
            duration_s=1800,
            min_iterations=1,
            runs=runs,
        )
        self.assertEqual(report["failed_iterations"], [1])
        self.assertIn("child_timeout", report["incomplete_reasons"])

    def test_request_includes_optional_thread_options(self):
        args = Namespace(
            source=Path("source.mp4"), snapshot=Path("snapshot.json"), ffmpeg=Path("ffmpeg.exe"),
            backend="native", width=1920, height=1080, duration_ms=6000,
            fps_num=30, fps_den=1, crf=26, audio_bitrate="160k",
            browser=None, compositor=Path("compositor.exe"),
            filter_threads=1, decoder_threads=1, encoder_threads=1,
            output_mode="tee",
        )
        request = _request(args, 1, Path("out.mp4"), Path("hls"))
        self.assertEqual(request["filter_threads"], 1)
        self.assertEqual(request["decoder_threads"], 1)
        self.assertEqual(request["encoder_threads"], 1)
        self.assertEqual(request["output_mode"], "tee")


if __name__ == "__main__":
    unittest.main()

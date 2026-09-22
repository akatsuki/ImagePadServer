import unittest

from t11_observe import (
    ProcessInfo,
    _process_details,
    parse_vrchat_performance_stats,
    parse_presentmon_csv,
    summarize_presentmon_rows,
    summarize_observation,
)


class T11ObserveTests(unittest.TestCase):
    def test_process_details_keeps_pid_name_and_cpu_for_posthoc_attribution(self):
        processes = {
            20: ProcessInfo(20, 10, "ffmpeg.exe", 2.5, 200, 8),
            10: ProcessInfo(10, 0, "nico-compositor.exe", 1.25, 300, 9),
        }
        self.assertEqual(
            _process_details(processes, {20, 10}),
            [
                {
                    "pid": 10,
                    "parent_pid": 0,
                    "name": "nico-compositor.exe",
                    "cpu_seconds": 1.25,
                    "working_set_bytes": 300,
                    "handles": 9,
                },
                {
                    "pid": 20,
                    "parent_pid": 10,
                    "name": "ffmpeg.exe",
                    "cpu_seconds": 2.5,
                    "working_set_bytes": 200,
                    "handles": 8,
                },
            ],
        )

    def test_parse_vrchat_performance_stats_converts_frame_times(self):
        text = """
2026.09.21 Debug - [Performance Stats]
{"runningTime": 40.0, "stats": [
  {"name": "fps", "units": "f/s", "mean": 36.63630, "tw-mean": 35.45611},
  {"name": "cpu_frame_time", "units": "f/s", "mean": 0.02745, "tw-mean": 0.02897},
  {"name": "gpu_frame_time", "units": "f/s", "mean": 0.00068, "tw-mean": 0.00068}
]}
not-json
"""
        rows = parse_vrchat_performance_stats(text)
        self.assertEqual(len(rows), 1)
        self.assertAlmostEqual(rows[0]["running_time_s"], 40.0)
        self.assertAlmostEqual(rows[0]["fps_mean"], 36.63630)
        self.assertAlmostEqual(rows[0]["frame_time_ms"], 1000 / 36.63630, places=5)
        self.assertAlmostEqual(rows[0]["cpu_frame_time_ms"], 27.45, places=5)
        self.assertAlmostEqual(rows[0]["gpu_frame_time_ms"], 0.68, places=5)

    def test_parse_vrchat_performance_stats_ignores_incomplete_records(self):
        text = '{"runningTime": 1, "stats": [{"name": "ping", "mean": 20}]}'
        self.assertEqual(parse_vrchat_performance_stats(text), [])

    def test_summary_keeps_vrchat_gate_pending_and_reports_residue(self):
        samples = [
            {
                "at_s": 0.0,
                "processes": {"count": 2, "working_set_bytes": 100, "handles": 5, "cpu_seconds": 0.25},
                "vrchat": {"count": 1, "pids": [99]},
                "process_snapshot_error": "ignored-for-summary",
            },
            {
                "at_s": 1.0,
                "processes": {
                    "count": 2,
                    "working_set_bytes": 150,
                    "handles": 7,
                    "cpu_seconds": 0.5,
                    "pids": [10, 11],
                },
                "vrchat": {"count": 1, "pids": [99]},
            },
        ]
        summary = summarize_observation(
            samples,
            observed_pids={10, 11},
            final_processes={11: ProcessInfo(11, 10, "child.exe", 1.0, 20, 2)},
            gpu_samples=[[{"utilization_gpu_percent": 50.0, "utilization_encoder_percent": 0.0, "memory_used_mib": 100.0, "memory_total_mib": 1000.0}]],
            gpu_errors=[],
            timed_out=False,
            exit_code=1,
            logical_cpus=16,
        )
        self.assertTrue(summary["vrc_acceptance_pending"])
        self.assertEqual(summary["vrchat"]["acceptance_status"], "pending")
        self.assertEqual(summary["child_residue"]["pids"], [11])
        self.assertEqual(summary["cpu_seconds_observed"], 0.5)
        self.assertEqual(summary["gpu"]["memory_used_mib"]["max"], 100.0)

    def test_summary_marks_missing_vrchat_without_fabricating_metrics(self):
        summary = summarize_observation(
            [],
            observed_pids=set(),
            final_processes={},
            gpu_samples=[],
            gpu_errors=["nvidia_smi_unavailable"],
            timed_out=True,
            exit_code=-9,
            logical_cpus=8,
        )
        self.assertIsNone(summary["cpu_seconds_observed"])
        self.assertTrue(summary["vrc_acceptance_pending"])
        self.assertIn("vrchat_process_not_detected", summary["vrchat"]["pending_reasons"])
        self.assertFalse(summary["gpu"]["available"])

    def test_summary_records_log_telemetry_without_turning_it_into_acceptance(self):
        summary = summarize_observation(
            [{"at_s": 0.0, "processes": {}, "vrchat": {"count": 1, "pids": [99]}}],
            observed_pids=set(),
            final_processes={},
            gpu_samples=[],
            gpu_errors=[],
            timed_out=False,
            exit_code=0,
            logical_cpus=8,
            vrchat_stats=[
                {
                    "running_time_s": 40.0,
                    "fps_mean": 36.6363,
                    "frame_time_ms": 1000.0 / 36.6363,
                }
            ],
        )
        self.assertAlmostEqual(summary["vrchat"]["frame_time_ms"], 1000.0 / 36.6363)
        self.assertEqual(summary["vrchat"]["acceptance_status"], "measured")
        self.assertTrue(summary["vrc_acceptance_pending"])

    def test_presentmon_parser_and_summary_keep_dropped_rows_separate(self):
        text = """Application,ProcessID,TimeInSeconds,Dropped,msBetweenPresents,msGPUActive
VRChat,58216,2026-9-22 8:02:04.000000000,0,27.0,8.0
VRChat,58216,2026-9-22 8:02:04.030000000,1,30.0,10.0
Other,77,2026-9-22 8:02:04.040000000,0,99.0,1.0
VRChat,58216,2026-9-22 8:02:04.080000000,0,25.0,12.0
"""
        rows = parse_presentmon_csv(text, process_id=58216)
        self.assertEqual(len(rows), 3)
        summary = summarize_presentmon_rows(rows)
        self.assertEqual(summary["sample_count"], 3)
        self.assertEqual(summary["dropped_present_rows"], 1)
        self.assertAlmostEqual(summary["dropped_present_ratio"], 1 / 3)
        self.assertAlmostEqual(summary["ms_between_presents"]["p50"], 27.0)
        self.assertAlmostEqual(summary["ms_between_presents"]["p95"], 30.0)
        self.assertAlmostEqual(summary["ms_between_presents"]["p99"], 30.0)
        self.assertAlmostEqual(summary["ms_gpu_active"]["mean"], 10.0)

    def test_presentmon_summary_can_limit_to_export_time_window(self):
        text = """Application,ProcessID,TimeInSeconds,Dropped,msBetweenPresents,msGPUActive
VRChat,58216,2026-9-22 8:02:00.000000000,0,20.0,8.0
VRChat,58216,2026-9-22 8:02:04.000000000,0,27.0,9.0
VRChat,58216,2026-9-22 8:02:05.000000000,0,29.0,10.0
"""
        rows = parse_presentmon_csv(text, process_id=58216)
        summary = summarize_presentmon_rows(
            rows,
            start_time="2026-09-22T08:02:04+00:00",
            end_time="2026-09-22T08:02:05+00:00",
        )
        self.assertEqual(summary["sample_count"], 2)
        self.assertEqual(summary["capture_first_time"], "2026-09-22T08:02:04+00:00")
        self.assertEqual(summary["capture_last_time"], "2026-09-22T08:02:05+00:00")


if __name__ == "__main__":
    unittest.main()

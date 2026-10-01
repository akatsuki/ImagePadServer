import unittest

from t6_acceptance import (
    REQUIRED_QUALITY_MATRIX,
    REQUIRED_SCENARIOS,
    evaluate_acceptance,
    evaluate_matrix_report,
    evaluate_operational_report,
    evaluate_statistics_report,
    evaluate_vrchat_report,
    evaluate_worker_report,
)


def _report(*, mode: str, worker_hash: str = "worker", wall_s: float = 10.0, complete: bool = True):
    return {
        "cpu_budget_percent": 20,
        "backend": "browser",
        "output_mode": mode,
        "parameters": {
            "width": 1920,
            "height": 1080,
            "duration_ms": 6000,
            "fps_num": 30,
            "fps_den": 1,
            "crf": 26,
            "audio_bitrate": "160k",
        },
        "runner_verified": True,
        "runner_report": {"budget": {"Percent": 20}},
        "worker": {
            "complete": complete,
            "iterations": 2,
            "duration_s": wall_s,
            "runs": [{"iteration": 1, "result_ok": complete}, {"iteration": 2, "result_ok": complete}],
        },
        "timed_out": False,
        "exit_code": 0 if complete else 1,
        "inputs": {
            "worker_sha256": worker_hash,
            "ffmpeg_sha256": "ffmpeg",
            "source_sha256": "source",
            "snapshot_sha256": "snapshot",
            "browser_sha256": "browser",
            "compositor_sha256": None,
        },
        "observation": {"gpu": {"available": True}},
    }


def _vrchat():
    return {
        "status": "complete",
        "segments": [
            {
                "name": name,
                "duration_s": 60,
                "cpu_budget_percent": 20,
                "frame_time_ms": {"p95": 16.7, "p99": 20.0},
                "player_av_verified": True,
            }
            for name in ("baseline-before", "candidate", "baseline-after")
        ],
    }


def _matrix():
    return {
        "matrix_complete": True,
        "scenarios": {name: {"n": 10} for name in REQUIRED_SCENARIOS},
        "quality_matrix": list(REQUIRED_QUALITY_MATRIX),
        "case_counts": {"complete": 30},
    }


def _operational():
    return {
        "status": "complete",
        "cancel": {"verified": True},
        "rerun": {"verified": True},
        "long_run": {
            "duration_s": 1800,
            "queue_stable": True,
            "vram_stable": True,
            "child_residue_clear": True,
        },
    }


def _statistics():
    return {
        "status": "complete",
        "pairs": {name: 40 for name in REQUIRED_SCENARIOS},
        "bootstrap": {
            "resamples": 10000,
            "median_speedup_ci_lower_percent": 5.0,
            "p95_delta_ci_upper_percent": 0.0,
        },
        "cpu_total_not_increased": True,
        "candidate_threshold_met": True,
    }


class T6AcceptanceTests(unittest.TestCase):
    def test_complete_same_input_pair_with_vrchat_is_eligible(self):
        result = evaluate_acceptance(
            _report(mode="separate", wall_s=10),
            _report(mode="tee", wall_s=8),
            vrchat=_vrchat(),
            baseline_matrix=_matrix(),
            candidate_matrix=_matrix(),
            operational=_operational(),
            statistics=_statistics(),
        )
        self.assertEqual(result["status"], "eligible")
        self.assertAlmostEqual(result["speedup_percent"], 25.0)

    def test_missing_vrchat_never_becomes_eligible(self):
        result = evaluate_acceptance(_report(mode="separate"), _report(mode="tee"))
        self.assertEqual(result["status"], "blocked")
        self.assertIn("vrchat_evidence_missing", result["reasons"])

    def test_worker_failure_and_input_mismatch_are_blocking(self):
        candidate = _report(mode="tee", complete=False, worker_hash="different")
        candidate["inputs"]["ffmpeg_sha256"] = "different-ffmpeg"
        result = evaluate_acceptance(_report(mode="separate"), candidate, vrchat=_vrchat())
        self.assertEqual(result["status"], "blocked")
        self.assertIn("candidate:worker_stability_incomplete", result["reasons"])
        self.assertIn("baseline_candidate_input_mismatch", result["reasons"])

    def test_vrchat_requires_all_three_60_second_segments_and_av(self):
        gate = evaluate_vrchat_report({"status": "complete", "segments": []})
        self.assertEqual(gate["status"], "blocked")
        self.assertIn("vrchat_segment_candidate_missing", gate["reasons"])

    def test_matrix_and_operational_evidence_are_required(self):
        matrix = evaluate_matrix_report(None, label="candidate")
        operational = evaluate_operational_report(None)
        self.assertEqual(matrix["status"], "pending")
        self.assertEqual(operational["status"], "pending")

        result = evaluate_acceptance(_report(mode="separate"), _report(mode="tee"), vrchat=_vrchat())
        self.assertEqual(result["status"], "blocked")
        self.assertIn("candidate:matrix_evidence_missing", result["reasons"])
        self.assertIn("operational_evidence_missing", result["reasons"])

    def test_statistics_requires_40_pairs_and_bootstrap_gate(self):
        gate = evaluate_statistics_report(None)
        self.assertEqual(gate["status"], "pending")
        result = evaluate_acceptance(
            _report(mode="separate"),
            _report(mode="tee"),
            vrchat=_vrchat(),
            baseline_matrix=_matrix(),
            candidate_matrix=_matrix(),
            operational=_operational(),
            statistics=_statistics(),
        )
        self.assertEqual(result["status"], "eligible")

    def test_cpu_budget_mismatch_is_blocking(self):
        report = _report(mode="separate")
        report["runner_report"]["budget"]["Percent"] = 100
        gate = evaluate_worker_report(report, label="baseline", expected_output_mode="separate")
        self.assertEqual(gate["status"], "blocked")
        self.assertIn("baseline:runner_budget_missing_or_mismatched", gate["reasons"])

    def test_native_pair_uses_compositor_hash_instead_of_browser_hash(self):
        baseline = _report(mode="separate")
        candidate = _report(mode="tee", wall_s=9)
        for report in (baseline, candidate):
            report["backend"] = "native"
            report["inputs"]["browser_sha256"] = None
            report["inputs"]["compositor_sha256"] = "compositor"
        result = evaluate_acceptance(
            baseline,
            candidate,
            vrchat=_vrchat(),
            baseline_matrix=_matrix(),
            candidate_matrix=_matrix(),
            operational=_operational(),
            statistics=_statistics(),
        )
        self.assertEqual(result["status"], "eligible")


if __name__ == "__main__":
    unittest.main()

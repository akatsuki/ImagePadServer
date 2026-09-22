import json
import tempfile
import unittest
from pathlib import Path

from manifest import ManifestError, build_result, validate_result, write_result


class ManifestTests(unittest.TestCase):
    def test_unavailable_result_is_explicit_and_never_passes_a_gate(self):
        result = build_result(
            "unavailable",
            reason="MSVC and the NVIDIA Video Codec SDK are not available",
            checks={"msvc": False, "nvenc_header": False},
        )

        self.assertEqual(result["schema_version"], 1)
        self.assertEqual(result["experiment"], "nico-gpu-nvenc")
        self.assertEqual(result["status"], "unavailable")
        self.assertIn("MSVC", result["reason"])
        self.assertFalse(result["gates"]["pool_capacity_exceeded_before_eos"])
        self.assertFalse(result["counters"]["eos_sent"])
        self.assertEqual(validate_result(result), [])

    def test_pass_requires_four_pool_turns_before_eos(self):
        result = build_result(
            "passed",
            checks={
                "d3d11_texture_registration": True,
                "nvenc_acceptance": True,
                "pool_progress": True,
            },
            counters={
                "pool_capacity": 4,
                "submitted_before_eos": 16,
                "accepted_frames": 16,
                "eos_sent": True,
                "eos_completed": True,
            },
        )

        self.assertEqual(validate_result(result), [])
        self.assertTrue(result["gates"]["pool_capacity_exceeded_before_eos"])

    def test_pass_is_rejected_when_eos_precedes_four_pool_turns(self):
        result = build_result(
            "passed",
            checks={
                "d3d11_texture_registration": True,
                "nvenc_acceptance": True,
                "pool_progress": True,
            },
            counters={
                "pool_capacity": 4,
                "submitted_before_eos": 15,
                "accepted_frames": 15,
                "eos_sent": True,
                "eos_completed": True,
            },
        )

        self.assertIn("pool_capacity_exceeded_before_eos", " ".join(validate_result(result)))
        with self.assertRaises(ManifestError):
            write_result(Path(tempfile.gettempdir()) / "nico-gpu-nvenc-invalid.json", result)

    def test_result_round_trips_as_json(self):
        result = build_result("unavailable", reason="windows_required")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "result.json"
            write_result(path, result)
            self.assertEqual(json.loads(path.read_text(encoding="utf-8")), result)


if __name__ == "__main__":
    unittest.main()

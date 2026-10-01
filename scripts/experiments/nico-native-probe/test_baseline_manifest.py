import hashlib
import json
import tempfile
import unittest
from pathlib import Path

import baseline_manifest


class BaselineManifestTests(unittest.TestCase):
    def test_file_identity_records_sha256_and_relative_path(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            target = root / "fixture.bin"
            target.write_bytes(b"nico-baseline")

            identity = baseline_manifest.file_identity(root, target)

            self.assertEqual(identity["path"], "fixture.bin")
            self.assertEqual(identity["bytes"], len(b"nico-baseline"))
            self.assertEqual(identity["sha256"], hashlib.sha256(b"nico-baseline").hexdigest())

    def test_manifest_marks_missing_worker_as_invalid_without_hiding_it(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            ffmpeg = root / "ffmpeg.exe"
            source = root / "source.mp4"
            summary = root / "summary.json"
            ffmpeg.write_bytes(b"ffmpeg")
            source.write_bytes(b"source")
            summary.write_text(json.dumps({"valid": True}), encoding="utf-8")

            manifest = baseline_manifest.build_manifest(
                root,
                ffmpeg=ffmpeg,
                source=source,
                run_summary=summary,
                worker=root / "missing-worker.exe",
            )

            self.assertFalse(manifest["valid"])
            self.assertIn("worker_missing", manifest["invalid_reasons"])
            self.assertEqual(manifest["artifacts"]["worker"]["exists"], False)
            self.assertEqual(manifest["stage_definition"][0]["name"], "frame_source_next")


if __name__ == "__main__":
    unittest.main()

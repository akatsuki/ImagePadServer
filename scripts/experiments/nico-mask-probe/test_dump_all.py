import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
SDK = ROOT / "build/nico-mask-probe/deps/ffmpeg-9.0.1-full_build-shared"


class DumpAllTests(unittest.TestCase):
    def test_dump_all_writes_each_requested_frame(self):
        executable = ROOT / "build/nico-mask-probe/mask-probe.exe"
        scene = ROOT / "build/nico-mask-probe/fixtures/real.reference.nmf1"
        self.assertTrue(executable.is_file())
        self.assertTrue(scene.is_file())
        environment = dict(
            os.environ,
            PATH=str(SDK / "bin")
            + os.pathsep
            + "C:/msys64/ucrt64/bin"
            + os.pathsep
            + os.environ.get("PATH", ""),
        )
        with tempfile.TemporaryDirectory() as temp:
            dump_dir = Path(temp) / "pixels"
            output = Path(temp) / "out.mp4"
            result = subprocess.run(
                [
                    str(executable),
                    str(scene),
                    "8",
                    "D",
                    str(output),
                    "--frames",
                    "3",
                    "--dump",
                    str(dump_dir),
                    "--dump-all",
                ],
                cwd=ROOT,
                env=environment,
                capture_output=True,
                text=True,
                timeout=120,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                sorted(path.name for path in dump_dir.glob("frame-*.rgba")),
                ["frame-0.rgba", "frame-1.rgba", "frame-2.rgba"],
            )


if __name__ == "__main__":
    unittest.main()

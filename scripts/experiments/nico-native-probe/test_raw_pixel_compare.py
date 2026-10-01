import tempfile
import unittest
from pathlib import Path

from raw_pixel_compare import compare_raw_frames


class RawPixelCompareTests(unittest.TestCase):
    def test_compares_selected_frames_and_marks_sparse_scope(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            browser = root / "browser"
            browser.mkdir()
            native = root / "frames.bin"
            frames = [bytes([0, 1, 2, 3]), bytes([4, 5, 6, 7]), bytes([8, 9, 10, 11])]
            native.write_bytes(b"".join(frames))
            (browser / "frame-000.rgba").write_bytes(frames[0])
            (browser / "frame-002.rgba").write_bytes(frames[2])

            result = compare_raw_frames(
                browser_dir=browser,
                native_path=native,
                width=1,
                height=1,
                frame_indices=[0, 2],
                browser_template="frame-{index:03d}.rgba",
                total_frames=3,
            )

        self.assertEqual(result["scope"], "sparse")
        self.assertEqual(result["frames_compared"], 2)
        self.assertEqual(result["missing_frames"], [])
        self.assertEqual(result["identical_frames"], 2)
        self.assertEqual(result["changed_channels"], 0)
        self.assertEqual(result["max_channel_diff"], 0)

    def test_reports_pixel_differences(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            browser = root / "browser"
            browser.mkdir()
            native = root / "frames.bin"
            native.write_bytes(bytes([0, 10, 20, 30]))
            (browser / "frame-000.rgba").write_bytes(bytes([0, 12, 20, 29]))

            result = compare_raw_frames(
                browser_dir=browser,
                native_path=native,
                width=1,
                height=1,
                frame_indices=[0],
                browser_template="frame-{index:03d}.rgba",
                total_frames=1,
            )

        self.assertEqual(result["scope"], "full")
        self.assertEqual(result["identical_frames"], 0)
        self.assertEqual(result["changed_channels"], 2)
        self.assertEqual(result["max_channel_diff"], 2)
        self.assertEqual(result["changed_frames"], [0])
        self.assertAlmostEqual(result["mean_abs_channel"], 0.75)

    def test_rejects_missing_or_malformed_frames(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            browser = root / "browser"
            browser.mkdir()
            native = root / "frames.bin"
            native.write_bytes(bytes(4))

            with self.assertRaises(FileNotFoundError):
                compare_raw_frames(
                    browser_dir=browser,
                    native_path=native,
                    width=1,
                    height=1,
                    frame_indices=[0],
                    browser_template="frame-{index:03d}.rgba",
                    total_frames=1,
                )

            (browser / "frame-000.rgba").write_bytes(bytes(3))
            with self.assertRaises(ValueError):
                compare_raw_frames(
                    browser_dir=browser,
                    native_path=native,
                    width=1,
                    height=1,
                    frame_indices=[0],
                    browser_template="frame-{index:03d}.rgba",
                    total_frames=1,
                )


if __name__ == "__main__":
    unittest.main()

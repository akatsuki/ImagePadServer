import unittest
from pathlib import Path
from tempfile import TemporaryDirectory

from media_validation import (
    ValidationError,
    check_frame_pts,
    h264_access_unit_slice_counts,
    parse_hls_playlist,
    summarize_timestamped_durations,
)


class MediaValidationTests(unittest.TestCase):
    def test_frame_pts_uses_exact_rational_clock(self):
        frames = [
            {"best_effort_timestamp": "0"},
            {"best_effort_timestamp": "3003"},
            {"best_effort_timestamp": "6006"},
        ]
        self.assertEqual(check_frame_pts(frames, "1/90000", "30000/1001", 3), 3)

    def test_frame_pts_rejects_drift_that_float_tolerance_can_hide(self):
        frames = [
            {"best_effort_timestamp": "0"},
            {"best_effort_timestamp": "3002"},
        ]
        with self.assertRaises(ValidationError):
            check_frame_pts(frames, "1/90000", "30000/1001", 2)

    def test_h264_requires_one_vcl_nal_per_access_unit(self):
        stream = b"\x00\x00\x00\x01\x09\xf0" + b"\x00\x00\x01\x41\x80"
        stream += b"\x00\x00\x01\x09\xf0" + b"\x00\x00\x01\x41\x80"
        self.assertEqual(h264_access_unit_slice_counts(stream), [1, 1])

    def test_h264_rejects_two_slices_in_one_access_unit(self):
        stream = b"\x00\x00\x01\x09\xf0" + b"\x00\x00\x01\x41\x80"
        stream += b"\x00\x00\x01\x41\x80"
        with self.assertRaises(ValidationError):
            h264_access_unit_slice_counts(stream, require_single_slice=True)

    def test_hls_playlist_requires_endlist_and_safe_segments(self):
        with TemporaryDirectory() as temp:
            root = Path(temp)
            playlist = root / "playlist.m3u8"
            (root / "a.ts").write_bytes(b"a")
            (root / "b.ts").write_bytes(b"b")
            playlist.write_text(
                "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:1.0,\na.ts\n"
                "#EXTINF:1.0,\nb.ts\n#EXT-X-ENDLIST\n",
                encoding="utf-8",
            )
            result = parse_hls_playlist(playlist)
            self.assertEqual([path.name for path in result["segments"]], ["a.ts", "b.ts"])
            self.assertTrue(result["endlist"])

    def test_hls_playlist_rejects_traversal_and_missing_endlist(self):
        with TemporaryDirectory() as temp:
            root = Path(temp)
            playlist = root / "playlist.m3u8"
            playlist.write_text("#EXTM3U\n#EXTINF:1,\n../outside.ts\n", encoding="utf-8")
            with self.assertRaises(ValidationError):
                parse_hls_playlist(playlist)

    def test_timestamped_duration_uses_timeline_span_and_keeps_payload_sum(self):
        summary = summarize_timestamped_durations(
            [
                {"start_time": "1.445333", "duration": "3.904000"},
                {"start_time": "5.413333", "duration": "3.968000"},
            ]
        )
        self.assertEqual(summary["source"], "timestamps")
        self.assertAlmostEqual(summary["payload_duration"], 7.872, places=6)
        self.assertAlmostEqual(summary["duration"], 7.936, places=6)
        self.assertAlmostEqual(summary["timeline_start"], 1.445333, places=6)
        self.assertAlmostEqual(summary["timeline_end"], 9.381333, places=6)

    def test_timestamped_duration_falls_back_to_payload_without_start_time(self):
        summary = summarize_timestamped_durations(
            [{"duration": "3.904000"}, {"duration": "3.968000"}]
        )
        self.assertEqual(summary["source"], "payload")
        self.assertAlmostEqual(summary["duration"], 7.872, places=6)
        self.assertIsNone(summary["timeline_start"])

if __name__ == "__main__":
    unittest.main()

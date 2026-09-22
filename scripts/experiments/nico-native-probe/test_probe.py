import unittest
from pathlib import Path

from probe import build_encoder_args


class ProbeEncoderArgsTests(unittest.TestCase):
    def test_thread_options_are_explicitly_scoped(self):
        args = build_encoder_args(
            Path("ffmpeg.exe"),
            Path("source.mp4"),
            Path("output.mp4"),
            1920,
            1080,
            180,
            30,
            1,
            filter_threads=1,
            decoder_threads=4,
            encoder_threads=8,
        )
        self.assertEqual(args[args.index("-filter_complex_threads") + 1], "1")
        self.assertEqual(args[args.index("-threads") + 1], "4")
        self.assertEqual(args[args.index("-threads:v") + 1], "8")
        self.assertLess(args.index("-threads"), args.index("-i"))
        self.assertGreater(args.index("-threads:v"), args.index("-c:v"))


if __name__ == "__main__":
    unittest.main()

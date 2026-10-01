import unittest

from ffmpeg_diagnostic import DiagnosticError, _OPTION_RE, build_diagnostic_args


class FFmpegDiagnosticTests(unittest.TestCase):
    def test_help_parser_accepts_indented_and_unindented_option_rows(self):
        text = "  -benchmark add timings\n-benchmark_all add per-task timings\n"
        self.assertEqual(set(_OPTION_RE.findall(text)), {"-benchmark", "-benchmark_all"})

    def test_benchmark_flags_are_added_only_when_advertised(self):
        args = build_diagnostic_args(
            ["-i", "source.mp4", "-f", "null", "-"],
            {"-benchmark", "-benchmark_all"},
            benchmark_all=True,
            verbose_filters=True,
        )
        self.assertEqual(
            args,
            ["-i", "source.mp4", "-f", "null", "-", "-benchmark_all", "-loglevel", "verbose"],
        )

    def test_unknown_benchmark_option_is_rejected(self):
        with self.assertRaises(DiagnosticError):
            build_diagnostic_args(["-f", "null", "-"], set(), benchmark_all=False)

    def test_benchmark_all_does_not_fallback_to_benchmark(self):
        with self.assertRaises(DiagnosticError):
            build_diagnostic_args(["-f", "null", "-"], {"-benchmark"}, benchmark_all=True)


if __name__ == "__main__":
    unittest.main()

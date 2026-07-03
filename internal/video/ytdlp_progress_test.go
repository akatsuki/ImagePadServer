package video

import "testing"

func TestParseYTDLPProgressPercent(t *testing.T) {
	line := "[download]  42.3% of 12.34MiB at 1.23MiB/s ETA 00:04"
	progress, ok := parseYTDLPProgress(line)
	if !ok {
		t.Fatal("expected progress line to parse")
	}
	if progress.Percent != 42 {
		t.Fatalf("Percent = %d, want 42", progress.Percent)
	}
	if progress.Text != line {
		t.Fatalf("Text = %q, want %q", progress.Text, line)
	}
}

func TestParseYTDLPProgressIgnoresNonDownloadLine(t *testing.T) {
	if progress, ok := parseYTDLPProgress("[youtube] Downloading webpage"); ok {
		t.Fatalf("unexpected progress: %+v", progress)
	}
}

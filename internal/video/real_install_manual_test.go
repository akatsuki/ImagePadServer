package video

import (
	"os"
	"testing"
)

// TestRealInstallEndToEnd performs an ACTUAL network download of ffmpeg and
// ffprobe into a clean temp data dir, with PATH and IMAGEPAD_* overrides
// cleared, to prove the bundled-only install pipeline (sources + retry +
// extract + validate) works without any reliance on a system PATH binary.
//
// It is skipped unless IMAGEPAD_RUN_REAL_INSTALL=1 because it downloads
// ~160MB and hits the network. Run it explicitly:
//
//	IMAGEPAD_RUN_REAL_INSTALL=1 go test ./internal/video -run TestRealInstallEndToEnd -timeout 600s -v
func TestRealInstallEndToEnd(t *testing.T) {
	if os.Getenv("IMAGEPAD_RUN_REAL_INSTALL") != "1" {
		t.Skip("set IMAGEPAD_RUN_REAL_INSTALL=1 to run the real network install test")
	}
	// Force a from-scratch bundle install: fresh data dir, no env overrides,
	// empty PATH so a stray system ffmpeg cannot satisfy resolution.
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", "")
	t.Setenv("IMAGEPAD_FFPROBE", "")
	t.Setenv("PATH", "")

	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		t.Fatalf("EnsureFFmpeg real install failed: %v", err)
	}
	if ffmpeg != localFFmpegPath() {
		t.Fatalf("ffmpeg = %q, want bundle path %q", ffmpeg, localFFmpegPath())
	}
	if err := validateExecutable(ffmpeg, "-version"); err != nil {
		t.Fatalf("installed ffmpeg failed -version: %v", err)
	}

	ffprobe, err := EnsureFFprobe()
	if err != nil {
		t.Fatalf("EnsureFFprobe real install failed: %v", err)
	}
	if err := validateExecutable(ffprobe, "-version"); err != nil {
		t.Fatalf("installed ffprobe failed -version: %v", err)
	}

	t.Logf("real install OK: ffmpeg=%s ffprobe=%s", ffmpeg, ffprobe)
}

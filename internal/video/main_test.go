package video

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMain pins IMAGEPAD_FFMPEG/IMAGEPAD_FFPROBE to the dev machine's tools
// when present on PATH. Production resolves tools from the bundle only (never
// PATH), so without these explicit overrides a test — or a leaked background
// queue worker — that calls EnsureFFmpeg/EnsureFFprobe with no bundle present
// would attempt a real multi-hundred-MB network download and stall the suite
// (the install mutex is held for the duration of a download, blocking other
// tool calls). Locating the dev tool via PATH here is a test convenience only;
// tests that exercise resolution behavior override these via t.Setenv.
func TestMain(m *testing.M) {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		_ = os.Setenv("IMAGEPAD_FFMPEG", p)
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		_ = os.Setenv("IMAGEPAD_FFPROBE", p)
	}
	os.Exit(m.Run())
}

// requireFakeYTDLP writes a minimal fake yt-dlp that exits 0 for --version,
// sets IMAGEPAD_YTDLP to it via t.Setenv, and returns the path.
// Use in tests that mock runDownloadCmd and just need EnsureYTDLP to succeed
// without a real yt-dlp installation.
func requireFakeYTDLP(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var path, content string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "yt-dlp.bat")
		content = "@echo off\r\necho 2025.01.01\r\n"
	} else {
		path = filepath.Join(dir, "yt-dlp")
		content = "#!/bin/sh\necho 2025.01.01\n"
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_YTDLP", path)
	return path
}

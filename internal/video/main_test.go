package video

import (
	"os"
	"os/exec"
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

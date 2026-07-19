package server

import (
	"os"
	"os/exec"
	"testing"
)

// TestMain pins developer/CI tools for the whole server test process. Several
// endpoint tests intentionally start the asynchronous tool readiness path; a
// per-test t.Setenv can be restored before that goroutine exits, causing a real
// download to open the next test's TempDir on Windows. Process-wide explicit
// paths keep those checks deterministic and prevent network/background file
// handles from leaking across test boundaries.
func TestMain(m *testing.M) {
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		_ = os.Setenv("IMAGEPAD_FFMPEG", path)
	}
	if path, err := exec.LookPath("ffprobe"); err == nil {
		_ = os.Setenv("IMAGEPAD_FFPROBE", path)
	}
	os.Exit(m.Run())
}

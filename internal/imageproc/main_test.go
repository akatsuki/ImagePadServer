package imageproc

import (
	"os"
	"os/exec"
	"testing"
)

func TestMain(m *testing.M) {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		_ = os.Setenv("IMAGEPAD_FFMPEG", p)
	}
	os.Exit(m.Run())
}

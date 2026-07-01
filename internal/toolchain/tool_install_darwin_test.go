//go:build darwin
// +build darwin

package toolchain

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinLocalToolInstall(t *testing.T) {
	if os.Getenv("IMAGEPAD_RUN_LOCAL_TOOL_INSTALL_TESTS") != "1" {
		t.Skip("set IMAGEPAD_RUN_LOCAL_TOOL_INSTALL_TESTS=1 to download and verify local app tools")
	}

	dataDir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dataDir)
	t.Setenv("PATH", "/usr/bin:/bin")

	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		t.Fatalf("EnsureFFmpeg: %v", err)
	}
	wantFFmpeg := filepath.Join(dataDir, "bin", "ffmpeg")
	if ffmpeg != wantFFmpeg {
		t.Fatalf("ffmpeg path = %q, want app-local %q", ffmpeg, wantFFmpeg)
	}
	if err := exec.Command(ffmpeg, "-version").Run(); err != nil {
		t.Fatalf("app-local ffmpeg did not run: %v", err)
	}

	ytdlp, err := EnsureYTDLP()
	if err != nil {
		t.Fatalf("EnsureYTDLP: %v", err)
	}
	wantYTDLP := filepath.Join(dataDir, "bin", "yt-dlp")
	if ytdlp != wantYTDLP {
		t.Fatalf("yt-dlp path = %q, want app-local %q", ytdlp, wantYTDLP)
	}
	if err := exec.Command(ytdlp, "--version").Run(); err != nil {
		t.Fatalf("app-local yt-dlp did not run: %v", err)
	}
}

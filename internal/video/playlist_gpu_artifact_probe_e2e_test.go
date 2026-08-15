package video

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPlaylistGPUArtifactProbeFFmpegE2E(t *testing.T) {
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg is not installed")
		}
	}
	ffprobe := os.Getenv("IMAGEPAD_FFPROBE")
	if ffprobe == "" {
		var err error
		ffprobe, err = exec.LookPath("ffprobe")
		if err != nil {
			t.Skip("ffprobe is not installed")
		}
	}
	path := filepath.Join(t.TempDir(), "playlist-gpu-artifact.ts")
	ctx, cancel := context.WithTimeout(context.Background(), 30_000_000_000)
	defer cancel()
	generate := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-t", "0.933333", "-i", "testsrc=size=64x36:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-shortest",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv",
		"-c:a", "aac", "-f", "mpegts", path,
	)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate artifact: %v: %s", err, output)
	}
	decode := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-f", "null", "-")
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("decode artifact: %v: %s", err, output)
	}
	report, err := ProbePlaylistGPUArtifact(ctx, ffprobe, path, 30, 28)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("artifact report: %+v", report)
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
}

package video

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

func TestNicoPipelinePerformance(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_PERF_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_PERF_TEST=1 with local snapshot and six-second source")
	}
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	ffmpeg := os.Getenv("IMAGEPAD_NICO_PERF_FFMPEG")
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot niconico.Snapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	durationMs := int64(6000)
	if value := os.Getenv("IMAGEPAD_NICO_PERF_DURATION_MS"); value != "" {
		durationMs, err = strconv.ParseInt(value, 10, 64)
		if err != nil || durationMs <= 0 {
			t.Fatal("invalid IMAGEPAD_NICO_PERF_DURATION_MS")
		}
	}
	batchFrames := 0
	sparseFrames := os.Getenv("IMAGEPAD_NICO_PERF_SPARSE") == "1"
	if value := os.Getenv("IMAGEPAD_NICO_PERF_BATCH_FRAMES"); value != "" {
		batchFrames, err = strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"png", "binary"} {
		if selected := os.Getenv("IMAGEPAD_NICO_PERF_MODE"); selected != "" && selected != mode {
			continue
		}
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			dir := t.TempDir()
			output := filepath.Join(dir, "commented.mp4")
			start := time.Now()
			_, rendered, err := EncodeNicoCommentedWithRenderer(ctx, ffmpeg, os.Getenv("IMAGEPAD_NICO_PERF_SOURCE"), output, snapshot,
				nicorender.RenderOptions{Backend: "browser", Width: 1920, Height: 1080, DurationMs: durationMs, FPSNum: 30, FPSDen: 1, Transport: mode, ReuseUnchanged: true, BatchFrames: batchFrames, SparseFrames: sparseFrames},
				NicoEncodeOptions{Width: 1920, Height: 1080, DurationMs: durationMs, FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k"})
			encodeTime := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			hlsStart := time.Now()
			_, err = CreateNicoHLS(ctx, ffmpeg, output, filepath.Join(dir, "hls"))
			if err != nil {
				t.Fatal(err)
			}
			hlsTime := time.Since(hlsStart)
			// Hash the decoded output to compare complete video contents across
			// transport changes; no browser screenshots or MP4 metadata involved.
			frames, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", output, "-map", "0:v:0", "-f", "framemd5", "-").Output()
			if err != nil {
				t.Fatal(err)
			}
			decoded := 0
			for _, line := range strings.Split(string(frames), "\n") {
				if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
					decoded++
				}
			}
			t.Logf("mode=%s batch=%d sparse=%t frames=%d decoded=%d encode_ms=%.1f hls_ms=%.1f total_ms=%.1f decoded_sha256=%x", mode, batchFrames, sparseFrames, rendered.FrameCount, decoded, float64(encodeTime.Microseconds())/1000, float64(hlsTime.Microseconds())/1000, float64((encodeTime+hlsTime).Microseconds())/1000, sha256.Sum256(frames))
			if destination := os.Getenv("IMAGEPAD_NICO_PERF_KEEP_OUTPUT"); destination != "" {
				// Preserve an explicitly requested local artifact, never overwrite it.
				src, err := os.Open(output)
				if err != nil {
					t.Fatal(err)
				}
				defer src.Close()
				dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, copyErr := io.Copy(dst, src)
				closeErr := dst.Close()
				if copyErr != nil {
					t.Fatal(copyErr)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
		})
	}
}

// Diagnostic lower bound: feed already prepared transparent frames so there
// is no browser or comment transport. This is not a visual-parity comparison.
func TestNicoEncoderBaselinePerformance(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_PERF_TEST") != "1" {
		t.Skip("opt-in performance diagnostic")
	}
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	frames := make([][]byte, 180)
	transparent := make([]byte, 1920*1080*4)
	for i := range frames {
		frames[i] = transparent
	}
	start := time.Now()
	_, err := EncodeNicoCommented(context.Background(), os.Getenv("IMAGEPAD_NICO_PERF_FFMPEG"), os.Getenv("IMAGEPAD_NICO_PERF_SOURCE"), filepath.Join(t.TempDir(), "baseline.mp4"), NicoEncodeOptions{Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k"}, &testRGBAFrames{frames: frames})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("precomputed_transparent_frames=180 encode_ms=%.1f", float64(time.Since(start).Microseconds())/1000)
}

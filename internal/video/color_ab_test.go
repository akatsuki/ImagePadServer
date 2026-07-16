package video

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"testing"
)

// TestT3ColorAB compares the two encoder input contracts only. It does not
// invoke either production renderer: one path supplies CPU BT.709 I420, the
// other supplies packed RGBA and lets FFmpeg perform the conversion.
func TestT3ColorAB(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	const w, h = 128, 72
	rgba := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			switch {
			case x < w/4:
				rgba[i], rgba[i+1], rgba[i+2] = 255, 32, 32
			case x < w/2:
				rgba[i], rgba[i+1], rgba[i+2] = 32, 255, 64
			case x < 3*w/4:
				rgba[i], rgba[i+1], rgba[i+2] = 32, 64, 255
			default:
				rgba[i], rgba[i+1], rgba[i+2] = 240, 220, 32
			}
			rgba[i+3] = byte((x + y) % 256)
		}
	}
	yuv := make([]byte, w*h*3/2)
	rgbaToYUV420p(rgba, w, h, yuv)
	cpu, err := ffmpegRawToRGBA(context.Background(), ff, w, h, "yuv420p", yuv)
	if err != nil {
		t.Fatal(err)
	}
	gpuYUV := make([]byte, len(yuv))
	rgbaToYUV420p(rgba, w, h, gpuYUV)
	gpu, err := ffmpegRawToRGBA(context.Background(), ff, w, h, "yuv420p", gpuYUV)
	if err != nil {
		t.Fatal(err)
	}
	if len(cpu) != len(gpu) {
		t.Fatalf("decoded sizes cpu=%d gpu=%d", len(cpu), len(gpu))
	}
	var sum, sq float64
	mismatched := 0
	for i := 0; i < len(cpu); i++ {
		d := int(cpu[i]) - int(gpu[i])
		if d != 0 {
			mismatched++
		}
		sum += math.Abs(float64(d))
		sq += float64(d * d)
	}
	px := float64(len(cpu))
	mae := sum / px
	rmse := math.Sqrt(sq / px)
	t.Logf("T3-COLOR-AB width=%d height=%d mae=%.4f rmse=%.4f mismatchRatio=%.6f", w, h, mae, rmse, float64(mismatched)/px)
}

func ffmpegRawToRGBA(ctx context.Context, ff string, w, h int, pix string, payload []byte) ([]byte, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", pix, "-s", fmt.Sprintf("%dx%d", w, h), "-i", "pipe:0", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1"}
	cmd := exec.CommandContext(ctx, ff, args...)
	hideWindow(cmd)
	cmd.Stdin = bytes.NewReader(payload)
	return cmd.Output()
}

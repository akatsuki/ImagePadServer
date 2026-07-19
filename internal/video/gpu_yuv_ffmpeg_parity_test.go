package video

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGPUYUVMatchesFFmpegSwscale(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if exe == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not configured")
	}
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := StartSidecar(ctx, exe, "yuv-ffmpeg-parity")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Hello(ctx, "yuv-ffmpeg-parity"); err != nil {
		t.Fatal(err)
	}
	const width, height = uint32(640), uint32(360)
	rgbaFrame, err := p.RenderScene(ctx, width, height, 17, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rgba, err := GPUFrameToPackedRGBA(rgbaFrame)
	if err != nil {
		t.Fatal(err)
	}
	gpu, err := p.RenderSceneYUV(ctx, width, height, 17, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	gpuPacked, err := gpu.PackedBytes()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "rgba",
		"-s", "640x360", "-i", "pipe:0", "-frames:v", "1",
		"-sws_flags", "bicubic+accurate_rnd+full_chroma_int", "-pix_fmt", "yuv420p",
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv",
		"-f", "rawvideo", "pipe:1")
	cmd.Stdin = bytes.NewReader(rgba)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, stderr.String())
	}
	ref := stdout.Bytes()
	if len(ref) != len(gpuPacked) {
		t.Fatalf("length: gpu=%d ffmpeg=%d", len(gpuPacked), len(ref))
	}
	goReference := make([]byte, len(gpuPacked))
	rgbaToYUV420p(rgba, int(width), int(height), goReference)
	yLen := int(width * height)
	cLen := int((width / 2) * (height / 2))
	for _, plane := range []struct {
		name       string
		start, end int
	}{{"Y", 0, yLen}, {"U", yLen, yLen + cLen}, {"V", yLen + cLen, yLen + 2*cLen}} {
		var abs uint64
		var max int
		var mismatched int
		for i := plane.start; i < plane.end; i++ {
			d := int(gpuPacked[i]) - int(ref[i])
			if d < 0 {
				d = -d
			}
			abs += uint64(d)
			if d > max {
				max = d
			}
			if d != 0 {
				mismatched++
			}
		}
		t.Logf("%s mae=%.6f max=%d mismatched=%d/%d", plane.name, float64(abs)/float64(plane.end-plane.start), max, mismatched, plane.end-plane.start)
		abs, max, mismatched = 0, 0, 0
		for i := plane.start; i < plane.end; i++ {
			d := int(gpuPacked[i]) - int(goReference[i])
			if d < 0 {
				d = -d
			}
			abs += uint64(d)
			if d > max {
				max = d
			}
			if d != 0 {
				mismatched++
			}
		}
		t.Logf("%s go-reference mae=%.6f max=%d mismatched=%d/%d", plane.name, float64(abs)/float64(plane.end-plane.start), max, mismatched, plane.end-plane.start)
	}
}

func TestGPUPostYUVWaveformMatchesFFmpegOverlay(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if exe == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not configured")
	}
	t.Setenv("IMAGEPAD_GPU_WAVEFORM_RAW", "1")
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	fixture := filepath.Join("..", "..", "artifacts", "strict-fixtures", "no-artwork-fallback-1s.wav")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("strict fixture unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	analysis, err := AnalyzeAudioForKind(ctx, ffmpeg, fixture, SourceMusic)
	if err != nil {
		t.Fatal(err)
	}
	const width, height = 640, 360
	input := AudioRenderInput{SourcePath: fixture, Kind: SourceMusic, Analysis: analysis}
	input, err = PrepareGPUMusicSceneInput(ctx, ffmpeg, input, width, height)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := LayoutForSize(width, height)
	if err != nil {
		t.Fatal(err)
	}
	waveW := int(math.Round(752 * float64(width) / 1280))
	waveH := int(math.Round(168 * float64(height) / 720))
	const targetFrame = 6
	var samples []uint16
	if err := StreamShowwavesPCMHistoryFrames(ctx, ffmpeg, input, waveW, targetFrame+1, func(index int, got []uint16) error {
		if index == targetFrame {
			samples = append([]uint16(nil), got...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scene := CanonicalMusicScene(input, targetFrame, int64(targetFrame)*int64(time.Second)/30)
	scene.Feature.WaveformQ16 = samples
	baseScene := scene
	baseScene.Feature.WaveformQ16 = nil
	p, err := StartSidecar(ctx, exe, "post-yuv-wave-parity")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Hello(ctx, "post-yuv-wave-parity"); err != nil {
		t.Fatal(err)
	}
	base, err := p.RenderSceneYUV(ctx, width, height, 0, 0, &baseScene)
	if err != nil {
		t.Fatal(err)
	}
	baseBytes, err := base.PackedBytes()
	if err != nil {
		t.Fatal(err)
	}
	gpu, err := p.RenderSceneYUV(ctx, width, height, 0, 0, &scene)
	if err != nil {
		t.Fatal(err)
	}
	gpuBytes, err := gpu.PackedBytes()
	if err != nil {
		t.Fatal(err)
	}
	var wave []byte
	waveColor := fmt.Sprintf("#%02X%02X%02X@0.55", scene.Palette.Accent[0], scene.Palette.Accent[1], scene.Palette.Accent[2])
	if err := StreamAudioWaveFrames(ctx, ffmpeg, fixture, waveW, waveH, targetFrame+1, waveColor, audioLoudnormFilter(SourceMusic), func(index int, got []byte) error {
		if index == targetFrame {
			wave = append([]byte(nil), got...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	basePath, wavePath := filepath.Join(tmp, "base.yuv"), filepath.Join(tmp, "wave.rgba")
	if err := os.WriteFile(basePath, baseBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wavePath, wave, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", "640x360", "-r", "30", "-i", basePath,
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", waveW, waveH), "-r", "30", "-i", wavePath,
		"-filter_complex", fmt.Sprintf("[0:v][1:v]overlay=%d:%d", layout.Spectrum.X, layout.Spectrum.Y),
		"-frames:v", "1", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg overlay: %v: %s", err, stderr.String())
	}
	ref := stdout.Bytes()
	if len(ref) != len(gpuBytes) {
		t.Fatalf("length: gpu=%d ffmpeg=%d", len(gpuBytes), len(ref))
	}
	yLen, cLen := width*height, width*height/4
	for _, plane := range []struct {
		name       string
		start, end int
	}{{"Y", 0, yLen}, {"U", yLen, yLen + cLen}, {"V", yLen + cLen, yLen + 2*cLen}} {
		var abs uint64
		var max, mismatched int
		for i := plane.start; i < plane.end; i++ {
			d := int(gpuBytes[i]) - int(ref[i])
			if d < 0 {
				d = -d
			}
			abs += uint64(d)
			if d > max {
				max = d
			}
			if d != 0 {
				mismatched++
			}
		}
		t.Logf("%s mae=%.6f max=%d mismatched=%d/%d", plane.name, float64(abs)/float64(plane.end-plane.start), max, mismatched, plane.end-plane.start)
	}
}

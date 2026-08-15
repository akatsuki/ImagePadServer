package video

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestPlaylistGPUWorkerRealSidecarDecodedParityE2E is an explicit evaluation
// harness for the actual bounded H.264/NVENC path. It is skipped unless
// IMAGEPAD_GPU_PARITY_H264=1 is set because it requires a real sidecar, GPU,
// and FFmpeg. IMAGEPAD_GPU_PARITY_ENFORCE=1 turns the measured thresholds into
// a failing gate; without it the test reports the current measured status.
func TestPlaylistGPUWorkerRealSidecarDecodedParityE2E(t *testing.T) {
	if os.Getenv("IMAGEPAD_GPU_PARITY_H264") != "1" {
		t.Skip("set IMAGEPAD_GPU_PARITY_H264=1 for explicit decoded H264 parity evaluation")
	}
	executable := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if executable == "" || ffmpeg == "" {
		t.Fatal("IMAGEPAD_PLAYLIST_COMPOSITORD and IMAGEPAD_FFMPEG are required")
	}
	if _, err := os.Stat(filepath.Clean(executable)); err != nil {
		t.Fatalf("configured playlist sidecar is unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	worker, err := StartPlaylistGPUWorker(ctx, executable, "real-playlist-decoded-parity-e2e")
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()

	input := playlistGPUWorkerTestInput()
	audioPath, err := createPlaylistParityAudio(ctx, ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(audioPath)
	input.SourcePath = audioPath
	analysis, err := AnalyzeAudio(ctx, ffmpeg, audioPath)
	if err != nil {
		t.Fatal(err)
	}
	input.Analysis = analysis
	if input.Analysis.Duration <= 0 || len(input.Analysis.Frames) < 8 {
		t.Fatalf("parity input must carry a real analyzed PCM stream: duration=%.3f frames=%d", input.Analysis.Duration, len(input.Analysis.Frames))
	}
	if input.SourcePath == "" {
		t.Fatal("parity input must carry the same PCM source before GPU preparation")
	}
	assets, timeline, err := worker.PrepareCompiledTrack(ctx, 1, input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	expectedBase, _, err := preparePlaylistGPUBaseTexture(ctx, ffmpeg, input, "track-a", worker.width, worker.height)
	if err != nil {
		t.Fatalf("rebuild canonical base texture for parity boundary: %v", err)
	}
	if assets.BaseTexture == nil || assets.BaseTexture.Width != expectedBase.Width || assets.BaseTexture.Height != expectedBase.Height || assets.BaseTexture.RowStride != expectedBase.RowStride || !bytes.Equal(assets.BaseTexture.Payload, expectedBase.Payload) {
		t.Fatal("prepared GPU base texture differs from the canonical CPU base raster")
	}
	gpuFrames := make([]EncodedH264Frame, 0, timeline.FrameCount)
	if err := worker.RenderPreparedTimeline(ctx, 1, assets, timeline, func(frame EncodedH264Frame) error {
		gpuFrames = append(gpuFrames, frame)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if uint64(len(gpuFrames)) != timeline.FrameCount {
		t.Fatalf("GPU generated frame count=%d want=%d", len(gpuFrames), timeline.FrameCount)
	}
	gpuRGBA, err := decodePlaylistH264RGBA(ctx, ffmpeg, gpuFrames)
	if err != nil {
		t.Fatal(err)
	}

	// Compare against a CPU raster made from the same immutable TrackAssets and
	// TrackTimeline sent to the GPU. The CPU reference is encoded and decoded
	// through the same NVENC/BT.709 boundary as the GPU output before comparing;
	// comparing raw CPU RGBA directly against decoded H264 would include a
	// one-sided YUV/codec loss floor and make the visual gate unfair.
	cpuRGBA, err := renderPlaylistParityCPUReference(assets, timeline, input.Analysis, int(gpuFrames[0].Width), int(gpuFrames[0].Height))
	if err != nil {
		t.Fatal(err)
	}
	cpuEncodedRGBA, err := encodeDecodePlaylistParityCPUReference(ctx, ffmpeg, cpuRGBA, gpuFrames[0].Width, gpuFrames[0].Height)
	if err != nil {
		t.Fatal(err)
	}
	if len(cpuEncodedRGBA) != len(gpuRGBA) {
		t.Fatalf("CPU/GPU decoded frame count=%d/%d", len(cpuEncodedRGBA), len(gpuRGBA))
	}

	minPSNR := math.Inf(1)
	minSSIM := math.Inf(1)
	for i := range gpuRGBA {
		psnr, ssim, err := comparePlaylistRGBA(cpuEncodedRGBA[i], gpuRGBA[i])
		if err != nil {
			t.Fatalf("frame %d parity comparison: %v", i, err)
		}
		if psnr < minPSNR {
			minPSNR = psnr
		}
		if ssim < minSSIM {
			minSSIM = ssim
		}
		t.Logf("frame=%d psnr_db=%.6f ssim=%.6f", i, psnr, ssim)
	}
	if dumpDir := os.Getenv("IMAGEPAD_GPU_PARITY_DUMP_DIR"); dumpDir != "" {
		if err := dumpPlaylistParityFrames(ctx, ffmpeg, dumpDir, cpuEncodedRGBA, gpuRGBA, gpuFrames[0].Width, gpuFrames[0].Height); err != nil {
			t.Fatal(err)
		}
		t.Logf("decoded parity PNG dump=%s", dumpDir)
	}
	gate := minPSNR >= 38.0 && minSSIM >= 0.97
	t.Logf("decoded_h264_parity frames=%d min_psnr_db=%.6f min_ssim=%.6f parity_gate=%t", len(gpuRGBA), minPSNR, minSSIM, gate)
	if os.Getenv("IMAGEPAD_GPU_PARITY_ENFORCE") == "1" && !gate {
		t.Fatalf("decoded H264 parity gate failed: min_psnr_db=%.6f min_ssim=%.6f", minPSNR, minSSIM)
	}
}

func renderPlaylistParityCPUReference(assets TrackAssets, timeline TrackTimeline, analysis AudioAnalysis, width, height int) ([][]byte, error) {
	if assets.BaseTexture == nil || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("parity CPU reference requires base texture and dimensions")
	}
	if len(timeline.Frames) == 0 || int(timeline.FrameCount) != len(timeline.Frames) {
		return nil, fmt.Errorf("parity CPU reference requires a complete timeline")
	}
	base := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		src := y * int(assets.BaseTexture.RowStride)
		dst := y * base.Stride
		copy(base.Pix[dst:dst+width*4], assets.BaseTexture.Payload[src:src+width*4])
	}
	layout, err := LayoutForSize(width, height)
	if err != nil {
		return nil, err
	}
	mode := ForegroundMode{
		PrimaryColor: colorFromArray(assets.Palette.Primary),
		AccentColor:  colorFromArray(assets.Palette.Accent),
		Overlay:      colorFromArray(assets.Palette.Overlay),
	}
	loudness := buildLoudnessLayer(analysis.Features, analysis.Duration, mode, layout, width, height)
	expectedLoudness, err := NewBaseTextureMetadata("parity-loudness", loudness, ColorSRGB)
	if err != nil {
		return nil, fmt.Errorf("parity CPU loudness metadata: %w", err)
	}
	if assets.LoudnessTexture == nil || assets.LoudnessTexture.Width != expectedLoudness.Width || assets.LoudnessTexture.Height != expectedLoudness.Height || assets.LoudnessTexture.RowStride != expectedLoudness.RowStride || !bytes.Equal(assets.LoudnessTexture.Payload, expectedLoudness.Payload) {
		return nil, fmt.Errorf("parity loudness texture differs from CPU canonical layer")
	}
	loudnessRect := nonTransparentBounds(loudness)
	frames := make([][]byte, 0, len(timeline.Frames))
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for index, directive := range timeline.Frames {
		if len(directive.SpectrumQ16) < 24 {
			return nil, fmt.Errorf("parity CPU reference frame %d spectrum is incomplete", index)
		}
		var spectrum [24]float64
		for band := range spectrum {
			spectrum[band] = float64(directive.SpectrumQ16[band]) / 65535.0
		}
		renderVisualizerFrame(canvas, base, loudness, loudnessRect, AudioFrame{Spectrum24: spectrum}, index, len(timeline.Frames), analysis.Duration, mode, layout)
		if assets.TextOverlay != nil && assets.TextOverlay.Kind == "screen_rgba" {
			compositePlaylistTextOverlay(canvas, assets.TextOverlay)
		}
		frames = append(frames, append([]byte(nil), canvas.Pix...))
	}
	return frames, nil
}

func colorFromArray(value [4]uint8) color.RGBA {
	return color.RGBA{R: value[0], G: value[1], B: value[2], A: value[3]}
}

func compositePlaylistTextOverlay(dst *image.RGBA, overlay *TextOverlayMetadata) {
	if overlay == nil || overlay.Width != uint32(dst.Bounds().Dx()) || overlay.Height != uint32(dst.Bounds().Dy()) {
		return
	}
	for y := 0; y < dst.Bounds().Dy(); y++ {
		for x := 0; x < dst.Bounds().Dx(); x++ {
			src := y*int(overlay.RowStride) + x*4
			dstOffset := y*dst.Stride + x*4
			// TextOverlayMetadata screen_rgba payloads are premultiplied. Match
			// the WGSL source-over expression: premultiplied RGB is already
			// coverage-weighted and must not be multiplied by alpha a second time.
			alpha := uint32(overlay.Payload[src+3])
			inv := 255 - alpha
			dst.Pix[dstOffset] = uint8((uint32(overlay.Payload[src])*255 + uint32(dst.Pix[dstOffset])*inv) / 255)
			dst.Pix[dstOffset+1] = uint8((uint32(overlay.Payload[src+1])*255 + uint32(dst.Pix[dstOffset+1])*inv) / 255)
			dst.Pix[dstOffset+2] = uint8((uint32(overlay.Payload[src+2])*255 + uint32(dst.Pix[dstOffset+2])*inv) / 255)
			dst.Pix[dstOffset+3] = 255
		}
	}
}

func dumpPlaylistParityFrames(ctx context.Context, ffmpeg, dir string, cpuFrames, gpuFrames [][]byte, width, height uint32) error {
	if len(cpuFrames) != len(gpuFrames) {
		return fmt.Errorf("cannot dump parity frames with count mismatch")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i := range cpuFrames {
		for label, rgba := range map[string][]byte{"cpu": cpuFrames[i], "gpu": gpuFrames[i]} {
			raw := filepath.Join(dir, fmt.Sprintf("frame_%02d_%s.rgba", i, label))
			png := filepath.Join(dir, fmt.Sprintf("frame_%02d_%s.png", i, label))
			if err := os.WriteFile(raw, rgba, 0o600); err != nil {
				return err
			}
			cmd := exec.CommandContext(ctx, ffmpeg,
				"-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pix_fmt", "rgba",
				"-s", fmt.Sprintf("%dx%d", width, height), "-i", raw, "-frames:v", "1", png,
			)
			hideWindow(cmd)
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("dump %s: %w: %s", label, err, output)
			}
			_ = os.Remove(raw)
		}
	}
	return nil
}

func createPlaylistParityAudio(ctx context.Context, ffmpeg string) (string, error) {
	duration := os.Getenv("IMAGEPAD_GPU_PARITY_DURATION")
	if duration == "" {
		duration = "4"
	}
	tmp, err := os.CreateTemp("", "imagepad-playlist-parity-*.wav")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	// A deterministic two-tone signal gives the spectrum bars two distinct
	// peaks while keeping the analysis reproducible. The analysis is computed
	// once and shared by both the CPU reference and the GPU path, so
	// determinism is preserved regardless of the source shape.
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%s", duration),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=1320:duration=%s", duration),
		"-filter_complex", "amix=inputs=2:duration=first",
		"-ar", "44100", "-ac", "2", "-c:a", "pcm_s16le", path,
	)
	hideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("create parity audio: %w: %s", err, output)
	}
	return path, nil
}

func decodePlaylistH264RGBA(ctx context.Context, ffmpeg string, frames []EncodedH264Frame) ([][]byte, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("no H264 frames to decode")
	}
	tmp, err := os.CreateTemp("", "imagepad-playlist-parity-*.h264")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	defer os.Remove(path)
	for _, frame := range frames {
		if err := frame.Validate(); err != nil {
			_ = tmp.Close()
			return nil, err
		}
		if _, err := tmp.Write(frame.Payload); err != nil {
			_ = tmp.Close()
			return nil, err
		}
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	width, height := frames[0].Width, frames[0].Height
	frameBytes := int(width * height * 4)
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "h264", "-i", path,
		"-frames:v", strconv.Itoa(len(frames)),
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1",
	)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	decoded := make([][]byte, 0, len(frames))
	for i := 0; i < len(frames); i++ {
		frame := make([]byte, frameBytes)
		if _, err := io.ReadFull(stdout, frame); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("decode frame %d: %w; ffmpeg=%s", i, err, stderr.String())
		}
		decoded = append(decoded, frame)
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("decode H264: %w; ffmpeg=%s", err, stderr.String())
	}
	return decoded, nil
}

func encodeDecodePlaylistParityCPUReference(ctx context.Context, ffmpeg string, frames [][]byte, width, height uint32) ([][]byte, error) {
	if len(frames) == 0 || width == 0 || height == 0 {
		return nil, fmt.Errorf("CPU parity codec reference requires frames and dimensions")
	}
	frameBytes := int(width * height * 4)
	for index, frame := range frames {
		if len(frame) != frameBytes {
			return nil, fmt.Errorf("CPU parity frame %d has %d bytes, want %d", index, len(frame), frameBytes)
		}
	}
	rawFile, err := os.CreateTemp("", "imagepad-playlist-parity-cpu-*.rgba")
	if err != nil {
		return nil, err
	}
	rawPath := rawFile.Name()
	defer os.Remove(rawPath)
	for _, frame := range frames {
		if _, err := rawFile.Write(frame); err != nil {
			_ = rawFile.Close()
			return nil, err
		}
	}
	if err := rawFile.Close(); err != nil {
		return nil, err
	}
	h264File, err := os.CreateTemp("", "imagepad-playlist-parity-cpu-*.h264")
	if err != nil {
		return nil, err
	}
	h264Path := h264File.Name()
	if err := h264File.Close(); err != nil {
		_ = os.Remove(h264Path)
		return nil, err
	}
	defer os.Remove(h264Path)
	encodeArgs := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height), "-framerate", "30", "-i", rawPath,
		"-frames:v", strconv.Itoa(len(frames)),
		"-c:v", "h264_nvenc", "-preset", "p1",
	}
	// Mirror the Rust sidecar's active NVENC rate-control so the CPU/GPU
	// parity comparison isolates compositor differences instead of a
	// lossless-vs-lossy quantization floor that no compositor change can close.
	encodeArgs = append(encodeArgs, playlistParityCPUNvencQPArgs()...)
	encodeArgs = append(encodeArgs,
		"-bf", "0",
		"-g", strconv.Itoa(int(^uint32(0)>>1)),
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv",
		"-f", "h264", h264Path,
	)
	encode := exec.CommandContext(ctx, ffmpeg, encodeArgs...)
	hideWindow(encode)
	if output, err := encode.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("encode CPU parity reference: %w: %s", err, output)
	}
	decode := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "h264", "-i", h264Path,
		"-frames:v", strconv.Itoa(len(frames)), "-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1",
	)
	hideWindow(decode)
	var stderr bytes.Buffer
	decode.Stderr = &stderr
	stdout, err := decode.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := decode.Start(); err != nil {
		return nil, err
	}
	decoded := make([][]byte, 0, len(frames))
	for index := 0; index < len(frames); index++ {
		frame := make([]byte, frameBytes)
		if _, err := io.ReadFull(stdout, frame); err != nil {
			_ = decode.Process.Kill()
			_ = decode.Wait()
			return nil, fmt.Errorf("decode CPU parity frame %d: %w; ffmpeg=%s", index, err, stderr.String())
		}
		decoded = append(decoded, frame)
	}
	if err := decode.Wait(); err != nil {
		return nil, fmt.Errorf("decode CPU parity reference: %w; ffmpeg=%s", err, stderr.String())
	}
	return decoded, nil
}

func comparePlaylistRGBA(reference, actual []byte) (psnr, ssim float64, err error) {
	if len(reference) != len(actual) || len(reference)%4 != 0 {
		return 0, 0, fmt.Errorf("RGBA lengths differ: %d/%d", len(reference), len(actual))
	}
	var mse float64
	refGray := make([]float64, len(reference)/4)
	actualGray := make([]float64, len(reference)/4)
	for i := 0; i < len(reference); i += 4 {
		pixel := i / 4
		for channel := 0; channel < 3; channel++ {
			delta := float64(reference[i+channel]) - float64(actual[i+channel])
			mse += delta * delta
		}
		refGray[pixel] = 0.299*float64(reference[i]) + 0.587*float64(reference[i+1]) + 0.114*float64(reference[i+2])
		actualGray[pixel] = 0.299*float64(actual[i]) + 0.587*float64(actual[i+1]) + 0.114*float64(actual[i+2])
	}
	mse /= float64((len(reference) / 4) * 3)
	if mse == 0 {
		psnr = math.Inf(1)
	} else {
		psnr = 10 * math.Log10((255*255)/mse)
	}
	ssim = playlistGlobalSSIM(refGray, actualGray)
	return psnr, ssim, nil
}

func playlistGlobalSSIM(reference, actual []float64) float64 {
	if len(reference) == 0 || len(reference) != len(actual) {
		return 0
	}
	var meanReference, meanActual float64
	for i := range reference {
		meanReference += reference[i]
		meanActual += actual[i]
	}
	meanReference /= float64(len(reference))
	meanActual /= float64(len(actual))
	var varianceReference, varianceActual, covariance float64
	for i := range reference {
		dr := reference[i] - meanReference
		da := actual[i] - meanActual
		varianceReference += dr * dr
		varianceActual += da * da
		covariance += dr * da
	}
	count := float64(len(reference))
	varianceReference /= count
	varianceActual /= count
	covariance /= count
	c1 := math.Pow(0.01*255, 2)
	c2 := math.Pow(0.03*255, 2)
	return ((2*meanReference*meanActual + c1) * (2*covariance + c2)) /
		((meanReference*meanReference + meanActual*meanActual + c1) * (varianceReference + varianceActual + c2))
}

// playlistParityCPUNvencQPArgs returns NVENC constant-QP arguments for the CPU
// parity reference that mirror the Rust sidecar's active rate-control. The
// sidecar uses constant QP (interP=28, interB=31, intra=25) by default, or
// (0,0,0) when IMAGEPAD_GPU_PARITY_LOSSLESS=1. The CPU reference previously
// hardcoded -qp 0 (lossless), which compared a lossless encode against the
// sidecar's lossy output and introduced a codec-quantization floor that no
// compositor change could close.
func playlistParityCPUNvencQPArgs() []string {
	if os.Getenv("IMAGEPAD_GPU_PARITY_LOSSLESS") == "1" {
		return []string{"-rc", "constqp", "-qp", "0"}
	}
	// Match the sidecar's dominant P-frame QP 28. The single intra frame
	// differs (sidecar 25 vs FFmpeg 28), but -g INT_MAX makes that exactly one
	// frame of the whole timeline, so the comparison is codec-equivalent.
	return []string{"-rc", "constqp", "-qp", "28"}
}

func TestPlaylistParityCPUNvencQPArgsMirrorsSidecarRateControl(t *testing.T) {
	t.Setenv("IMAGEPAD_GPU_PARITY_LOSSLESS", "")
	lossy := playlistParityCPUNvencQPArgs()
	if len(lossy) != 4 || lossy[0] != "-rc" || lossy[1] != "constqp" || lossy[2] != "-qp" || lossy[3] != "28" {
		t.Fatalf("default CPU QP args = %v, want constqp QP 28", lossy)
	}
	t.Setenv("IMAGEPAD_GPU_PARITY_LOSSLESS", "1")
	lossless := playlistParityCPUNvencQPArgs()
	if len(lossless) != 4 || lossless[0] != "-rc" || lossless[1] != "constqp" || lossless[2] != "-qp" || lossless[3] != "0" {
		t.Fatalf("lossless CPU QP args = %v, want constqp QP 0", lossless)
	}
}

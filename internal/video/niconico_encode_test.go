package video

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

type testRGBAFrames struct {
	frames [][]byte
	index  int
}

func (s *testRGBAFrames) Next(context.Context) ([]byte, bool, error) {
	if s.index >= len(s.frames) {
		return nil, false, nil
	}
	pixels := s.frames[s.index]
	s.index++
	return pixels, true, nil
}

func TestEncodeNicoCommentedAndCreateHLS(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	// Deliberately use a 60fps source while the comment clock/output contract is
	// 30fps. The result must remain 30fps so motion speed cannot depend on the
	// source frame rate. The source outlasts the comment timeline so repeating
	// the last overlay frame must not extend the completed output.
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=64x36:r=60", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-t", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create source: %v: %s", err, output)
	}
	options := NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 200, FPSNum: 30, FPSDen: 1, CRF: 28}
	clock, err := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	if err != nil {
		t.Fatal(err)
	}
	count := clock.FrameCountForDurationMs(options.DurationMs)
	frames := make([][]byte, count)
	for n := range frames {
		pixels := make([]byte, options.Width*options.Height*4)
		for i := 0; i < len(pixels); i += 4 {
			pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 255, 0, 0, 255
		}
		frames[n] = pixels
	}
	outputPath := filepath.Join(dir, "commented.mp4")
	report, err := EncodeNicoCommented(context.Background(), ffmpeg, source, outputPath, options, &testRGBAFrames{frames: frames})
	if err != nil {
		t.Fatal(err)
	}
	if report.FrameCount != count {
		t.Fatalf("frame count = %d, want %d", report.FrameCount, count)
	}
	decoded := probeNicoStream(t, ffprobe, outputPath, "v:0", "nb_frames")
	if strings.TrimSpace(decoded) != fmt.Sprint(count) {
		t.Fatalf("encoded frame count = %q, want %d", decoded, count)
	}
	videoMeta := probeNicoStream(t, ffprobe, outputPath, "v:0", "codec_name,width,height,pix_fmt,r_frame_rate,avg_frame_rate")
	if !strings.Contains(videoMeta, "h264") || !strings.Contains(videoMeta, "64") || !strings.Contains(videoMeta, "36") || !strings.Contains(videoMeta, "yuv420p") {
		t.Fatalf("video metadata = %q", videoMeta)
	}
	if !strings.Contains(videoMeta, "30/1") {
		t.Fatalf("output fps followed source instead of 30fps: %q", videoMeta)
	}
	audioMeta := probeNicoStream(t, ffprobe, outputPath, "a:0", "codec_name")
	if !strings.Contains(audioMeta, "aac") {
		t.Fatalf("audio metadata = %q", audioMeta)
	}
	hls, err := CreateNicoHLS(context.Background(), ffmpeg, outputPath, filepath.Join(dir, "hls"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hls.Segments) == 0 {
		t.Fatal("HLS has no segments")
	}
	playlist, err := os.ReadFile(hls.PlaylistPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(playlist), "#EXT-X-ENDLIST") {
		t.Fatalf("playlist has no ENDLIST: %s", playlist)
	}
	hlsMeta := probeNicoStream(t, ffprobe, hls.PlaylistPath, "v:0", "codec_name")
	if !strings.Contains(hlsMeta, "h264") {
		t.Fatalf("HLS video metadata = %q", hlsMeta)
	}
}

func TestEncodeNicoCommentedWithBrowserRenderer(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_PIPELINE_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_PIPELINE_TEST=1 for browser plus FFmpeg")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=30", "-t", "0.1", "-an", source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create source: %v: %s", err, output)
	}
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{VideoID: "sm9", SelectedForks: []string{"main"}, Threads: []niconico.Thread{{ID: "thread", Fork: "main", Comments: []niconico.Comment{{ID: "c1", VposMs: 0, Body: "browser pipeline", Commands: []string{"red"}, PostedAt: "2026-01-01T00:00:00Z"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	options := NicoEncodeOptions{Width: 320, Height: 180, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 28}
	outputPath := filepath.Join(dir, "commented.mp4")
	_, renderReport, err := EncodeNicoCommentedWithRenderer(context.Background(), ffmpeg, source, outputPath, snapshot, nicorender.RenderOptions{Backend: "browser", Width: 320, Height: 180, DurationMs: 100, FPSNum: 30, FPSDen: 1, Transport: "binary", ReuseUnchanged: true, BatchFrames: 30, SparseFrames: true}, options)
	if err != nil {
		t.Fatal(err)
	}
	if renderReport.FrameCount != 3 {
		t.Fatalf("render frame count = %d, want 3", renderReport.FrameCount)
	}
	if info, err := os.Stat(outputPath); err != nil || info.Size() == 0 {
		t.Fatalf("output = %v", err)
	}
	// The public auto path must also finish through the browser when a native
	// helper cannot be prepared; failure must be limited to preparation.
	_, fallback, err := EncodeNicoCommentedWithRenderer(context.Background(), ffmpeg, source, filepath.Join(dir, "fallback.mp4"), snapshot, nicorender.RenderOptions{
		Backend: "auto", CompositorPath: filepath.Join(dir, "unavailable-helper.exe"),
		Width: 320, Height: 180, DurationMs: 100, FPSNum: 30, FPSDen: 1,
		Transport: "binary", ReuseUnchanged: true, BatchFrames: 30, SparseFrames: true,
	}, options)
	if err != nil || fallback.Backend != "browser" || fallback.FallbackReason == "" || fallback.FrameCount != 3 {
		t.Fatalf("auto fallback: %+v, %v", fallback, err)
	}
}

func probeNicoStream(t *testing.T, ffprobe, input, stream, entries string) string {
	t.Helper()
	output, err := exec.Command(ffprobe, "-v", "error", "-select_streams", stream, "-show_entries", "stream="+entries, "-of", "csv=p=0", input).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v: %s", err, output)
	}
	return fmt.Sprintf("%s", output)
}

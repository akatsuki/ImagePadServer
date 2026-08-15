package obsrtmp

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

type fakeFallbackRenderer struct {
	closed bool
}

func (r *fakeFallbackRenderer) Close() error {
	r.closed = true
	return nil
}

func (r *fakeFallbackRenderer) RenderReusableRGBA(float64) []byte {
	return []byte("fallback-frame")
}

func TestRadioFallbackFeederRunsInjectedPipeline(t *testing.T) {
	t.Setenv("IMAGEPAD_PLAYLIST_COMPOSITORD", "must-not-change-normal-fallback")
	renderer := &fakeFallbackRenderer{}
	var gotArgs []string
	var gotRenderWidth, gotRenderHeight int
	var gotLogo, gotSemiBold, gotMedium string
	var tracked bool

	feeder := NewRadioFallbackFeeder(t.TempDir(), func() video.QualityPreset {
		return video.QualityPreset{Height: 360, AudioBitrate: "96k"}
	})
	feeder.ensureFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	feeder.selectEncoder = func(ctx context.Context, ffmpeg string, purpose video.EncoderPurpose) video.VideoEncoderProfile {
		if ffmpeg != "fake-ffmpeg" {
			t.Fatalf("selectEncoder ffmpeg = %q", ffmpeg)
		}
		if purpose != video.EncoderLowLatency {
			t.Fatalf("selectEncoder purpose = %q, want low latency", purpose)
		}
		return video.NewVideoEncoderProfile("h264_nvenc", purpose)
	}
	feeder.fonts = func() (video.FontSet, error) {
		return video.FontSet{SemiBold600: "semi.ttf", Medium500: "medium.ttf"}, nil
	}
	feeder.writeLogo = func(outDir string) (string, error) {
		if outDir == "" {
			t.Fatal("outDir must be passed to writeLogo")
		}
		return "logo.png", nil
	}
	feeder.newRenderer = func(width, height int, logoPath, semiboldFontPath, mediumFontPath string) (radioFallbackRenderer, error) {
		gotRenderWidth, gotRenderHeight = width, height
		gotLogo, gotSemiBold, gotMedium = logoPath, semiboldFontPath, mediumFontPath
		return renderer, nil
	}
	feeder.writeFrames = func(_ context.Context, out io.Writer, r radioFallbackRenderer) (int, error) {
		if r != renderer {
			t.Fatal("writeFrames received unexpected renderer")
		}
		_, err := out.Write(r.RenderReusableRGBA(0))
		return 15, err
	}
	feeder.started = func(*exec.Cmd) func() {
		tracked = true
		return func() {}
	}
	feeder.command = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		gotArgs = append([]string{}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRadioFallbackFeederHelperProcess", "--")
		cmd.Env = append(os.Environ(), "IMAGEPAD_RADIO_FALLBACK_HELPER=1")
		return cmd
	}

	var sink bytes.Buffer
	duration, err := feeder.Run(context.Background(), 12.345, &sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if duration != 0.5 {
		t.Fatalf("duration = %.3f, want 0.500", duration)
	}

	if sink.String() != "fallback-frame" {
		t.Fatalf("sink = %q", sink.String())
	}
	if gotRenderWidth != 640 || gotRenderHeight != 360 {
		t.Fatalf("render size = %dx%d", gotRenderWidth, gotRenderHeight)
	}
	if gotLogo != "logo.png" || gotSemiBold != "semi.ttf" || gotMedium != "medium.ttf" {
		t.Fatalf("renderer inputs = logo=%q semibold=%q medium=%q", gotLogo, gotSemiBold, gotMedium)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"-pix_fmt rgba", "-s 640x360", "-c:v h264_nvenc", "-preset p1", "-tune ull", "-output_ts_offset 12.345", "-b:a 96k", "-f mpegts pipe:1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ffmpeg args missing %q: %s", want, joined)
		}
	}
	if !renderer.closed {
		t.Fatal("renderer must be closed")
	}
	if !tracked {
		t.Fatal("ffmpeg process must be tracked")
	}
}

func TestRadioFallbackFeederStopsHungProcessAfterCancel(t *testing.T) {
	renderer := &fakeFallbackRenderer{}
	feeder := NewRadioFallbackFeeder(t.TempDir(), func() video.QualityPreset {
		return video.QualityPreset{Height: 360, AudioBitrate: "96k"}
	})
	feeder.ensureFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	feeder.fonts = func() (video.FontSet, error) {
		return video.FontSet{SemiBold600: "semi.ttf", Medium500: "medium.ttf"}, nil
	}
	feeder.writeLogo = func(string) (string, error) { return "logo.png", nil }
	feeder.newRenderer = func(int, int, string, string, string) (radioFallbackRenderer, error) {
		return renderer, nil
	}
	feeder.writeFrames = func(ctx context.Context, _ io.Writer, _ radioFallbackRenderer) (int, error) {
		<-ctx.Done()
		return 45, nil
	}
	feeder.started = func(*exec.Cmd) func() { return func() {} }
	feeder.stopTimeout = 30 * time.Millisecond
	feeder.command = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRadioFallbackFeederHelperProcess", "--")
		cmd.Env = append(os.Environ(), "IMAGEPAD_RADIO_FALLBACK_HELPER=1", "IMAGEPAD_RADIO_FALLBACK_HELPER_HANG=1")
		return cmd
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	duration, err := feeder.Run(ctx, 0, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Run took %s after cancellation; want bounded fallback shutdown", elapsed)
	}
	if duration != 1.5 {
		t.Fatalf("duration = %.3f, want encoded frame duration 1.500", duration)
	}
}

func TestRadioFallbackFeederHelperProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_RADIO_FALLBACK_HELPER") != "1" {
		return
	}
	if os.Getenv("IMAGEPAD_RADIO_FALLBACK_HELPER_HANG") == "1" {
		select {}
	}
	_, _ = io.Copy(os.Stdout, os.Stdin)
	os.Exit(0)
}

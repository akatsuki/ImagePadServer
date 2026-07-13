package obsrtmp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"imagepadserver/internal/video"
)

type radioFallbackRenderer interface {
	Close() error
	RenderReusableRGBA(seconds float64) []byte
}

type RadioFallbackFeeder struct {
	outDir string
	preset func() video.QualityPreset

	ensureFFmpeg  func() (string, error)
	selectEncoder func(ctx context.Context, ffmpeg string, purpose video.EncoderPurpose) video.VideoEncoderProfile
	fonts         func() (video.FontSet, error)
	writeLogo     func(outDir string) (string, error)
	newRenderer   func(width, height int, logoPath, semiboldFontPath, mediumFontPath string) (radioFallbackRenderer, error)
	writeFrames   func(ctx context.Context, out io.Writer, renderer radioFallbackRenderer) (int, error)
	started       func(cmd *exec.Cmd) func()
	command       func(ctx context.Context, name string, args ...string) *exec.Cmd
	stopTimeout   time.Duration
}

func NewRadioFallbackFeeder(outDir string, preset func() video.QualityPreset) *RadioFallbackFeeder {
	return &RadioFallbackFeeder{
		outDir:        outDir,
		preset:        preset,
		ensureFFmpeg:  video.EnsureFFmpeg,
		selectEncoder: video.SelectVideoEncoder,
		fonts:         video.VisualizerFonts,
		writeLogo:     video.WriteRadioFallbackLogo,
		newRenderer: func(width, height int, logoPath, semiboldFontPath, mediumFontPath string) (radioFallbackRenderer, error) {
			return video.NewRadioFallbackRenderer(width, height, logoPath, semiboldFontPath, mediumFontPath)
		},
		writeFrames: writeRadioFallbackFrames,
		started:     video.TrackStartedFFmpeg,
		command:     exec.CommandContext,
		stopTimeout: 2 * time.Second,
	}
}

func (f *RadioFallbackFeeder) Run(ctx context.Context, timestampOffset float64, sink io.Writer) (float64, error) {
	ffmpeg, err := f.ensureFFmpeg()
	if err != nil {
		return 0, err
	}
	fonts, err := f.fonts()
	if err != nil {
		return 0, err
	}
	logoPath, err := f.writeLogo(f.outDir)
	if err != nil {
		return 0, err
	}
	preset := video.MusicRadioQualityPreset("auto", 0, 0)
	if f.preset != nil {
		preset = f.preset()
	}
	renderWidth, renderHeight := video.RadioFallbackRenderSize(preset)
	encoder := video.CPUVideoEncoder(video.EncoderLowLatency)
	if f.selectEncoder != nil {
		encoder = f.selectEncoder(ctx, ffmpeg, video.EncoderLowLatency)
	}
	renderer, err := f.newRenderer(renderWidth, renderHeight, logoPath, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		return 0, err
	}
	defer renderer.Close()

	procCtx, stopProc := context.WithCancel(context.Background())
	defer stopProc()
	cmd := f.command(procCtx, ffmpeg, video.RadioFallbackFeederArgsWithEncoder(preset, renderWidth, renderHeight, timestampOffset, encoder)...)
	hideWindow(cmd)
	cmd.Dir = f.outDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	untrack := f.started(cmd)
	defer untrack()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			timeout := f.stopTimeout
			if timeout <= 0 {
				timeout = 2 * time.Second
			}
			timer := time.NewTimer(timeout)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				stopProc()
			}
		case <-done:
		}
	}()
	defer close(done)

	type renderResult struct {
		frames int
		err    error
	}
	renderErr := make(chan renderResult, 1)
	go func() {
		frames, err := f.writeFrames(ctx, stdin, renderer)
		renderErr <- renderResult{frames: frames, err: err}
		_ = stdin.Close()
	}()
	_, copyErr := io.Copy(sink, stdout)
	waitErr := cmd.Wait()
	result := <-renderErr
	duration := float64(result.frames) / 30.0
	if result.err != nil && ctx.Err() == nil {
		return duration, fmt.Errorf("fallback render: %w", result.err)
	}
	if ctx.Err() != nil {
		return duration, nil
	}
	if copyErr != nil {
		return duration, fmt.Errorf("%w: %v", errPublisherDown, copyErr)
	}
	if waitErr != nil {
		detail := stderr.String()
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		return duration, fmt.Errorf("fallback feeder: %w: %s", waitErr, detail)
	}
	return duration, nil
}

func writeRadioFallbackFrames(ctx context.Context, out io.Writer, renderer radioFallbackRenderer) (int, error) {
	return video.WriteTimedRadioFallbackFrames(ctx, out, renderer.RenderReusableRGBA)
}

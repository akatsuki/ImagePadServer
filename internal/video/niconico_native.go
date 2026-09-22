package video

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

// Only the child's stderr carries status; RGBA travels through inherited file
// handles directly to FFmpeg. A bounded parser never retains per-frame output.
type nicoNativeStatus struct {
	pending        []byte
	expected, last int64
	done           bool
	err            error
	progress       func(int64, int64)
}

func (s *nicoNativeStatus) Write(p []byte) (int, error) {
	n := len(p)
	for _, b := range p {
		if b == '\n' {
			line := strings.TrimSpace(string(s.pending))
			s.pending = s.pending[:0]
			f := strings.Fields(line)
			if len(f) > 0 && (f[0] == "NICO_PROGRESS" || f[0] == "NICO_DONE") {
				var value, total int64
				var e error
				if len(f) < 2 {
					e = fmt.Errorf("missing status count")
				} else {
					value, e = strconv.ParseInt(f[1], 10, 64)
				}
				if f[0] == "NICO_PROGRESS" {
					if len(f) != 3 {
						e = fmt.Errorf("invalid progress")
					} else if e == nil {
						total, e = strconv.ParseInt(f[2], 10, 64)
					}
					if e == nil && (s.done || total != s.expected || value <= s.last || value > s.expected) {
						e = fmt.Errorf("invalid progress sequence")
					}
					if e == nil {
						s.last = value
						if s.progress != nil {
							s.progress(value, s.expected)
						}
					}
				} else {
					if len(f) != 2 || value != s.expected || s.last != s.expected || s.done {
						e = fmt.Errorf("invalid completion count")
					}
					if e == nil {
						s.done = true
					}
				}
				if e != nil {
					s.err = e
					return n, e
				}
			}
		} else {
			if len(s.pending) >= 4096 {
				s.err = fmt.Errorf("native status line exceeds limit")
				return n, s.err
			}
			s.pending = append(s.pending, b)
		}
	}
	return n, nil
}
func (s *nicoNativeStatus) complete() error {
	if s.err != nil {
		return s.err
	}
	if len(s.pending) > 0 || !s.done {
		return fmt.Errorf("missing native completion")
	}
	return nil
}

type nicoErrorTail struct{ bytes.Buffer }

func (b *nicoErrorTail) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 8192
	if n >= limit {
		b.Reset()
		_, _ = b.Buffer.Write(p[n-limit:])
		return n, nil
	}
	if b.Len()+n > limit {
		b.Next(b.Len() + n - limit)
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

type nicoNativePipeResult struct {
	report       nicorender.RenderReport
	stageTimings []NicoStageTiming
}

func nicoProcessError(stage string, err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s exit_code=%d: %w", stage, exitErr.ExitCode(), err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}

func runNicoNativePipe(ctx context.Context, nativePath string, nativeArgs []string, encoderPath string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error)) (nicorender.RenderReport, error) {
	result, err := runNicoNativePipeTimed(ctx, nativePath, nativeArgs, encoderPath, encoderArgs, expected, progress, produce)
	return result.report, err
}

func runNicoNativePipeTimed(ctx context.Context, nativePath string, nativeArgs []string, encoderPath string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error)) (nicoNativePipeResult, error) {
	return runNicoNativePipeTimedInDir(ctx, nativePath, nativeArgs, encoderPath, encoderArgs, expected, progress, produce, "")
}

func runNicoNativePipeTimedInDir(ctx context.Context, nativePath string, nativeArgs []string, encoderPath string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), encoderDir string) (nicoNativePipeResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var report nicorender.RenderReport
	if expected <= 0 || produce == nil {
		return nicoNativePipeResult{}, fmt.Errorf("invalid native job")
	}
	frameR, frameW, err := os.Pipe()
	if err != nil {
		return nicoNativePipeResult{}, err
	}
	defer frameR.Close()
	defer frameW.Close()
	feedR, feedW, err := os.Pipe()
	if err != nil {
		return nicoNativePipeResult{}, err
	}
	defer feedR.Close()
	defer feedW.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = feedW.Close() })
	defer stopClose()
	native := exec.CommandContext(ctx, nativePath, nativeArgs...)
	encoder := exec.CommandContext(ctx, encoderPath, encoderArgs...)
	hideWindow(native)
	hideWindow(encoder)
	if encoderDir != "" {
		encoder.Dir = encoderDir
	}
	native.WaitDelay = 2 * time.Second
	encoder.WaitDelay = 2 * time.Second
	native.Stdin = feedR
	native.Stdout = frameW
	encoder.Stdin = frameR
	status := nicoNativeStatus{expected: expected, progress: progress}
	var nativeTail, encoderTail nicoErrorTail
	native.Stderr = io.MultiWriter(&nativeTail, &status)
	encoder.Stderr = &encoderTail
	encoderStartedAt := time.Now()
	if err = encoder.Start(); err != nil {
		return nicoNativePipeResult{}, fmt.Errorf("niconico: start ffmpeg: %w", err)
	}
	nativeStartedAt := time.Now()
	if err = native.Start(); err != nil {
		cancel()
		_ = encoder.Wait()
		return nicoNativePipeResult{}, fmt.Errorf("niconico: start compositor: %w", err)
	}
	// Parent copies must close immediately, or child EOF cannot propagate.
	_ = frameR.Close()
	_ = frameW.Close()
	_ = feedR.Close()
	type result struct {
		stage  string
		report nicorender.RenderReport
		err    error
		timing NicoStageTiming
	}
	done := make(chan result, 3)
	go func() {
		e := native.Wait()
		if e == nil {
			e = status.complete()
		} else {
			e = nicoProcessError("compositor", e)
		}
		finishedAt := time.Now()
		done <- result{stage: "compositor", err: e, timing: NicoStageTiming{
			Name: "native_compositor", StartedAt: nativeStartedAt, FinishedAt: finishedAt, Elapsed: finishedAt.Sub(nativeStartedAt),
		}}
	}()
	go func() {
		e := encoder.Wait()
		if e != nil {
			e = nicoProcessError("ffmpeg", e)
		}
		finishedAt := time.Now()
		done <- result{stage: "ffmpeg", err: e, timing: NicoStageTiming{
			Name: "native_ffmpeg", StartedAt: encoderStartedAt, FinishedAt: finishedAt, Elapsed: finishedAt.Sub(encoderStartedAt),
		}}
	}()
	producerStartedAt := time.Now()
	go func() {
		r, e := produce(ctx, feedW)
		closeErr := feedW.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil && r.FrameCount != expected {
			e = fmt.Errorf("generated %d frames, want %d", r.FrameCount, expected)
		}
		finishedAt := time.Now()
		done <- result{stage: "renderer", report: r, err: e, timing: NicoStageTiming{
			Name: "native_sprite_stream", StartedAt: producerStartedAt, FinishedAt: finishedAt, Elapsed: finishedAt.Sub(producerStartedAt),
		}}
	}()
	var failures []error
	var compositorTiming, ffmpegTiming, spriteTiming NicoStageTiming
	for i := 0; i < 3; i++ {
		r := <-done
		if r.stage == "renderer" {
			report = r.report
			spriteTiming = r.timing
		} else if r.stage == "compositor" {
			compositorTiming = r.timing
		} else if r.stage == "ffmpeg" {
			ffmpegTiming = r.timing
		}
		if r.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", r.stage, r.err))
			cancel()
		}
	}
	pipeResult := nicoNativePipeResult{report: report, stageTimings: []NicoStageTiming{spriteTiming, compositorTiming, ffmpegTiming}}
	filtered := pipeResult.stageTimings[:0]
	for _, timing := range pipeResult.stageTimings {
		if timing.Name != "" {
			filtered = append(filtered, timing)
		}
	}
	pipeResult.stageTimings = filtered
	if len(failures) > 0 {
		return pipeResult, fmt.Errorf("niconico: %w; compositor: %s; ffmpeg: %s", errors.Join(failures...), strings.TrimSpace(nativeTail.String()), strings.TrimSpace(encoderTail.String()))
	}
	return pipeResult, nil
}

func encodeNicoNative(ctx context.Context, compositor, ffmpeg, source, output string, snapshot niconico.Snapshot, render nicorender.RenderOptions, enc NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
	timer := newNicoStageTimer(enc.StageObserver)
	var rr nicorender.RenderReport
	validateStart := time.Now()
	clock, err := enc.validate()
	timer.mark("validate", validateStart)
	if err != nil {
		return NicoEncodeReport{}, rr, err
	}
	if _, err = os.Stat(source); err != nil {
		return NicoEncodeReport{}, rr, err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return NicoEncodeReport{}, rr, err
	}
	pending, err := os.CreateTemp(filepath.Dir(output), ".niconico-*.mp4")
	if err != nil {
		return NicoEncodeReport{}, rr, err
	}
	pendingPath := pending.Name()
	_ = pending.Close()
	defer os.Remove(pendingPath)
	count := clock.FrameCountForDurationMs(enc.DurationMs)
	mode, err := NormalizeNicoOutputMode(enc.OutputMode)
	if err != nil {
		return NicoEncodeReport{}, rr, err
	}
	teeWorkDir := ""
	if mode == NicoOutputTee {
		teeWorkDir, err = createNicoTeeWorkspace(output)
		if err != nil {
			return NicoEncodeReport{}, rr, err
		}
		defer os.RemoveAll(teeWorkDir)
	}
	args := []string{"--stdin"}
	if render.NativeCopyOutput {
		args = append(args, "--copy-output")
	}
	argsStart := time.Now()
	ffmpegArgs := nicoEncodeArgs(source, pendingPath, enc)
	timer.mark("build_ffmpeg_args", argsStart)
	nativeStart := time.Now()
	pipeResult, err := runNicoNativePipeTimedInDir(ctx, compositor, args, ffmpeg, ffmpegArgs, count, render.Progress, func(ctx context.Context, w io.Writer) (nicorender.RenderReport, error) {
		r, e := nicorender.WriteSpriteStream(ctx, snapshot, render, w)
		r.RenderReport.SpriteTextureBytes = r.TextureBytes
		r.RenderReport.SpritePayloadBytes = r.PackedTextureBytes
		r.RenderReport.SpriteTextures = r.Textures
		return r.RenderReport, e
	}, teeWorkDir)
	rr = pipeResult.report
	for _, stage := range pipeResult.stageTimings {
		timer.record(stage)
	}
	rawFrameBytes := int64(enc.Width) * int64(enc.Height) * 4 * count
	timer.markBytes("native_render_encode", nativeStart, rawFrameBytes)
	rr.Backend = "native-warp"
	if err != nil {
		// Preserve the completed stage timing on runtime failure so T11
		// diagnostics can account for failed native attempts as well.
		return NicoEncodeReport{StageTimings: timer.snapshot()}, rr, err
	}
	if err = ctx.Err(); err != nil {
		return NicoEncodeReport{}, rr, err
	}
	outputValidateStart := time.Now()
	var outputSize int64
	if mode == NicoOutputTee {
		if err := promoteNicoTeeArtifacts(teeWorkDir, output, enc.HLSOutputDir); err != nil {
			return NicoEncodeReport{}, rr, err
		}
		stat, err := os.Stat(output)
		if err != nil {
			return NicoEncodeReport{}, rr, err
		}
		outputSize = stat.Size()
	} else {
		stat, err := os.Stat(pendingPath)
		if err != nil {
			return NicoEncodeReport{}, rr, err
		}
		if stat.Size() == 0 {
			return NicoEncodeReport{}, rr, fmt.Errorf("niconico: empty native output")
		}
		if err = os.Rename(pendingPath, output); err != nil {
			return NicoEncodeReport{}, rr, err
		}
		outputSize = stat.Size()
	}
	if outputSize == 0 {
		return NicoEncodeReport{}, rr, fmt.Errorf("niconico: empty native output")
	}
	timer.markBytes("output_validate", outputValidateStart, outputSize)
	return NicoEncodeReport{OutputPath: output, FrameCount: count, Width: enc.Width, Height: enc.Height, FPSNum: enc.FPSNum, FPSDen: enc.FPSDen, StageTimings: timer.snapshot()}, rr, nil
}

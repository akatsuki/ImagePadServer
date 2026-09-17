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

func runNicoNativePipe(ctx context.Context, nativePath string, nativeArgs []string, encoderPath string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error)) (nicorender.RenderReport, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var report nicorender.RenderReport
	if expected <= 0 || produce == nil {
		return report, fmt.Errorf("invalid native job")
	}
	frameR, frameW, err := os.Pipe()
	if err != nil {
		return report, err
	}
	defer frameR.Close()
	defer frameW.Close()
	feedR, feedW, err := os.Pipe()
	if err != nil {
		return report, err
	}
	defer feedR.Close()
	defer feedW.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = feedW.Close() })
	defer stopClose()
	native := exec.CommandContext(ctx, nativePath, nativeArgs...)
	encoder := exec.CommandContext(ctx, encoderPath, encoderArgs...)
	hideWindow(native)
	hideWindow(encoder)
	native.WaitDelay = 2 * time.Second
	encoder.WaitDelay = 2 * time.Second
	native.Stdin = feedR
	native.Stdout = frameW
	encoder.Stdin = frameR
	status := nicoNativeStatus{expected: expected, progress: progress}
	var nativeTail, encoderTail nicoErrorTail
	native.Stderr = io.MultiWriter(&nativeTail, &status)
	encoder.Stderr = &encoderTail
	if err = encoder.Start(); err != nil {
		return report, fmt.Errorf("niconico: start ffmpeg: %w", err)
	}
	if err = native.Start(); err != nil {
		cancel()
		_ = encoder.Wait()
		return report, fmt.Errorf("niconico: start compositor: %w", err)
	}
	// Parent copies must close immediately, or child EOF cannot propagate.
	_ = frameR.Close()
	_ = frameW.Close()
	_ = feedR.Close()
	type result struct {
		stage  string
		report nicorender.RenderReport
		err    error
	}
	done := make(chan result, 3)
	go func() {
		e := native.Wait()
		if e == nil {
			e = status.complete()
		}
		done <- result{stage: "compositor", err: e}
	}()
	go func() { done <- result{stage: "ffmpeg", err: encoder.Wait()} }()
	go func() {
		r, e := produce(ctx, feedW)
		closeErr := feedW.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil && r.FrameCount != expected {
			e = fmt.Errorf("generated %d frames, want %d", r.FrameCount, expected)
		}
		done <- result{stage: "renderer", report: r, err: e}
	}()
	var failures []error
	for i := 0; i < 3; i++ {
		r := <-done
		if r.stage == "renderer" {
			report = r.report
		}
		if r.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", r.stage, r.err))
			cancel()
		}
	}
	if len(failures) > 0 {
		return report, fmt.Errorf("niconico: %w; compositor: %s; ffmpeg: %s", errors.Join(failures...), strings.TrimSpace(nativeTail.String()), strings.TrimSpace(encoderTail.String()))
	}
	return report, nil
}

func encodeNicoNative(ctx context.Context, compositor, ffmpeg, source, output string, snapshot niconico.Snapshot, render nicorender.RenderOptions, enc NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
	var rr nicorender.RenderReport
	clock, err := enc.validate()
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
	args := []string{"--stdin"}
	if render.NativeCopyOutput {
		args = append(args, "--copy-output")
	}
	rr, err = runNicoNativePipe(ctx, compositor, args, ffmpeg, nicoEncodeArgs(source, pendingPath, enc), count, render.Progress, func(ctx context.Context, w io.Writer) (nicorender.RenderReport, error) {
		r, e := nicorender.WriteSpriteStream(ctx, snapshot, render, w)
		r.RenderReport.SpriteTextureBytes = r.TextureBytes
		r.RenderReport.SpritePayloadBytes = r.PackedTextureBytes
		r.RenderReport.SpriteTextures = r.Textures
		return r.RenderReport, e
	})
	rr.Backend = "native-warp"
	if err != nil {
		return NicoEncodeReport{}, rr, err
	}
	if err = ctx.Err(); err != nil {
		return NicoEncodeReport{}, rr, err
	}
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
	return NicoEncodeReport{OutputPath: output, FrameCount: count, Width: enc.Width, Height: enc.Height, FPSNum: enc.FPSNum, FPSDen: enc.FPSDen}, rr, nil
}

//go:build nico_sprite_spike

package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

// Probe-only argv: identical encoding/filter settings to EncodeNicoCommented.
// The saved-scene test compares every decoded video frame with that production
// encoder so that a drift in this experimental copy cannot masquerade as speed.
func spriteDirectArgs(source, output string, o NicoEncodeOptions) []string {
	preset := o.Preset
	if preset == "" {
		preset = "veryfast"
	}
	audio := o.AudioBitrate
	if audio == "" {
		audio = "128k"
	}
	fps := fmt.Sprintf("%d/%d", o.FPSNum, o.FPSDen)
	gop := strconv.Itoa(max(1, int(math.Ceil(4*float64(o.FPSNum)/float64(o.FPSDen)))))
	filter := fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black[base];[1:v]format=rgba[overlay];[base][overlay]overlay=0:0:format=auto,fps=%s,format=yuv420p[v]", o.Width, o.Height, o.Width, o.Height, fps)
	return []string{"-hide_banner", "-loglevel", "error", "-y", "-i", source,
		"-f", "rawvideo", "-pix_fmt", "rgba", "-video_size", fmt.Sprintf("%dx%d", o.Width, o.Height), "-framerate", fps, "-i", "pipe:0",
		"-filter_complex", filter, "-map", "[v]", "-map", "0:a?",
		"-c:v", "libx264", "-preset", preset, "-crf", strconv.Itoa(o.CRF),
		"-x264-params", "sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0",
		"-g", gop, "-keyint_min", gop, "-sc_threshold", "0", "-force_key_frames", "expr:gte(t,n_forced*4)",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", audio, "-movflags", "+faststart", "-shortest", "-f", "mp4", output}
}

// Both children receive the actual OS pipe handles. No Go goroutine copies the
// multi-megabyte RGBA frames. Every successful Start has exactly one Wait.
func spritePipePair(ctx context.Context, nativePath string, nativeArgs []string, encoderPath string, encoderArgs []string, produce func(context.Context, io.Writer) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	defer w.Close()
	native := exec.CommandContext(ctx, nativePath, nativeArgs...)
	encoder := exec.CommandContext(ctx, encoderPath, encoderArgs...)
	hideWindow(native)
	hideWindow(encoder)
	var nativeErr, encoderErr strings.Builder
	native.Stdout = w
	native.Stderr = &nativeErr
	encoder.Stdin = r
	encoder.Stderr = &encoderErr
	var feedR, feedW *os.File
	if produce != nil {
		feedR, feedW, err = os.Pipe()
		if err != nil {
			return err
		}
		defer feedR.Close()
		defer feedW.Close()
		native.Stdin = feedR
		stop := context.AfterFunc(ctx, func() { _ = feedW.Close() })
		defer stop()
	}
	if err = encoder.Start(); err != nil {
		return fmt.Errorf("start encoder: %w", err)
	}
	if err = native.Start(); err != nil {
		cancel()
		_ = encoder.Wait()
		return fmt.Errorf("start native: %w", err)
	}
	_ = r.Close()
	_ = w.Close()
	if feedR != nil {
		_ = feedR.Close()
	}
	type result struct {
		stage string
		err   error
	}
	done := make(chan result, 3)
	go func() { done <- result{"native", native.Wait()} }()
	go func() { done <- result{"encoder", encoder.Wait()} }()
	n := 2
	if produce != nil {
		n++
		go func() { e := produce(ctx, feedW); _ = feedW.Close(); done <- result{"capture", e} }()
	}
	var failures []error
	for i := 0; i < n; i++ {
		res := <-done
		if res.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", res.stage, res.err))
			cancel()
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%w; native=%s; encoder=%s", errors.Join(failures...), strings.TrimSpace(nativeErr.String()), strings.TrimSpace(encoderErr.String()))
	}
	return nil
}

func encodeSpriteDirect(ctx context.Context, ffmpeg, source, output string, snapshot niconico.Snapshot, options nicorender.RenderOptions, enc NicoEncodeOptions, stream bool) (nicorender.SpriteSpikeStats, error) {
	var stats nicorender.SpriteSpikeStats
	if _, err := enc.validate(); err != nil {
		return stats, err
	}
	driver := os.Getenv("IMAGEPAD_NICO_SPRITE_DRIVER")
	if driver == "" {
		driver = "warp"
	}
	depth := os.Getenv("IMAGEPAD_NICO_SPRITE_READBACK")
	if depth == "" {
		depth = "1"
	}
	scene := os.Getenv("IMAGEPAD_NICO_SPRITE_SCENE")
	var produce func(context.Context, io.Writer) error
	if stream {
		scene = "-"
		produce = func(ctx context.Context, w io.Writer) error {
			var err error
			stats, err = nicorender.StreamSpriteSpike(ctx, snapshot, options, w)
			return err
		}
	} else if scene == "" {
		dir, err := os.MkdirTemp("", "nico-direct-scene-*")
		if err != nil {
			return stats, err
		}
		defer os.RemoveAll(dir)
		scene = filepath.Join(dir, "scene.bin")
		stats, err = nicorender.CaptureSpriteSpikeScene(ctx, snapshot, options, scene)
		if err != nil {
			return stats, err
		}
	}
	start := time.Now()
	err := spritePipePair(ctx, os.Getenv("IMAGEPAD_NICO_SPRITE_EXE"), []string{scene, driver, depth}, ffmpeg, spriteDirectArgs(source, output, enc), produce)
	stats.CompositeMs = float64(time.Since(start).Microseconds()) / 1000
	stats.Backend = driver
	return stats, err
}

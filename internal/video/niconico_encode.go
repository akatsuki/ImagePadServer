package video

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"imagepadserver/internal/niconico"
)

// RGBAFrameSource supplies exactly one frame for each requested output frame.
// The encoder consumes frames sequentially, so at most one frame is held by
// this layer in addition to FFmpeg's pipe buffer.
type RGBAFrameSource interface {
	Next(ctx context.Context) (pixels []byte, ok bool, err error)
}

type NicoEncodeOptions struct {
	Width        int
	Height       int
	DurationMs   int64
	FPSNum       int64
	FPSDen       int64
	CRF          int
	Preset       string
	AudioBitrate string
}

type NicoEncodeReport struct {
	OutputPath string
	FrameCount int64
	Width      int
	Height     int
	FPSNum     int64
	FPSDen     int64
}

func (o NicoEncodeOptions) validate() (niconico.FrameClock, error) {
	if o.Width <= 0 || o.Height <= 0 || o.Width > 3840 || o.Height > 2160 {
		return niconico.FrameClock{}, fmt.Errorf("niconico: invalid encode size %dx%d", o.Width, o.Height)
	}
	if o.DurationMs <= 0 {
		return niconico.FrameClock{}, fmt.Errorf("niconico: duration must be positive")
	}
	clock, err := niconico.NewFrameClock(o.FPSNum, o.FPSDen)
	if err != nil {
		return niconico.FrameClock{}, err
	}
	if o.CRF < 0 || o.CRF > 51 {
		return niconico.FrameClock{}, fmt.Errorf("niconico: invalid CRF %d", o.CRF)
	}
	return clock, nil
}

// EncodeNicoCommented overlays raw RGBA comment frames on the source video.
// The output video is always CPU libx264 with one slice per frame, while the
// source audio is encoded as AAC. HLS is intentionally a separate copy-remux
// step (CreateNicoHLS) and never re-encodes this MP4.
func EncodeNicoCommented(ctx context.Context, ffmpeg, sourcePath, outputPath string, options NicoEncodeOptions, frames RGBAFrameSource) (NicoEncodeReport, error) {
	if frames == nil {
		return NicoEncodeReport{}, errors.New("niconico: frame source is required")
	}
	clock, err := options.validate()
	if err != nil {
		return NicoEncodeReport{}, err
	}
	if strings.TrimSpace(ffmpeg) == "" {
		return NicoEncodeReport{}, errors.New("niconico: ffmpeg path is required")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return NicoEncodeReport{}, fmt.Errorf("niconico: source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return NicoEncodeReport{}, fmt.Errorf("niconico: output directory: %w", err)
	}
	frameCount := clock.FrameCountForDurationMs(options.DurationMs)
	if frameCount <= 0 {
		return NicoEncodeReport{}, errors.New("niconico: duration produced no frames")
	}
	args := nicoEncodeArgs(sourcePath, outputPath, options)
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return NicoEncodeReport{}, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return NicoEncodeReport{}, fmt.Errorf("niconico: start ffmpeg: %w", err)
	}
	writeErr := func(err error) (NicoEncodeReport, error) {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		if err == nil {
			err = errors.New("niconico: frame pipeline failed")
		}
		return NicoEncodeReport{}, err
	}
	bytesPerFrame := options.Width * options.Height * 4
	for frame := int64(0); frame < frameCount; frame++ {
		pixels, ok, err := frames.Next(ctx)
		if err != nil {
			return writeErr(fmt.Errorf("niconico: frame %d: %w", frame, err))
		}
		if !ok {
			return writeErr(fmt.Errorf("niconico: frame source ended at %d/%d", frame, frameCount))
		}
		if len(pixels) != bytesPerFrame {
			return writeErr(fmt.Errorf("niconico: frame %d has %d bytes, want %d", frame, len(pixels), bytesPerFrame))
		}
		if _, err := stdin.Write(pixels); err != nil {
			return writeErr(fmt.Errorf("niconico: frame %d pipe: %w", frame, err))
		}
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return NicoEncodeReport{}, fmt.Errorf("niconico: close frame pipe: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return NicoEncodeReport{}, fmt.Errorf("niconico: ffmpeg: %w: %s", err, message)
		}
		return NicoEncodeReport{}, fmt.Errorf("niconico: ffmpeg: %w", err)
	}
	info, err := os.Stat(outputPath)
	if err != nil || info.Size() == 0 {
		if err == nil {
			err = errors.New("empty output")
		}
		return NicoEncodeReport{}, fmt.Errorf("niconico: output: %w", err)
	}
	return NicoEncodeReport{OutputPath: outputPath, FrameCount: frameCount, Width: options.Width, Height: options.Height, FPSNum: options.FPSNum, FPSDen: options.FPSDen}, nil
}

type NicoHLSReport struct {
	PlaylistPath string
	Segments     []string
}

// CreateNicoHLS creates HLS VOD from a completed commented MP4 using stream
// copy. Segment files are validated against the playlist before returning.
func CreateNicoHLS(ctx context.Context, ffmpeg, inputPath, outputDir string) (NicoHLSReport, error) {
	if strings.TrimSpace(ffmpeg) == "" {
		return NicoHLSReport{}, errors.New("niconico: ffmpeg path is required")
	}
	if _, err := os.Stat(inputPath); err != nil {
		return NicoHLSReport{}, fmt.Errorf("niconico: input MP4: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return NicoHLSReport{}, err
	}
	playlist := filepath.Join(outputDir, "playlist.m3u8")
	segmentPattern := filepath.Join(outputDir, "segment-%05d.ts")
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", inputPath, "-map", "0:v:0", "-map", "0:a?", "-c:v", "copy", "-c:a", "copy", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod", "-hls_flags", "independent_segments", "-start_number", "0", "-hls_segment_filename", segmentPattern, playlist}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return NicoHLSReport{}, fmt.Errorf("niconico: hls ffmpeg: %w: %s", err, message)
		}
		return NicoHLSReport{}, fmt.Errorf("niconico: hls ffmpeg: %w", err)
	}
	segments, err := validateNicoPlaylist(playlist, outputDir)
	if err != nil {
		return NicoHLSReport{}, err
	}
	return NicoHLSReport{PlaylistPath: playlist, Segments: segments}, nil
}

func validateNicoPlaylist(playlist, outputDir string) ([]string, error) {
	f, err := os.Open(playlist)
	if err != nil {
		return nil, fmt.Errorf("niconico: open playlist: %w", err)
	}
	defer f.Close()
	var segments []string
	scanner := bufio.NewScanner(f)
	endlist := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "#EXT-X-ENDLIST" {
			endlist = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, `/\\:`) || line == "." || line == ".." {
			return nil, fmt.Errorf("niconico: playlist segment escapes output directory: %q", line)
		}
		path := filepath.Join(outputDir, line)
		if info, err := os.Stat(path); err != nil || info.IsDir() || info.Size() == 0 {
			return nil, fmt.Errorf("niconico: missing playlist segment %q", line)
		}
		segments = append(segments, path)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !endlist || len(segments) == 0 {
		return nil, fmt.Errorf("niconico: incomplete HLS playlist")
	}
	return segments, nil
}

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
	"time"

	"imagepadserver/internal/niconico"
)

// RGBAFrameSource supplies exactly one frame for each requested output frame.
// The encoder consumes frames sequentially, so at most one frame is held by
// this layer in addition to FFmpeg's pipe buffer.
type RGBAFrameSource interface {
	Next(ctx context.Context) (pixels []byte, ok bool, err error)
}

type NicoEncodeOptions struct {
	Width          int
	Height         int
	DurationMs     int64
	FPSNum         int64
	FPSDen         int64
	CRF            int
	Preset         string
	Encoder        string
	AudioBitrate   string
	FilterThreads  int
	DecoderThreads int
	EncoderThreads int
	OutputMode     NicoOutputMode
	HLSOutputDir   string
	StageObserver  func(NicoStageTiming)
}

type NicoEncodeReport struct {
	OutputPath   string
	FrameCount   int64
	Width        int
	Height       int
	FPSNum       int64
	FPSDen       int64
	StageTimings []NicoStageTiming `json:"stage_timings,omitempty"`
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
	mode, err := NormalizeNicoOutputMode(o.OutputMode)
	if err != nil {
		return niconico.FrameClock{}, err
	}
	if mode == NicoOutputTee && strings.TrimSpace(o.HLSOutputDir) == "" {
		return niconico.FrameClock{}, errors.New("niconico: HLS staging path is required for tee output")
	}
	switch strings.ToLower(strings.TrimSpace(o.Encoder)) {
	case "", "x264", "nvenc":
	default:
		return niconico.FrameClock{}, fmt.Errorf("niconico: invalid encoder %q", o.Encoder)
	}
	return clock, nil
}

// EncodeNicoCommented overlays raw RGBA comment frames on the source video.
// The default output video is CPU libx264 with one slice per frame. The
// explicit "nvenc" profile uses h264_nvenc while retaining the same one-slice
// contract. Source audio is encoded as AAC. Separate mode leaves HLS to the
// copy-remux step (CreateNicoHLS); tee mode writes both artifacts from this
// single encoder process.
func EncodeNicoCommented(ctx context.Context, ffmpeg, sourcePath, outputPath string, options NicoEncodeOptions, frames RGBAFrameSource) (NicoEncodeReport, error) {
	timer := newNicoStageTimer(options.StageObserver)
	if frames == nil {
		return NicoEncodeReport{}, errors.New("niconico: frame source is required")
	}
	validateStart := time.Now()
	clock, err := options.validate()
	timer.mark("validate", validateStart)
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
	argsStart := time.Now()
	args := nicoEncodeArgs(sourcePath, outputPath, options)
	timer.mark("build_ffmpeg_args", argsStart)
	mode, err := NormalizeNicoOutputMode(options.OutputMode)
	if err != nil {
		return NicoEncodeReport{}, err
	}
	teeWorkDir := ""
	if mode == NicoOutputTee {
		teeWorkDir, err = createNicoTeeWorkspace(outputPath)
		if err != nil {
			return NicoEncodeReport{}, err
		}
		defer os.RemoveAll(teeWorkDir)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	if teeWorkDir != "" {
		cmd.Dir = teeWorkDir
	}
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return NicoEncodeReport{}, err
	}
	startFFmpeg := time.Now()
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return NicoEncodeReport{}, fmt.Errorf("niconico: start ffmpeg: %w", err)
	}
	timer.mark("ffmpeg_start", startFFmpeg)
	var sourceElapsed time.Duration
	var sourceStarted, sourceFinished time.Time
	var pipeElapsed time.Duration
	var pipeStarted, pipeFinished time.Time
	var pipeBytes int64
	recordFrameStageTimings := func() {
		if !sourceStarted.IsZero() {
			timer.record(NicoStageTiming{
				Name:       "frame_source_next",
				StartedAt:  sourceStarted,
				FinishedAt: sourceFinished,
				Elapsed:    sourceElapsed,
			})
		}
		if !pipeStarted.IsZero() {
			timer.record(NicoStageTiming{
				Name:       "frame_pipe_write_blocking",
				StartedAt:  pipeStarted,
				FinishedAt: pipeFinished,
				Elapsed:    pipeElapsed,
				Bytes:      pipeBytes,
			})
		}
	}
	writeErr := func(err error) (NicoEncodeReport, error) {
		recordFrameStageTimings()
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
	writeFramesStart := time.Now()
	for frame := int64(0); frame < frameCount; frame++ {
		nextStart := time.Now()
		if sourceStarted.IsZero() {
			sourceStarted = nextStart
		}
		pixels, ok, err := frames.Next(ctx)
		sourceElapsed += time.Since(nextStart)
		sourceFinished = time.Now()
		if err != nil {
			return writeErr(fmt.Errorf("niconico: frame %d: %w", frame, err))
		}
		if !ok {
			return writeErr(fmt.Errorf("niconico: frame source ended at %d/%d", frame, frameCount))
		}
		if len(pixels) != bytesPerFrame {
			return writeErr(fmt.Errorf("niconico: frame %d has %d bytes, want %d", frame, len(pixels), bytesPerFrame))
		}
		writeStart := time.Now()
		if pipeStarted.IsZero() {
			pipeStarted = writeStart
		}
		written, err := stdin.Write(pixels)
		pipeElapsed += time.Since(writeStart)
		pipeFinished = time.Now()
		pipeBytes += int64(written)
		if err != nil {
			return writeErr(fmt.Errorf("niconico: frame %d pipe: %w", frame, err))
		}
	}
	recordFrameStageTimings()
	if err := stdin.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return NicoEncodeReport{}, fmt.Errorf("niconico: close frame pipe: %w", err)
	}
	rawFrameBytes := int64(options.Width) * int64(options.Height) * 4 * frameCount
	timer.markBytes("frame_pipe_write", writeFramesStart, rawFrameBytes)
	waitStart := time.Now()
	if err := cmd.Wait(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return NicoEncodeReport{}, fmt.Errorf("niconico: ffmpeg: %w: %s", err, message)
		}
		return NicoEncodeReport{}, fmt.Errorf("niconico: ffmpeg: %w", err)
	}
	timer.mark("ffmpeg_wait", waitStart)
	if mode == NicoOutputTee {
		if err := promoteNicoTeeArtifacts(teeWorkDir, outputPath, options.HLSOutputDir); err != nil {
			return NicoEncodeReport{}, err
		}
	}
	outputValidateStart := time.Now()
	info, err := os.Stat(outputPath)
	if err != nil || info.Size() == 0 {
		if err == nil {
			err = errors.New("empty output")
		}
		return NicoEncodeReport{}, fmt.Errorf("niconico: output: %w", err)
	}
	timer.markBytes("output_validate", outputValidateStart, info.Size())
	return NicoEncodeReport{OutputPath: outputPath, FrameCount: frameCount, Width: options.Width, Height: options.Height, FPSNum: options.FPSNum, FPSDen: options.FPSDen, StageTimings: timer.snapshot()}, nil
}

func createNicoTeeWorkspace(outputPath string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return "", fmt.Errorf("niconico: tee output directory: %w", err)
	}
	workDir, err := os.MkdirTemp(filepath.Dir(outputPath), ".niconico-tee-*")
	if err != nil {
		return "", fmt.Errorf("niconico: tee workspace: %w", err)
	}
	return workDir, nil
}

func promoteNicoTeeArtifacts(workDir, outputPath, hlsOutputDir string) error {
	if strings.TrimSpace(workDir) == "" || strings.TrimSpace(hlsOutputDir) == "" {
		return errors.New("niconico: tee workspace and HLS staging are required")
	}
	if _, err := os.Stat(outputPath); err == nil {
		return errors.New("niconico: tee output MP4 already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("niconico: inspect tee output MP4: %w", err)
	}
	if _, err := os.Stat(hlsOutputDir); err == nil {
		return errors.New("niconico: tee HLS staging already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("niconico: inspect tee HLS staging: %w", err)
	}
	playlist := filepath.Join(workDir, "playlist.m3u8")
	segments, err := validateNicoPlaylist(playlist, workDir)
	if err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(workDir, "out.mp4"), outputPath); err != nil {
		return fmt.Errorf("niconico: promote tee MP4: %w", err)
	}
	cleanupOutput := true
	defer func() {
		if cleanupOutput {
			_ = os.Remove(outputPath)
			_ = os.RemoveAll(hlsOutputDir)
		}
	}()
	if err := os.Mkdir(hlsOutputDir, 0700); err != nil {
		return fmt.Errorf("niconico: create tee HLS staging: %w", err)
	}
	for _, segment := range segments {
		if err := os.Rename(segment, filepath.Join(hlsOutputDir, filepath.Base(segment))); err != nil {
			return fmt.Errorf("niconico: promote tee segment: %w", err)
		}
	}
	if err := os.Rename(playlist, filepath.Join(hlsOutputDir, "playlist.m3u8")); err != nil {
		return fmt.Errorf("niconico: promote tee playlist: %w", err)
	}
	if _, err := validateNicoPlaylist(filepath.Join(hlsOutputDir, "playlist.m3u8"), hlsOutputDir); err != nil {
		return err
	}
	cleanupOutput = false
	return nil
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

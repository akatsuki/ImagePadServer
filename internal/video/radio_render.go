package video

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// runFFmpegOnce runs a short one-shot ffmpeg command, folding stderr into the
// error on failure.
func runFFmpegOnce(ctx context.Context, ffmpeg string, args []string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	untrack := TrackStartedFFmpeg(cmd)
	defer untrack()
	if err := cmd.Run(); err != nil {
		detail := stderr.String()
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		return fmt.Errorf("%w: %s", err, detail)
	}
	return nil
}

// radioEdgeFadeSeconds is the black fade burned into the start and end of
// every pre-rendered playlist track so track changes cross through black.
const radioEdgeFadeSeconds = 0.7

// RadioTrackFileName is the on-disk name of a pre-rendered playlist track.
// MP4 keeps AAC global headers so downstream copy remuxes stay clean.
func RadioTrackFileName(trackID string) string {
	return "radio-track-" + trackID + ".mp4"
}

// RenderRadioTrack renders the audio visualizer for one playlist track into a
// single MP4 file (with black edge fades) and returns its absolute path. The
// heavy lifting is the same pipeline as RunAudioVisualizerHLS; only the
// output muxer and the edge fades differ. progress (nil ok) receives the
// render fraction (0..1).
func RenderRadioTrack(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, trackID string, preset QualityPreset, progress func(float64)) (string, error) {
	outPath := filepath.Join(outDir, RadioTrackFileName(trackID))
	buildArgs := func(assPath, fontDir string, mode *ForegroundMode, encoder VideoEncoderProfile) []string {
		return audioVisualizerMP4ArgsWithEncoder(input.SourcePath, assPath, fontDir, outPath, preset, mode, encoder, audioLoudnormFilter(input.Kind), input.Analysis.Duration)
	}
	err := runAudioVisualizerEncode(ctx, outDir, ffmpeg, input, "radio-"+trackID, preset, buildArgs, func() { _ = os.Remove(outPath) }, progress)
	if err != nil {
		return "", fmt.Errorf("render radio track: %w", err)
	}
	return outPath, nil
}

func audioVisualizerMP4ArgsWithEncoder(audioPath, assPath, fontDir, outPath string, preset QualityPreset, mode *ForegroundMode, encoder VideoEncoderProfile, audioFilter string, durationSeconds float64) []string {
	args := audioVisualizerCoreArgsWithEncoder(audioPath, assPath, fontDir, preset, mode, encoder, audioFilter, radioEdgeFadeSeconds, durationSeconds)
	return append(args,
		"-movflags", "+faststart",
		"-f", "mp4",
		"-y",
		outPath,
	)
}

// RenderRadioFiller renders (and caches) the black + silent clip the radio
// broadcasts between tracks, while paused, and while idle, so the publisher
// connection never goes quiet. Stream parameters (resolution, fps, codecs)
// match the pre-rendered tracks.
func RenderRadioFiller(ctx context.Context, outDir, ffmpeg string, preset QualityPreset) (string, error) {
	height := preset.Height
	if height <= 0 {
		height = 720
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}
	outPath := filepath.Join(outDir, fmt.Sprintf("radio-filler-%d.mp4", height))
	if stat, err := os.Stat(outPath); err == nil && stat.Size() > 0 {
		return outPath, nil
	}
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("color=black:s=%dx%d:r=30", width, height),
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo",
		"-t", "2",
		"-c:v", "libx264", "-preset", "veryfast", "-b:v", "300k", "-pix_fmt", "yuv420p", "-g", "60",
		"-c:a", "aac", "-b:a", preset.AudioBitrate, "-ar", "48000", "-ac", "2",
		"-movflags", "+faststart",
		"-f", "mp4", "-y", outPath,
	}
	if err := runFFmpegOnce(ctx, ffmpeg, args); err != nil {
		_ = os.Remove(outPath)
		return "", fmt.Errorf("render radio filler: %w", err)
	}
	return outPath, nil
}

// RadioPublisherArgs is the persistent RTMP publisher: it consumes an
// endless MPEG-TS byte stream on stdin and republishes it to mediamtx.
// Wallclock timestamps make the concatenated per-track segments (whose own
// timestamps restart at zero) monotonic, so the publisher connection — and
// therefore the viewer-facing stream — survives track changes and pauses.
func RadioPublisherArgs(rtmpURL string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-fflags", "+genpts",
		"-use_wallclock_as_timestamps", "1",
		"-f", "mpegts",
		"-i", "pipe:0",
		"-c", "copy",
		"-bsf:a", "aac_adtstoasc",
		"-f", "flv",
		rtmpURL,
	}
}

// RadioFeederArgs converts one pre-rendered MP4 into a real-time MPEG-TS
// stream on stdout, for piping into the persistent publisher. loop repeats
// the input forever (filler); startSeconds resumes mid-track.
func RadioFeederArgs(mediaPath string, startSeconds int, loop bool) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-re",
	}
	if loop {
		args = append(args, "-stream_loop", "-1")
	}
	if startSeconds > 0 {
		args = append(args, "-ss", strconv.Itoa(startSeconds))
	}
	return append(args,
		"-i", mediaPath,
		"-c", "copy",
		"-f", "mpegts",
		"pipe:1",
	)
}

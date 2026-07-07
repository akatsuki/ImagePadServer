package video

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// RadioTrackFileName is the on-disk name of a pre-rendered playlist track.
// MP4 is required (not MPEG-TS): the ffmpeg RTSP muxer refuses AAC without
// global headers, and ADTS-in-TS carries none ("AAC with no global headers is
// currently not supported"), while MP4 stores the AudioSpecificConfig so a
// plain codec copy push works.
func RadioTrackFileName(trackID string) string {
	return "radio-track-" + trackID + ".mp4"
}

// RenderRadioTrack renders the audio visualizer for one playlist track into a
// single MP4 file and returns its absolute path. The heavy lifting is the
// same pipeline as RunAudioVisualizerHLS; only the output muxer differs.
func RenderRadioTrack(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, trackID string, preset QualityPreset) (string, error) {
	outPath := filepath.Join(outDir, RadioTrackFileName(trackID))
	buildArgs := func(assPath, fontDir string, mode *ForegroundMode, encoder VideoEncoderProfile) []string {
		return audioVisualizerMP4ArgsWithEncoder(input.SourcePath, assPath, fontDir, outPath, preset, mode, encoder, audioLoudnormFilter(input.Kind))
	}
	err := runAudioVisualizerEncode(ctx, outDir, ffmpeg, input, "radio-"+trackID, preset, buildArgs, func() { _ = os.Remove(outPath) })
	if err != nil {
		return "", fmt.Errorf("render radio track: %w", err)
	}
	return outPath, nil
}

func audioVisualizerMP4ArgsWithEncoder(audioPath, assPath, fontDir, outPath string, preset QualityPreset, mode *ForegroundMode, encoder VideoEncoderProfile, audioFilter string) []string {
	args := audioVisualizerCoreArgsWithEncoder(audioPath, assPath, fontDir, preset, mode, encoder, audioFilter)
	return append(args,
		"-movflags", "+faststart",
		"-f", "mp4",
		"-y",
		outPath,
	)
}

// RadioPushArgs streams a pre-rendered track to mediamtx in real time without
// re-encoding. -re paces reads at native speed so the RTSP session behaves
// like a live source.
func RadioPushArgs(mediaPath, rtspURL string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-re",
		"-i", mediaPath,
		"-c", "copy",
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		rtspURL,
	}
}

// RunRadioPush executes the codec-copy push and, on failure, folds the tail
// of ffmpeg's stderr into the returned error so the playlist UI can show the
// real cause instead of a bare exit status.
func RunRadioPush(ctx context.Context, dir, ffmpeg, mediaPath, rtspURL string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, RadioPushArgs(mediaPath, rtspURL)...)
	hideWindow(cmd)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	untrack := TrackStartedFFmpeg(cmd)
	defer untrack()
	if err := cmd.Run(); err != nil {
		detail := stderr.String()
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

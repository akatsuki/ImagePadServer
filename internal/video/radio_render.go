package video

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// RadioTrackFileName is the on-disk name of a pre-rendered playlist track:
// a self-contained MPEG-TS (H.264 + AAC) that the radio streamer can push to
// mediamtx with a plain codec copy.
func RadioTrackFileName(trackID string) string {
	return "radio-track-" + trackID + ".ts"
}

// RenderRadioTrack renders the audio visualizer for one playlist track into a
// single MPEG-TS file and returns its absolute path. The heavy lifting is the
// same pipeline as RunAudioVisualizerHLS; only the output muxer differs.
func RenderRadioTrack(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, trackID string, preset QualityPreset) (string, error) {
	outPath := filepath.Join(outDir, RadioTrackFileName(trackID))
	buildArgs := func(assPath, fontDir string, mode *ForegroundMode, encoder VideoEncoderProfile) []string {
		return audioVisualizerMPEGTSArgsWithEncoder(input.SourcePath, assPath, fontDir, outPath, preset, mode, encoder, audioLoudnormFilter(input.Kind))
	}
	err := runAudioVisualizerEncode(ctx, outDir, ffmpeg, input, "radio-"+trackID, preset, buildArgs, func() { _ = os.Remove(outPath) })
	if err != nil {
		return "", fmt.Errorf("render radio track: %w", err)
	}
	return outPath, nil
}

func audioVisualizerMPEGTSArgsWithEncoder(audioPath, assPath, fontDir, outPath string, preset QualityPreset, mode *ForegroundMode, encoder VideoEncoderProfile, audioFilter string) []string {
	args := audioVisualizerCoreArgsWithEncoder(audioPath, assPath, fontDir, preset, mode, encoder, audioFilter)
	return append(args,
		"-f", "mpegts",
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

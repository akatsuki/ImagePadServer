package airplay

import (
	"fmt"
	"strings"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

// DirectOutputConfig is the native GStreamer delivery contract. SourceFPS is
// the cadence used to absorb and hold iPhone frames; OutputFPS is the cadence
// exposed to MediaMTX and downstream players.
type DirectOutputConfig struct {
	Width            int
	Height           int
	SourceFPS        int
	OutputFPS        int
	VideoBitrateKbps int
	MaxRateKbps      int
	BufferSizeKbps   int
	AudioBitrateBps  int
	GOPFrames        int
}

// BuildGStreamerBridgeArgs returns the stable CLI contract for the native
// decoder bridge. The helper owns RTP reception and emits one muxed stream on
// stdout; it must never write diagnostics to stdout because that stream is
// connected directly to FFmpeg's stdin.
func BuildGStreamerBridgeArgs(videoPort, audioPort int) []string {
	return []string{
		"--video-port", fmt.Sprintf("%d", videoPort),
		"--audio-port", fmt.Sprintf("%d", audioPort),
		"--stdout",
	}
}

// BuildGStreamerDirectArgs builds the native bridge contract for the direct
// MediaMTX path. The native process owns decode, encode, recording, and RTSP
// publish; no binary stdout or FFmpeg child is involved.
func BuildGStreamerDirectArgs(videoPort, audioPort int, publishURL, recording, stopFile string, output DirectOutputConfig) []string {
	return []string{
		"--video-port", fmt.Sprintf("%d", videoPort),
		"--audio-port", fmt.Sprintf("%d", audioPort),
		"--publish-url", publishURL,
		"--recording", recording,
		"--stop-file", stopFile,
		"--width", fmt.Sprintf("%d", output.Width),
		"--height", fmt.Sprintf("%d", output.Height),
		"--source-fps", fmt.Sprintf("%d", output.SourceFPS),
		"--fps", fmt.Sprintf("%d", output.OutputFPS),
		"--bitrate", fmt.Sprintf("%d", output.VideoBitrateKbps),
		"--maxrate", fmt.Sprintf("%d", output.MaxRateKbps),
		"--buffer-size", fmt.Sprintf("%d", output.BufferSizeKbps),
		"--audio-bitrate", fmt.Sprintf("%d", output.AudioBitrateBps),
		"--gop", fmt.Sprintf("%d", output.GOPFrames),
	}
}

// BuildSourceClockDirectArgs builds the source-clock bridge contract. The
// bridge owns ephemeral loopback listeners; the selected ports are returned in
// its ready file for the patched UxPlay process to consume.
func BuildSourceClockDirectArgs(publishURL, recording, stopFile, sessionToken, sessionID string, publisherGeneration uint64, paths airplaycontract.FixedPaths, output DirectOutputConfig) []string {
	return buildSourceClockDirectArgs(0, 0, publishURL, recording, stopFile, sessionToken, sessionID, publisherGeneration, paths, output)
}

func buildSourceClockDirectArgs(videoListenPort, audioListenPort int, publishURL, recording, stopFile, sessionToken, sessionID string, publisherGeneration uint64, paths airplaycontract.FixedPaths, output DirectOutputConfig) []string {
	return buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: videoListenPort,
		AudioListenPort: audioListenPort,
		PublishURL:      publishURL,
		SessionToken:    sessionToken,
		Artifacts: airplaycontract.PublisherArtifacts{
			SessionID:   sessionID,
			Generation:  publisherGeneration,
			Recording:   recording,
			Ready:       paths.Ready,
			MediaReady:  paths.MediaReady,
			EventLog:    paths.EventLog,
			StopRequest: stopFile,
		},
		Output: output,
	})
}

type sourceClockPublisherArgsConfig struct {
	VideoListenPort int
	AudioListenPort int
	PublishURL      string
	SessionToken    string
	Artifacts       airplaycontract.PublisherArtifacts
	Output          DirectOutputConfig
}

func buildSourceClockPublisherArgs(config sourceClockPublisherArgsConfig) []string {
	artifacts := config.Artifacts
	output := config.Output
	return []string{
		"--source-clock",
		"--video-listen-port", fmt.Sprintf("%d", config.VideoListenPort),
		"--audio-listen-port", fmt.Sprintf("%d", config.AudioListenPort),
		"--session-token", config.SessionToken,
		"--session-id", artifacts.SessionID,
		"--publisher-generation", fmt.Sprintf("%d", artifacts.Generation),
		"--ready-file", artifacts.Ready,
		"--media-ready-file", artifacts.MediaReady,
		"--event-log", artifacts.EventLog,
		"--no-signal-seconds", "0",
		"--publish-url", config.PublishURL,
		"--recording", artifacts.Recording,
		"--stop-file", artifacts.StopRequest,
		"--width", fmt.Sprintf("%d", output.Width),
		"--height", fmt.Sprintf("%d", output.Height),
		"--source-fps", fmt.Sprintf("%d", output.SourceFPS),
		"--fps", fmt.Sprintf("%d", output.OutputFPS),
		"--bitrate", fmt.Sprintf("%d", output.VideoBitrateKbps),
		"--maxrate", fmt.Sprintf("%d", output.MaxRateKbps),
		"--buffer-size", fmt.Sprintf("%d", output.BufferSizeKbps),
		"--audio-bitrate", fmt.Sprintf("%d", output.AudioBitrateBps),
		"--gop", fmt.Sprintf("%d", output.GOPFrames),
	}
}

func nextSourceClockPublisherArgs(args []string, artifacts airplaycontract.PublisherArtifacts) ([]string, error) {
	if strings.TrimSpace(artifacts.SessionID) == "" || artifacts.Generation == 0 {
		return nil, fmt.Errorf("source-clock publisher artifact identity is incomplete")
	}
	for name, path := range map[string]string{
		"recording":    artifacts.Recording,
		"ready":        artifacts.Ready,
		"media-ready":  artifacts.MediaReady,
		"event-log":    artifacts.EventLog,
		"process-log":  artifacts.ProcessLog,
		"stop-request": artifacts.StopRequest,
	} {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("source-clock publisher %s artifact path is empty", name)
		}
	}
	replacements := map[string]string{
		"--session-id":           artifacts.SessionID,
		"--publisher-generation": fmt.Sprintf("%d", artifacts.Generation),
		"--recording":            artifacts.Recording,
		"--ready-file":           artifacts.Ready,
		"--media-ready-file":     artifacts.MediaReady,
		"--event-log":            artifacts.EventLog,
		"--stop-file":            artifacts.StopRequest,
	}
	next := append([]string(nil), args...)
	counts := make(map[string]int, len(replacements))
	for index := 0; index < len(next); index++ {
		value, replace := replacements[next[index]]
		if !replace {
			continue
		}
		counts[next[index]]++
		if index+1 >= len(next) {
			return nil, fmt.Errorf("source-clock publisher argument %s has no value", next[index])
		}
		next[index+1] = value
		index++
	}
	for flag := range replacements {
		if counts[flag] != 1 {
			return nil, fmt.Errorf("source-clock publisher argument %s occurs %d times", flag, counts[flag])
		}
	}
	return next, nil
}

// BuildGStreamerFFmpegArgs builds the persistent encoder command for the
// bridge's streamable Matroska output. Unlike BuildBridgeArgs, this input has
// already been decoded and clocked by GStreamer, so FFmpeg must not probe an
// SDP or apply a second RTP reorder/cache policy.
func BuildGStreamerFFmpegArgs(publishURL string, encoder video.VideoEncoderProfile, preset video.QualityPreset) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-f", "matroska",
		"-i", "pipe:0",
		"-map", "0:v:0",
		"-map", "0:a:0",
		"-vf", "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=black",
	}
	args = append(args, encoder.FFmpegArgs(preset, "ultrafast")...)
	args = append(args,
		"-c:a", "aac",
		"-b:a", "160k",
		"-ar", "48000",
		"-ac", "2",
		"-avoid_negative_ts", "make_zero",
		"-f", "flv",
		publishURL,
	)
	return args
}

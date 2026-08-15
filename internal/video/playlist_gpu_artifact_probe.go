package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// PlaylistGPUArtifactProbe is the bounded acceptance receipt for an explicit
// playlist GPU MPEG-TS artifact. It does not alter the normal CPU music route.
type PlaylistGPUArtifactProbe struct {
	VideoCodec           string  `json:"video_codec"`
	AudioCodec           string  `json:"audio_codec"`
	Width                int     `json:"width"`
	Height               int     `json:"height"`
	PixelFormat          string  `json:"pixel_format"`
	ColorSpace           string  `json:"color_space"`
	ColorRange           string  `json:"color_range"`
	VideoFrames          uint64  `json:"video_frames"`
	ExpectedVideoFrames  uint64  `json:"expected_video_frames"`
	VideoDurationSeconds float64 `json:"video_duration_seconds"`
	AudioDurationSeconds float64 `json:"audio_duration_seconds"`
	// DurationDeltaFrames is |video - audio| in 30Hz frames. The gate allows up
	// to 2 frames because the native AAC encoder prepends ~1024-sample priming
	// plus one frame-boundary pad (~1792 samples total ≈ 37ms at 48kHz) that
	// the decoder skips on playback; the decoded A/V spans are equal, but the
	// raw stream duration reads ~1.1 frames longer on the audio track.
	DurationDeltaFrames float64 `json:"duration_delta_frames"`
	BT709Limited        bool    `json:"bt709_limited"`
}

func (p PlaylistGPUArtifactProbe) Validate() error {
	if p.VideoCodec != "h264" || p.AudioCodec != "aac" {
		return errors.New("playlist GPU artifact codecs are not h264/aac")
	}
	if p.Width <= 0 || p.Height <= 0 || p.PixelFormat != "yuv420p" {
		return errors.New("playlist GPU artifact video format is invalid")
	}
	if p.ColorSpace != "bt709" || p.ColorRange != "tv" || !p.BT709Limited {
		return errors.New("playlist GPU artifact is not BT.709 limited")
	}
	if p.ExpectedVideoFrames == 0 || p.VideoFrames != p.ExpectedVideoFrames {
		return errors.New("playlist GPU artifact video frame count does not match expectation")
	}
	if p.VideoDurationSeconds <= 0 || p.AudioDurationSeconds <= 0 || math.IsNaN(p.DurationDeltaFrames) || p.DurationDeltaFrames > 2 {
		return fmt.Errorf("playlist GPU artifact A/V duration gate failed: video=%.6fs audio=%.6fs delta_frames=%.6f", p.VideoDurationSeconds, p.AudioDurationSeconds, p.DurationDeltaFrames)
	}
	return nil
}

type playlistGPUArtifactProbeJSON struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		PixelFormat string `json:"pix_fmt"`
		ColorSpace  string `json:"color_space"`
		ColorRange  string `json:"color_range"`
		Frames      string `json:"nb_read_frames"`
		Duration    string `json:"duration"`
	} `json:"streams"`
}

// ParsePlaylistGPUArtifactProbeJSON parses ffprobe -count_frames JSON and
// applies the explicit evaluation artifact gate without reading GPU pixels.
func ParsePlaylistGPUArtifactProbeJSON(data []byte, fps int, expectedFrames uint64) (PlaylistGPUArtifactProbe, error) {
	if fps <= 0 || expectedFrames == 0 {
		return PlaylistGPUArtifactProbe{}, errors.New("playlist GPU artifact probe requires fps and expected frame count")
	}
	var raw playlistGPUArtifactProbeJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return PlaylistGPUArtifactProbe{}, fmt.Errorf("parse playlist GPU artifact probe: %w", err)
	}
	var report PlaylistGPUArtifactProbe
	report.ExpectedVideoFrames = expectedFrames
	for _, stream := range raw.Streams {
		duration, err := strconv.ParseFloat(stream.Duration, 64)
		if err != nil || duration <= 0 {
			return PlaylistGPUArtifactProbe{}, errors.New("playlist GPU artifact stream duration is invalid")
		}
		if stream.CodecType == "video" {
			if report.VideoCodec != "" {
				return PlaylistGPUArtifactProbe{}, errors.New("playlist GPU artifact has multiple video streams")
			}
			frames, err := strconv.ParseUint(stream.Frames, 10, 64)
			if err != nil || frames == 0 {
				return PlaylistGPUArtifactProbe{}, errors.New("playlist GPU artifact video frame count is invalid")
			}
			report.VideoCodec = stream.CodecName
			report.Width = stream.Width
			report.Height = stream.Height
			report.PixelFormat = stream.PixelFormat
			report.ColorSpace = strings.ToLower(stream.ColorSpace)
			report.ColorRange = strings.ToLower(stream.ColorRange)
			report.VideoFrames = frames
			report.VideoDurationSeconds = duration
		} else if stream.CodecType == "audio" {
			if report.AudioCodec != "" {
				return PlaylistGPUArtifactProbe{}, errors.New("playlist GPU artifact has multiple audio streams")
			}
			report.AudioCodec = stream.CodecName
			report.AudioDurationSeconds = duration
		}
	}
	if report.VideoDurationSeconds <= 0 || report.AudioDurationSeconds <= 0 {
		return PlaylistGPUArtifactProbe{}, errors.New("playlist GPU artifact requires video and audio streams")
	}
	report.DurationDeltaFrames = math.Abs(report.VideoDurationSeconds-report.AudioDurationSeconds) * float64(fps)
	report.BT709Limited = report.ColorSpace == "bt709" && report.ColorRange == "tv"
	if err := report.Validate(); err != nil {
		return report, err
	}
	return report, nil
}

// ProbePlaylistGPUArtifact runs ffprobe against a completed evaluation artifact.
func ProbePlaylistGPUArtifact(ctx context.Context, ffprobe, path string, fps int, expectedFrames uint64) (PlaylistGPUArtifactProbe, error) {
	if strings.TrimSpace(ffprobe) == "" || strings.TrimSpace(path) == "" {
		return PlaylistGPUArtifactProbe{}, errors.New("playlist GPU artifact probe requires ffprobe and path")
	}
	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-count_frames",
		"-show_entries", "stream=codec_type,codec_name,width,height,pix_fmt,color_space,color_range,nb_read_frames,duration",
		"-of", "json",
		path,
	)
	hideWindow(cmd)
	stdout, stderr, err := SeparateOutputTrackedFFmpeg(cmd)
	if err != nil {
		return PlaylistGPUArtifactProbe{}, fmt.Errorf("ffprobe playlist GPU artifact failed: %w: %s", err, trimOutput(stderr))
	}
	return ParsePlaylistGPUArtifactProbeJSON(stdout, fps, expectedFrames)
}

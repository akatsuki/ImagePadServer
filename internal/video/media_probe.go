package video

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
)

// ---------------------------------------------------------------------------
// ffprobe JSON output structs
// ---------------------------------------------------------------------------

// ffprobeOutput maps the JSON output of ffprobe -show_streams -show_format -of json.
type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeStream struct {
	Index       int                `json:"index"`
	CodecType   string             `json:"codec_type"`
	CodecName   string             `json:"codec_name"`
	Disposition ffprobeDisposition `json:"disposition"`
	Width       int                `json:"width"`
	Height      int                `json:"height"`
	Tags        map[string]string  `json:"tags"`
}

type ffprobeDisposition struct {
	AttachedPic int `json:"attached_pic"`
}

type ffprobeFormat struct {
	Duration string            `json:"duration"`
	Tags     map[string]string `json:"tags"`
}

// ---------------------------------------------------------------------------
// ParseMediaProbeJSON
// ---------------------------------------------------------------------------

// ParseMediaProbeJSON parses the JSON output from ffprobe
// -show_streams -show_format -of json and returns a MediaProbe.
func ParseMediaProbeJSON(data []byte) (MediaProbe, error) {
	var out ffprobeOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return MediaProbe{}, fmt.Errorf("parse ffprobe output: %w", err)
	}

	streams := make([]MediaStream, len(out.Streams))
	for i, s := range out.Streams {
		streams[i] = MediaStream{
			Index:       s.Index,
			CodecType:   s.CodecType,
			CodecName:   s.CodecName,
			AttachedPic: s.Disposition.AttachedPic != 0,
			Width:       s.Width,
			Height:      s.Height,
			Tags:        s.Tags,
		}
	}

	var duration float64
	if out.Format.Duration != "" {
		duration, _ = strconv.ParseFloat(out.Format.Duration, 64)
	}

	return MediaProbe{
		Streams:    streams,
		Duration:   duration,
		FormatTags: out.Format.Tags,
	}, nil
}

// ---------------------------------------------------------------------------
// ClassifyMediaProbe
// ---------------------------------------------------------------------------

// ClassifyMediaProbe returns the MediaClass for the probed media using the
// spec classification rules:
//
//  1. If any stream has CodecType "video" AND NOT AttachedPic → MediaVideo.
//  2. If any stream has CodecType "audio" → MediaAudio.
//  3. Otherwise → MediaUnsupported.
func ClassifyMediaProbe(probe MediaProbe) MediaClass {
	hasAudio := false
	for _, s := range probe.Streams {
		if s.CodecType == "video" && !s.AttachedPic {
			return MediaVideo
		}
		if s.CodecType == "audio" {
			hasAudio = true
		}
	}
	if hasAudio {
		return MediaAudio
	}
	return MediaUnsupported
}

// ---------------------------------------------------------------------------
// ProbeMedia
// ---------------------------------------------------------------------------

// ProbeMedia runs ffprobe on the given path and parses its JSON output.
//
// The ffprobe binary is invoked with:
//
//	-v error -show_streams -show_format -of json <path>
func ProbeMedia(ctx context.Context, ffprobe, path string) (MediaProbe, error) {
	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-show_streams",
		"-show_format",
		"-of", "json",
		path,
	)
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return MediaProbe{}, fmt.Errorf("ffprobe failed: %w: %s", err, trimOutput(output))
	}
	return ParseMediaProbeJSON(output)
}

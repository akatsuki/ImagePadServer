package video

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MotionScore summarizes how compressible a source video is. Low-motion,
// visually-simple content has small inter-frame sizes; high-motion or
// detailed content has large inter-frame sizes.
type MotionScore struct {
	AverageFrameSize     int
	NonIAverageFrameSize int // average excluding keyframes, which are outliers
	FrameCount           int
}

// IsLowMotion reports whether the score indicates largely static content.
// It uses non-keyframe sizes because I-frames are periodic outliers that
// inflate the overall average even for slideshow-like video.
func (s MotionScore) IsLowMotion() bool {
	if s.FrameCount < 5 || s.NonIAverageFrameSize <= 0 {
		return false
	}
	return float64(s.NonIAverageFrameSize) < 12*1024
}

// IsVeryLowMotion reports whether the score indicates near-static content
// (e.g. slideshows, idle game screens, vtuber talking head with little movement).
func (s MotionScore) IsVeryLowMotion() bool {
	if s.FrameCount < 5 || s.NonIAverageFrameSize <= 0 {
		return false
	}
	return float64(s.NonIAverageFrameSize) < 5*1024
}

// ProbeMotionScore samples the first few seconds of a video and returns a
// MotionScore describing its frame-size distribution. Frame size is a proxy
// for motion/complexity: identical frames compress to tiny sizes, whereas
// high-motion or noisy frames compress poorly. Keyframes are excluded from
// the low-motion heuristic because they are large regardless of content.
func ProbeMotionScore(sourcePath string) (MotionScore, error) {
	ffprobe, err := EnsureFFprobe()
	if err != nil {
		return MotionScore{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "frame=pkt_size,pict_type",
		"-of", "csv=p=0",
		"-read_intervals", "%+8",
		sourcePath,
	)
	hideWindow(cmd)

	out, err := cmd.Output()
	if err != nil {
		return MotionScore{}, err
	}

	var all []int
	var nonI []int
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		parts := strings.Split(strings.TrimSpace(scanner.Text()), ",")
		if len(parts) < 2 {
			continue
		}
		n, err := strconv.Atoi(parts[0])
		if err != nil || n <= 0 {
			continue
		}
		all = append(all, n)
		if parts[1] != "I" {
			nonI = append(nonI, n)
		}
	}
	if len(all) == 0 {
		return MotionScore{}, nil
	}

	return MotionScore{
		AverageFrameSize:     averageInt(all),
		NonIAverageFrameSize: averageInt(nonI),
		FrameCount:           len(all),
	}, nil
}

func averageInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	var sum int
	for _, v := range values {
		sum += v
	}
	return sum / len(values)
}

// AdaptPresetForContent adjusts the encoding preset based on source motion
// complexity. Low-motion / near-static sources get a higher CRF and a lower
// maxrate/bufsize ceiling, so they do not bloat to the same size as high-motion
// content. High-motion sources keep the original preset.
func AdaptPresetForContent(preset QualityPreset, score MotionScore) QualityPreset {
	if score.FrameCount < 5 || score.NonIAverageFrameSize <= 0 {
		return preset
	}

	boost := 0
	maxRateFactor := 1.0
	switch {
	case score.IsVeryLowMotion():
		boost = 5
		maxRateFactor = 0.4
	case score.IsLowMotion():
		boost = 3
		maxRateFactor = 0.6
	default:
		return preset
	}

	preset.CRF = clampInt(preset.CRF+boost, 18, 40)
	if preset.MaxRate != "" {
		preset.MaxRate = scaleBitrate(preset.MaxRate, maxRateFactor)
	}
	if preset.BufferSize != "" {
		preset.BufferSize = scaleBitrate(preset.BufferSize, maxRateFactor)
	}
	return preset
}

// capPresetToSourceBitrate lowers the bitrate ceiling when the source video
// is already compressed at a lower bitrate. This prevents the converted output
// from being larger than the original just because our default ceiling is high.
// The cap is source bitrate * 1.25 so re-encoding to H.264 has a little headroom
// over more efficient source codecs like AV1.
func capPresetToSourceBitrate(preset QualityPreset, sourceBitrate int) QualityPreset {
	if sourceBitrate <= 0 || preset.MaxRate == "" {
		return preset
	}
	maxBps := parseBitrateToBps(preset.MaxRate)
	if maxBps <= 0 {
		return preset
	}
	capBps := int(float64(sourceBitrate) * 1.25)
	if capBps >= maxBps {
		return preset
	}
	factor := float64(capBps) / float64(maxBps)
	preset.MaxRate = scaleBitrate(preset.MaxRate, factor)
	if preset.BufferSize != "" {
		preset.BufferSize = scaleBitrate(preset.BufferSize, factor)
	}
	return preset
}

func parseBitrateToBps(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	unit := ""
	if last := s[len(s)-1]; last < '0' || last > '9' {
		unit = s[len(s)-1:]
		s = s[:len(s)-1]
	}
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	switch strings.ToLower(unit) {
	case "k":
		v *= 1000
	case "m":
		v *= 1000000
	}
	return v
}

// ProbeSourceBitrate returns the source video stream bitrate in bits per
// second. If the video stream has no bitrate, it falls back to the container
// format bitrate.
func ProbeSourceBitrate(sourcePath string) (int, error) {
	ffprobe, err := EnsureFFprobe()
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	probe := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, ffprobe, args...)
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}

	s, err := probe(
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=bit_rate",
		"-of", "csv=p=0",
		sourcePath,
	)
	if err != nil || s == "" || s == "N/A" {
		s, err = probe(
			"-v", "error",
			"-show_entries", "format=bit_rate",
			"-of", "csv=p=0",
			sourcePath,
		)
		if err != nil {
			return 0, err
		}
	}
	if s == "" || s == "N/A" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

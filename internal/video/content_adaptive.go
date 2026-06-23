package video

import (
	"bufio"
	"context"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MotionScore summarizes how compressible a source video is. Low-motion,
// visually-simple content has small, stable frame sizes; high-motion or
// detailed content has larger, more variable frame sizes.
type MotionScore struct {
	AverageFrameSize int
	StdDev           float64
	FrameCount       int
}

// IsLowMotion reports whether the score indicates largely static content.
func (s MotionScore) IsLowMotion() bool {
	if s.FrameCount < 5 || s.AverageFrameSize <= 0 {
		return false
	}
	avgKB := float64(s.AverageFrameSize) / 1024.0
	coeffVar := s.StdDev / float64(s.AverageFrameSize)
	return avgKB < 45 && coeffVar < 0.55
}

// IsVeryLowMotion reports whether the score indicates near-static content
// (e.g. slideshows, idle game screens, vtuber talking head with little movement).
func (s MotionScore) IsVeryLowMotion() bool {
	if s.FrameCount < 5 || s.AverageFrameSize <= 0 {
		return false
	}
	avgKB := float64(s.AverageFrameSize) / 1024.0
	coeffVar := s.StdDev / float64(s.AverageFrameSize)
	return avgKB < 22 && coeffVar < 0.45
}

// ProbeMotionScore samples the first few seconds of a video and returns a
// MotionScore describing its frame-size distribution. Frame size is a proxy
// for motion/complexity: identical frames compress to tiny sizes, whereas
// high-motion or noisy frames compress poorly.
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
		"-show_entries", "frame=pkt_size",
		"-of", "csv=p=0",
		"-read_intervals", "%+8",
		sourcePath,
	)
	hideWindow(cmd)

	out, err := cmd.Output()
	if err != nil {
		return MotionScore{}, err
	}

	var sizes []int
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		n, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil || n <= 0 {
			continue
		}
		sizes = append(sizes, n)
	}
	if len(sizes) == 0 {
		return MotionScore{}, nil
	}

	var sum int
	for _, s := range sizes {
		sum += s
	}
	avg := float64(sum) / float64(len(sizes))
	var variance float64
	for _, s := range sizes {
		d := float64(s) - avg
		variance += d * d
	}
	variance /= float64(len(sizes))

	return MotionScore{
		AverageFrameSize: int(avg),
		StdDev:           math.Sqrt(variance),
		FrameCount:       len(sizes),
	}, nil
}

// AdaptPresetForContent adjusts the encoding preset based on source motion
// complexity. Low-motion sources get a higher CRF and a lower bitrate ceiling,
// so they do not bloat to the same size as high-motion content. High-motion
// sources keep the original preset.
func AdaptPresetForContent(preset QualityPreset, score MotionScore) QualityPreset {
	if score.FrameCount < 5 || score.AverageFrameSize <= 0 {
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

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Marker struct {
	PTS time.Duration `json:"pts"`
}

type MarkerMatch struct {
	FirstOffset       time.Duration `json:"firstOffset"`
	LastOffset        time.Duration `json:"lastOffset"`
	Drift             time.Duration `json:"drift"`
	MaxAbsoluteOffset time.Duration `json:"maxAbsoluteOffset"`
	Matched           int           `json:"matched"`
}

type MediaAnalysis struct {
	Schema       int         `json:"schema"`
	Status       string      `json:"status"`
	VideoMarkers []Marker    `json:"videoMarkers"`
	AudioMarkers []Marker    `json:"audioMarkers"`
	Match        MarkerMatch `json:"match"`
	Error        string      `json:"error,omitempty"`
}

var ptsPattern = regexp.MustCompile(`pts_time[:=](-?[0-9]+(?:\.[0-9]+)?)`)

func parseMarkers(metadata, valueKey string, threshold float64) ([]Marker, error) {
	var markers []Marker
	var pts time.Duration
	havePTS := false
	above := false
	scanner := bufio.NewScanner(strings.NewReader(metadata))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if match := ptsPattern.FindStringSubmatch(line); match != nil {
			seconds, err := strconv.ParseFloat(match[1], 64)
			if err != nil {
				return nil, fmt.Errorf("parse pts %q: %w", match[1], err)
			}
			pts = time.Duration(seconds * float64(time.Second))
			havePTS = true
		}
		position := strings.Index(line, valueKey+"=")
		if position < 0 {
			continue
		}
		valueText := strings.TrimSpace(line[position+len(valueKey)+1:])
		value, err := strconv.ParseFloat(valueText, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s value %q: %w", valueKey, valueText, err)
		}
		isAbove := value >= threshold
		if isAbove && !above {
			if !havePTS {
				return nil, fmt.Errorf("%s marker has no pts_time", valueKey)
			}
			markers = append(markers, Marker{PTS: pts})
		}
		above = isAbove
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return markers, nil
}

func ParseVideoMarkers(metadata string, threshold float64) ([]Marker, error) {
	return parseMarkers(metadata, "lavfi.signalstats.YAVG", threshold)
}

func ParseAudioMarkers(metadata string, threshold float64) ([]Marker, error) {
	return parseMarkers(metadata, "lavfi.astats.Overall.RMS_level", threshold)
}

func MatchMarkers(video, audio []Marker, maximumDistance time.Duration) (MarkerMatch, error) {
	if len(video) == 0 || len(audio) == 0 {
		return MarkerMatch{}, errors.New("video and audio markers are required")
	}
	offsets := make([]time.Duration, 0, len(video))
	for _, videoMarker := range video {
		bestDistance := time.Duration(math.MaxInt64)
		var bestOffset time.Duration
		for _, audioMarker := range audio {
			offset := videoMarker.PTS - audioMarker.PTS
			distance := offset
			if distance < 0 {
				distance = -distance
			}
			if distance < bestDistance {
				bestDistance = distance
				bestOffset = offset
			}
		}
		if bestDistance > maximumDistance {
			return MarkerMatch{}, fmt.Errorf("nearest marker is %s away (limit %s)", bestDistance, maximumDistance)
		}
		offsets = append(offsets, bestOffset)
	}
	result := MarkerMatch{FirstOffset: offsets[0], LastOffset: offsets[len(offsets)-1], Matched: len(offsets)}
	result.Drift = result.LastOffset - result.FirstOffset
	for _, offset := range offsets {
		absolute := offset
		if absolute < 0 {
			absolute = -absolute
		}
		if absolute > result.MaxAbsoluteOffset {
			result.MaxAbsoluteOffset = absolute
		}
	}
	return result, nil
}

func AspectWithinTolerance(sourceWidth, sourceHeight, activeWidth, activeHeight int, tolerance float64) bool {
	if sourceWidth <= 0 || sourceHeight <= 0 || activeWidth <= 0 || activeHeight <= 0 || tolerance < 0 {
		return false
	}
	source := float64(sourceWidth) / float64(sourceHeight)
	active := float64(activeWidth) / float64(activeHeight)
	return math.Abs(active-source)/source <= tolerance
}

func runMetadata(ffmpegPath, input, filter string, audio bool) (string, error) {
	args := []string{"-hide_banner", "-nostats", "-i", input}
	if audio {
		args = append(args, "-vn", "-af", filter)
	} else {
		args = append(args, "-an", "-vf", filter)
	}
	args = append(args, "-f", "null", "-")
	command := exec.Command(ffmpegPath, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("ffmpeg metadata probe: %w", err)
	}
	return string(output), nil
}

func main() {
	var input, ffmpegPath, outputPath string
	var maximumDistance time.Duration
	flag.StringVar(&input, "input", "", "recorded media file")
	flag.StringVar(&ffmpegPath, "ffmpeg", "ffmpeg", "FFmpeg executable")
	flag.StringVar(&outputPath, "out", "media-analysis.json", "analysis JSON path")
	flag.DurationVar(&maximumDistance, "max-distance", 200*time.Millisecond, "maximum marker pairing distance")
	flag.Parse()
	if input == "" {
		fmt.Fprintln(os.Stderr, "-input is required")
		os.Exit(2)
	}
	analysis := MediaAnalysis{Schema: 1, Status: "FAIL"}
	videoText, err := runMetadata(ffmpegPath, input, "signalstats,metadata=print", false)
	if err == nil {
		analysis.VideoMarkers, err = ParseVideoMarkers(videoText, 220)
	}
	if err == nil {
		var audioText string
		audioText, err = runMetadata(ffmpegPath, input, "astats=metadata=1:reset=1,ametadata=print", true)
		if err == nil {
			analysis.AudioMarkers, err = ParseAudioMarkers(audioText, -12)
		}
	}
	if err == nil {
		analysis.Match, err = MatchMarkers(analysis.VideoMarkers, analysis.AudioMarkers, maximumDistance)
	}
	if err != nil {
		analysis.Error = err.Error()
	} else {
		analysis.Status = "PASS"
	}
	data, marshalErr := json.MarshalIndent(analysis, "", "  ")
	if marshalErr != nil {
		fmt.Fprintln(os.Stderr, marshalErr)
		os.Exit(1)
	}
	if writeErr := os.WriteFile(outputPath, append(data, '\n'), 0o600); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

package video

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Result struct {
	OK              bool   `json:"ok"`
	Message         string `json:"message"`
	MP4             bool   `json:"mp4"`
	HLS             bool   `json:"hls"`
	Active          bool   `json:"active"`
	ProgressPercent int    `json:"progressPercent"`
	ProgressText    string `json:"progressText"`
}

type QueueItem struct {
	ID              string    `json:"id"`
	MediaID         string    `json:"mediaID"`
	Title           string    `json:"title"`
	Kind            string    `json:"kind"`
	Status          string    `json:"status"`
	Message         string    `json:"message"`
	ProgressPercent int       `json:"progressPercent"`
	ProgressText    string    `json:"progressText"`
	Quality         string    `json:"quality"`
	CreatedAt       time.Time `json:"createdAt"`
	StartedAt       time.Time `json:"startedAt,omitempty"`
	FinishedAt      time.Time `json:"finishedAt,omitempty"`
}

const (
	MP4File      = "current.mp4"
	HLSPlaylist  = "current.m3u8"
	HLSSegment   = "current0.ts"
	FrameRate    = "10"
	ClipDuration = "10"
)

var activeHLS sync.Map
var queues sync.Map

type activeJob struct {
	Preset       QualityPreset
	Cancel       context.CancelFunc
	Done         chan struct{}
	TotalSeconds int
	QueueJob     *queueJob
	MediaID      string
}

type queueState struct {
	mu      sync.Mutex
	items   []*queueJob
	running bool
}

type queueJob struct {
	QueueItem
	OutDir       string
	SourcePath   string
	ArtworkPath  string // for SoundCloud artwork
	Mode         string
	Audio        *AudioRenderInput
	Preset       QualityPreset
	Cancel       context.CancelFunc
	Done         chan struct{}
	TotalSeconds int
	Preempted    bool
}

type QualityPreset struct {
	Mode         string `json:"mode"`
	Effective    string `json:"effective"`
	Height       int    `json:"height"`
	VideoBitrate string `json:"videoBitrate"`
	MaxRate      string `json:"maxRate"`
	BufferSize   string `json:"bufferSize"`
	AudioBitrate string `json:"audioBitrate"`
	CRF          int    `json:"crf"`
	NetworkMbps  int    `json:"networkMbps"`
	UploadMbps   int    `json:"uploadMbps"`
	BitrateOnly  bool   `json:"bitrateOnly"`
}

func PublishStillImage(imagePath, outDir string, preset QualityPreset) Result {
	return PublishStillImageForID(imagePath, outDir, "", preset)
}

func PublishStillImageForID(imagePath, outDir, id string, preset QualityPreset) Result {
	stopActive(outDir)
	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		removeGenerated(outDir)
		return Result{Message: err.Error()}
	}
	if preset.Height <= 0 {
		preset = ResolveQuality("auto", 0)
	}

	removeGenerated(outDir)
	selected := SelectVideoEncoder(context.Background(), ffmpeg, EncoderStandard)
	mp4Path := filepath.Join(outDir, MP4File)
	mp4Err := runVideoEncodeWithFallback(context.Background(), selected, func() { _ = os.Remove(mp4Path) }, func(encoder VideoEncoderProfile) error {
		return run(ffmpeg, stillMP4ArgsWithEncoder(imagePath, mp4Path, preset, encoder)...)
	})
	hlsErr := runVideoEncodeWithFallback(context.Background(), selected, func() { removeHLSForID(outDir, id) }, func(encoder VideoEncoderProfile) error {
		return runInDir(outDir, ffmpeg, stillHLSArgsWithEncoderType(imagePath, id, preset, encoder, "vod")...)
	})

	result := Result{
		MP4: fileExists(filepath.Join(outDir, MP4File)),
		HLS: fileExists(filepath.Join(outDir, playlistName(id))) && hlsSegmentExists(outDir),
	}
	result.OK = result.MP4 || result.HLS
	switch {
	case mp4Err == nil && hlsErr == nil:
		result.Message = "VRChat video outputs generated at " + preset.Effective + "p."
	case result.OK:
		result.Message = fmt.Sprintf("Some VRChat video outputs generated. MP4: %v, HLS: %v", errorText(mp4Err), errorText(hlsErr))
	default:
		result.Message = fmt.Sprintf("FFmpeg failed. MP4: %v, HLS: %v", errorText(mp4Err), errorText(hlsErr))
	}
	return result
}

func stillMP4ArgsWithEncoder(imagePath, outputPath string, preset QualityPreset, encoder VideoEncoderProfile) []string {
	args := []string{
		"-y",
		"-loop", "1",
		"-t", ClipDuration,
		"-i", imagePath,
		"-f", "lavfi",
		"-t", ClipDuration,
		"-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
	}
	args = append(args, encoder.FFmpegArgs(preset, "veryfast")...)
	if !encoder.Hardware {
		args = append(args,
			"-b:v", preset.VideoBitrate,
			"-maxrate", preset.MaxRate,
			"-bufsize", preset.BufferSize,
		)
	}
	return append(args,
		"-vf", "fps="+FrameRate+",scale=w='min(1920,iw)':h='min("+strconv.Itoa(preset.Height)+",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,pad=ceil(iw/2)*2:ceil(ih/2)*2",
		"-r", FrameRate,
		"-c:a", "aac",
		"-b:a", "64k",
		"-shortest",
		"-movflags", "+faststart",
		outputPath,
	)
}

func stillHLSArgsWithEncoder(imagePath, id string, preset QualityPreset, encoder VideoEncoderProfile) []string {
	return stillHLSArgsWithEncoderType(imagePath, id, preset, encoder, "event")
}

func stillHLSArgsWithEncoderType(imagePath, id string, preset QualityPreset, encoder VideoEncoderProfile, playlistType string) []string {
	args := []string{
		"-y",
		"-loop", "1",
		"-t", ClipDuration,
		"-i", imagePath,
		"-f", "lavfi",
		"-t", ClipDuration,
		"-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
	}
	args = append(args, encoder.FFmpegArgs(preset, "veryfast")...)
	return append(args,
		"-vf", "fps="+FrameRate+",scale=w='min(1920,iw)':h='min("+strconv.Itoa(preset.Height)+",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,pad=ceil(iw/2)*2:ceil(ih/2)*2",
		"-r", FrameRate,
		"-g", "20",
		"-keyint_min", "20",
		"-sc_threshold", "0",
		"-c:a", "aac",
		"-b:a", "64k",
		"-shortest",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_playlist_type", playlistType,
		"-hls_segment_filename", segmentPattern(id),
		playlistName(id),
	)
}

func PublishStillImageAsyncForID(imagePath, outDir, id string, preset QualityPreset) {
	EnqueueStillImageForID(imagePath, outDir, id, filepath.Base(imagePath), preset)
}

func PublishUploadedVideoAsync(sourcePath, outDir string, preset QualityPreset) {
	PublishUploadedVideoAsyncForID(sourcePath, outDir, "", preset)
}

func PublishUploadedVideoAsyncForID(sourcePath, outDir, id string, preset QualityPreset) {
	EnqueueUploadedVideoForID(sourcePath, outDir, id, filepath.Base(sourcePath), preset, 0)
}

// downloadVideoURL downloads a video from a URL using yt-dlp.
// It handles regular (non-SoundCloud) URLs with the standard video format
// selector and produces an MP4 file prefixed "yt-dlp-source". The returned
// name is the video's title (from yt-dlp's info JSON) so history/favorites
// show a meaningful title instead of the generic "yt-dlp-source.mp4"; it
// falls back to the file name when the title is unavailable. The returned
// thumbnailPath is the yt-dlp-written thumbnail image (if any).
func errorText(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func queueID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func fallbackTitle(title, fallback string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return fallback
	}
	return title
}

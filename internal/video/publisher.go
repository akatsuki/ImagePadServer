package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
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

func ResolveQuality(mode string, networkMbps int) QualityPreset {
	return ResolveQualityForUpload(mode, networkMbps, 0)
}

func ResolveQualityForUpload(mode string, downloadMbps, uploadMbps int) QualityPreset {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	decisionMbps := uploadMbps
	if decisionMbps <= 0 {
		decisionMbps = downloadMbps
	}
	effective := mode
	if mode == "auto" {
		switch {
		case decisionMbps >= 12:
			effective = "1080"
		case decisionMbps >= 5:
			effective = "720"
		default:
			effective = "360"
		}
	}
	preset := QualityPreset{
		Mode:        mode,
		Effective:   effective,
		Height:      720,
		NetworkMbps: downloadMbps,
		UploadMbps:  uploadMbps,
	}
	switch effective {
	// CRF is +2 vs the older presets: the encoder efficiency upgrades (NVENC p6
	// + AQ + B-frames, AMF quality + VBAQ, libx264 medium) buy back the quality,
	// so the higher CRF lands at a similar look for a smaller file. CRF is not
	// used by the OBS low-latency path, so live streaming quality is unchanged.
	case "1080":
		preset.Height = 1080
		preset.VideoBitrate = "4500k"
		preset.MaxRate = "5200k"
		preset.BufferSize = "9000k"
		preset.AudioBitrate = "160k"
		preset.CRF = 26
	case "360":
		preset.Height = 360
		preset.VideoBitrate = "900k"
		preset.MaxRate = "1100k"
		preset.BufferSize = "1800k"
		preset.AudioBitrate = "96k"
		preset.CRF = 32
	default:
		preset.Effective = "720"
		preset.Height = 720
		preset.VideoBitrate = "2500k"
		preset.MaxRate = "3000k"
		preset.BufferSize = "5000k"
		preset.AudioBitrate = "128k"
		preset.CRF = 29
	}
	return preset
}

// ResolveQualityForMusic returns a preset tuned for the music visualizer path
// (SoundCloud / uploaded audio). The output is mostly a static background plus
// a small animated waveform, so it compresses far better than camera/game
// footage. CRF is raised and the bitrate ceiling is lowered to keep songs
// small, but we avoid pushing it so hard that the waveform area becomes
// blocky.
func ResolveQualityForMusic(mode string, downloadMbps, uploadMbps int) QualityPreset {
	preset := ResolveQualityForUpload(mode, downloadMbps, uploadMbps)
	preset.CRF = clampInt(preset.CRF+2, 18, 40)
	// Cap peaks at ~30% of the uploaded-video ceiling. Buffer is 40% so short
	// waveform spikes do not stutter the rate controller. Spatial AQ is disabled
	// in the NVENC static-content path so the moving waveform is not starved of
	// bits in favor of the flat background.
	if preset.MaxRate != "" {
		preset.MaxRate = scaleBitrate(preset.MaxRate, 0.30)
	}
	if preset.BufferSize != "" {
		preset.BufferSize = scaleBitrate(preset.BufferSize, 0.40)
	}
	return preset
}

// scaleBitrate multiplies a bitrate string like "3000k" by factor, preserving
// the unit suffix. Empty or unparseable values are returned unchanged.
func scaleBitrate(s string, factor float64) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	unit := ""
	num := s
	if last := s[len(s)-1]; last < '0' || last > '9' {
		unit = s[len(s)-1:]
		num = s[:len(s)-1]
	}
	v, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		return s
	}
	return strconv.Itoa(int(float64(v)*factor)) + unit
}

func BitrateOnlyPreset(requested, active QualityPreset) QualityPreset {
	if active.Height <= 0 || requested.Height <= 0 {
		return requested
	}
	requested.Height = active.Height
	requested.Effective = active.Effective
	requested.BitrateOnly = true
	return requested
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

func CurrentStatus(outDir string) Result {
	return CurrentStatusForID(outDir, "")
}

func CurrentStatusForID(outDir, id string) Result {
	mp4 := fileExists(filepath.Join(outDir, MP4File))
	hls := hlsPlaylistExistsForID(outDir, id) && hlsSegmentExistsForID(outDir, id)
	active := isActiveForID(outDir, id)
	pending := isPendingForID(outDir, id)
	result := Result{
		OK:     mp4 || hls,
		MP4:    mp4,
		HLS:    hls,
		Active: active || pending,
	}
	if active && hls {
		applyProgress(outDir, &result)
		result.Message = "HLS conversion is streaming."
		return result
	}
	if active {
		applyProgress(outDir, &result)
		result.Message = "HLS conversion is starting."
		return result
	}
	if pending {
		result.Message = "HLS conversion is waiting."
		return result
	}
	if result.OK {
		result.Message = "VRChat video outputs are available."
		return result
	}
	if _, err := ffmpegPath(); err != nil {
		result.Message = "FFmpeg not found. Turn on video player support to download it, set IMAGEPAD_FFMPEG, or add ffmpeg to PATH."
		return result
	}
	result.Message = "VRChat video outputs have not been generated yet."
	return result
}

func applyProgress(outDir string, result *Result) {
	result.ProgressText = "変換中"
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return
	}
	job, ok := active.(*activeJob)
	if !ok || job.TotalSeconds <= 0 {
		count := hlsSegmentCount(outDir)
		if job != nil && job.QueueJob != nil {
			count = hlsSegmentCountForID(outDir, job.QueueJob.MediaID)
		}
		if count > 0 {
			result.ProgressText = strconv.Itoa(count) + " segments generated"
		}
		return
	}
	id := ""
	if job.QueueJob != nil {
		id = job.QueueJob.MediaID
	}
	percent, seconds, ok := hlsProgress(outDir, id, job.TotalSeconds)
	if !ok {
		return
	}
	result.ProgressPercent = percent
	result.ProgressText = fmt.Sprintf("%d%% (%d / %d sec)", percent, seconds, job.TotalSeconds)
}

func PublishUploadedVideoAsync(sourcePath, outDir string, preset QualityPreset) {
	PublishUploadedVideoAsyncForID(sourcePath, outDir, "", preset)
}

func PublishUploadedVideoAsyncForID(sourcePath, outDir, id string, preset QualityPreset) {
	EnqueueUploadedVideoForID(sourcePath, outDir, id, filepath.Base(sourcePath), preset, 0)
}

func EnqueueStillImageForID(imagePath, outDir, id, title string, preset QualityPreset) string {
	return enqueueConversion(&queueJob{
		QueueItem: QueueItem{
			ID:        queueID(),
			MediaID:   id,
			Title:     fallbackTitle(title, "画像"),
			Kind:      "image",
			Status:    "pending",
			Message:   "変換待ち",
			Quality:   preset.Effective,
			CreatedAt: time.Now(),
		},
		OutDir:       outDir,
		SourcePath:   imagePath,
		Mode:         "still",
		Preset:       preset,
		TotalSeconds: clipDurationSeconds(),
	})
}

// EnqueueUploadedVideoForID queues an HLS conversion for an uploaded video.
// totalSeconds is the source video's duration in seconds (from ffprobe); when
// it is known the queue status surfaces a segment-based "X% (N / M sec)"
// progress like music mode, instead of only a raw segment count.
func EnqueueUploadedVideoForID(sourcePath, outDir, id, title string, preset QualityPreset, totalSeconds int) string {
	return enqueueConversion(&queueJob{
		QueueItem: QueueItem{
			ID:        queueID(),
			MediaID:   id,
			Title:     fallbackTitle(title, "動画"),
			Kind:      "video",
			Status:    "pending",
			Message:   "変換待ち",
			Quality:   preset.Effective,
			CreatedAt: time.Now(),
		},
		OutDir:       outDir,
		SourcePath:   sourcePath,
		Mode:         "uploaded",
		Preset:       preset,
		TotalSeconds: totalSeconds,
	})
}

func EnqueueSoundCloudForID(audioPath, artworkPath, outDir, id, title string, preset QualityPreset, totalSeconds int) string {
	return enqueueConversion(&queueJob{
		QueueItem: QueueItem{
			ID:        queueID(),
			MediaID:   id,
			Title:     fallbackTitle(title, "SoundCloud"),
			Kind:      "soundcloud",
			Status:    "pending",
			Message:   "変換待ち",
			Quality:   preset.Effective,
			CreatedAt: time.Now(),
		},
		OutDir:       outDir,
		SourcePath:   audioPath,
		ArtworkPath:  artworkPath,
		Mode:         "soundcloud",
		Preset:       preset,
		TotalSeconds: totalSeconds,
	})
}

func QueueStatus(outDir string) []QueueItem {
	state := queueFor(outDir)
	state.mu.Lock()
	defer state.mu.Unlock()
	items := make([]QueueItem, 0, len(state.items))
	for i := len(state.items) - 1; i >= 0; i-- {
		job := state.items[i]
		if job == nil {
			continue
		}
		item := job.QueueItem
		if job.Status == "running" {
			applyQueueProgressLocked(job, &item)
		}
		items = append(items, item)
	}
	return items
}

func GeneratedFiles(outDir, id string) []string {
	var files []string
	playlist := filepath.Join(outDir, playlistName(id))
	if fileExists(playlist) {
		files = append(files, playlist)
	}
	pattern := filepath.Join(outDir, "current*.ts")
	if id != "" {
		pattern = filepath.Join(outDir, "current-"+safeID(id)+"-*.ts")
	}
	matches, _ := filepath.Glob(pattern)
	files = append(files, matches...)
	mp4 := filepath.Join(outDir, MP4File)
	if fileExists(mp4) {
		files = append(files, mp4)
	}
	return files
}

func removeHLSForID(outDir, id string) {
	_ = os.Remove(filepath.Join(outDir, playlistName(id)))
	pattern := filepath.Join(outDir, "current*.ts")
	if id != "" {
		pattern = filepath.Join(outDir, "current-"+safeID(id)+"-*.ts")
	}
	matches, _ := filepath.Glob(pattern)
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func enqueueConversion(job *queueJob) string {
	if job.Preset.Height <= 0 {
		job.Preset = ResolveQuality("auto", 0)
		job.Quality = job.Preset.Effective
	}
	state := queueFor(job.OutDir)
	state.mu.Lock()
	state.items = append([]*queueJob{job}, state.items...)
	state.preemptRunningLocked(job)
	state.pruneLocked(30)
	if !state.running {
		state.running = true
		go state.run(job.OutDir)
	}
	state.mu.Unlock()
	return job.ID
}

func queueFor(outDir string) *queueState {
	value, _ := queues.LoadOrStore(outDir, &queueState{})
	return value.(*queueState)
}

func (s *queueState) run(outDir string) {
	for {
		job := s.nextPending()
		if job == nil {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			return
		}
		runQueueJob(job)
	}
}

func (s *queueState) nextPending() *queueJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < len(s.items); i++ {
		if s.items[i].Status == "pending" {
			return s.items[i]
		}
	}
	return nil
}

func (s *queueState) preemptRunningLocked(priority *queueJob) {
	for _, job := range s.items {
		if job == nil || job == priority || job.Status != "running" {
			continue
		}
		job.Preempted = true
		job.Status = "pending"
		job.Message = "新しいジョブを優先するため待機中"
		job.ProgressText = ""
		job.ProgressPercent = 0
		job.StartedAt = time.Time{}
		job.FinishedAt = time.Time{}
		if job.Cancel != nil {
			job.Cancel()
		}
		return
	}
}

func (s *queueState) pruneLocked(limit int) {
	if limit <= 0 || len(s.items) <= limit {
		return
	}
	kept := s.items[:0]
	finished := 0
	for _, job := range s.items {
		if job.Status == "pending" || job.Status == "running" {
			kept = append(kept, job)
			continue
		}
		finished++
		if finished <= limit {
			kept = append(kept, job)
		}
	}
	s.items = kept
}

func runQueueJob(job *queueJob) {
	ctx, cancel := context.WithCancel(context.Background())
	job.Cancel = cancel
	job.Done = make(chan struct{})
	job.Preempted = false
	job.Status = "running"
	job.Message = "変換中"
	job.StartedAt = time.Now()
	job.ProgressText = "変換中"
	active := &activeJob{
		Preset:       job.Preset,
		Cancel:       cancel,
		Done:         job.Done,
		TotalSeconds: job.TotalSeconds,
		QueueJob:     job,
	}
	activeHLS.Store(job.OutDir, active)
	defer func() {
		cancel()
		close(job.Done)
		if current, ok := activeHLS.Load(job.OutDir); ok && current == active {
			activeHLS.Delete(job.OutDir)
		}
	}()

	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		finishQueueJob(job, "error", err.Error())
		return
	}
	if job.Preset.Height <= 0 {
		job.Preset = ResolveQuality("auto", 0)
		active.Preset = job.Preset
	}
	switch job.Mode {
	case "still":
		err = runStillHLS(ctx, job.OutDir, ffmpeg, job.SourcePath, job.MediaID, job.Preset)
	case "soundcloud":
		err = runSoundCloudHLS(ctx, job.OutDir, ffmpeg, job.SourcePath, job.ArtworkPath, job.MediaID, job.Preset)
	case "audio":
		err = RunAudioVisualizerHLS(ctx, job.OutDir, ffmpeg, *job.Audio, job.MediaID, job.Preset)
	default:
		err = runUploadedHLS(ctx, job.OutDir, ffmpeg, job.SourcePath, job.MediaID, job.Preset)
	}
	if err != nil {
		if ctx.Err() != nil {
			if job.Preempted {
				job.Preempted = false
				job.Status = "pending"
				job.Message = "待機中"
				job.ProgressPercent = 0
				job.ProgressText = ""
				job.StartedAt = time.Time{}
				return
			}
			finishQueueJob(job, "canceled", "キャンセルしました")
			return
		}
		finishQueueJob(job, "error", err.Error())
		return
	}
	if err := finalizeHLSPlaylist(job.OutDir, job.MediaID); err != nil {
		finishQueueJob(job, "error", err.Error())
		return
	}
	job.ProgressPercent = 100
	job.ProgressText = "100%"
	finishQueueJob(job, "done", "変換完了")
}

func finishQueueJob(job *queueJob, status, message string) {
	job.Status = status
	job.Message = message
	job.FinishedAt = time.Now()
}

func runStillHLS(ctx context.Context, outDir, ffmpeg, imagePath, id string, preset QualityPreset) error {
	selected := SelectVideoEncoder(ctx, ffmpeg, EncoderStandard)
	return runVideoEncodeWithFallback(ctx, selected, func() { removeHLSForID(outDir, id) }, func(encoder VideoEncoderProfile) error {
		return runInDirContext(ctx, outDir, ffmpeg, stillHLSArgsWithEncoder(imagePath, id, preset, encoder)...)
	})
}

func runUploadedHLS(ctx context.Context, outDir, ffmpeg, sourcePath, id string, preset QualityPreset) error {
	selected := SelectVideoEncoder(ctx, ffmpeg, EncoderStandard)
	return runVideoEncodeWithFallback(ctx, selected, func() { removeHLSForID(outDir, id) }, func(encoder VideoEncoderProfile) error {
		vod := preset
		if score, err := ProbeMotionScore(sourcePath); err == nil {
			vod = AdaptPresetForContent(vod, score)
		}
		if sourceBitrate, err := ProbeSourceBitrate(sourcePath); err == nil {
			vod = capPresetToSourceBitrate(vod, sourceBitrate)
		}
		return runInDirContext(ctx, outDir, ffmpeg, uploadedHLSArgsWithEncoder(sourcePath, id, vod, encoder)...)
	})
}

func uploadedHLSArgsWithEncoder(sourcePath, id string, preset QualityPreset, encoder VideoEncoderProfile) []string {
	// The source is a finite file on disk (uploaded, or fully downloaded from a
	// URL), so do NOT pace input to real time (-re): like the music visualizer,
	// encode as fast as the machine allows — the HLS event playlist still serves
	// chase playback while it races ahead. This is also what lets GPU/CPU speed
	// actually matter for throughput. The "medium" libx264 preset (CPU fallback)
	// then trades the freed-up time for better compression.
	args := []string{
		"-y",
		"-i", sourcePath,
		"-map", "0:v:0",
		"-map", "0:a:0?",
	}
	// Trim the NVENC bitrate ceiling ~17% for this VOD path only (a copy of the
	// preset), so complex content that would otherwise clip at the full ceiling
	// gets smaller. The OBS live path reads the shared preset directly, so its
	// MaxRate is left untouched.
	vod := preset
	vod.MaxRate = scaleBitrate(preset.MaxRate, 0.83)
	vod.BufferSize = scaleBitrate(preset.BufferSize, 0.83)
	args = append(args, encoder.FFmpegArgs(vod, "medium")...)
	return append(args,
		"-vf", "scale=w='min(1920,iw)':h='min("+strconv.Itoa(preset.Height)+",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
		"-g", "60",
		"-keyint_min", "60",
		"-sc_threshold", "0",
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ac", "2",
		"-ar", "48000",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segmentPattern(id),
		playlistName(id),
	)
}

func applyQueueProgressLocked(job *queueJob, item *QueueItem) {
	if job.TotalSeconds <= 0 {
		count := hlsSegmentCountForID(job.OutDir, job.MediaID)
		if count > 0 {
			item.ProgressText = strconv.Itoa(count) + " segments generated"
		}
		return
	}
	percent, seconds, ok := hlsProgress(job.OutDir, job.MediaID, job.TotalSeconds)
	if !ok {
		return
	}
	item.ProgressPercent = percent
	item.ProgressText = fmt.Sprintf("%d%% (%d / %d sec)", percent, seconds, job.TotalSeconds)
}

func hlsProgress(outDir, id string, totalSeconds int) (percent, completedSeconds int, ok bool) {
	if totalSeconds <= 0 {
		return 0, 0, false
	}
	completed, ok := hlsCompletedDurationForID(outDir, id)
	if !ok {
		return 0, 0, false
	}
	percent = int(math.Round(completed / float64(totalSeconds) * 100))
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	if completed > float64(totalSeconds) {
		completed = float64(totalSeconds)
	}
	completedSeconds = int(math.Round(completed))
	return percent, completedSeconds, true
}

func hlsCompletedDurationForID(outDir, id string) (float64, bool) {
	data, err := os.ReadFile(filepath.Join(outDir, playlistName(id)))
	if err != nil {
		return 0, false
	}
	var total float64
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXTINF:") {
			continue
		}
		value := strings.TrimPrefix(line, "#EXTINF:")
		if comma := strings.IndexByte(value, ','); comma >= 0 {
			value = value[:comma]
		}
		seconds, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if parseErr != nil || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			continue
		}
		total += seconds
		found = true
	}
	return total, found
}

func cancelQueue(outDir string) {
	value, ok := queues.Load(outDir)
	if !ok {
		return
	}
	state, ok := value.(*queueState)
	if !ok {
		return
	}
	state.mu.Lock()
	for _, job := range state.items {
		if job == nil {
			continue
		}
		switch job.Status {
		case "pending":
			job.Status = "canceled"
			job.Message = "キャンセルしました"
			job.FinishedAt = time.Now()
		case "running":
			if job.Cancel != nil {
				job.Cancel()
			}
		}
	}
	state.mu.Unlock()
}

// downloadVideoURL downloads a video from a URL using yt-dlp.
// It handles regular (non-SoundCloud) URLs with the standard video format
// selector and produces an MP4 file prefixed "yt-dlp-source". The returned
// name is the video's title (from yt-dlp's info JSON) so history/favorites
// show a meaningful title instead of the generic "yt-dlp-source.mp4"; it
// falls back to the file name when the title is unavailable. The returned
// thumbnailPath is the yt-dlp-written thumbnail image (if any).
func downloadVideoURL(rawURL, outDir string) (sourcePath, name, thumbnailPath string, err error) {
	exe, err := EnsureYTDLP()
	if err != nil {
		return "", "", "", err
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return "", "", "", err
	}
	removeYTDLPFiles(outDir)
	target := filepath.Join(outDir, "yt-dlp-source.%(ext)s")
	infoPath := filepath.Join(outDir, "yt-dlp-source.info.json")
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--max-filesize", "2G",
		"-f", "bv*[height<=1080]+ba/b[height<=1080]/best[height<=1080]/best",
		"--merge-output-format", "mp4",
		// Download DASH/HLS fragments in parallel to work around per-connection
		// throttling (notably YouTube), the same speedup music mode uses.
		"--concurrent-fragments", "4",
		"--write-info-json",
		"--write-thumbnail",
		"-o", target,
	}
	args = append(args, ffmpegLocationArgs()...)
	if err := runYTDLPDownload(exe, rawURL, args); err != nil {
		return "", "", "", err
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "yt-dlp-source.*"))
	var thumbPaths []string
	for _, match := range matches {
		ext := strings.ToLower(filepath.Ext(match))
		if ext == ".json" {
			continue
		}
		if info, statErr := os.Stat(match); statErr != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp", ".bmp", ".gif":
			thumbPaths = append(thumbPaths, match)
		default:
			if sourcePath == "" {
				sourcePath = match
			}
		}
	}
	if sourcePath == "" {
		return "", "", "", errors.New("yt-dlp did not produce a video file")
	}
	name = "yt-dlp-source" + filepath.Ext(sourcePath)
	if data, readErr := os.ReadFile(infoPath); readErr == nil {
		if meta, parseErr := ParseMusicInfoJSON(data); parseErr == nil && strings.TrimSpace(meta.Title) != "" {
			name = meta.Title
		}
	}
	if len(thumbPaths) > 0 {
		thumbnailPath = thumbPaths[0]
	}
	return sourcePath, name, thumbnailPath, nil
}

// DownloadURL downloads media from a URL and returns the source path and
// display name. It is a compatibility wrapper around DownloadMediaURL.
// For SoundCloud URLs, artwork is silently discarded. Use DownloadMediaURL
// if you need the full DownloadedMedia result.
func DownloadURL(rawURL, outDir string) (string, string, error) {
	media, err := DownloadMediaURL(rawURL, outDir)
	if err != nil {
		return "", "", err
	}
	return media.SourcePath, media.Name, nil
}

func GenerateThumbnail(sourcePath, outPath string) error {
	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0700); err != nil {
		return err
	}
	var lastErr error
	for _, seek := range []string{"00:00:01", "00:00:00"} {
		args := []string{"-y"}
		if seek != "" {
			args = append(args, "-ss", seek)
		}
		args = append(args,
			"-i", sourcePath,
			"-frames:v", "1",
			"-vf", "scale='min(480,iw)':-2",
			"-q:v", "4",
			outPath,
		)
		if err := run(ffmpeg, args...); err == nil {
			if stat, statErr := os.Stat(outPath); statErr == nil && stat.Size() > 0 {
				return nil
			}
			lastErr = errors.New("thumbnail output was empty")
			continue
		} else {
			lastErr = err
		}
	}
	return lastErr
}

type NetworkMeasurement struct {
	DownloadMbps int `json:"downloadMbps"`
	UploadMbps   int `json:"uploadMbps"`
}

func MeasureNetwork() NetworkMeasurement {
	return NetworkMeasurement{
		UploadMbps: MeasureNetworkUploadMbps(),
	}
}

func MeasureNetworkMbps() int {
	return MeasureNetworkDownloadMbps()
}

func MeasureNetworkDownloadMbps() int {
	const bytesToMeasure = 10_000_000
	rawURL := "https://speed.cloudflare.com/__down?bytes=" + strconv.Itoa(bytesToMeasure)
	client := http.Client{Timeout: 30 * time.Second}
	start := time.Now()
	resp, err := client.Get(rawURL)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}
	written, err := io.Copy(io.Discard, io.LimitReader(resp.Body, bytesToMeasure))
	return mbpsFromBytes(written, start, err)
}

func MeasureNetworkUploadMbps() int {
	const bytesToMeasure = 40_000_000
	client := http.Client{Timeout: 90 * time.Second}
	reader := io.LimitReader(zeroReader{}, bytesToMeasure)
	req, err := http.NewRequest(http.MethodPost, "https://speed.cloudflare.com/__up", reader)
	if err != nil {
		return 0
	}
	req.ContentLength = bytesToMeasure
	req.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}
	return mbpsFromBytes(bytesToMeasure, start, nil)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func mbpsFromBytes(written int64, start time.Time, err error) int {
	if err != nil || written <= 0 {
		return 0
	}
	seconds := time.Since(start).Seconds()
	if seconds <= 0 {
		return 0
	}
	mbps := int(math.Round(float64(written*8) / seconds / 1000 / 1000))
	if mbps < 1 {
		return 1
	}
	return mbps
}

func RemoveGenerated(outDir string) {
	stopActive(outDir)
	removeGenerated(outDir)
}

func CancelQueue(outDir string) {
	cancelQueue(outDir)
	stopActive(outDir)
}

// CancelConversion stops and discards any pending or running conversion job for
// the given media id so it can never be resumed. Unlike preemption (which sends
// a running job back to "pending" to be retried later), this is used when the
// published media is being replaced: the old job must not come back and
// regenerate stale HLS output after the new media has taken over.
func CancelConversion(outDir, id string) {
	if id == "" {
		return
	}
	value, ok := queues.Load(outDir)
	if !ok {
		return
	}
	state, ok := value.(*queueState)
	if !ok {
		return
	}
	state.mu.Lock()
	for _, job := range state.items {
		if job == nil || job.MediaID != id {
			continue
		}
		switch job.Status {
		case "pending", "running":
			job.Preempted = false
			job.Status = "canceled"
			job.Message = "差し替えのため中止しました"
			job.FinishedAt = time.Now()
			if job.Cancel != nil {
				job.Cancel()
			}
		}
	}
	state.mu.Unlock()
}

func PlaylistName(id string) string {
	return playlistName(id)
}

func SegmentPattern(id string) string {
	return segmentPattern(id)
}

func FinalizeHLSPlaylist(outDir, id string) error {
	return finalizeHLSPlaylist(outDir, id)
}

func BeginExternalHLS(outDir, id string, preset QualityPreset, cancel context.CancelFunc, done chan struct{}) {
	activeHLS.Store(outDir, &activeJob{
		Preset:  preset,
		Cancel:  cancel,
		Done:    done,
		MediaID: id,
	})
}

func EndExternalHLS(outDir string, done chan struct{}) {
	if current, ok := activeHLS.Load(outDir); ok {
		if job, ok := current.(*activeJob); ok && job != nil && job.Done == done {
			activeHLS.Delete(outDir)
		}
	}
}

func isActive(outDir string) bool {
	return isActiveForID(outDir, "")
}

func isActiveForID(outDir, id string) bool {
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return false
	}
	if value, ok := active.(bool); ok {
		return value && id == ""
	}
	job, ok := active.(*activeJob)
	if !ok || job == nil {
		return false
	}
	return id == "" || (job.QueueJob != nil && job.QueueJob.MediaID == id) || (job.MediaID != "" && job.MediaID == id)
}

func isPendingForID(outDir, id string) bool {
	if id == "" {
		return false
	}
	value, ok := queues.Load(outDir)
	if !ok {
		return false
	}
	state, ok := value.(*queueState)
	if !ok {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, job := range state.items {
		if job == nil || job.MediaID != id {
			continue
		}
		if job.Status == "pending" || job.Status == "running" {
			return true
		}
	}
	return false
}

func ActiveQuality(outDir string) (QualityPreset, bool) {
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return QualityPreset{}, false
	}
	if preset, ok := active.(QualityPreset); ok {
		return preset, true
	}
	if job, ok := active.(*activeJob); ok && job != nil {
		return job.Preset, true
	}
	return QualityPreset{}, false
}

func stopActive(outDir string) {
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return
	}
	if job, ok := active.(*activeJob); ok && job != nil && job.Cancel != nil {
		job.Cancel()
		if job.Done != nil {
			select {
			case <-job.Done:
			case <-time.After(2 * time.Second):
			}
		}
	}
	activeHLS.Delete(outDir)
}

func removeGenerated(outDir string) {
	_ = os.Remove(filepath.Join(outDir, MP4File))
	_ = os.Remove(filepath.Join(outDir, HLSPlaylist))
	matches, _ := filepath.Glob(filepath.Join(outDir, "current-*.m3u8"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	matches, _ = filepath.Glob(filepath.Join(outDir, "current*.ts"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	matches, _ = filepath.Glob(filepath.Join(outDir, "current*.tmp"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func removeYTDLPFiles(outDir string) {
	matches, _ := filepath.Glob(filepath.Join(outDir, "yt-dlp-source.*"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	matches, _ = filepath.Glob(filepath.Join(outDir, "yt-dlp-source.*.part"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func finalizeHLSPlaylist(outDir, id string) error {
	path := filepath.Join(outDir, playlistName(id))
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	if strings.Contains(text, "#EXT-X-PLAYLIST-TYPE:EVENT") {
		text = strings.Replace(text, "#EXT-X-PLAYLIST-TYPE:EVENT", "#EXT-X-PLAYLIST-TYPE:VOD", 1)
	}
	if !strings.Contains(text, "#EXT-X-ENDLIST") {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "#EXT-X-ENDLIST\n"
	}
	return os.WriteFile(path, []byte(text), 0600)
}

func hlsSegmentExists(outDir string) bool {
	matches, _ := filepath.Glob(filepath.Join(outDir, "current*.ts"))
	return len(matches) > 0
}

func hlsSegmentExistsForID(outDir, id string) bool {
	if id == "" {
		return hlsSegmentExists(outDir)
	}
	return hlsSegmentCountForID(outDir, id) > 0
}

func hlsSegmentCount(outDir string) int {
	matches, _ := filepath.Glob(filepath.Join(outDir, "current*.ts"))
	return len(matches)
}

func hlsSegmentCountForID(outDir, id string) int {
	if id == "" {
		return hlsSegmentCount(outDir)
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "current-"+safeID(id)+"-*.ts"))
	return len(matches)
}

func hlsPlaylistExists(outDir string) bool {
	matches, _ := filepath.Glob(filepath.Join(outDir, "current*.m3u8"))
	return len(matches) > 0
}

func hlsPlaylistExistsForID(outDir, id string) bool {
	if id == "" {
		return hlsPlaylistExists(outDir)
	}
	return fileExists(filepath.Join(outDir, playlistName(id)))
}

func playlistName(id string) string {
	if id == "" {
		return HLSPlaylist
	}
	return "current-" + safeID(id) + ".m3u8"
}

func segmentPattern(id string) string {
	prefix := "current"
	if id != "" {
		prefix = "current-" + safeID(id)
	}
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "-%d.ts"
}

func clipDurationSeconds() int {
	seconds, err := strconv.Atoi(ClipDuration)
	if err != nil || seconds <= 0 {
		return 0
	}
	return seconds
}

func safeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "current"
	}
	return b.String()
}

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

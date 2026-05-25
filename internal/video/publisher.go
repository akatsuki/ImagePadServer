package video

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/settings"
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

	ffmpegDownloadURL    = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
	ffmpegSHA256URL      = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip.sha256"
	ytdlpDownloadURL     = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"
	ytdlpMacOSURL        = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_macos"
	ytdlpSHA256SumsURL   = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/SHA2-256SUMS"
	ffmpegDownloadSHA256 = ""
	ytdlpDownloadSHA256  = ""
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
	Mode         string
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
	case "1080":
		preset.Height = 1080
		preset.VideoBitrate = "4500k"
		preset.MaxRate = "5200k"
		preset.BufferSize = "9000k"
		preset.AudioBitrate = "160k"
		preset.CRF = 24
	case "360":
		preset.Height = 360
		preset.VideoBitrate = "900k"
		preset.MaxRate = "1100k"
		preset.BufferSize = "1800k"
		preset.AudioBitrate = "96k"
		preset.CRF = 30
	default:
		preset.Effective = "720"
		preset.Height = 720
		preset.VideoBitrate = "2500k"
		preset.MaxRate = "3000k"
		preset.BufferSize = "5000k"
		preset.AudioBitrate = "128k"
		preset.CRF = 27
	}
	return preset
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

	mp4Err := run(ffmpeg,
		"-y",
		"-loop", "1",
		"-t", ClipDuration,
		"-i", imagePath,
		"-f", "lavfi",
		"-t", ClipDuration,
		"-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", strconv.Itoa(preset.CRF),
		"-pix_fmt", "yuv420p",
		"-vf", "fps="+FrameRate+",scale=w='min(1920,iw)':h='min("+strconv.Itoa(preset.Height)+",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,pad=ceil(iw/2)*2:ceil(ih/2)*2",
		"-r", FrameRate,
		"-c:a", "aac",
		"-b:a", "64k",
		"-shortest",
		"-movflags", "+faststart",
		filepath.Join(outDir, MP4File),
	)

	hlsErr := runInDir(outDir, ffmpeg,
		"-y",
		"-loop", "1",
		"-t", ClipDuration,
		"-i", imagePath,
		"-f", "lavfi",
		"-t", ClipDuration,
		"-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", strconv.Itoa(preset.CRF),
		"-pix_fmt", "yuv420p",
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
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", segmentPattern(id),
		playlistName(id),
	)

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
	count := hlsSegmentCount(outDir)
	if job.QueueJob != nil {
		count = hlsSegmentCountForID(outDir, job.QueueJob.MediaID)
	}
	seconds := count * 2
	percent := int(math.Round(float64(seconds) / float64(job.TotalSeconds) * 100))
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	if seconds > job.TotalSeconds {
		seconds = job.TotalSeconds
	}
	result.ProgressPercent = percent
	result.ProgressText = fmt.Sprintf("%d%% (%d / %d sec)", percent, seconds, job.TotalSeconds)
}

func PublishUploadedVideoAsync(sourcePath, outDir string, preset QualityPreset) {
	PublishUploadedVideoAsyncForID(sourcePath, outDir, "", preset)
}

func PublishUploadedVideoAsyncForID(sourcePath, outDir, id string, preset QualityPreset) {
	EnqueueUploadedVideoForID(sourcePath, outDir, id, filepath.Base(sourcePath), preset)
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

func EnqueueUploadedVideoForID(sourcePath, outDir, id, title string, preset QualityPreset) string {
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
		OutDir:     outDir,
		SourcePath: sourcePath,
		Mode:       "uploaded",
		Preset:     preset,
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
	return runInDirContext(ctx, outDir, ffmpeg,
		"-y",
		"-loop", "1",
		"-t", ClipDuration,
		"-i", imagePath,
		"-f", "lavfi",
		"-t", ClipDuration,
		"-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", strconv.Itoa(preset.CRF),
		"-pix_fmt", "yuv420p",
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
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segmentPattern(id),
		playlistName(id),
	)
}

func runUploadedHLS(ctx context.Context, outDir, ffmpeg, sourcePath, id string, preset QualityPreset) error {
	return runInDirContext(ctx, outDir, ffmpeg,
		"-y",
		"-re",
		"-i", sourcePath,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", strconv.Itoa(preset.CRF),
		"-b:v", preset.VideoBitrate,
		"-maxrate", preset.MaxRate,
		"-bufsize", preset.BufferSize,
		"-pix_fmt", "yuv420p",
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
	seconds := hlsSegmentCountForID(job.OutDir, job.MediaID) * 2
	percent := int(math.Round(float64(seconds) / float64(job.TotalSeconds) * 100))
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	if seconds > job.TotalSeconds {
		seconds = job.TotalSeconds
	}
	item.ProgressPercent = percent
	item.ProgressText = fmt.Sprintf("%d%% (%d / %d sec)", percent, seconds, job.TotalSeconds)
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

func EnsureFFmpeg() (string, error) {
	if ffmpeg, err := ffmpegPath(); err == nil {
		return ffmpeg, nil
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return "", fmt.Errorf("FFmpeg not found. %s You can also set IMAGEPAD_FFMPEG.", toolInstallHint("ffmpeg"))
	}
	return downloadFFmpeg()
}

func EnsureYTDLP() (string, error) {
	if exe, err := ytdlpPath(); err == nil {
		return exe, nil
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return "", fmt.Errorf("yt-dlp not found. %s You can also set IMAGEPAD_YTDLP.", toolInstallHint("yt-dlp"))
	}
	return downloadYTDLP()
}

func DownloadURL(rawURL, outDir string) (string, string, error) {
	exe, err := EnsureYTDLP()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return "", "", err
	}
	removeYTDLPFiles(outDir)
	target := filepath.Join(outDir, "yt-dlp-source.%(ext)s")
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--max-filesize", "2G",
		"-f", "bv*[height<=1080]+ba/b[height<=1080]/best[height<=1080]/best",
		"--merge-output-format", "mp4",
		"-o", target,
		rawURL,
	}
	if err := run(exe, args...); err != nil {
		return "", "", err
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "yt-dlp-source.*"))
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && !info.IsDir() && info.Size() > 0 {
			return match, "yt-dlp-source" + filepath.Ext(match), nil
		}
	}
	return "", "", errors.New("yt-dlp did not produce a video file")
}

func GenerateThumbnail(sourcePath, outPath string) error {
	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0700); err != nil {
		return err
	}
	return run(ffmpeg,
		"-y",
		"-ss", "00:00:01",
		"-i", sourcePath,
		"-frames:v", "1",
		"-vf", "scale='min(480,iw)':-2",
		"-q:v", "4",
		outPath,
	)
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

func ffmpegPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		return "", fmt.Errorf("IMAGEPAD_FFMPEG does not exist: %s", configured)
	}
	if local := localFFmpegPath(); fileExists(local) {
		return local, nil
	}
	return exec.LookPath("ffmpeg")
}

func ytdlpPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		return "", fmt.Errorf("IMAGEPAD_YTDLP does not exist: %s", configured)
	}
	if local := localYTDLPPath(); fileExists(local) {
		return local, nil
	}
	return exec.LookPath("yt-dlp")
}

func localFFmpegPath() string {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(settings.Dir(), "bin", name)
}

func localYTDLPPath() string {
	name := "yt-dlp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(settings.Dir(), "bin", name)
}

func toolInstallHint(name string) string {
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("Install it with Homebrew (`brew install %s`) or add it to PATH.", name)
	case "linux":
		return fmt.Sprintf("Install %s with your package manager or add it to PATH.", name)
	default:
		return fmt.Sprintf("Add %s to PATH.", name)
	}
}

func downloadFFmpeg() (string, error) {
	if runtime.GOOS == "darwin" {
		return downloadDarwinFFmpeg()
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("automatic FFmpeg download is currently supported on Windows and macOS only")
	}

	target := localFFmpegPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare FFmpeg folder: %w", err)
	}

	checksum := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_SHA256"))
	if checksum == "" {
		checksum = ffmpegDownloadSHA256
	}
	if checksum == "" {
		var err error
		checksum, err = remoteTextSHA256(ffmpegSHA256URL)
		if err != nil {
			return "", fmt.Errorf("failed to resolve FFmpeg checksum: %w", err)
		}
	}

	zipPath := filepath.Join(settings.Dir(), "bin", "ffmpeg-release-essentials.zip")
	if err := downloadFile(zipPath, ffmpegDownloadURL, 160<<20, checksum); err != nil {
		return "", fmt.Errorf("failed to download FFmpeg: %w", err)
	}
	defer os.Remove(zipPath)

	if err := extractFFmpegExe(zipPath, target); err != nil {
		return "", fmt.Errorf("failed to install FFmpeg: %w", err)
	}
	return target, nil
}

func downloadYTDLP() (string, error) {
	if runtime.GOOS == "darwin" {
		return downloadDarwinYTDLP()
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("automatic yt-dlp download is currently supported on Windows and macOS only")
	}
	target := localYTDLPPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare yt-dlp folder: %w", err)
	}

	checksum := strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP_SHA256"))
	if checksum == "" {
		checksum = ytdlpDownloadSHA256
	}
	if checksum == "" {
		var err error
		checksum, err = remoteSHA256For("yt-dlp.exe")
		if err != nil {
			return "", fmt.Errorf("failed to resolve yt-dlp checksum: %w", err)
		}
	}

	if err := downloadFile(target, ytdlpDownloadURL, 50<<20, checksum); err != nil {
		return "", fmt.Errorf("failed to download yt-dlp: %w", err)
	}
	return target, nil
}

func downloadDarwinFFmpeg() (string, error) {
	target := localFFmpegPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare FFmpeg folder: %w", err)
	}
	rawURL, err := darwinFFmpegDownloadURL()
	if err != nil {
		return "", err
	}
	zipPath := filepath.Join(settings.Dir(), "bin", "ffmpeg-macos.zip")
	checksum := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_SHA256"))
	if err := downloadFileAllowMissingChecksum(zipPath, rawURL, 180<<20, checksum); err != nil {
		return "", fmt.Errorf("failed to download FFmpeg: %w", err)
	}
	defer os.Remove(zipPath)
	if err := extractNamedBinaryFromZip(zipPath, target, "ffmpeg"); err != nil {
		return "", fmt.Errorf("failed to install FFmpeg: %w", err)
	}
	if err := validateExecutable(target, "-version"); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}

func darwinFFmpegDownloadURL() (string, error) {
	switch runtime.GOARCH {
	case "arm64":
		return "https://ffmpeg.martin-riedl.de/redirect/latest/macos/arm64/release/ffmpeg.zip", nil
	case "amd64":
		return "https://ffmpeg.martin-riedl.de/redirect/latest/macos/amd64/release/ffmpeg.zip", nil
	default:
		return "", fmt.Errorf("automatic FFmpeg install is not available for darwin/%s", runtime.GOARCH)
	}
}

func downloadDarwinYTDLP() (string, error) {
	target := localYTDLPPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare yt-dlp folder: %w", err)
	}
	checksum := strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP_SHA256"))
	if checksum == "" {
		var err error
		checksum, err = remoteSHA256For("yt-dlp_macos")
		if err != nil {
			return "", fmt.Errorf("failed to resolve yt-dlp checksum: %w", err)
		}
	}
	if err := downloadExecutable(target, ytdlpMacOSURL, 80<<20, checksum); err != nil {
		return "", fmt.Errorf("failed to download yt-dlp: %w", err)
	}
	if err := validateExecutable(target, "--version"); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}

func downloadFile(path, rawURL string, maxBytes int64, expectedSHA256 string) error {
	if strings.TrimSpace(expectedSHA256) == "" {
		return errors.New("missing SHA256 checksum for trusted download")
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("download exceeds size limit")
	}

	tempPath := path + ".tmp"
	out, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return closeErr
	}
	if written > maxBytes {
		_ = os.Remove(tempPath)
		return fmt.Errorf("download exceeds size limit")
	}

	if err := verifySHA256(tempPath, expectedSHA256); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tempPath, path)
}

func downloadFileAllowMissingChecksum(path, rawURL string, maxBytes int64, expectedSHA256 string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("download exceeds size limit")
	}
	tempPath := path + ".tmp"
	out, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return closeErr
	}
	if written == 0 || written > maxBytes {
		_ = os.Remove(tempPath)
		return fmt.Errorf("download has invalid size")
	}
	if strings.TrimSpace(expectedSHA256) != "" {
		if err := verifySHA256(tempPath, expectedSHA256); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
	}
	_ = os.Remove(path)
	return os.Rename(tempPath, path)
}

func downloadExecutable(path, rawURL string, maxBytes int64, expectedSHA256 string) error {
	if err := downloadFile(path, rawURL, maxBytes, expectedSHA256); err != nil {
		return err
	}
	return os.Chmod(path, 0755)
}

func remoteSHA256For(fileName string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, ytdlpSHA256SumsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("checksum download returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == fileName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s was not found", fileName)
}

func remoteTextSHA256(rawURL string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("checksum download returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", errors.New("checksum response was empty")
	}
	return fields[0], nil
}

func verifySHA256(path, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return errors.New("expected SHA256 checksum is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	actual := hex.EncodeToString(hash[:])
	if actual != expected {
		return fmt.Errorf("download checksum mismatch: want %s, got %s", expected, actual)
	}
	return nil
}

func extractFFmpegExe(zipPath, target string) error {
	return extractNamedBinaryFromZip(zipPath, target, "ffmpeg.exe")
}

func extractNamedBinaryFromZip(zipPath, target, binaryName string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		if strings.ToLower(filepath.Base(name)) != strings.ToLower(binaryName) {
			continue
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		defer src.Close()

		tempTarget := target + ".tmp"
		dst, err := os.OpenFile(tempTarget, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		if copyErr != nil {
			_ = os.Remove(tempTarget)
			return copyErr
		}
		if closeErr != nil {
			_ = os.Remove(tempTarget)
			return closeErr
		}
		_ = os.Remove(target)
		return os.Rename(tempTarget, target)
	}
	return fmt.Errorf("%s was not found in the downloaded archive", binaryName)
}

func validateExecutable(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("installed executable validation failed: %w: %s", err, trimOutput(output))
	}
	return nil
}

func run(ffmpeg string, args ...string) error {
	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

func runInDir(dir, ffmpeg string, args ...string) error {
	cmd := exec.Command(ffmpeg, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

func runInDirContext(ctx context.Context, dir, ffmpeg string, args ...string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func trimOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 700 {
		return text[len(text)-700:]
	}
	if text == "" {
		return "no output"
	}
	return text
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

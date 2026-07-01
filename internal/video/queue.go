package video

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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

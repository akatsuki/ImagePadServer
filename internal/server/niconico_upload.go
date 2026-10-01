package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/library"
	"imagepadserver/internal/nicoexportworker"
	"imagepadserver/internal/niconico"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

var fetchNiconicoSnapshot = func(ctx context.Context, videoID string) (niconico.Snapshot, error) {
	return niconico.NewClient(nil, nil).Fetch(ctx, videoID)
}

// Kept behind a seam so the publish path can be tested without starting a
// background FFmpeg worker.
var enqueueNiconicoCommentedVideo = video.EnqueueNicoCommentedVideoForID

// These seams keep the HTTP/publication tests deterministic. Production uses
// the real ffprobe and the CPU-budgeted worker below.
var niconicoProbeMedia = video.ProbeMedia
var niconicoWorkerRunner = runNicoWorkerWithBudget
var niconicoEnsureFFprobe = video.EnsureFFprobe

func nicoEncoderForMode(mode string) string {
	if settings.NormalizeEncoderMode(mode) == "cpu" {
		return "x264"
	}
	return "nvenc"
}

func isNicoNVENCFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "nvenc") || strings.Contains(message, "libnvidia-encode")
}

// waitForNiconicoIngest makes Nico requests a bounded-by-request-context
// queue. Other ingest types keep the existing reject-on-busy behavior.
func (s *Server) waitForNiconicoIngest(ctx context.Context, title string) (time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.tryBeginIngest(ingestDownloading, title) {
			return time.Since(started), nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

// processNiconicoCommentedURL is the synchronous first integration of the
// offline comment pipeline. It deliberately keeps the existing publication
// untouched until the completed commented MP4 has been produced.
func (s *Server) processNiconicoCommentedURL(r *http.Request, rawURL string, queue bool) (map[string]interface{}, error) {
	parsed, err := niconico.ParseVideoURL(rawURL)
	if err != nil {
		return nil, err
	}
	snapshot, err := fetchNiconicoSnapshot(r.Context(), parsed.ID)
	if err != nil {
		return nil, fmt.Errorf("ニコニココメント取得に失敗しました: %w", err)
	}
	s.setIngestProgress(8, fmt.Sprintf("コメント%d件を取得しました", snapshot.CommentCount))
	var media video.DownloadedMedia
	if err := s.withYTDLPIngestProgress(func() error {
		var downloadErr error
		media, downloadErr = pageMediaDownloader(parsed.CanonicalURL, s.store.Dir())
		return downloadErr
	}); err != nil {
		return nil, fmt.Errorf("%s", videoURLDownloadError(err))
	}
	if media.SourcePath == "" {
		return nil, fmt.Errorf("ニコニコ動画の取得結果に動画ファイルがありません")
	}
	defer os.Remove(media.SourcePath)
	s.setIngest(ingestAnalyzing, "ニコニコ動画の情報を解析中…")

	ffmpeg, err := ensureFFmpeg()
	if err != nil {
		return nil, err
	}
	ffprobe, err := niconicoEnsureFFprobe()
	if err != nil {
		return nil, err
	}
	probe, err := niconicoProbeMedia(r.Context(), ffprobe, media.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("ニコニコ動画のメタデータ取得に失敗しました: %w", err)
	}
	if probe.Duration <= 0 {
		return nil, fmt.Errorf("ニコニコ動画の長さを取得できません")
	}
	s.setIngestProgress(24, fmt.Sprintf("動画 %.1f秒 / コメント%d件", probe.Duration, snapshot.CommentCount))
	durationMs := int64(math.Ceil(probe.Duration * 1000))
	width, height := niconicoTargetGeometry(probe, s.videoQualityPreset().Height)
	preset := s.videoQualityPreset()
	if preset.CRF <= 0 {
		preset.CRF = 28
	}
	s.setIngest(ingestProcessing, "コメントを描画して動画へ合成中…")
	s.setIngestProgress(30, fmt.Sprintf("コメント%d件を描画中", snapshot.CommentCount))
	runID := "run-" + randomSuffix()
	mediaID := "nico-" + randomSuffix()
	stagingDir := filepath.Join(s.store.Dir(), ".niconico-export-"+runID)
	if err := os.MkdirAll(stagingDir, 0700); err != nil {
		return nil, fmt.Errorf("ニコニコ書き出しstagingの作成に失敗しました: %w", err)
	}
	defer os.RemoveAll(stagingDir)
	snapshotPath := filepath.Join(stagingDir, "snapshot.json")
	if err := writeNicoSnapshotPath(snapshotPath, snapshot); err != nil {
		return nil, fmt.Errorf("ニコニコsnapshotの準備に失敗しました: %w", err)
	}
	outputPath := filepath.Join(stagingDir, "rendered.mp4")
	hlsDir := filepath.Join(stagingDir, "hls")
	nicoExportMu.Lock()
	defer nicoExportMu.Unlock()
	expectedRevision := s.store.PublishedRevision()
	newWorkerContext := func() context.Context {
		frameProgress := newNicoProgressReporter(time.Now, s.setIngestProgress)
		return withNicoWorkerProgress(r.Context(), func(event nicoexportworker.Event) {
			switch event.Stage {
			case "render":
				if event.Total > 0 {
					frameProgress(event.Completed, event.Total)
				}
			case "timeline_fallback":
				s.setIngest(ingestProcessing, "WGPUコメント描画に失敗したため、CPU描画へ切り替えて処理を継続中…")
			case "hls":
				s.setIngestProgress(90, "HLSを生成中…")
			case "validate":
				s.setIngestProgress(99, "完成物を検証中…")
			}
		})
	}
	timelineOptions, err := nicoTimelineWorkerRequestOptions()
	if err != nil {
		return nil, fmt.Errorf("ニコニコtimeline設定が不正です: %w", err)
	}
	encoder := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_ENCODER"))
	if encoder == "" {
		appSettings, err := settings.Load()
		if err != nil {
			return nil, fmt.Errorf("エンコーダ設定を読み込めません: %w", err)
		}
		encoder = nicoEncoderForMode(appSettings.EncoderMode)
	}
	workerRequest := nicoexportworker.Request{
		Version: nicoexportworker.ProtocolVersion,
		RunID:   runID, MediaID: mediaID,
		SourcePath: media.SourcePath, SnapshotPath: snapshotPath, OutputPath: outputPath, HLSStagingDir: hlsDir,
		Backend: timelineOptions.Backend, TimelineEnabled: timelineOptions.TimelineEnabled,
		TimelineCompositor: timelineOptions.TimelineCompositor, TimelineReadbackSlots: timelineOptions.TimelineReadbackSlots,
		TimelineGPUBackend: timelineOptions.TimelineGPUBackend,
		FFmpeg:             ffmpeg, Width: width, Height: height, DurationMs: durationMs, FPSNum: 30, FPSDen: 1,
		CRF: preset.CRF, AudioBitrate: preset.AudioBitrate,
		// The UI/API encoder choice drives Nico exports. The environment
		// override remains available for diagnostic runs.
		Encoder: encoder,
		// Tee is also opt-in until the combined MP4/HLS path has completed the
		// VRChat-concurrent acceptance gate. Empty keeps the legacy separate
		// HLS pass for rollback compatibility.
		OutputMode: strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_OUTPUT_MODE")),
	}
	workerResult, budgetReport, err := s.runNicoWorkerRequest(newWorkerContext(), workerRequest)
	renderFallbackNotice := ""
	if err == nil {
		renderFallbackNotice = strings.TrimSpace(workerResult.FallbackNotice)
	}
	encoderFallbackNotice := ""
	allowCPUFallback := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_ENCODER")) == "" && encoder == "nvenc"
	if err != nil && allowCPUFallback && isNicoNVENCFailure(err) && r.Context().Err() == nil {
		nvencErr := err
		s.setIngest(ingestProcessing, "NVENCに失敗したためCPU/libx264へ切り替えて再試行中…")
		cpuAttemptDir := filepath.Join(stagingDir, "cpu-fallback")
		if err := os.MkdirAll(cpuAttemptDir, 0700); err != nil {
			return nil, fmt.Errorf("NVENC失敗後のCPUフォールバック用stagingを作成できません: %w", err)
		}
		cpuRequest := workerRequest
		cpuRequest.RunID = "run-" + randomSuffix()
		cpuRequest.Encoder = "x264"
		cpuRequest.OutputPath = filepath.Join(cpuAttemptDir, "rendered.mp4")
		cpuRequest.HLSStagingDir = filepath.Join(cpuAttemptDir, "hls")
		workerResult, budgetReport, err = s.runNicoWorkerRequest(newWorkerContext(), cpuRequest)
		if err != nil {
			return nil, fmt.Errorf("NVENCに失敗しました (%v)。CPU/libx264へのフォールバックも失敗しました: %w", nvencErr, err)
		}
		outputPath = cpuRequest.OutputPath
		hlsDir = cpuRequest.HLSStagingDir
		renderFallbackNotice = strings.TrimSpace(workerResult.FallbackNotice)
		encoderFallbackNotice = "NVENCに失敗したためCPU/libx264へ切り替えました"
	}
	fallbackNotice := appendNicoUploadFallbackNotice(encoderFallbackNotice, renderFallbackNotice)
	if err != nil {
		return nil, fmt.Errorf("ニコニココメント合成に失敗しました: %w", err)
	}
	if !budgetReport.Verified {
		message := fmt.Sprintf("ニコニココメント合成のCPU予算を検証できませんでした: %s", budgetReport.Reason)
		if fallbackNotice != "" {
			message = fallbackNotice + "。" + message
		}
		return nil, errors.New(message)
	}
	// Thumbnail generation is deliberately not run in the parent: it would
	// launch an unbudgeted FFmpeg process after the worker exits. A downloaded
	// thumbnail is already available; when it is absent, commit without one and
	// let a later explicitly budgeted thumbnail job fill it.
	thumbnail := media.ThumbnailPath
	publicName := "current-video" + filepath.Ext(outputPath)
	if queue {
		publicName = "queued-video" + filepath.Ext(outputPath)
	}
	committed, err := s.store.CommitPreparedNicoMedia(library.PreparedNicoMedia{
		Info: library.CurrentImage{
			ID: mediaID, Kind: "video", FileName: "niconico-commented-" + runID + ".mp4", PublicName: publicName,
			ContentType: videoContentType(outputPath), OriginalName: media.Name, Width: width, Height: height,
		},
		SourcePath: outputPath, ThumbnailPath: thumbnail, SnapshotPath: snapshotPath, HLSDir: hlsDir,
		RunID: runID, SelectCurrent: !queue, ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return nil, fmt.Errorf("ニコニコ完成物の公開確定に失敗しました: %w", err)
	}
	s.setIngestProgress(100, fmt.Sprintf("コメント付き動画を公開しました (%s)", committed.ID))
	result := s.state(r)
	if !queue {
		result = s.withClipboardResult(r, result)
	}
	if fallbackNotice != "" {
		result["fallbackNotice"] = fallbackNotice
	}
	if encoderFallbackNotice != "" {
		result["encoderFallbackNotice"] = encoderFallbackNotice
	}
	return result, nil
}

func appendNicoUploadFallbackNotice(current, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	switch {
	case current == "":
		return next
	case next == "":
		return current
	default:
		return current + "。" + next
	}
}

func (s *Server) processPreparedNicoVideo(r *http.Request, sourcePath, name, providedThumbnail string, snapshot niconico.Snapshot, queue bool) (map[string]interface{}, error) {
	thumbnail := s.useOrCreateVideoThumbnail(sourcePath, providedThumbnail)
	info := library.CurrentImage{
		Kind: "video", FileName: filepath.Base(sourcePath), ContentType: videoContentType(sourcePath),
		OriginalName: name, Thumbnail: thumbnail,
	}
	if queue {
		info.PublicName = "queued-video" + filepath.Ext(sourcePath)
		historyItem, err := s.store.AddHistory(sourcePath, info)
		if err != nil {
			return nil, fmt.Errorf("failed to add niconico video to history")
		}
		path, _, ok := s.store.HistoryPath(historyItem.ID)
		if !ok {
			return nil, fmt.Errorf("failed to locate saved niconico video")
		}
		if err := writeNicoSnapshot(s.store.Dir(), historyItem.ID, snapshot); err != nil {
			return nil, fmt.Errorf("failed to save niconico snapshot: %w", err)
		}
		jobID := enqueueNiconicoCommentedVideo(path, s.store.Dir(), historyItem.ID, historyItem.OriginalName, 0)
		s.watchConversion(jobID, historyItem.ID)
		_ = os.Remove(sourcePath)
		return s.state(r), nil
	}
	info.PublicName = "current-video" + filepath.Ext(sourcePath)
	if stat, err := os.Stat(sourcePath); err == nil {
		info.SizeBytes = stat.Size()
	}
	if prev := s.store.Current(); prev != nil && prev.ID != "" {
		video.CancelConversion(s.store.Dir(), prev.ID)
	}
	// The rendered MP4 is already complete and lives inside the store. Keep the
	// existing publication until SetCurrentInfo commits the new item; calling
	// clearPublication here would remove the just-rendered niconico-commented
	// file because Store.Clear deletes non-history files in the store directory.
	if err := s.store.SetCurrentInfo(info); err != nil {
		return nil, fmt.Errorf("failed to save niconico video")
	}
	current := s.store.Current()
	if current == nil || current.ID == "" {
		return nil, fmt.Errorf("failed to save niconico video")
	}
	if err := writeNicoSnapshot(s.store.Dir(), current.ID, snapshot); err != nil {
		return nil, fmt.Errorf("failed to save niconico snapshot: %w", err)
	}
	jobID := enqueueNiconicoCommentedVideo(sourcePath, s.store.Dir(), current.ID, current.OriginalName, 0)
	s.watchConversion(jobID, current.ID)
	return s.withClipboardResult(r, s.state(r)), nil
}

func writeNicoSnapshot(dir, mediaID string, snapshot niconico.Snapshot) error {
	return writeNicoSnapshotPath(filepath.Join(dir, "niconico-snapshot-"+mediaID+".json"), snapshot)
}

func writeNicoSnapshotPath(path string, snapshot niconico.Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func niconicoTargetGeometry(probe video.MediaProbe, targetHeight int) (int, int) {
	if targetHeight <= 0 {
		targetHeight = 720
	}
	width, height := 16, 9
	for _, stream := range probe.Streams {
		if stream.CodecType == "video" && !stream.AttachedPic && stream.Width > 0 && stream.Height > 0 {
			width, height = stream.Width, stream.Height
			break
		}
	}
	outHeight := targetHeight
	outWidth := int(math.Round(float64(width) * float64(outHeight) / float64(height)))
	if outWidth < 2 {
		outWidth = 2
	}
	if outWidth > 3840 {
		outWidth = 3840
	}
	if outWidth%2 != 0 {
		outWidth--
	}
	if outHeight%2 != 0 {
		outHeight--
	}
	return outWidth, outHeight
}

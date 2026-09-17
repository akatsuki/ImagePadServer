package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"imagepadserver/internal/library"
	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
	"imagepadserver/internal/video"
)

var fetchNiconicoSnapshot = func(ctx context.Context, videoID string) (niconico.Snapshot, error) {
	return niconico.NewClient(nil, nil).Fetch(ctx, videoID)
}

// Kept behind a seam so the publish path can be tested without starting a
// background FFmpeg worker.
var enqueueNiconicoCommentedVideo = video.EnqueueNicoCommentedVideoForID

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
	ffprobe, err := video.EnsureFFprobe()
	if err != nil {
		return nil, err
	}
	probe, err := video.ProbeMedia(r.Context(), ffprobe, media.SourcePath)
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
	progress := newNicoProgressReporter(time.Now, s.setIngestProgress)
	outputPath := filepath.Join(s.store.Dir(), "niconico-commented-"+randomSuffix()+".mp4")
	renderOptions := nicorender.RenderOptions{
		Width: width, Height: height, DurationMs: durationMs, FPSNum: 30, FPSDen: 1,
		RendererLabel: "niconicomments@0.4.1",
		Transport:     "binary", ReuseUnchanged: true,
		BatchFrames: 30, SparseFrames: true,
		Progress: progress,
	}
	_, _, err = video.EncodeNicoCommentedWithRenderer(r.Context(), ffmpeg, media.SourcePath, outputPath, snapshot, renderOptions, video.NicoEncodeOptions{
		Width: width, Height: height, DurationMs: durationMs, FPSNum: 30, FPSDen: 1, CRF: preset.CRF, AudioBitrate: preset.AudioBitrate,
	})
	if err != nil {
		_ = os.Remove(outputPath)
		return nil, fmt.Errorf("ニコニココメント合成に失敗しました: %w", err)
	}
	if queue {
		state, err := s.processPreparedNicoVideo(r, outputPath, media.Name, media.ThumbnailPath, snapshot, true)
		if err != nil {
			_ = os.Remove(outputPath)
			return nil, err
		}
		return state, nil
	}
	state, err := s.processPreparedNicoVideo(r, outputPath, media.Name, media.ThumbnailPath, snapshot, false)
	if err != nil {
		_ = os.Remove(outputPath)
		return nil, err
	}
	return state, nil
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
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(dir, "niconico-snapshot-"+mediaID+".json")
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

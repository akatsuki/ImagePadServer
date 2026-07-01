package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/library"
	"imagepadserver/internal/video"
)

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.historyState())
}

func (s *Server) handleHistoryFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID       string `json:"id"`
		Favorite bool   `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "invalid history favorite request", http.StatusBadRequest)
		return
	}
	if err := s.store.SetFavorite(req.ID, req.Favorite); err != nil {
		http.Error(w, "history item not found", http.StatusNotFound)
		return
	}
	writeJSON(w, s.historyState())
}

func (s *Server) handleHistoryQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "invalid history queue request", http.StatusBadRequest)
		return
	}
	if !s.videoPlayerEnabled() {
		http.Error(w, "video player support is disabled", http.StatusBadRequest)
		return
	}
	if !s.tryBeginIngest(ingestAnalyzing, req.ID) {
		http.Error(w, "別の取り込み処理が進行中です", http.StatusConflict)
		return
	}
	defer s.clearIngest()
	if err := s.enqueueHistoryItem(req.ID); err != nil {
		http.Error(w, "history item not found", http.StatusNotFound)
		return
	}
	writeJSON(w, s.state(r))
}

func (s *Server) handleHistorySelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "invalid history select request", http.StatusBadRequest)
		return
	}
	if err := s.store.SetCurrentFromHistory(req.ID); err != nil {
		http.Error(w, "history item not found", http.StatusNotFound)
		return
	}
	current := s.store.Current()
	if current != nil && current.Converted {
		writeJSON(w, s.withClipboardResult(s.state(r)))
		return
	}
	if path, current, ok := s.store.CurrentPath(); ok && s.videoPlayerEnabled() {
		if !s.tryBeginIngest(ingestAnalyzing, current.ID) {
			http.Error(w, "別の取り込み処理が進行中です", http.StatusConflict)
			return
		}
		defer s.clearIngest()
		if current.SourceKind == "soundcloud" || current.SourceKind == "local_audio" || current.SourceKind == "remote_audio" {
			input, err := s.audioRenderInputForStored(r.Context(), path, *current)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.enqueueAudioConversion(input, current.ID, current.OriginalName)
		} else if current.Kind == "video" {
			s.enqueueUploadedConversion(path, current.ID, current.OriginalName)
		} else {
			s.enqueueStillConversion(path, current.ID, current.OriginalName)
		}
	}
	writeJSON(w, s.withClipboardResult(s.state(r)))
}

func (s *Server) enqueueHistoryItem(id string) error {
	path, item, ok := s.store.HistoryPath(id)
	if !ok {
		return os.ErrNotExist
	}
	if item.Converted {
		return s.store.SetCurrentFromHistory(id)
	}
	if item.SourceKind == "soundcloud" || item.SourceKind == "local_audio" || item.SourceKind == "remote_audio" {
		input, err := s.audioRenderInputForStored(context.Background(), path, item.CurrentImage)
		if err != nil {
			return fmt.Errorf("audio re-analysis: %w", err)
		}
		s.enqueueAudioConversion(input, item.ID, item.OriginalName)
		return nil
	}
	if item.Kind == "video" {
		s.enqueueUploadedConversion(path, item.ID, item.OriginalName)
		return nil
	}
	s.enqueueStillConversion(path, item.ID, item.OriginalName)
	return nil
}

func (s *Server) enqueueStillConversion(path, id, title string) {
	jobID := video.EnqueueStillImageForID(path, s.store.Dir(), id, title, s.videoQualityPreset())
	s.watchConversion(jobID, id)
}

func (s *Server) enqueueUploadedConversion(path, id, title string) {
	jobID := video.EnqueueUploadedVideoForID(path, s.store.Dir(), id, title, s.videoQualityPreset(), s.probeVideoDuration(path))
	s.watchConversion(jobID, id)
}

// probeVideoDuration returns the source video's duration in whole seconds via
// ffprobe, rounding partial seconds up so the segment-based progress percentage
// (completed / total) never reports >100% mid-conversion. Returns 0 when the
// duration cannot be determined, in which case the queue falls back to a raw
// segment count instead of a percentage.
func (s *Server) probeVideoDuration(path string) int {
	ffprobe, err := findFFprobe()
	if err != nil {
		return 0
	}
	probe, err := video.ProbeMedia(context.Background(), ffprobe, path)
	if err != nil {
		return 0
	}
	if probe.Duration <= 0 {
		return 0
	}
	secs := int(probe.Duration)
	if float64(secs) < probe.Duration {
		secs++
	}
	return secs
}

func soundCloudCurrentInfo(media video.DownloadedMedia, publicName, thumbnail string) library.CurrentImage {
	return library.CurrentImage{
		Kind:         "video",
		SourceKind:   "soundcloud",
		FileName:     filepath.Base(media.SourcePath),
		PublicName:   publicName,
		ContentType:  soundCloudContentType(media.SourcePath),
		OriginalName: media.Name,
		Thumbnail:    thumbnail,
	}
}

func (s *Server) watchConversion(jobID, mediaID string) {
	if jobID == "" || mediaID == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.After(6 * time.Hour)
		for {
			select {
			case <-deadline:
				return
			case <-ticker.C:
				item, ok := queueItemByID(video.QueueStatus(s.store.Dir()), jobID)
				if !ok {
					return
				}
				switch item.Status {
				case "done":
					files := video.GeneratedFiles(s.store.Dir(), mediaID)
					if len(files) > 0 {
						convertedSize := totalFileSize(files)
						if current := s.store.Current(); current != nil && current.ID == mediaID {
							_ = s.store.UpdateCurrentSize(convertedSize)
						}
						_ = s.store.UpdateHistorySize(mediaID, convertedSize)
						_ = s.store.MarkConverted(mediaID, files)
					}
					return
				case "error", "canceled":
					return
				}
			}
		}
	}()
}

func queueItemByID(items []video.QueueItem, jobID string) (video.QueueItem, bool) {
	for _, item := range items {
		if item.ID == jobID {
			return item, true
		}
	}
	return video.QueueItem{}, false
}

func totalFileSize(paths []string) int64 {
	var total int64
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			total += info.Size()
		}
	}
	return total
}

func (s *Server) handleHistoryMedia(w http.ResponseWriter, r *http.Request) {
	pathPart := strings.TrimPrefix(r.URL.Path, "/history/")
	thumbnail := false
	if strings.HasSuffix(pathPart, "/thumbnail") {
		thumbnail = true
		pathPart = strings.TrimSuffix(pathPart, "/thumbnail")
	}
	id, err := url.PathUnescape(strings.Trim(pathPart, "/"))
	if err != nil || id == "" {
		http.NotFound(w, r)
		return
	}
	var path string
	var item library.HistoryItem
	var ok bool
	if thumbnail {
		path, item, ok = s.store.HistoryThumbnailPath(id)
	} else {
		path, item, ok = s.store.HistoryPath(id)
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	contentType := item.ContentType
	if thumbnail {
		contentType = "image/jpeg"
	}
	if contentType == "" && item.Kind == "video" {
		contentType = videoContentType(item.FileName)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	http.ServeContent(w, r, safeFileName(item.PublicName), item.UpdatedAt, file)
}

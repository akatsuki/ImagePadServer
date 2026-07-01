package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"imagepadserver/internal/toolchain"
	"imagepadserver/internal/video"
)

func (s *Server) handleUploadURL(w http.ResponseWriter, r *http.Request) {
	s.handleUploadURLAction(w, r, uploadURLPublish)
}

func (s *Server) handleUploadURLQueue(w http.ResponseWriter, r *http.Request) {
	s.handleUploadURLAction(w, r, uploadURLQueue)
}

func (s *Server) handleUploadURLAction(w http.ResponseWriter, r *http.Request, action uploadURLAction) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req uploadURLRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	if err := validateHTTPURL(req.URL); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if s.videoPlayerEnabled() {
		s.handleVideoURLAction(w, r, req.URL, action)
		return
	}

	reader, name, err := downloadRemoteImage(req.URL, optionsFromValues(func(key string) string {
		switch key {
		case "format":
			return req.Format
		case "quality":
			return req.Quality
		case "maxDimension":
			return req.MaxDimension
		case "maxMB":
			return req.MaxMB
		default:
			return ""
		}
	}).MaxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer reader.Close()

	opts := optionsFromValues(func(key string) string {
		switch key {
		case "format":
			return req.Format
		case "quality":
			return req.Quality
		case "maxDimension":
			return req.MaxDimension
		case "maxMB":
			return req.MaxMB
		default:
			return ""
		}
	})
	var state map[string]interface{}
	if action == uploadURLQueue {
		state, err = s.processAndQueue(r, reader, name, "", opts)
	} else {
		state, err = s.processAndPublish(r, reader, name, "", opts)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, state)
}

func (s *Server) handleVideoURLAction(w http.ResponseWriter, r *http.Request, rawURL string, action uploadURLAction) {
	if s.musicModeEnabled() {
		if !s.tryBeginIngest(ingestDownloading, rawURL) {
			http.Error(w, "another ingest is already running", http.StatusConflict)
			return
		}
		acquired, err := musicURLAcquirer(r.Context(), s, rawURL)
		s.clearIngest()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.writeAcquiredAudioURLResult(w, r, acquired, action)
		return
	}

	if !s.tryBeginIngest(ingestDownloading, rawURL) {
		http.Error(w, "another ingest is already running", http.StatusConflict)
		return
	}
	media, ytdlpErr := pageMediaDownloader(rawURL, s.store.Dir())
	s.clearIngest()
	if ytdlpErr == nil {
		switch media.Kind {
		case "soundcloud":
			acquired, err := s.acquireDownloadedSoundCloud(r.Context(), media)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			s.writeAcquiredAudioURLResult(w, r, acquired, action)
		default:
			s.writeVideoFileURLResult(w, r, media.SourcePath, media.Name, media.ThumbnailPath, action)
		}
		return
	}
	if video.IsPageMediaURL(rawURL) {
		http.Error(w, videoURLDownloadError(ytdlpErr), http.StatusBadRequest)
		return
	}

	downloaded, directErr := s.downloadDirectMedia(r.Context(), rawURL)
	if directErr != nil {
		http.Error(w, videoURLDownloadError(combineURLErrors(ytdlpErr, directErr)), http.StatusBadRequest)
		return
	}
	switch downloaded.Class {
	case video.MediaAudio:
		acquired, err := s.acquiredRemoteAudio(r.Context(), downloaded)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.writeAcquiredAudioURLResult(w, r, acquired, action)
	case video.MediaVideo:
		s.writeVideoFileURLResult(w, r, downloaded.Path, downloaded.Name, "", action)
	default:
		_ = os.Remove(downloaded.Path)
		http.Error(w, fmt.Sprintf("downloaded media is %s, not playable audio or video", downloaded.Class), http.StatusBadRequest)
	}
}

func (s *Server) acquiredRemoteAudio(ctx context.Context, media downloadedRemoteMedia) (video.AcquiredAudio, error) {
	ffmpeg, err := toolchain.EnsureFFmpeg()
	if err != nil {
		return video.AcquiredAudio{}, err
	}
	candidates, _ := video.ExtractEmbeddedArtwork(ctx, ffmpeg, media.Path, s.store.Dir(), media.Probe)
	return video.AcquiredAudio{
		SourcePath:       media.Path,
		SourceName:       media.Name,
		Kind:             video.SourceRemoteAudio,
		Probe:            media.Probe,
		EmbeddedMetadata: extractEmbeddedMetadata(media.Probe),
		EmbeddedArtwork:  candidates,
	}, nil
}

func (s *Server) writeAcquiredAudioURLResult(w http.ResponseWriter, r *http.Request, acquired video.AcquiredAudio, action uploadURLAction) {
	var (
		state map[string]interface{}
		err   error
	)
	if action == uploadURLQueue {
		state, err = s.processAudioFileAndQueue(r, acquired)
	} else {
		state, err = s.processAudioFileAndPublish(r, acquired)
	}
	if err != nil {
		_ = os.Remove(acquired.SourcePath)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, state)
}

func (s *Server) writeVideoFileURLResult(w http.ResponseWriter, r *http.Request, sourcePath, name, thumbnail string, action uploadURLAction) {
	var (
		state map[string]interface{}
		err   error
	)
	if action == uploadURLQueue {
		state, err = s.processVideoFileAndQueue(r, sourcePath, name, thumbnail)
	} else {
		state, err = s.processVideoFileAndPublish(r, sourcePath, name, thumbnail)
	}
	if err != nil {
		_ = os.Remove(sourcePath)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, state)
}

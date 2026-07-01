package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"imagepadserver/internal/library"
	"imagepadserver/internal/toolchain"
	"imagepadserver/internal/video"
)

// processAudioFileAndPublish resolves audio metadata, selects artwork, saves
// the audio as the current publication with metadata, enqueues audio conversion,
// and returns the full server state.
func (s *Server) processAudioFileAndPublish(r *http.Request, acquired video.AcquiredAudio) (map[string]interface{}, error) {
	return s.processAudioFile(r, acquired, uploadURLPublish)
}

// processAudioFileAndQueue resolves audio metadata, selects artwork, saves
// the audio to history with metadata, enqueues audio conversion, and returns
// the server state.
func (s *Server) processAudioFileAndQueue(r *http.Request, acquired video.AcquiredAudio) (map[string]interface{}, error) {
	return s.processAudioFile(r, acquired, uploadURLQueue)
}

func (s *Server) processAudioFile(r *http.Request, acquired video.AcquiredAudio, action uploadURLAction) (map[string]interface{}, error) {
	ffmpegPath, err := toolchain.EnsureFFmpeg()
	if err != nil {
		return nil, err
	}

	// Resolve metadata using source-specific precedence rules.
	meta := video.ResolveAudioMetadata(acquired.Kind, acquired.SourceName, acquired.EmbeddedMetadata, acquired.SoundCloudMetadata)

	// Select best artwork from embedded candidates or SoundCloud.
	artworkPath, err := video.SelectArtwork(acquired.EmbeddedArtwork, acquired.SoundCloudArtworkPath, acquired.Kind)
	if err != nil {
		return nil, fmt.Errorf("select artwork: %w", err)
	}

	// Generate thumbnail from artwork for history/display.
	thumbnail := ""
	if artworkPath != "" {
		thumbnail = s.createVideoThumbnail(artworkPath)
	}

	s.setIngest(ingestAnalyzing, meta.Title)
	analysis, err := video.AnalyzeAudioForKind(r.Context(), ffmpegPath, acquired.SourcePath, acquired.Kind)
	if err != nil {
		return nil, fmt.Errorf("analyze audio: %w", err)
	}

	sourceKind := string(acquired.Kind)
	info := library.CurrentImage{
		Kind:         "video",
		SourceKind:   sourceKind,
		FileName:     filepath.Base(acquired.SourcePath),
		PublicName:   videoPublicName(acquired.SourcePath, action),
		ContentType:  audioContentType(acquired.SourcePath),
		OriginalName: acquired.SourceName,
		Thumbnail:    thumbnail,
		Title:        meta.Title,
		Artist:       meta.Artist,
		Album:        meta.Album,
	}
	if stat, err := os.Stat(acquired.SourcePath); err == nil {
		info.SizeBytes = stat.Size()
	}

	if action == uploadURLPublish {
		if prev := s.store.Current(); prev != nil && prev.ID != "" {
			video.CancelConversion(s.store.Dir(), prev.ID)
		}
		if err := s.store.SetCurrentInfo(info); err != nil {
			return nil, fmt.Errorf("failed to save media")
		}
		currentID := ""
		if current := s.store.Current(); current != nil {
			currentID = current.ID
		}
		usedArtwork := artworkPath
		if thumbnail != "" {
			usedArtwork = filepath.Join(s.store.Dir(), thumbnail)
		}
		input := video.AudioRenderInput{
			SourcePath:  acquired.SourcePath,
			Kind:        acquired.Kind,
			Metadata:    meta,
			ArtworkPath: usedArtwork,
			Analysis:    analysis,
		}
		s.enqueueAudioConversion(input, currentID, acquired.SourceName)
		return s.withClipboardResult(s.state(r)), nil
	}

	historyItem, err := s.store.AddHistory(acquired.SourcePath, info)
	if err != nil {
		return nil, fmt.Errorf("failed to add to history")
	}

	if historyPath, _, ok := s.store.HistoryPath(historyItem.ID); ok {
		// Use history thumbnail if available, otherwise artwork.
		usedArtwork := artworkPath
		if thumbPath, _, ok := s.store.HistoryThumbnailPath(historyItem.ID); ok {
			usedArtwork = thumbPath
		}

		input := video.AudioRenderInput{
			SourcePath:  historyPath,
			Kind:        acquired.Kind,
			Metadata:    meta,
			ArtworkPath: usedArtwork,
			Analysis:    analysis,
		}
		s.enqueueAudioConversion(input, historyItem.ID, historyItem.OriginalName)
	}

	return s.state(r), nil
}

// enqueueAudioConversion enqueues an audio visualizer job using the generic
// EnqueueAudioForID path and watches for completion.
func (s *Server) enqueueAudioConversion(input video.AudioRenderInput, id, title string) {
	jobID := video.EnqueueAudioForID(input, s.store.Dir(), id, title, s.musicQualityPreset())
	s.watchConversion(jobID, id)
}

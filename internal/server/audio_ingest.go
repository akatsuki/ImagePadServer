package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"imagepadserver/internal/library"
	"imagepadserver/internal/video"
)

// processAudioFileAndPublish resolves audio metadata, selects artwork, saves
// the audio as the current publication with metadata, enqueues audio conversion,
// and returns the full server state.
func (s *Server) processAudioFileAndPublish(r *http.Request, acquired video.AcquiredAudio) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
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

	sourceKind := string(acquired.Kind)
	info := library.CurrentImage{
		Kind:         "video",
		SourceKind:   sourceKind,
		FileName:     filepath.Base(acquired.SourcePath),
		PublicName:   "current-video" + filepath.Ext(acquired.SourcePath),
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

	// Cancel any in-flight conversion for the previous media.
	if prev := s.store.Current(); prev != nil && prev.ID != "" {
		video.CancelConversion(s.store.Dir(), prev.ID)
	}

	if err := s.store.SetCurrentInfo(info); err != nil {
		return nil, fmt.Errorf("failed to save media")
	}

	current := s.store.Current()
	currentID := ""
	if current != nil {
		currentID = current.ID
	}

	// Resolve final artwork path — prefer the generated thumbnail.
	usedArtwork := artworkPath
	if thumbnail != "" {
		usedArtwork = filepath.Join(s.store.Dir(), thumbnail)
	}

	input := video.AudioRenderInput{
		SourcePath:  acquired.SourcePath,
		Kind:        acquired.Kind,
		Metadata:    meta,
		ArtworkPath: usedArtwork,
	}

	s.enqueueAudioConversion(input, currentID, acquired.SourceName)

	state := s.state(r)
	return s.withClipboardResult(state), nil
}

// processAudioFileAndQueue resolves audio metadata, selects artwork, saves
// the audio to history with metadata, enqueues audio conversion, and returns
// the server state.
func (s *Server) processAudioFileAndQueue(r *http.Request, acquired video.AcquiredAudio) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
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

	sourceKind := string(acquired.Kind)
	info := library.CurrentImage{
		Kind:         "video",
		SourceKind:   sourceKind,
		FileName:     filepath.Base(acquired.SourcePath),
		PublicName:   "queued-video" + filepath.Ext(acquired.SourcePath),
		ContentType:  audioContentType(acquired.SourcePath),
		OriginalName: acquired.SourceName,
		Thumbnail:    thumbnail,
		Title:        meta.Title,
		Artist:       meta.Artist,
		Album:        meta.Album,
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
		}
		s.enqueueAudioConversion(input, historyItem.ID, historyItem.OriginalName)
	}

	return s.state(r), nil
}

// enqueueAudioConversion enqueues an audio visualizer job using the generic
// EnqueueAudioForID path and watches for completion.
func (s *Server) enqueueAudioConversion(input video.AudioRenderInput, id, title string) {
	jobID := video.EnqueueAudioForID(input, s.store.Dir(), id, title, s.videoQualityPreset())
	s.watchConversion(jobID, id)
}

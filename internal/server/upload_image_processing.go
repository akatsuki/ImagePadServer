package server

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"imagepadserver/internal/imageproc"
	"imagepadserver/internal/library"
)

func (s *Server) processAndPublish(r *http.Request, reader io.Reader, name, contentType string, opts imageproc.Options) (map[string]interface{}, error) {
	if s.videoPlayerEnabled() && isVideoUpload(name, contentType) {
		return s.processVideoAndPublish(r, reader, name)
	}

	if s.videoPlayerEnabled() && (isAudioUpload(name, contentType) || shouldProbeUploadedMedia(name, contentType)) {
		s.setIngest(ingestProcessing, name)
		defer s.clearIngest()
		acquired, err := s.acquireUploadedAudio(r.Context(), reader, name)
		if err != nil {
			return nil, err
		}
		state, err := s.processAudioFileAndPublish(r, acquired)
		if err != nil {
			os.Remove(acquired.SourcePath)
			return nil, err
		}
		return state, nil
	}

	result, err := imageproc.Process(reader, name, s.store.Dir(), opts)
	if err != nil {
		return nil, err
	}
	info := library.CurrentImage{
		PublicName:   result.PublicName,
		ContentType:  result.ContentType,
		Width:        result.Width,
		Height:       result.Height,
		OriginalName: name,
	}
	if err := s.store.SetCurrent(result.Path, info); err != nil {
		return nil, fmt.Errorf("failed to save image")
	}
	_ = os.Remove(result.Path)
	if s.videoPlayerEnabled() {
		if imagePath, current, ok := s.store.CurrentPath(); ok {
			s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
		}
	}

	state := s.state(r)
	return s.withClipboardResult(state), nil
}

func (s *Server) processAndQueue(r *http.Request, reader io.Reader, name, contentType string, opts imageproc.Options) (map[string]interface{}, error) {
	if !s.videoPlayerEnabled() {
		return nil, fmt.Errorf("video player support is disabled")
	}
	if isVideoUpload(name, contentType) {
		return s.processVideoAndQueue(r, reader, name)
	}

	if isAudioUpload(name, contentType) || shouldProbeUploadedMedia(name, contentType) {
		s.setIngest(ingestProcessing, name)
		defer s.clearIngest()
		acquired, err := s.acquireUploadedAudio(r.Context(), reader, name)
		if err != nil {
			return nil, err
		}
		state, err := s.processAudioFileAndQueue(r, acquired)
		if err != nil {
			os.Remove(acquired.SourcePath)
			return nil, err
		}
		return state, nil
	}

	result, err := imageproc.Process(reader, name, s.store.Dir(), opts)
	if err != nil {
		return nil, err
	}
	info := library.CurrentImage{
		PublicName:   result.PublicName,
		ContentType:  result.ContentType,
		Width:        result.Width,
		Height:       result.Height,
		OriginalName: name,
	}
	historyItem, err := s.store.AddHistory(result.Path, info)
	if err != nil {
		return nil, fmt.Errorf("failed to add image to history")
	}
	_ = os.Remove(result.Path)
	if path, _, ok := s.store.HistoryPath(historyItem.ID); ok {
		s.enqueueStillConversion(path, historyItem.ID, historyItem.OriginalName)
	}
	return s.state(r), nil
}

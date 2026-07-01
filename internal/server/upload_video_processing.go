package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"imagepadserver/internal/library"
	"imagepadserver/internal/video"
)

func (s *Server) processVideoAndPublish(r *http.Request, reader io.Reader, name string) (map[string]interface{}, error) {
	return s.processVideoUpload(r, reader, name, uploadURLPublish)
}

func (s *Server) processVideoAndQueue(r *http.Request, reader io.Reader, name string) (map[string]interface{}, error) {
	return s.processVideoUpload(r, reader, name, uploadURLQueue)
}

func (s *Server) processVideoUpload(r *http.Request, reader io.Reader, name string, action uploadURLAction) (map[string]interface{}, error) {
	if _, err := ensureFFmpeg(); err != nil {
		return nil, err
	}
	prefix := "source-"
	if action == uploadURLQueue {
		prefix = "queued-source-"
	}
	sourcePath := filepath.Join(s.store.Dir(), prefix+randomSuffix()+safeVideoExt(name))
	source, err := os.Create(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to save video upload")
	}
	written, err := io.Copy(source, io.LimitReader(reader, maxVideoUploadBytes+1))
	if err != nil {
		_ = source.Close()
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("failed to save video upload")
	}
	if err := source.Close(); err != nil {
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("failed to save video upload")
	}
	if written > maxVideoUploadBytes {
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("video exceeds size limit of %d bytes", maxVideoUploadBytes)
	}
	return s.processVideoFile(r, sourcePath, name, "", action)
}

func (s *Server) processVideoFileAndPublish(r *http.Request, sourcePath, name, providedThumbnail string) (map[string]interface{}, error) {
	return s.processVideoFile(r, sourcePath, name, providedThumbnail, uploadURLPublish)
}

func (s *Server) processVideoFileAndQueue(r *http.Request, sourcePath, name, providedThumbnail string) (map[string]interface{}, error) {
	return s.processVideoFile(r, sourcePath, name, providedThumbnail, uploadURLQueue)
}

func (s *Server) processVideoFile(r *http.Request, sourcePath, name, providedThumbnail string, action uploadURLAction) (map[string]interface{}, error) {
	if _, err := ensureFFmpeg(); err != nil {
		return nil, err
	}
	thumbnail := s.useOrCreateVideoThumbnail(sourcePath, providedThumbnail)
	info := library.CurrentImage{
		Kind:         "video",
		FileName:     filepath.Base(sourcePath),
		PublicName:   videoPublicName(sourcePath, action),
		ContentType:  videoContentType(sourcePath),
		OriginalName: name,
		Thumbnail:    thumbnail,
	}
	if stat, err := os.Stat(sourcePath); err == nil {
		info.SizeBytes = stat.Size()
	}
	if action == uploadURLPublish {
		if prev := s.store.Current(); prev != nil && prev.ID != "" {
			video.CancelConversion(s.store.Dir(), prev.ID)
		}
		if err := s.store.SetCurrentInfo(info); err != nil {
			return nil, fmt.Errorf("failed to save video")
		}
		currentID := ""
		if current := s.store.Current(); current != nil {
			currentID = current.ID
		}
		s.enqueueUploadedConversion(sourcePath, currentID, name)
		return s.withClipboardResult(s.state(r)), nil
	}

	historyItem, err := s.store.AddHistory(sourcePath, info)
	if err != nil {
		return nil, fmt.Errorf("failed to add video to history")
	}
	if path, _, ok := s.store.HistoryPath(historyItem.ID); ok {
		s.enqueueUploadedConversion(path, historyItem.ID, historyItem.OriginalName)
	}
	return s.state(r), nil
}

func videoPublicName(sourcePath string, action uploadURLAction) string {
	if action == uploadURLPublish {
		return "current-video" + filepath.Ext(sourcePath)
	}
	return "queued-video" + filepath.Ext(sourcePath)
}

// useOrCreateVideoThumbnail copies an externally provided thumbnail into the
// store directory when available, otherwise generates one from the video.
func (s *Server) useOrCreateVideoThumbnail(sourcePath, provided string) string {
	if provided != "" {
		if info, err := os.Stat(provided); err == nil && !info.IsDir() && info.Size() > 0 {
			name := "video-thumb-external-" + randomSuffix() + filepath.Ext(provided)
			dest := filepath.Join(s.store.Dir(), name)
			if err := copyFile(provided, dest); err == nil {
				return name
			}
		}
	}
	return s.createVideoThumbnail(sourcePath)
}

func (s *Server) createVideoThumbnail(sourcePath string) string {
	name := "video-thumb-" + randomSuffix() + ".jpg"
	path := filepath.Join(s.store.Dir(), name)
	if err := video.GenerateThumbnail(sourcePath, path); err != nil {
		_ = os.Remove(path)
		return ""
	}
	return name
}

func copyFile(src, dst string) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func (s *Server) clearPublication() {
	if current := s.store.Current(); current != nil && current.ID != "" {
		video.CancelConversion(s.store.Dir(), current.ID)
	}
	_ = s.store.Clear()
}

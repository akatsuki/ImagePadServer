package server

import (
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/appicon"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/video"
)

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(appicon.IconICO)
}

func (s *Server) handleCurrentImage(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	path, img, ok := s.store.CurrentPath()
	if !ok {
		s.serveDeletedImage(w, r)
		return
	}
	if img.Kind == "video" {
		s.serveDeletedImage(w, r)
		return
	}
	if requestedID := r.URL.Query().Get("v"); requestedID != "" && requestedID != img.ID {
		s.serveDeletedImage(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		s.serveDeletedImage(w, r)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", img.ContentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeFileName(img.PublicName)))
	if r.URL.Query().Get("preview") != "1" {
		s.recordImageRequest(r)
	}
	http.ServeContent(w, r, img.PublicName, img.UpdatedAt, file)
}

func (s *Server) serveDeletedImage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("preview") != "1" {
		s.recordImageRequest(r)
	}
	contentType := deletedContentType(r.URL.Path)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", `inline; filename="deleted.jpg"`)
	if contentType == "image/png" {
		_ = png.Encode(w, deletedImage())
		return
	}
	_ = jpeg.Encode(w, deletedImage(), &jpeg.Options{Quality: 90})
}

func (s *Server) handleCurrentVideo(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	if !s.videoPlayerEnabled() {
		http.NotFound(w, r)
		return
	}
	img := s.store.Current()
	if img == nil {
		http.NotFound(w, r)
		return
	}
	if requestedID := r.URL.Query().Get("v"); requestedID != "" && requestedID != img.ID {
		http.NotFound(w, r)
		return
	}
	if img.Kind == "video" {
		path, current, ok := s.store.CurrentPath()
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
		contentType := current.ContentType
		if contentType == "" {
			contentType = videoContentType(current.FileName)
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeFileName(current.PublicName)))
		s.recordImageRequest(r)
		http.ServeContent(w, r, current.PublicName, current.UpdatedAt, file)
		return
	}
	s.serveGeneratedFile(w, r, video.MP4File, "video/mp4", "current.mp4", img.UpdatedAt)
}

func (s *Server) handleCurrentHLS(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	if !s.videoPlayerEnabled() {
		http.NotFound(w, r)
		return
	}
	if requestedID := streamRequestID(r); requestedID != "" && s.obsMediaActive(requestedID) {
		if s.serveLHLSArtifact(w, r, requestedID) {
			return
		}
		if s.serveLLHLSProxy(w, r, requestedID) {
			return
		}
		s.serveGeneratedFile(w, r, video.PlaylistName(requestedID), "application/vnd.apple.mpegurl", "current.m3u8", time.Now())
		return
	}
	img := s.store.Current()
	if img == nil {
		http.NotFound(w, r)
		return
	}
	if requestedID := streamRequestID(r); requestedID != "" && requestedID != img.ID {
		http.NotFound(w, r)
		return
	}
	s.serveGeneratedFile(w, r, video.PlaylistName(img.ID), "application/vnd.apple.mpegurl", "current.m3u8", img.UpdatedAt)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(filepath.Base(r.URL.Path), ".m3u8") {
		s.handleCurrentHLS(w, r)
		return
	}
	s.handleCurrentHLSSegment(w, r)
}

func (s *Server) handleCurrentHLSSegment(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	if !s.videoPlayerEnabled() {
		http.NotFound(w, r)
		return
	}
	if requestedID := streamRequestID(r); requestedID != "" && s.obsMediaActive(requestedID) {
		if s.serveLHLSArtifact(w, r, requestedID) {
			return
		}
		if s.serveLLHLSProxy(w, r, requestedID) {
			return
		}
		fileName := filepath.Base(r.URL.Path)
		if !isHLSSegmentName(fileName) {
			http.NotFound(w, r)
			return
		}
		s.serveGeneratedFile(w, r, fileName, "video/mp2t", fileName, time.Now())
		return
	}
	img := s.store.Current()
	if img == nil {
		http.NotFound(w, r)
		return
	}
	if requestedID := streamRequestID(r); requestedID != "" && requestedID != img.ID {
		http.NotFound(w, r)
		return
	}
	fileName := filepath.Base(r.URL.Path)
	if !isHLSSegmentName(fileName) {
		http.NotFound(w, r)
		return
	}
	s.serveGeneratedFile(w, r, fileName, "video/mp2t", fileName, img.UpdatedAt)
}

func (s *Server) serveGeneratedFile(w http.ResponseWriter, r *http.Request, fileName, contentType, publicName string, modTime time.Time) {
	path := filepath.Join(s.store.Dir(), fileName)
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeFileName(publicName)))
	s.recordImageRequest(r)
	http.ServeContent(w, r, publicName, modTime, file)
}

// serveLHLSArtifact serves community-LHLS playlists and fMP4 segments from the
// active OBS session's private sink directory. It returns true when it has
// handled the request (i.e. LHLS is the active transport), so the HLS-family
// handlers fall back to the standard MPEG-TS path only for non-LHLS modes.
func (s *Server) serveLHLSArtifact(w http.ResponseWriter, r *http.Request, id string) bool {
	if s.obs == nil {
		return false
	}
	if obsrtmp.NormalizeLatencyMode(s.obs.Status().Latency.Mode) != obsrtmp.LatencyModeLHLS {
		return false
	}
	name := filepath.Base(r.URL.Path)
	if isOBSEntryPlaylistAlias(id, name) {
		name = "master.m3u8"
	}
	path, ok := s.obs.LHLSPublicFile(id, name)
	if !ok {
		http.NotFound(w, r)
		return true
	}
	s.serveGeneratedAbsFile(w, r, path, lhlsContentType(name), name, time.Now())
	return true
}

// serveLLHLSProxy forwards LL-HLS playlist/segment requests for the active OBS
// session to its MediaMTX sidecar. It returns true when LL-HLS is the active
// transport and the request was proxied, so the HLS-family handlers do not fall
// back to the standard MPEG-TS path.
func (s *Server) serveLLHLSProxy(w http.ResponseWriter, r *http.Request, id string) bool {
	if s.obs == nil {
		return false
	}
	if obsrtmp.NormalizeLatencyMode(s.obs.Status().Latency.Mode) != obsrtmp.LatencyModeLLHLS {
		return false
	}
	name := filepath.Base(r.URL.Path)
	if isOBSEntryPlaylistAlias(id, name) {
		name = "index.m3u8"
	}
	return s.obs.ProxyLLHLS(w, r, id, name)
}

func isOBSEntryPlaylistAlias(id, name string) bool {
	return name == "." || name == "/" || name == "current.m3u8" || name == video.PlaylistName(id)
}

func (s *Server) serveGeneratedAbsFile(w http.ResponseWriter, r *http.Request, absPath, contentType, publicName string, modTime time.Time) {
	file, err := os.Open(absPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeFileName(publicName)))
	s.recordImageRequest(r)
	http.ServeContent(w, r, publicName, modTime, file)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "ok")
}

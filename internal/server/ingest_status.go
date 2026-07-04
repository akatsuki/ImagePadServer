package server

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"imagepadserver/internal/video"
)

// Ingest phase identifiers surfaced to the UI for the synchronous
// download/analyze portion of media ingest (render progress is reported
// separately via the video player state).
const (
	ingestUploading   = "uploading"
	ingestDownloading = "downloading"
	ingestAnalyzing   = "analyzing"
	ingestProcessing  = "processing"
)

type uploadProgressReadCloser struct {
	io.ReadCloser
	server *Server
	total  int64
	read   int64
}

func (r *uploadProgressReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.read += int64(n)
		r.server.setDownloadByteProgress(r.read, r.total)
	}
	return n, err
}

type ingestStatus struct {
	mu              sync.Mutex
	active          bool
	phase           string
	title           string
	progressPercent int
	progressText    string
}

// tryBeginIngest atomically claims the single ingest slot. It returns false if
// an ingest is already in progress, so callers can reject concurrent/duplicate
// requests (preventing the runaway re-processing loop) instead of piling work
// up. The holder must call clearIngest when done (via defer).
func (s *Server) tryBeginIngest(phase, title string) bool {
	s.ingest.mu.Lock()
	defer s.ingest.mu.Unlock()
	if s.ingest.active {
		return false
	}
	s.ingest.active = true
	s.ingest.phase = phase
	s.ingest.title = title
	s.ingest.progressPercent = 0
	s.ingest.progressText = ""
	go s.broadcastStateChanged()
	return true
}

func (s *Server) setIngest(phase, title string) {
	s.ingest.mu.Lock()
	s.ingest.active = true
	s.ingest.phase = phase
	s.ingest.title = title
	s.ingest.progressPercent = 0
	s.ingest.progressText = ""
	s.ingest.mu.Unlock()
	s.broadcastStateChanged()
}

func (s *Server) setIngestProgress(percent int, text string) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	s.ingest.mu.Lock()
	if !s.ingest.active {
		s.ingest.mu.Unlock()
		return
	}
	changed := s.ingest.progressPercent != percent || s.ingest.progressText != text
	s.ingest.progressPercent = percent
	s.ingest.progressText = text
	s.ingest.mu.Unlock()
	if changed {
		s.broadcastStateChanged()
	}
}

func (s *Server) clearIngest() {
	s.ingest.mu.Lock()
	s.ingest.active = false
	s.ingest.phase = ""
	s.ingest.title = ""
	s.ingest.progressPercent = 0
	s.ingest.progressText = ""
	s.ingest.mu.Unlock()
	s.broadcastStateChanged()
}

func (s *Server) trackUploadReceiveProgress(body io.ReadCloser, total int64, title string) (io.ReadCloser, func()) {
	if strings.TrimSpace(title) == "" {
		title = "ファイル"
	}
	if !s.tryBeginIngest(ingestUploading, title) {
		return body, func() {}
	}
	return &uploadProgressReadCloser{
		ReadCloser: body,
		server:     s,
		total:      total,
	}, s.clearIngest
}

func (s *Server) ingestState() map[string]interface{} {
	s.ingest.mu.Lock()
	defer s.ingest.mu.Unlock()
	return map[string]interface{}{
		"active":          s.ingest.active,
		"phase":           s.ingest.phase,
		"title":           s.ingest.title,
		"progressPercent": s.ingest.progressPercent,
		"progressText":    s.ingest.progressText,
	}
}

func (s *Server) withYTDLPIngestProgress(fn func() error) error {
	return video.WithYTDLPProgress(func(progress video.DownloadProgress) {
		s.setIngestProgress(progress.Percent, progress.Text)
	}, fn)
}

func (s *Server) setDownloadByteProgress(written, total int64) {
	if written < 0 {
		written = 0
	}
	text := fmt.Sprintf("受信済み %s", humanBytes(written))
	percent := 0
	if total > 0 {
		percent = int(written * 100 / total)
		if percent > 100 {
			percent = 100
		}
		text = fmt.Sprintf("%s / %s", humanBytes(written), humanBytes(total))
	}
	s.setIngestProgress(percent, text)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}

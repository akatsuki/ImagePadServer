package server

import "sync"

// Ingest phase identifiers surfaced to the UI for the synchronous
// download/analyze portion of media ingest (render progress is reported
// separately via the video player state).
const (
	ingestDownloading = "downloading"
	ingestAnalyzing   = "analyzing"
	ingestProcessing  = "processing"
)

type ingestStatus struct {
	mu     sync.Mutex
	active bool
	phase  string
	title  string
}

func (s *Server) setIngest(phase, title string) {
	s.ingest.mu.Lock()
	s.ingest.active = true
	s.ingest.phase = phase
	s.ingest.title = title
	s.ingest.mu.Unlock()
}

func (s *Server) clearIngest() {
	s.ingest.mu.Lock()
	s.ingest.active = false
	s.ingest.phase = ""
	s.ingest.title = ""
	s.ingest.mu.Unlock()
}

func (s *Server) ingestState() map[string]interface{} {
	s.ingest.mu.Lock()
	defer s.ingest.mu.Unlock()
	return map[string]interface{}{
		"active": s.ingest.active,
		"phase":  s.ingest.phase,
		"title":  s.ingest.title,
	}
}

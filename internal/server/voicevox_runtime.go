package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/voicevoxruntime"
)

type voicevoxRuntimeService interface {
	Start(context.Context)
	Retry(context.Context)
	Close() error
	Status() voicevoxruntime.Status
}

func (s *Server) voicevoxManager() voicevoxRuntimeService {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.voicevoxRuntime == nil {
		s.voicevoxRuntime = voicevoxruntime.New(filepath.Join(settings.Dir(), "tools", "voicevox"), s.broadcastStateChangedThrottled)
	}
	return s.voicevoxRuntime
}

// Start asynchronously, just like the video tool installer. HTTP startup never waits for a download.
func (s *Server) StartVoicevoxRuntime() { s.voicevoxManager().Start(s.lifecycleContext()) }
func (s *Server) StopVoicevoxRuntime() error {
	s.mu.RLock()
	m := s.voicevoxRuntime
	s.mu.RUnlock()
	if m != nil {
		return m.Close()
	}
	return nil
}

func (s *Server) managedVoiceReady(w http.ResponseWriter) bool {
	m := s.voicevoxManager()
	m.Start(s.lifecycleContext())
	status := m.Status()
	if status.Phase == "ready" {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"managed": true, "runtime": status, "error": status.Message})
	return false
}

func (s *Server) handleVoicevoxRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m := s.voicevoxManager()
	if r.Method == http.MethodPost {
		var input struct {
			Action string `json:"action"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid runtime action", 400)
			return
		}
		switch input.Action {
		case "retry":
			m.Retry(s.lifecycleContext())
		case "cancel":
			if err := m.Close(); err != nil {
				http.Error(w, err.Error(), 503)
				return
			}
		default:
			http.Error(w, "invalid runtime action", 400)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, m.Status())
}

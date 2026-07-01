package server

import (
	"net/http"
	"time"

	"imagepadserver/internal/about"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := s.state(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.Execute(w, data)
}

func (s *Server) handleTunnelReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	reconnect := s.tunnelReconnect
	s.mu.RUnlock()
	if reconnect == nil {
		http.Error(w, "tunnel reconnect unavailable", http.StatusServiceUnavailable)
		return
	}

	select {
	case reconnect <- struct{}{}:
		writeJSON(w, map[string]interface{}{"ok": true, "message": "再接続を要求しました"})
	default:
		writeJSON(w, map[string]interface{}{"ok": true, "message": "再接続要求は保留中です"})
	}
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	exit := s.exitRequested
	s.mu.RUnlock()
	if exit == nil {
		http.Error(w, "app shutdown unavailable", http.StatusServiceUnavailable)
		return
	}

	writeJSON(w, map[string]interface{}{"ok": true, "message": "アプリを終了します"})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		exit()
	}()
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.store.Clear(); err != nil {
		http.Error(w, "failed to clear image", http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.state(r))
}

func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"appName":     about.AppName,
		"version":     about.Version,
		"author":      about.Author,
		"license":     about.License,
		"copyright":   about.Copyright,
		"description": about.Description,
		"openSource":  about.OpenSourceNotices,
	})
}

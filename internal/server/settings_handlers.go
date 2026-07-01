package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/toolchain"
)

func (s *Server) handleVideoPlayer(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.videoPlayerState())
	case http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid video player request", http.StatusBadRequest)
			return
		}
		if req.Enabled && !videoToolsReady() {
			// Tools not ready: install in the background and return now. The
			// toggle stays OFF until install succeeds (or reverts on failure).
			// The UI shows progress via state.toolInstall.
			s.startVideoToolInstall()
			writeJSON(w, s.videoPlayerState())
			return
		}
		if err := s.updateSettings(func(appSettings *settings.Settings) error {
			appSettings.VideoPlayerEnabled = req.Enabled
			if !req.Enabled {
				appSettings.MusicModeEnabled = false
			}
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		if req.Enabled {
			if imagePath, current, ok := s.store.CurrentPath(); ok {
				s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
			}
		}
		s.SyncOBSReceiver()
		writeJSON(w, s.videoPlayerState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMusicMode(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.videoPlayerState())
	case http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid music mode request", http.StatusBadRequest)
			return
		}
		if req.Enabled && !s.videoPlayerEnabled() {
			http.Error(w, "music mode requires video player support", http.StatusConflict)
			return
		}
		if err := s.updateSettings(func(appSettings *settings.Settings) error {
			appSettings.MusicModeEnabled = req.Enabled && appSettings.VideoPlayerEnabled
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		writeJSON(w, s.videoPlayerState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleFFmpeg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path, err := toolchain.EnsureFFmpeg()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":   true,
		"path": path,
	})
}

func (s *Server) handleVideoQuality(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.videoQualityState())
	case http.MethodPost:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid video quality request", http.StatusBadRequest)
			return
		}
		mode := normalizeQualityMode(req.Mode)
		if err := s.updateSettings(func(appSettings *settings.Settings) error {
			appSettings.VideoQualityMode = mode
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		writeJSON(w, s.videoQualityState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleNetworkCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	measurement := networkMeasurer()
	if err := s.updateSettings(func(appSettings *settings.Settings) error {
		appSettings.NetworkUploadMbps = measurement.UploadMbps
		return nil
	}); err != nil {
		http.Error(w, "failed to save settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.videoQualityState())
}

func normalizeQualityMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "1080", "720", "360":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "auto"
	}
}

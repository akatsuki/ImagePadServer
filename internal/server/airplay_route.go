package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"imagepadserver/internal/airplay"
	"imagepadserver/internal/video"
)

func (s *Server) airplayState() airplay.Status {
	if s.airplay == nil {
		return airplay.Status{Enabled: false, Message: "AirPlay受信は利用できません。"}
	}
	return s.airplay.Status()
}

func (s *Server) handleAirPlayStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.airplay == nil {
		http.Error(w, "AirPlay receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !airplay.FeatureEnabled() {
		http.Error(w, "AirPlay feature is disabled", http.StatusNotFound)
		return
	}
	if s.airplay.Status().Running {
		writeJSON(w, map[string]interface{}{"ok": true, "airplay": s.airplayState()})
		return
	}
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	obsBefore := s.obs.Status()
	if obsBefore.Connected {
		http.Error(w, "an OBS stream is already connected", http.StatusConflict)
		return
	}
	wasListening := obsBefore.Listening

	ffmpegPath, err := video.EnsureFFmpeg()
	if err != nil {
		http.Error(w, "FFmpeg is unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	relayConfig, err := s.obsRelayConfig(true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	publishURL, _ := relayConfig["rtmpURL"].(string)
	if strings.TrimSpace(publishURL) == "" {
		http.Error(w, "OBS RTMP publish URL is unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.airplay.Start(s.lifecycleContext(), ffmpegPath, publishURL); err != nil {
		// The receiver was only armed for this attempt. Keep existing OBS
		// behavior if it was already running; otherwise leave it stopped.
		if !wasListening {
			s.obs.Stop()
		}
		if errors.Is(err, airplay.ErrDisabled) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":      true,
		"airplay": s.airplayState(),
		"obs":     s.obsState(),
	})
}

func (s *Server) handleAirPlayEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.airplay == nil {
		http.Error(w, "AirPlay receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !s.airplay.Status().Running {
		writeJSON(w, map[string]interface{}{"ok": true, "airplay": s.airplayState()})
		return
	}
	if !s.airplay.Stop(8 * time.Second) {
		http.Error(w, "AirPlay receiver did not stop before timeout", http.StatusGatewayTimeout)
		return
	}
	if s.obs != nil && s.videoPlayerEnabled() {
		s.obs.Restart(8 * time.Second)
	}
	writeJSON(w, map[string]interface{}{
		"ok":      true,
		"airplay": s.airplayState(),
		"obs":     s.obsState(),
	})
}

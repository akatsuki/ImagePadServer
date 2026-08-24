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
	restoreOBS := func() {
		s.obs.StopContinuousPublishing()
		if obsBefore.Publishing {
			s.obs.StartPublishing()
		}
		if !wasListening {
			s.obs.Stop()
		}
	}

	ffmpegPath, err := video.EnsureFFmpeg()
	if err != nil {
		http.Error(w, "FFmpeg is unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if _, err := s.obsRelayConfig(true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.obs.StartContinuousPublishing()
	// AirPlay runs on this host; use the receiver loopback endpoint instead of
	// the externally advertised LAN address, which may not be locally reachable.
	publishURL := s.obs.InternalPublishURL()
	if strings.TrimSpace(publishURL) == "" {
		restoreOBS()
		http.Error(w, "OBS RTMP publish URL is unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.airplay.Start(s.lifecycleContext(), ffmpegPath, publishURL); err != nil {
		// The receiver was only armed for this attempt. Restore the previous
		// one-shot OBS state instead of leaving an AirPlay-owned arm behind.
		restoreOBS()
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
	if s.obs != nil {
		s.obs.StopContinuousPublishing()
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

func (s *Server) handleAirPlayReconnectTimeout() {
	if s.airplay != nil {
		s.airplay.Stop(8 * time.Second)
	}
	if s.obs != nil && s.videoPlayerEnabled() {
		s.obs.Restart(8 * time.Second)
	}
	s.broadcastStateChanged()
}

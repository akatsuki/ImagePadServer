package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/library"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

func (s *Server) SyncOBSReceiver() {
	if s.obs == nil {
		return
	}
	if s.videoPlayerEnabled() {
		s.obs.Start()
		return
	}
	s.closeRTSPMapping("")
	s.obs.Stop()
}

func (s *Server) createOBSVideoThumbnail(session obsrtmp.Session) string {
	if thumbnail := s.createVideoThumbnail(session.Recording); thumbnail != "" {
		return thumbnail
	}
	playlist := filepath.Join(s.store.Dir(), session.PlaylistName)
	if thumbnail := s.createVideoThumbnail(playlist); thumbnail != "" {
		return thumbnail
	}
	return ""
}

func (s *Server) handleOBSStreamStart(session obsrtmp.Session) {
	info := library.CurrentImage{
		ID:           session.ID,
		Kind:         "video",
		FileName:     filepath.Base(session.Recording),
		PublicName:   "obs-" + session.ID + ".mp4",
		ContentType:  "video/mp4",
		OriginalName: session.Title,
	}
	_ = s.store.SetCurrentInfoWithID(info)
}

func (s *Server) handleOBSStreamDone(session obsrtmp.Session) {
	current := s.store.Current()
	thumbnail := s.createOBSVideoThumbnail(session)
	info := library.CurrentImage{
		ID:           session.ID,
		Kind:         "video",
		FileName:     filepath.Base(session.Recording),
		PublicName:   "obs-" + session.ID + ".mp4",
		ContentType:  "video/mp4",
		OriginalName: session.Title,
		Thumbnail:    thumbnail,
	}
	if current != nil && current.ID == session.ID {
		info = *current
	}
	info.ID = session.ID
	info.Kind = "video"
	info.FileName = filepath.Base(session.Recording)
	info.PublicName = "obs-" + session.ID + ".mp4"
	info.ContentType = "video/mp4"
	info.OriginalName = session.Title
	info.Thumbnail = thumbnail
	if stat, err := os.Stat(session.Recording); err == nil {
		info.SizeBytes = stat.Size()
	}
	if err := s.store.SetCurrentInfoWithID(info); err == nil {
		files := video.GeneratedFiles(s.store.Dir(), session.ID)
		if len(files) > 0 {
			_ = s.store.MarkConverted(session.ID, files)
		}
	}
}

func (s *Server) handleOBSEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.videoPlayerEnabled() {
		http.Error(w, "video player support is disabled", http.StatusBadRequest)
		return
	}
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.obs.Restart(8 * time.Second)
	writeJSON(w, s.state(r))
}

func (s *Server) handleOBSStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.videoPlayerEnabled() {
		http.Error(w, "video player support is disabled", http.StatusBadRequest)
		return
	}
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.obs.StartPublishing()
	writeJSON(w, s.state(r))
}

func (s *Server) handleOBSRelayConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminAllowed := s.adminAllowed(r)
	relayAllowed := false
	if !adminAllowed {
		relayAllowed = s.relayDeviceAllowed(r)
	}
	if !adminAllowed && !relayAllowed {
		http.Error(w, "OBS relay config requires admin access or a paired relay device", http.StatusForbidden)
		return
	}
	if adminAllowed {
		s.rememberAdminToken(w, r)
	}
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	relayConfig, err := s.obsRelayConfig(true, adminAllowed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, relayConfig)
}

func (s *Server) obsRelayConfig(startReceiver bool, persistVideoPlayer bool) (map[string]interface{}, error) {
	if s.obs == nil {
		return nil, fmt.Errorf("OBS receiver is unavailable")
	}
	if persistVideoPlayer {
		if err := s.updateSettings(func(appSettings *settings.Settings) error {
			appSettings.VideoPlayerEnabled = true
			return nil
		}); err != nil {
			return nil, fmt.Errorf("failed to enable video player support")
		}
	}
	if startReceiver {
		s.obs.Start()
		s.obs.StartPublishing()
	}
	status := s.obs.Status()
	serverAddress := strings.TrimRight(status.ServerAddress, "/")
	return map[string]interface{}{
		"ok":                 true,
		"serverAddress":      status.ServerAddress,
		"streamKey":          status.StreamKey,
		"rtmpURL":            serverAddress + "/" + url.PathEscape(status.StreamKey),
		"videoPlayerEnabled": true,
		"listening":          status.Listening,
		"publishing":         status.Publishing,
		"latency":            status.Latency,
	}, nil
}

func (s *Server) handleOBSKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		StreamKey string `json:"streamKey"`
	}
	key := ""
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "invalid OBS stream key request", http.StatusBadRequest)
			return
		}
		key = strings.TrimSpace(req.StreamKey)
	}
	var err error
	if key != "" {
		if err := validateOBSStreamKey(key); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		err = settings.SetOBSStreamKey(key)
	} else {
		key, err = settings.RotateOBSStreamKey()
	}
	if err != nil {
		http.Error(w, "failed to update OBS stream key", http.StatusInternalServerError)
		return
	}
	s.obs.SetStreamKey(key, 8*time.Second)
	writeJSON(w, s.state(r))
}

func validateOBSStreamKey(key string) error {
	if key == "" {
		return fmt.Errorf("OBS Stream Key is required")
	}
	if len(key) > 128 {
		return fmt.Errorf("OBS Stream Key must be 128 characters or fewer")
	}
	for _, r := range key {
		if r <= 0x20 || r == '/' || r == '\\' || r == '?' || r == '#' {
			return fmt.Errorf("OBS Stream Key contains unsupported characters")
		}
	}
	return nil
}

func (s *Server) handleOBSLatency(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.obsState())
	case http.MethodPost:
		var req struct {
			Mode string `json:"mode"`
			DVR  bool   `json:"dvr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid OBS latency request", http.StatusBadRequest)
			return
		}
		mode := obsrtmp.NormalizeLatencyMode(req.Mode)
		if err := s.updateSettings(func(appSettings *settings.Settings) error {
			appSettings.OBSLatencyMode = mode
			appSettings.OBSDVREnabled = false
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		if s.obs != nil {
			s.obs.Restart(8 * time.Second)
		}
		writeJSON(w, s.obsState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) obsMediaActive(id string) bool {
	if s.obs == nil || id == "" {
		return false
	}
	status := s.obs.Status()
	return status.Connected && status.MediaID == id
}

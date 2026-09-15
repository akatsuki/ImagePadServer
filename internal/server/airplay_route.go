package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"imagepadserver/internal/airplay"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

const airPlayReceiverStopTimeout = 15 * time.Second

const (
	airPlayPhaseStopped             = "stopped"
	airPlayPhaseWaitingMedia        = "waiting-media"
	airPlayPhaseMediaActive         = "media-active"
	airPlayPhasePublisherRecovering = "publisher-recovering"
	airPlayPhaseDeliveryFailed      = "delivery-failed"
)

type airPlayQualitySnapshot struct {
	Mode    string
	Height  int
	Preset  video.QualityPreset
	Profile obsrtmp.LatencyProfile
}

func (s *Server) airplayQualityStatusSnapshot() airplay.Status {
	if s.airplayQualityStatus != nil {
		return s.airplayQualityStatus()
	}
	if s.airplay == nil {
		return airplay.Status{}
	}
	return s.airplay.Status()
}

func (s *Server) airplayQualityState() map[string]interface{} {
	desired := captureAirPlayQualitySnapshot()
	if active, managed := s.managedAirPlayDeliveryState(); managed {
		output, err := obsrtmp.ResolveDirectAirPlayOutput(desired.Profile, desired.Preset)
		return map[string]interface{}{
			"managed": true, "sessionID": active.Handle.ID, "generation": active.Generation,
			"desiredMode": desired.Mode, "activeMode": active.Plan.RequestedQualityMode,
			"activeHeight":       active.Plan.EffectiveHeight,
			"desiredLatencyMode": desired.Profile.Mode, "activeLatencyMode": active.Plan.Profile.Mode,
			"changePending":   active.ChangePending,
			"restartRequired": err != nil || desired.Mode != active.Plan.RequestedQualityMode || output != active.Plan.Output || desired.Profile != active.Plan.Profile,
		}
	}
	activeMode := desired.Mode
	activeHeight := desired.Height
	running := s.airplayQualityStatusSnapshot().Running
	if running {
		s.mu.RLock()
		if s.airplayActiveSet {
			activeMode = s.airplayActiveMode
			activeHeight = s.airplayActiveHeight
		}
		s.mu.RUnlock()
	}
	return map[string]interface{}{
		"desiredMode":     desired.Mode,
		"activeMode":      activeMode,
		"activeHeight":    activeHeight,
		"changePending":   false,
		"restartRequired": running && (desired.Mode != activeMode || desired.Height != activeHeight),
	}
}

func captureAirPlayQualitySnapshot() airPlayQualitySnapshot {
	desiredMode := "auto"
	latencyMode := "auto"
	downloadMbps := 0
	uploadMbps := 0
	appSettings, err := settings.Load()
	if err == nil {
		desiredMode = airplay.NormalizeAirPlayQualityMode(appSettings.AirPlayQualityMode)
		latencyMode = appSettings.OBSLatencyMode
		downloadMbps = appSettings.NetworkMbps
		uploadMbps = appSettings.NetworkUploadMbps
	}
	active := airplay.ResolveAirPlayQuality(desiredMode, downloadMbps, uploadMbps)
	profile := obsrtmp.NormalizeLatencyProfile(latencyMode)
	effectiveHeight := active.Height
	if output, resolveErr := obsrtmp.ResolveDirectAirPlayOutput(profile, active); resolveErr == nil {
		effectiveHeight = output.Height
	}
	return airPlayQualitySnapshot{
		Mode: desiredMode, Height: effectiveHeight, Preset: active,
		Profile: profile,
	}
}

func (s *Server) commitAirPlayQualitySnapshot(snapshot airPlayQualitySnapshot) {
	s.mu.Lock()
	s.airplayActiveMode = snapshot.Mode
	s.airplayActiveHeight = snapshot.Height
	s.airplayActiveSet = true
	s.mu.Unlock()
}

func decodeAirPlayQualityMode(r *http.Request) (string, error) {
	var req struct {
		Mode *string `json:"mode"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return "", err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", errors.New("multiple JSON values")
		}
		return "", err
	}
	if req.Mode == nil {
		return "", errors.New("mode is required")
	}
	mode := strings.ToLower(strings.TrimSpace(*req.Mode))
	switch mode {
	case "auto", "360", "720", "1080":
		return mode, nil
	default:
		return "", errors.New("invalid AirPlay quality mode")
	}
}

func (s *Server) handleAirPlayQuality(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{"airplayQuality": s.airplayQualityState()})
	case http.MethodPost:
		mode, err := decodeAirPlayQualityMode(r)
		if err != nil {
			http.Error(w, "invalid AirPlay quality request", http.StatusBadRequest)
			return
		}
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.AirPlayQualityMode = mode
			return nil
		}); err != nil {
			http.Error(w, "failed to save AirPlay quality", http.StatusInternalServerError)
			return
		}
		s.broadcastStateChanged()
		if s.reconfigureAirPlayDelivery(w, r) {
			return
		}
		writeJSON(w, map[string]interface{}{
			"ok":             true,
			"airplayQuality": s.airplayQualityState(),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) airplayState() airplay.Status {
	if s.airplay == nil {
		return airplay.Status{Enabled: false, Phase: airPlayPhaseStopped, Message: "AirPlay受信は利用できません。"}
	}
	status := s.airplay.Status()
	obsConnected := false
	if s.obs != nil {
		obsStatus := s.obs.Status()
		obsConnected = obsStatus.Connected && strings.TrimSpace(obsStatus.MediaID) != ""
	}
	status.Phase = airPlayUIPhase(status, obsConnected)
	return status
}

func airPlayUIPhase(status airplay.Status, obsConnected bool) string {
	if !status.Running {
		return airPlayPhaseStopped
	}
	if !status.ReceiverRunning {
		return airPlayPhaseDeliveryFailed
	}
	if !status.BridgeRunning {
		if strings.Contains(status.Message, "再接続しています") {
			return airPlayPhasePublisherRecovering
		}
		return airPlayPhaseDeliveryFailed
	}
	mediaReady := obsConnected
	if status.MediaReadyKnown {
		mediaReady = status.MediaReady
	}
	if mediaReady {
		return airPlayPhaseMediaActive
	}
	return airPlayPhaseWaitingMedia
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
	if airplay.DirectPipelineEnabled() && s.obs.DirectPublishing() {
		http.Error(w, "previous AirPlay direct cleanup is still pending", http.StatusServiceUnavailable)
		return
	}
	obsBefore := s.obs.Status()
	if obsBefore.Connected {
		http.Error(w, "an OBS stream is already connected", http.StatusConflict)
		return
	}
	qualitySnapshot := captureAirPlayQualitySnapshot()
	wasListening := obsBefore.Listening
	if airplay.DirectPipelineEnabled() {
		deliveryPlan, err := s.obs.NewDirectDeliveryPlan(qualitySnapshot.Profile, qualitySnapshot.Mode, qualitySnapshot.Preset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Direct AirPlay owns its own MediaMTX sidecar. Stop the ordinary OBS
		// listener first so the two lifecycles cannot race for the manager.
		if wasListening || obsBefore.Publishing {
			s.obs.StopAndWait(8 * time.Second)
		}
		if _, err := s.obsRelayConfig(false); err != nil {
			if wasListening {
				s.obs.Start()
				if obsBefore.Publishing {
					s.obs.StartPublishing()
				}
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var direct obsrtmp.DirectPublishInfo
		if airplay.SourceClockPipelineEnabled() {
			direct, err = s.obs.StartManagedDirectPublishingWithPlan(s.lifecycleContext(), deliveryPlan)
		} else {
			direct, err = s.obs.StartDirectPublishingWithPlan(s.lifecycleContext(), deliveryPlan)
		}
		if err != nil {
			if wasListening {
				s.obs.Start()
				if obsBefore.Publishing {
					s.obs.StartPublishing()
				}
			}
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		restoreDirect := func() {
			if !wasListening {
				return
			}
			s.obs.Start()
			if obsBefore.Publishing {
				s.obs.StartPublishing()
			}
		}
		stopDirect := func() {
			if !s.obs.StopDirect(direct.Handle, 8*time.Second) {
				// Keep the ordinary OBS listener stopped until the exact direct
				// session is confirmed gone. The start route remains fail-closed
				// while DirectPublishing reports the old owner.
				return
			}
			restoreDirect()
		}
		output := airplay.DirectOutputConfig{
			Width:            direct.Output.Width,
			Height:           direct.Output.Height,
			SourceFPS:        direct.Output.SourceFPS,
			OutputFPS:        direct.Output.OutputFPS,
			VideoBitrateKbps: direct.Output.VideoBitrateKbps,
			MaxRateKbps:      direct.Output.MaxRateKbps,
			BufferSizeKbps:   direct.Output.BufferSizeKbps,
			AudioBitrateBps:  direct.Output.AudioBitrateBps,
			GOPFrames:        direct.Output.GOPFrames,
		}
		var startErr error
		if airplay.SourceClockPipelineEnabled() {
			startErr = s.airplay.StartSourceClockDirect(s.lifecycleContext(), direct.Session.ID, direct.PublishURL, direct.PublisherArtifactRoot, direct.PublisherObserver, output, stopDirect)
		} else {
			startErr = s.airplay.StartDirect(s.lifecycleContext(), direct.Session.ID, direct.PublishURL, direct.Session.Recording, output, stopDirect)
		}
		if startErr != nil {
			if s.obs.StopDirect(direct.Handle, 8*time.Second) {
				restoreDirect()
			}
			if errors.Is(startErr, airplay.ErrDisabled) {
				http.Error(w, startErr.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, startErr.Error(), http.StatusServiceUnavailable)
			return
		}
		s.commitAirPlayQualitySnapshot(airPlayQualitySnapshot{
			Mode:   direct.Plan.RequestedQualityMode,
			Height: direct.Plan.EffectiveHeight,
			Preset: qualitySnapshot.Preset,
		})
		writeJSON(w, map[string]interface{}{
			"ok":      true,
			"airplay": s.airplayState(),
			"obs":     s.obsState(),
		})
		return
	}
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
	s.commitAirPlayQualitySnapshot(qualitySnapshot)
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
	directOBS := s.obs != nil && s.obs.DirectPublishing()
	if s.obs != nil {
		s.obs.StopContinuousPublishing()
	}
	if !s.airplay.Status().Running {
		if directOBS && s.obs != nil {
			if !s.obs.StopCurrentDirect(8 * time.Second) {
				http.Error(w, "direct OBS publishing did not stop before timeout", http.StatusGatewayTimeout)
				return
			}
		}
		writeJSON(w, map[string]interface{}{"ok": true, "airplay": s.airplayState()})
		return
	}
	if !s.airplay.StopForUser(airPlayReceiverStopTimeout) {
		http.Error(w, "AirPlay receiver did not stop before timeout", http.StatusGatewayTimeout)
		return
	}
	if s.obs != nil && s.videoPlayerEnabled() && !directOBS {
		s.obs.Restart(8 * time.Second)
	}
	writeJSON(w, map[string]interface{}{
		"ok":      true,
		"airplay": s.airplayState(),
		"obs":     s.obsState(),
	})
}

func (s *Server) handleAirPlayRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.airplay == nil {
		http.Error(w, "AirPlay receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.airplay.RetrySourceClockDelivery(); err != nil {
		if errors.Is(err, airplay.ErrDeliveryRetryUnavailable) || errors.Is(err, airplay.ErrDeliveryRetryPending) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "airplay": s.airplayState()})
}

func (s *Server) handleAirPlayReconnectTimeout() {
	directOBS := s.obs != nil && s.obs.DirectPublishing()
	if s.airplay != nil {
		s.airplay.Stop(airPlayReceiverStopTimeout)
	}
	if s.obs != nil && s.videoPlayerEnabled() && !directOBS {
		s.obs.Restart(8 * time.Second)
	}
	s.broadcastStateChanged()
}

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/airplay"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
)

func airPlayQualityResponse(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body struct {
		AirPlayQuality map[string]interface{} `json:"airplayQuality"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode AirPlay quality response: %v; body=%s", err, rr.Body.String())
	}
	if body.AirPlayQuality == nil {
		t.Fatalf("AirPlay quality response is missing airplayQuality: %s", rr.Body.String())
	}
	return body.AirPlayQuality
}

func postAirPlayQuality(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	return adminJSON(t, mux, httptest.NewRequest(http.MethodPost, "/api/airplay/quality", bytes.NewBufferString(body)))
}

func TestAirPlayQualityAPIReadsAndPersistsRequestedMode(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	get := adminJSON(t, mux, httptest.NewRequest(http.MethodGet, "/api/airplay/quality", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET AirPlay quality status = %d, want %d", get.Code, http.StatusOK)
	}

	post := postAirPlayQuality(t, mux, `{"mode":" 720 "}`)
	if post.Code != http.StatusOK {
		t.Fatalf("POST AirPlay quality status = %d, want %d; body=%s", post.Code, http.StatusOK, post.Body.String())
	}
	quality := airPlayQualityResponse(t, post)
	if quality["desiredMode"] != "720" || quality["activeMode"] != "720" {
		t.Fatalf("stopped AirPlay quality = %#v, want desired/active 720", quality)
	}
	if quality["activeHeight"] != float64(720) {
		t.Fatalf("stopped AirPlay activeHeight = %#v, want 720", quality["activeHeight"])
	}
	if quality["restartRequired"] != false || quality["changePending"] != false {
		t.Fatalf("stopped AirPlay quality flags = %#v, want both false", quality)
	}

	saved, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.AirPlayQualityMode != "720" {
		t.Fatalf("saved AirPlay quality mode = %q, want 720", saved.AirPlayQualityMode)
	}
}

func TestAirPlayQualityAPIRejectsEmptyInvalidUnknownAndMalformedBodies(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	for _, body := range []string{`{"mode":""}`, `{"mode":"1440"}`, `{"other":"720"}`, `{broken`} {
		rr := postAirPlayQuality(t, mux, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("POST AirPlay quality body %s status = %d, want %d", body, rr.Code, http.StatusBadRequest)
		}
	}
}

func TestAirPlayQualityStoppedChangeDoesNotStartAirPlayProcess(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	rr := postAirPlayQuality(t, mux, `{"mode":"1080"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("stopped AirPlay quality status = %d, want %d", rr.Code, http.StatusOK)
	}
	if status := srv.airplay.Status(); status.Running {
		t.Fatal("stopped AirPlay quality change started the receiver")
	}
}

func TestAirPlayQualityRunningChangeKeepsActiveSnapshotAndRequiresRestart(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	srv.airplayQualityStatus = func() airplay.Status { return airplay.Status{Running: true} }
	srv.mu.Lock()
	srv.airplayActiveMode = "720"
	srv.airplayActiveHeight = 720
	srv.airplayActiveSet = true
	srv.mu.Unlock()

	rr := postAirPlayQuality(t, mux, `{"mode":"1080"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("running AirPlay quality status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	quality := airPlayQualityResponse(t, rr)
	if quality["desiredMode"] != "1080" || quality["activeMode"] != "720" || quality["activeHeight"] != float64(720) {
		t.Fatalf("running AirPlay quality = %#v, want desired 1080 and active 720p snapshot", quality)
	}
	if quality["restartRequired"] != true || quality["changePending"] != false {
		t.Fatalf("running AirPlay quality flags = %#v, want restartRequired=true/changePending=false", quality)
	}
	if status := srv.airplay.Status(); status.Running {
		t.Fatal("running quality change touched the real AirPlay receiver")
	}
}

func TestAirPlayQualitySaveFailureKeepsActiveSnapshot(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	srv.airplayQualityStatus = func() airplay.Status { return airplay.Status{Running: true} }
	srv.mu.Lock()
	srv.airplayActiveMode = "720"
	srv.airplayActiveHeight = 720
	srv.airplayActiveSet = true
	srv.mu.Unlock()

	badDir := filepath.Join(t.TempDir(), "settings-file")
	if err := os.WriteFile(badDir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_DATA_DIR", badDir)
	rr := postAirPlayQuality(t, mux, `{"mode":"1080"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("failed AirPlay quality save status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
	quality := srv.airplayQualityState()
	if quality["activeMode"] != "720" || quality["activeHeight"] != 720 {
		t.Fatalf("failed save changed active snapshot: %#v", quality)
	}
}

func TestAirPlayQualityStartSnapshotDoesNotRereadChangedSettings(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.AirPlayQualityMode = "720"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	candidate := captureAirPlayQualitySnapshot()
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.AirPlayQualityMode = "1080"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	srv := &Server{airplayQualityStatus: func() airplay.Status { return airplay.Status{Running: true} }}
	srv.commitAirPlayQualitySnapshot(candidate)
	quality := srv.airplayQualityState()
	if quality["desiredMode"] != "1080" || quality["activeMode"] != "720" || quality["activeHeight"] != 720 {
		t.Fatalf("quality state = %#v, want desired 1080 and start snapshot 720p", quality)
	}
	if quality["restartRequired"] != true {
		t.Fatalf("restartRequired = %#v, want true", quality["restartRequired"])
	}
}

func TestAirPlayQualityAutoHeightChangeRequiresNextSession(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.AirPlayQualityMode = "auto"
		appSettings.NetworkUploadMbps = 100
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{airplayQualityStatus: func() airplay.Status { return airplay.Status{Running: true} }}
	srv.commitAirPlayQualitySnapshot(airPlayQualitySnapshot{Mode: "auto", Height: 360})
	quality := srv.airplayQualityState()
	if quality["activeMode"] != "auto" || quality["activeHeight"] != 360 || quality["restartRequired"] != true {
		t.Fatalf("auto effective-height change was not reported as pending: %#v", quality)
	}
}

func TestAirPlayQualityRealtimeCapDoesNotRemainPending(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.AirPlayQualityMode = "1080"
		appSettings.OBSLatencyMode = obsrtmp.LatencyModeRTSPRealtime
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{airplayQualityStatus: func() airplay.Status { return airplay.Status{Running: true} }}
	srv.commitAirPlayQualitySnapshot(airPlayQualitySnapshot{Mode: "1080", Height: 720})
	quality := srv.airplayQualityState()
	if quality["activeHeight"] != 720 || quality["restartRequired"] != false {
		t.Fatalf("realtime 720p cap remained falsely pending: %#v", quality)
	}
}

func TestAirPlayQualityFallbackSnapshotReplacesStaleActiveValue(t *testing.T) {
	badDir := filepath.Join(t.TempDir(), "settings-file")
	if err := os.WriteFile(badDir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_DATA_DIR", badDir)

	srv := &Server{airplayQualityStatus: func() airplay.Status { return airplay.Status{Running: true} }}
	srv.mu.Lock()
	srv.airplayActiveMode = "720"
	srv.airplayActiveHeight = 720
	srv.airplayActiveSet = true
	srv.mu.Unlock()
	srv.commitAirPlayQualitySnapshot(captureAirPlayQualitySnapshot())

	quality := srv.airplayQualityState()
	if quality["activeMode"] != "auto" {
		t.Fatalf("fallback start snapshot retained stale active mode: %#v", quality)
	}
}

func TestAirPlayStateIncludesQualitySnapshot(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	rr := adminJSON(t, mux, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET state status = %d, want %d", rr.Code, http.StatusOK)
	}
	var state map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	quality, ok := state["airplayQuality"].(map[string]interface{})
	if !ok {
		t.Fatalf("state airplayQuality = %#v, want object", state["airplayQuality"])
	}
	for _, key := range []string{"desiredMode", "activeMode", "activeHeight", "changePending", "restartRequired"} {
		if _, ok := quality[key]; !ok {
			t.Errorf("state airplayQuality missing %q: %#v", key, quality)
		}
	}
}

func TestAirPlayStartDisabledIsFailClosed(t *testing.T) {
	t.Setenv("IMAGEPAD_AIRPLAY", "0")
	srv := &Server{airplay: airplay.New(nil)}
	req := httptest.NewRequest(http.MethodPost, "/api/airplay/start", nil)
	rr := httptest.NewRecorder()

	srv.handleAirPlayStart(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled AirPlay start status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if status := srv.airplay.Status(); status.Running {
		t.Fatal("disabled AirPlay start unexpectedly launched a receiver")
	}
}

func TestAirPlayEndIsAllowedWhenFeatureIsDisabled(t *testing.T) {
	t.Setenv("IMAGEPAD_AIRPLAY", "0")
	obs := obsrtmp.New(t.TempDir(), "127.0.0.1", 1935, "test-key", nil, nil, obsrtmp.Callbacks{})
	obs.StartContinuousPublishing()
	srv := &Server{airplay: airplay.New(nil), obs: obs}
	req := httptest.NewRequest(http.MethodPost, "/api/airplay/end", nil)
	rr := httptest.NewRecorder()

	srv.handleAirPlayEnd(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("disabled AirPlay end status = %d, want %d", rr.Code, http.StatusOK)
	}
	if status := srv.airplay.Status(); status.Running {
		t.Fatal("disabled AirPlay end unexpectedly reports a running receiver")
	}
	if status := obs.Status(); status.Publishing {
		t.Fatal("explicit AirPlay end left continuous OBS publishing armed")
	}
}

func TestAirPlayRetryRejectsUnavailableSessionAndWrongMethod(t *testing.T) {
	srv := &Server{airplay: airplay.New(nil)}

	wrongMethod := httptest.NewRecorder()
	srv.handleAirPlayRetry(wrongMethod, httptest.NewRequest(http.MethodGet, "/api/airplay/retry", nil))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("AirPlay retry GET status = %d, want %d", wrongMethod.Code, http.StatusMethodNotAllowed)
	}

	unavailable := httptest.NewRecorder()
	srv.handleAirPlayRetry(unavailable, httptest.NewRequest(http.MethodPost, "/api/airplay/retry", nil))
	if unavailable.Code != http.StatusConflict {
		t.Fatalf("stopped AirPlay retry status = %d, want %d", unavailable.Code, http.StatusConflict)
	}
}

func TestAirPlayUIPhaseUsesValidatedMediaAndPublisherLifecycle(t *testing.T) {
	tests := []struct {
		name         string
		status       airplay.Status
		obsConnected bool
		want         string
	}{
		{name: "stopped", status: airplay.Status{}, want: airPlayPhaseStopped},
		{name: "waiting media", status: airplay.Status{Running: true, ReceiverRunning: true, BridgeRunning: true}, want: airPlayPhaseWaitingMedia},
		{name: "media active", status: airplay.Status{Running: true, ReceiverRunning: true, BridgeRunning: true}, obsConnected: true, want: airPlayPhaseMediaActive},
		{name: "new source clock generation waits despite old OBS connection", status: airplay.Status{Running: true, ReceiverRunning: true, BridgeRunning: true, MediaReadyKnown: true}, obsConnected: true, want: airPlayPhaseWaitingMedia},
		{name: "current source clock generation media active", status: airplay.Status{Running: true, ReceiverRunning: true, BridgeRunning: true, MediaReadyKnown: true, MediaReady: true}, want: airPlayPhaseMediaActive},
		{name: "publisher recovering", status: airplay.Status{Running: true, ReceiverRunning: true, Message: "GStreamerを再接続しています。"}, want: airPlayPhasePublisherRecovering},
		{name: "delivery failed", status: airplay.Status{Running: true, ReceiverRunning: true, Message: "GStreamer publisherが停止しました。"}, want: airPlayPhaseDeliveryFailed},
		{name: "receiver missing", status: airplay.Status{Running: true, BridgeRunning: true}, obsConnected: true, want: airPlayPhaseDeliveryFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := airPlayUIPhase(tt.status, tt.obsConnected); got != tt.want {
				t.Fatalf("airPlayUIPhase() = %q, want %q", got, tt.want)
			}
		})
	}
}

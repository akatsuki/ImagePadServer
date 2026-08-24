package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"imagepadserver/internal/airplay"
	"imagepadserver/internal/obsrtmp"
)

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

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/airplay"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
)

type airPlayDeliveryHTTPFixture struct {
	state obsrtmp.DirectDeliveryState
	apply func(context.Context, obsrtmp.DirectSessionHandle, airPlayQualitySnapshot) (obsrtmp.DirectDeliveryChangeResult, error)
}

func (d *airPlayDeliveryHTTPFixture) State() (obsrtmp.DirectDeliveryState, bool) {
	return d.state, true
}
func (d *airPlayDeliveryHTTPFixture) Reconfigure(ctx context.Context, h obsrtmp.DirectSessionHandle, s airPlayQualitySnapshot) (obsrtmp.DirectDeliveryChangeResult, error) {
	return d.apply(ctx, h, s)
}

func TestAirPlayManagedQualityUsesCommittedState(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	if err := settings.Update(func(s *settings.Settings) error {
		s.OBSLatencyMode = "rtsp-ultra"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv.airplayQualityStatus = func() airplay.Status { return airplay.Status{Running: true} }
	d := &airPlayDeliveryHTTPFixture{state: obsrtmp.DirectDeliveryState{
		Handle: obsrtmp.DirectSessionHandle{ID: "test-session", Generation: 9}, Generation: 1,
		Plan: obsrtmp.DirectDeliveryPlan{RequestedQualityMode: "720", EffectiveHeight: 720, Profile: obsrtmp.NormalizeLatencyProfile("rtsp-ultra")},
	}}
	srv.airplayDelivery = d
	calls := 0
	d.apply = func(ctx context.Context, h obsrtmp.DirectSessionHandle, snapshot airPlayQualitySnapshot) (obsrtmp.DirectDeliveryChangeResult, error) {
		calls++
		if h != d.state.Handle || snapshot.Mode != "1080" {
			t.Fatalf("wrong captured request: %+v %+v", h, snapshot)
		}
		d.state.ChangePending = true
		<-ctx.Done()
		return obsrtmp.DirectDeliveryChangeResult{}, ctx.Err()
	}
	rr := postAirPlayQuality(t, mux, `{"mode":"1080"}`)
	if rr.Code != http.StatusAccepted || calls != 1 {
		t.Fatalf("code=%d calls=%d body=%s", rr.Code, calls, rr.Body.String())
	}
	q := airPlayQualityResponse(t, rr)
	if q["activeMode"] != "720" || q["desiredMode"] != "1080" || q["changePending"] != true {
		t.Fatalf("pending projection=%v", q)
	}
	// A detached HTTP wait must not own the active snapshot. A later GET sees
	// the manager's committed generation without a second settings request.
	d.state.ChangePending = false
	d.state.Generation = 2
	snapshot := captureAirPlayQualitySnapshot()
	plan, err := srv.obs.NewDirectDeliveryPlan(snapshot.Profile, snapshot.Mode, snapshot.Preset)
	if err != nil {
		t.Fatal(err)
	}
	plan.SessionID = d.state.Handle.ID
	d.state.Plan = plan
	get := adminJSON(t, mux, httptest.NewRequest(http.MethodGet, "/api/airplay/quality", nil))
	q = airPlayQualityResponse(t, get)
	if q["activeMode"] != "1080" || q["activeHeight"] != float64(1080) || q["generation"] != float64(2) || q["changePending"] != false || q["restartRequired"] != false {
		t.Fatalf("commit projection=%v", q)
	}
}

func TestAirPlayManagedSaveFailureDoesNotDispatch(t *testing.T) {
	for _, path := range []string{"/api/airplay/quality", "/api/obs/latency"} {
		t.Run(path, func(t *testing.T) {
			srv, mux := testServer(t, true)
			defer cleanupTestServer(srv)
			srv.airplayDelivery = &airPlayDeliveryHTTPFixture{apply: func(context.Context, obsrtmp.DirectSessionHandle, airPlayQualitySnapshot) (obsrtmp.DirectDeliveryChangeResult, error) {
				t.Fatal("failed settings persistence dispatched a delivery change")
				return obsrtmp.DirectDeliveryChangeResult{}, nil
			}}
			badDir := filepath.Join(t.TempDir(), "not-a-directory")
			if err := os.WriteFile(badDir, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("IMAGEPAD_DATA_DIR", badDir)
			mode := "1080"
			if path == "/api/obs/latency" {
				mode = "hls"
			}
			rr := adminJSON(t, mux, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"mode":"`+mode+`"}`)))
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAirPlayManagedNativeDeadlineIsNotHTTPAcceptance(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	srv.airplayDelivery = &airPlayDeliveryHTTPFixture{apply: func(ctx context.Context, _ obsrtmp.DirectSessionHandle, _ airPlayQualitySnapshot) (obsrtmp.DirectDeliveryChangeResult, error) {
		if ctx.Err() != nil {
			t.Fatal("HTTP wait already expired")
		}
		return obsrtmp.DirectDeliveryChangeResult{}, context.DeadlineExceeded
	}}
	rr := postAirPlayQuality(t, mux, `{"mode":"1080"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("native failure reported as acceptance: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAirPlayManagedLatencyFailureKeepsSavedDesiredAndOldActive(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	srv.obs = nil // No external process belongs to this HTTP-boundary unit test.
	d := &airPlayDeliveryHTTPFixture{state: obsrtmp.DirectDeliveryState{
		Handle: obsrtmp.DirectSessionHandle{ID: "same-session", Generation: 7}, Generation: 3,
		Plan: obsrtmp.DirectDeliveryPlan{RequestedQualityMode: "720", EffectiveHeight: 720, Profile: obsrtmp.NormalizeLatencyProfile("rtsp-ultra")},
	}}
	srv.airplayDelivery = d
	calls := 0
	d.apply = func(ctx context.Context, h obsrtmp.DirectSessionHandle, snapshot airPlayQualitySnapshot) (obsrtmp.DirectDeliveryChangeResult, error) {
		calls++
		if h != d.state.Handle || snapshot.Profile.Mode != obsrtmp.LatencyModeHLS {
			t.Fatalf("wrong latency request: %+v", snapshot)
		}
		return obsrtmp.DirectDeliveryChangeResult{}, errors.New("candidate validation failed")
	}
	rr := adminJSON(t, mux, httptest.NewRequest(http.MethodPost, "/api/obs/latency", strings.NewReader(`{"mode":"hls"}`)))
	if rr.Code != http.StatusConflict || calls != 1 {
		t.Fatalf("code=%d calls=%d body=%s", rr.Code, calls, rr.Body.String())
	}
	saved, err := settings.Load()
	if err != nil || saved.OBSLatencyMode != obsrtmp.LatencyModeHLS {
		t.Fatalf("desired not saved: %+v %v", saved, err)
	}
	q := srv.airplayQualityState()
	if q["activeLatencyMode"] != "rtsp-ultra" || q["restartRequired"] != true {
		t.Fatalf("failure hid last commit: %v", q)
	}
}

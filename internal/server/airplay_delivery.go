package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"imagepadserver/internal/airplay"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/video"
)

// HTTP owns only the bounded wait. The manager owns generation allocation,
// commit, compensation and the receiver-session context.
type airPlayDeliveryController interface {
	State() (obsrtmp.DirectDeliveryState, bool)
	Reconfigure(context.Context, obsrtmp.DirectSessionHandle, airPlayQualitySnapshot) (obsrtmp.DirectDeliveryChangeResult, error)
}

type managedAirPlayDelivery struct {
	manager  *obsrtmp.Manager
	receiver *airplay.Manager
}

func (d managedAirPlayDelivery) State() (obsrtmp.DirectDeliveryState, bool) {
	return d.manager.ManagedDirectDeliveryState()
}

func (d managedAirPlayDelivery) Reconfigure(ctx context.Context, handle obsrtmp.DirectSessionHandle, snapshot airPlayQualitySnapshot) (obsrtmp.DirectDeliveryChangeResult, error) {
	plan, err := d.manager.NewDirectDeliveryPlan(snapshot.Profile, snapshot.Mode, snapshot.Preset)
	if err != nil {
		return obsrtmp.DirectDeliveryChangeResult{}, err
	}
	// This is a new request for the captured receiver session, not a new
	// session. The manager validates the complete handle again at admission.
	plan.SessionID = handle.ID
	ffprobe, err := video.ExistingFFprobePath()
	if err != nil {
		return obsrtmp.DirectDeliveryChangeResult{}, err
	}
	return d.manager.ReconfigureManagedDirectDelivery(ctx, handle, plan, ffprobe, d.receiver)
}

func (s *Server) managedAirPlayDeliveryState() (obsrtmp.DirectDeliveryState, bool) {
	if s.airplayDelivery == nil {
		return obsrtmp.DirectDeliveryState{}, false
	}
	return s.airplayDelivery.State()
}

// Returns true when this request belongs to direct AirPlay. That includes
// unsupported/retiring direct sessions: never fall through to ordinary Restart.
func (s *Server) reconfigureAirPlayDelivery(w http.ResponseWriter, r *http.Request) bool {
	state, managed := s.managedAirPlayDeliveryState()
	if !managed {
		if s.obs != nil && s.obs.DirectPublishing() {
			http.Error(w, "AirPlay settings were saved; live delivery change is unavailable", http.StatusConflict)
			return true
		}
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	_, err := s.airplayDelivery.Reconfigure(ctx, state.Handle, captureAirPlayQualitySnapshot())
	if r.Context().Err() != nil {
		return true
	}
	waitExpired := errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded)
	if err != nil && !waitExpired {
		http.Error(w, "AirPlay settings were saved but delivery change failed: "+err.Error(), http.StatusConflict)
		return true
	}
	if waitExpired {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
	}
	writeJSON(w, map[string]interface{}{
		"ok": true, "airplayQuality": s.airplayQualityState(), "obs": s.obsState(),
	})
	return true
}

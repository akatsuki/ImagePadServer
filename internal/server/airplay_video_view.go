package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"imagepadserver/internal/airplay"
	"imagepadserver/internal/airplaycontract"
)

const airPlayVideoViewRequestLimit = 4 << 10

func (s *Server) airplayVideoViewState() airplay.VideoViewState {
	if s.airplay == nil {
		return airplay.VideoViewState{
			Schema:         1,
			ConfiguredMode: airplaycontract.VideoViewContain,
			AppliedMode:    airplaycontract.VideoViewContain,
			Phase:          "idle",
		}
	}
	return s.airplay.VideoViewStatus()
}

func decodeAirPlayVideoViewRequest(w http.ResponseWriter, r *http.Request) (airplay.VideoViewRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, airPlayVideoViewRequestLimit)
	var request airplay.VideoViewRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return airplay.VideoViewRequest{}, err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return airplay.VideoViewRequest{}, errors.New("multiple JSON values")
		}
		return airplay.VideoViewRequest{}, err
	}
	if strings.TrimSpace(string(request.Mode)) == "" {
		return airplay.VideoViewRequest{}, errors.New("mode is required")
	}
	if _, err := airplaycontract.ParseVideoViewMode(string(request.Mode)); err != nil {
		return airplay.VideoViewRequest{}, err
	}
	return request, nil
}

func (s *Server) handleAirPlayVideoView(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{"airplayVideoView": s.airplayVideoViewState()})
	case http.MethodPost:
		request, err := decodeAirPlayVideoViewRequest(w, r)
		if err != nil {
			http.Error(w, "invalid AirPlay video view request", http.StatusBadRequest)
			return
		}
		if s.airplay == nil {
			http.Error(w, "AirPlay receiver is unavailable", http.StatusServiceUnavailable)
			return
		}
		state, err := s.airplay.SetVideoView(r.Context(), request)
		if err != nil {
			switch {
			case errors.Is(err, airplay.ErrVideoViewStale):
				http.Error(w, "AirPlay video view state is stale", http.StatusConflict)
			case errors.Is(err, airplay.ErrVideoViewInvalid):
				http.Error(w, "invalid AirPlay video view request", http.StatusBadRequest)
			case errors.Is(err, airplay.ErrVideoViewPersistence):
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(w, map[string]interface{}{
					"ok":               false,
					"error":            "AirPlay video view was applied live but could not be persisted",
					"airplayVideoView": state,
				})
			default:
				http.Error(w, "failed to save AirPlay video view", http.StatusInternalServerError)
			}
			return
		}
		s.broadcastStateChanged()
		statusCode := http.StatusOK
		if state.Phase == "pending" || state.Phase == "waiting-input" || state.Phase == "waiting_input" {
			statusCode = http.StatusAccepted
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(statusCode)
		writeJSON(w, map[string]interface{}{"ok": true, "airplayVideoView": state})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

package airplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/settings"
)

type VideoViewRequest struct {
	Mode                        airplaycontract.VideoViewMode `json:"mode"`
	ExpectedSessionID           string                        `json:"expectedSessionId"`
	ExpectedPublisherGeneration uint64                        `json:"expectedPublisherGeneration,string"`
	ExpectedRevision            uint64                        `json:"expectedRevision,string"`
}

type VideoViewState struct {
	Schema              int                           `json:"schema"`
	SessionID           string                        `json:"sessionId"`
	PublisherGeneration uint64                        `json:"publisherGeneration,string"`
	ConfiguredRevision  uint64                        `json:"configuredRevision,string"`
	ConfiguredMode      airplaycontract.VideoViewMode `json:"configuredMode"`
	AppliedRevision     uint64                        `json:"appliedRevision,string"`
	AppliedMode         airplaycontract.VideoViewMode `json:"appliedMode"`
	Phase               string                        `json:"phase"`
	OutputPtsNs         uint64                        `json:"outputPtsNs,string"`
	Error               string                        `json:"error,omitempty"`
}

var (
	ErrVideoViewStale   = errors.New("airplay video view state is stale")
	ErrVideoViewInvalid = errors.New("invalid airplay video view request")
)

func defaultVideoViewState() VideoViewState {
	return VideoViewState{Schema: 1, ConfiguredMode: airplaycontract.VideoViewContain, AppliedMode: airplaycontract.VideoViewContain, Phase: "idle"}
}

func writeVideoViewState(path string, state VideoViewState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return atomicReplaceVideoViewFile(tmp, path)
}

func readVideoViewState(path string) (VideoViewState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VideoViewState{}, err
	}
	var state VideoViewState
	if err := json.Unmarshal(data, &state); err != nil {
		return VideoViewState{}, err
	}
	if state.Schema != 1 {
		return VideoViewState{}, fmt.Errorf("unsupported video view schema %d", state.Schema)
	}
	if _, err := airplaycontract.ParseVideoViewMode(string(state.ConfiguredMode)); err != nil {
		return VideoViewState{}, err
	}
	if _, err := airplaycontract.ParseVideoViewMode(string(state.AppliedMode)); err != nil {
		return VideoViewState{}, err
	}
	return state, nil
}

type VideoViewController struct {
	mu    sync.Mutex
	paths airplaycontract.VideoViewPaths
	state VideoViewState
}

func NewVideoViewController(paths airplaycontract.VideoViewPaths, sessionID string, generation uint64) *VideoViewController {
	s := defaultVideoViewState()
	s.SessionID, s.PublisherGeneration = sessionID, generation
	return &VideoViewController{paths: paths, state: s}
}

func (c *VideoViewController) State() VideoViewState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Refresh observes the state sidecar written by the native scheduler.  An
// older session or a regressing revision is ignored so a late file from a
// stopped publisher cannot overwrite the current request.
func (c *VideoViewController) Refresh() VideoViewState {
	c.mu.Lock()
	defer c.mu.Unlock()
	persisted, err := readVideoViewState(c.paths.State)
	if err == nil && persisted.SessionID == c.state.SessionID &&
		persisted.PublisherGeneration == c.state.PublisherGeneration &&
		persisted.ConfiguredRevision >= c.state.ConfiguredRevision &&
		persisted.AppliedRevision >= c.state.AppliedRevision {
		c.state = persisted
	}
	return c.state
}

func (c *VideoViewController) Set(ctxRequest VideoViewRequest) (VideoViewState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	mode, err := airplaycontract.ParseVideoViewMode(string(ctxRequest.Mode))
	if err != nil {
		return c.state, ErrVideoViewInvalid
	}
	if ctxRequest.ExpectedSessionID != "" && ctxRequest.ExpectedSessionID != c.state.SessionID || ctxRequest.ExpectedPublisherGeneration != 0 && ctxRequest.ExpectedPublisherGeneration != c.state.PublisherGeneration || ctxRequest.ExpectedRevision != c.state.ConfiguredRevision {
		return c.state, ErrVideoViewStale
	}
	c.state.ConfiguredRevision++
	c.state.ConfiguredMode = mode
	c.state.Phase = "pending"
	c.state.Error = ""
	if err := os.MkdirAll(filepath.Dir(c.paths.State), 0755); err != nil {
		return c.state, err
	}
	if err := writeVideoViewState(c.paths.State, c.state); err != nil {
		return c.state, err
	}
	return c.state, nil
}

func (c *VideoViewController) Apply(revision uint64, ptsNs uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision != c.state.ConfiguredRevision || revision == 0 {
		return ErrVideoViewStale
	}
	c.state.AppliedRevision, c.state.AppliedMode, c.state.OutputPtsNs = revision, c.state.ConfiguredMode, ptsNs
	c.state.Phase, c.state.Error = "active", ""
	return writeVideoViewState(c.paths.State, c.state)
}

func (m *Manager) VideoViewStatus() VideoViewState {
	m.mu.Lock()
	controller := m.videoView
	m.mu.Unlock()
	if controller != nil {
		return controller.Refresh()
	}
	state := defaultVideoViewState()
	if s, err := settings.Load(); err == nil {
		mode, _ := airplaycontract.ParseVideoViewMode(settings.NormalizeAirPlayVideoViewMode(s.AirPlayVideoViewMode))
		state.ConfiguredMode, state.AppliedMode = mode, mode
	}
	return state
}

func (m *Manager) SetVideoView(ctx context.Context, request VideoViewRequest) (VideoViewState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return m.VideoViewStatus(), ctx.Err()
	default:
	}
	m.mu.Lock()
	controller, running := m.videoView, m.running
	m.mu.Unlock()
	if !running || controller == nil {
		mode, err := airplaycontract.ParseVideoViewMode(string(request.Mode))
		if err != nil {
			return m.VideoViewStatus(), ErrVideoViewInvalid
		}
		if err := settings.Update(func(s *settings.Settings) error { s.AirPlayVideoViewMode = string(mode); return nil }); err != nil {
			return m.VideoViewStatus(), err
		}
		state := m.VideoViewStatus()
		state.ConfiguredMode, state.AppliedMode = mode, mode
		return state, nil
	}
	state, err := controller.Set(request)
	if err != nil {
		return state, err
	}
	if err := settings.Update(func(s *settings.Settings) error { s.AirPlayVideoViewMode = string(state.ConfiguredMode); return nil }); err != nil {
		return state, err
	}
	request.ExpectedRevision = state.ConfiguredRevision
	if err := WriteVideoViewControl(controller.paths.Control, request, state.SessionID, state.PublisherGeneration); err != nil {
		return state, err
	}
	return state, nil
}

func (m *Manager) initializeVideoView(paths airplaycontract.VideoViewPaths, sessionID string, generation uint64) error {
	s, err := settings.Load()
	if err != nil {
		return err
	}
	mode, err := airplaycontract.ParseVideoViewMode(settings.NormalizeAirPlayVideoViewMode(s.AirPlayVideoViewMode))
	if err != nil {
		return err
	}
	c := NewVideoViewController(paths, sessionID, generation)
	c.state.ConfiguredRevision, c.state.ConfiguredMode, c.state.AppliedMode, c.state.Phase = 1, mode, mode, "pending"
	if err := writeVideoViewState(paths.State, c.state); err != nil {
		return err
	}
	if err := WriteVideoViewControl(paths.Control, VideoViewRequest{Mode: mode, ExpectedRevision: 1}, sessionID, generation); err != nil {
		return err
	}
	m.mu.Lock()
	m.videoView = c
	m.mu.Unlock()
	return nil
}

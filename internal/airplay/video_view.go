package airplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	PersistenceState    string                        `json:"persistenceState,omitempty"`
	PersistenceError    string                        `json:"persistenceError,omitempty"`
	Error               string                        `json:"error,omitempty"`
}

var (
	ErrVideoViewStale       = errors.New("airplay video view state is stale")
	ErrVideoViewInvalid     = errors.New("invalid airplay video view request")
	ErrVideoViewPersistence = errors.New("airplay video view persistence failed")
)

func defaultVideoViewState() VideoViewState {
	return VideoViewState{
		Schema:           1,
		ConfiguredMode:   airplaycontract.VideoViewContain,
		AppliedMode:      airplaycontract.VideoViewContain,
		Phase:            "idle",
		PersistenceState: "saved",
	}
}

func writeVideoViewState(path string, state VideoViewState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicWriteVideoViewFile(path, append(data, '\n'))
}

func atomicWriteVideoViewFile(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return atomicReplaceVideoViewFile(temporaryPath, path)
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
		// The native writer owns only applied/clock fields and older native
		// binaries do not know the Go-owned persistence fields. Preserve a
		// pending/failed settings intent when such an ACK arrives.
		if persisted.PersistenceState == "" {
			persisted.PersistenceState = c.state.PersistenceState
			persisted.PersistenceError = c.state.PersistenceError
		}
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
	if ctxRequest.ExpectedSessionID == "" || ctxRequest.ExpectedPublisherGeneration == 0 {
		return c.state, ErrVideoViewInvalid
	}
	if ctxRequest.ExpectedSessionID != c.state.SessionID ||
		ctxRequest.ExpectedPublisherGeneration != c.state.PublisherGeneration ||
		ctxRequest.ExpectedRevision != c.state.ConfiguredRevision {
		return c.state, ErrVideoViewStale
	}
	next := c.state
	next.ConfiguredRevision++
	next.ConfiguredMode = mode
	next.Phase = "pending"
	next.PersistenceState = "pending"
	next.PersistenceError = ""
	next.Error = ""
	if err := os.MkdirAll(filepath.Dir(c.paths.State), 0755); err != nil {
		return c.state, err
	}
	if err := writeVideoViewState(c.paths.State, next); err != nil {
		return c.state, err
	}
	c.state = next
	return c.state, nil
}

func (c *VideoViewController) Apply(revision uint64, ptsNs uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision != c.state.ConfiguredRevision || revision == 0 {
		return ErrVideoViewStale
	}
	next := c.state
	next.AppliedRevision, next.AppliedMode, next.OutputPtsNs = revision, next.ConfiguredMode, ptsNs
	next.Phase, next.Error = "active", ""
	if err := writeVideoViewState(c.paths.State, next); err != nil {
		return err
	}
	c.state = next
	return nil
}

func (c *VideoViewController) markPersistence(state, persistenceErr string) (VideoViewState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.state
	next.PersistenceState = state
	next.PersistenceError = persistenceErr
	if err := writeVideoViewState(c.paths.State, next); err != nil {
		c.state = next
		return c.state, err
	}
	c.state = next
	return c.state, nil
}

func (c *VideoViewController) markPersistenceSaved() (VideoViewState, error) {
	return c.markPersistence("saved", "")
}

func (c *VideoViewController) markPersistenceFailed(err error) (VideoViewState, error) {
	message := "video view persistence failed"
	if err != nil {
		message = err.Error()
	}
	return c.markPersistence("failed", message)
}

func videoViewRetryMatches(state VideoViewState, request VideoViewRequest) bool {
	return state.PersistenceState != "saved" &&
		request.ExpectedSessionID != "" &&
		request.ExpectedSessionID == state.SessionID &&
		request.ExpectedPublisherGeneration != 0 &&
		request.ExpectedPublisherGeneration == state.PublisherGeneration &&
		request.ExpectedRevision != 0 &&
		request.ExpectedRevision == state.ConfiguredRevision &&
		request.Mode == state.ConfiguredMode
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
	current := controller.Refresh()
	if videoViewRetryMatches(current, request) {
		if err := WriteVideoViewControl(controller.paths.Control, VideoViewRequest{
			Mode:             current.ConfiguredMode,
			ExpectedRevision: current.ConfiguredRevision,
		}, current.SessionID, current.PublisherGeneration); err != nil {
			state, markErr := controller.markPersistenceFailed(err)
			return state, errors.Join(ErrVideoViewPersistence, err, markErr)
		}
		if err := settings.Update(func(s *settings.Settings) error {
			s.AirPlayVideoViewMode = string(current.ConfiguredMode)
			return nil
		}); err != nil {
			state, markErr := controller.markPersistenceFailed(err)
			return state, errors.Join(ErrVideoViewPersistence, err, markErr)
		}
		return controller.markPersistenceSaved()
	}
	state, err := controller.Set(request)
	if err != nil {
		return state, err
	}
	request.ExpectedRevision = state.ConfiguredRevision
	if err := WriteVideoViewControl(controller.paths.Control, request, state.SessionID, state.PublisherGeneration); err != nil {
		failed, markErr := controller.markPersistenceFailed(err)
		return failed, errors.Join(ErrVideoViewPersistence, err, markErr)
	}
	if err := settings.Update(func(s *settings.Settings) error {
		s.AirPlayVideoViewMode = string(state.ConfiguredMode)
		return nil
	}); err != nil {
		failed, markErr := controller.markPersistenceFailed(err)
		return failed, errors.Join(ErrVideoViewPersistence, err, markErr)
	}
	return controller.markPersistenceSaved()
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

// prepareVideoViewGeneration writes the current display intent to a new
// publisher generation without publishing the controller pointer yet. This
// keeps a failed candidate isolated: its native process can consume the
// generation-scoped control file, while status requests continue to observe
// the committed publisher until the delivery transaction succeeds.
func (m *Manager) prepareVideoViewGeneration(paths airplaycontract.VideoViewPaths, sessionID string, generation uint64) (*VideoViewController, error) {
	if strings.TrimSpace(sessionID) == "" || generation == 0 {
		return nil, errors.New("video view generation identity is incomplete")
	}
	m.mu.Lock()
	current := m.videoView
	m.mu.Unlock()
	state := defaultVideoViewState()
	if current != nil {
		state = current.Refresh()
	}
	if state.ConfiguredRevision == 0 {
		s, err := settings.Load()
		if err != nil {
			return nil, err
		}
		mode, err := airplaycontract.ParseVideoViewMode(settings.NormalizeAirPlayVideoViewMode(s.AirPlayVideoViewMode))
		if err != nil {
			return nil, err
		}
		state.ConfiguredRevision = 1
		state.ConfiguredMode = mode
	}
	state.SessionID = sessionID
	state.PublisherGeneration = generation
	state.AppliedRevision = 0
	state.AppliedMode = state.ConfiguredMode
	state.Phase = "pending"
	state.Error = ""
	c := NewVideoViewController(paths, sessionID, generation)
	c.state = state
	if err := os.MkdirAll(filepath.Dir(paths.State), 0755); err != nil {
		return nil, err
	}
	if err := writeVideoViewState(paths.State, state); err != nil {
		return nil, err
	}
	if err := WriteVideoViewControl(paths.Control, VideoViewRequest{
		Mode:             state.ConfiguredMode,
		ExpectedRevision: state.ConfiguredRevision,
	}, sessionID, generation); err != nil {
		return nil, err
	}
	return c, nil
}

func (m *Manager) adoptVideoViewGeneration(controller *VideoViewController) {
	if controller == nil {
		return
	}
	m.mu.Lock()
	m.videoView = controller
	m.mu.Unlock()
}

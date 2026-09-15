package obsrtmp

import (
	"errors"
	"sync"
)

var (
	errDirectBackendRouterInvalidRoute           = errors.New("direct backend route is invalid")
	errDirectBackendRouterGenerationNotMonotonic = errors.New("direct backend generation is not monotonic")
	errDirectBackendRouterStaleCommit            = errors.New("direct backend commit has stale active identity")
	errDirectBackendRouterForeignSession         = errors.New("direct backend route belongs to another session")
	errDirectBackendRouterTerminal               = errors.New("direct backend router is terminal")
	errDirectBackendRouterDraining               = errors.New("direct backend router is draining")
	errDirectBackendRouterNotDraining            = errors.New("direct backend router is not draining")
)

// directBackendRoute is immutable after construction. The runtime pointer is
// only an identity-bearing reference here; starting or stopping it belongs to
// a later coordinator step.
type directBackendRoute struct {
	sessionID       string
	sessionEpoch    uint64
	generation      uint64
	requestID       string
	privateRTSPPort int
	runtime         *mediaMTXRuntime
}

type directBackendRouter struct {
	mu       sync.RWMutex
	active   directBackendRoute
	terminal bool
	draining bool
	failed   bool
}

func newDirectBackendRouter(initial directBackendRoute) (*directBackendRouter, error) {
	if err := validateDirectBackendRoute(initial); err != nil {
		return nil, err
	}
	return &directBackendRouter{active: initial}, nil
}

func newLegacyDirectBackendRouter(privateRTSPPort int) *directBackendRouter {
	// Legacy gate construction keeps the historical start-time validation in
	// rtspGate.start and is the only unchecked router construction path.
	return &directBackendRouter{active: directBackendRoute{
		sessionID:       "legacy-rtsp-gate",
		sessionEpoch:    1,
		generation:      1,
		requestID:       "legacy-initial",
		privateRTSPPort: privateRTSPPort,
	}}
}

func (r *directBackendRouter) snapshot() directBackendRoute {
	r.mu.RLock()
	active := r.active
	r.mu.RUnlock()
	return active
}

// prepare validates a candidate without changing the active route.
func (r *directBackendRouter) prepare(candidate directBackendRoute) (directBackendRoute, error) {
	r.mu.RLock()
	active := r.active
	terminal := r.terminal
	draining := r.draining
	r.mu.RUnlock()
	if terminal {
		return directBackendRoute{}, errDirectBackendRouterTerminal
	}
	if draining {
		return directBackendRoute{}, errDirectBackendRouterDraining
	}
	if candidate.sessionID != active.sessionID || candidate.sessionEpoch != active.sessionEpoch {
		return directBackendRoute{}, errDirectBackendRouterForeignSession
	}
	if candidate.generation == 0 || candidate.generation <= active.generation {
		return directBackendRoute{}, errDirectBackendRouterGenerationNotMonotonic
	}
	if err := validateDirectBackendRoute(candidate); err != nil {
		return directBackendRoute{}, err
	}
	return candidate, nil
}

// commit is the single active-route mutation. The expected route is compared
// as a complete value, including request and runtime identity.
func (r *directBackendRouter) commit(expected, candidate directBackendRoute) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal {
		return errDirectBackendRouterTerminal
	}
	if r.draining {
		return errDirectBackendRouterDraining
	}
	if expected != r.active {
		return errDirectBackendRouterStaleCommit
	}
	if candidate.sessionID != r.active.sessionID || candidate.sessionEpoch != r.active.sessionEpoch {
		return errDirectBackendRouterForeignSession
	}
	if candidate.generation == 0 || candidate.generation <= r.active.generation {
		return errDirectBackendRouterGenerationNotMonotonic
	}
	if err := validateDirectBackendRoute(candidate); err != nil {
		return err
	}
	r.active = candidate
	r.failed = false
	return nil
}

// beginDrain reserves the current active route for a gate-owned drain. It
// changes only the router state, never the active route identity.
func (r *directBackendRouter) beginDrain(expected directBackendRoute) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal {
		return errDirectBackendRouterTerminal
	}
	if expected != r.active {
		return errDirectBackendRouterStaleCommit
	}
	if r.draining {
		return errDirectBackendRouterDraining
	}
	r.draining = true
	return nil
}

// commitDrained is the drain-only CAS. The route cannot change until the
// caller has confirmed that all connections for expected are closed.
func (r *directBackendRouter) commitDrained(expected, candidate directBackendRoute) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal {
		return errDirectBackendRouterTerminal
	}
	if expected != r.active {
		return errDirectBackendRouterStaleCommit
	}
	if !r.draining {
		return errDirectBackendRouterNotDraining
	}
	if candidate.sessionID != r.active.sessionID || candidate.sessionEpoch != r.active.sessionEpoch {
		return errDirectBackendRouterForeignSession
	}
	if candidate.generation == 0 || candidate.generation <= r.active.generation {
		return errDirectBackendRouterGenerationNotMonotonic
	}
	if err := validateDirectBackendRoute(candidate); err != nil {
		return err
	}
	r.active = candidate
	r.draining = false
	r.failed = false
	return nil
}

func (r *directBackendRouter) abortDrain(expected directBackendRoute) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if expected != r.active {
		return errDirectBackendRouterStaleCommit
	}
	if !r.draining {
		return errDirectBackendRouterNotDraining
	}
	r.draining = false
	return nil
}

func (r *directBackendRouter) canAccept() bool {
	r.mu.RLock()
	accept := !r.terminal && !r.draining && !r.failed
	r.mu.RUnlock()
	return accept
}

func (r *directBackendRouter) markTerminal() {
	r.mu.Lock()
	r.terminal = true
	r.mu.Unlock()
}

func validateDirectBackendRoute(route directBackendRoute) error {
	if route.sessionID == "" || route.sessionEpoch == 0 || route.generation == 0 || route.requestID == "" ||
		route.privateRTSPPort <= 0 || route.privateRTSPPort > 65535 {
		return errDirectBackendRouterInvalidRoute
	}
	return nil
}

// Caller holds Manager.mu. Once a direct session adopts the strict router,
// RTSP, HLS and status probes use the same committed backend identity. m.mtx
// remains the legacy runtime reference and must not serve stale candidate data.
func (m *Manager) activeMediaMTXRuntimeLocked() *mediaMTXRuntime {
	if !m.directPublishing || m.rtspGate == nil || m.rtspGate.backendRouter == nil {
		return m.mtx
	}
	router := m.rtspGate.backendRouter
	route := router.snapshot()
	if route.sessionID == "legacy-rtsp-gate" {
		return m.mtx
	}
	if !router.canAccept() || route.sessionID != m.directHandle.ID ||
		route.sessionEpoch != m.directHandle.Generation || m.listenerGeneration != m.directHandle.Generation {
		return nil
	}
	return route.runtime
}

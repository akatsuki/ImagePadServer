package obsrtmp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"imagepadserver/internal/airplaycontract"
)

var errDirectReconfigureUnavailable = errors.New("direct reconfigure session is unavailable")

// One coordinator belongs to one strict receiver/session epoch. Its context
// is the session context, never an HTTP caller's wait context.
type directDeliveryCoordinator struct {
	mu                sync.Mutex
	ctx               context.Context
	manager           *Manager
	gate              *rtspGate
	ledger            *airplaycontract.DeliveryLedger
	observer          *directPublisherObserver
	active            airplaycontract.DeliveryGeneration
	activePlan        DirectDeliveryPlan
	publicState       atomic.Pointer[DirectDeliveryState]
	transactionActive bool
	recoveryBudget    airplaycontract.RecoveryBudget
	recoveryAttempted map[uint64]bool
	pending           *directReconfigureAttempt
}

type directReconfigureAttempt struct {
	descriptor            airplaycontract.DeliveryGeneration
	plan                  DirectDeliveryPlan
	expected              directBackendRoute
	backend               *directBackendGeneration
	backendStartAttempted bool
	commitDrain           *rtspGateDrainHandle
	compensation          bool
}

func (c *directDeliveryCoordinator) CommitDirectReconfigure(attempt *directReconfigureAttempt, oldEvents, candidateEvents []airplaycontract.Event, output directBackendOutputProof, deadline time.Time) error {
	if err := c.prepareDirectCommit(c.ctx, attempt, oldEvents, candidateEvents, output, deadline); err != nil {
		return err
	}
	return c.commitPreparedDirectReconfigure(attempt, deadline)
}

func (c *directDeliveryCoordinator) prepareDirectCommit(ctx context.Context, attempt *directReconfigureAttempt, oldEvents, candidateEvents []airplaycontract.Event, output directBackendOutputProof, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if attempt == nil || c.pending != attempt || attempt.backend == nil || !attempt.backendStartAttempted || attempt.commitDrain != nil {
		return airplaycontract.ErrDeliveryGenerationStale
	}
	if _, err := airplaycontract.NewSourceClockPostWatermarkProof(oldEvents, candidateEvents, attempt.descriptor.SessionID, attempt.expected.generation, attempt.descriptor.Generation); err != nil {
		return err
	}
	if !output.matches(attempt.descriptor, attempt.backend.route) {
		return errDirectBackendOutputInvalid
	}
	if deadline.IsZero() || !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	route, err := c.activeRoute()
	if err != nil {
		return err
	}
	if route != attempt.expected {
		return airplaycontract.ErrDeliveryGenerationStale
	}
	drain, err := c.gate.beginDrain(attempt.expected)
	if err != nil {
		return err
	}
	// The gate owns connection shutdown. Never publish the new route while an
	// old connection can still send through it; cancellation cannot skip drain.
	timeout := min(10*time.Second, time.Until(deadline))
	timer := time.NewTimer(max(timeout, time.Nanosecond))
	defer timer.Stop()
	select {
	case <-drain.done:
	case <-ctx.Done():
		err = ctx.Err()
	case <-c.ctx.Done():
		err = c.ctx.Err()
	case <-timer.C:
		err = errRTSPGateDrainTimeout
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = c.ctx.Err()
	}
	if err != nil {
		// Reopen only our drain of the still-old route. A terminal gate stays
		// terminal; a later owner's drain cannot be released here.
		_ = c.gate.abortDrain(drain)
		return err
	}
	attempt.commitDrain = drain
	return nil
}

func (c *directDeliveryCoordinator) commitPreparedDirectReconfigure(attempt *directReconfigureAttempt, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if attempt == nil || c.pending != attempt || attempt.commitDrain == nil {
		return airplaycontract.ErrDeliveryGenerationStale
	}
	err := c.commitDrained(attempt, deadline)
	if err != nil {
		_ = c.gate.abortDrain(attempt.commitDrain)
	}
	attempt.commitDrain = nil
	return err
}

// Lock order: coordinator -> Manager -> gate -> router -> observer -> runtime
// -> ledger. No process wait/probe or callback runs under these locks. Ledger
// commit is the final fallible operation: route and public snapshots then move
// together, so an aborted ledger cannot leave the gate pointing at a candidate.
func (c *directDeliveryCoordinator) commitDrained(a *directReconfigureAttempt, deadline time.Time) error {
	m, g, o := c.manager, c.gate, c.observer
	m.mu.Lock()
	defer m.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	router := g.backendRouter
	router.mu.Lock()
	defer router.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	runtime := a.backend.runtime
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if !m.running || !m.directPublishing || m.rtspGate != g || m.current == nil || m.current.ID != a.descriptor.SessionID ||
		m.directHandle.ID != a.descriptor.SessionID || m.directHandle.Generation != a.descriptor.SessionEpoch || m.listenerGeneration != a.descriptor.SessionEpoch ||
		router.terminal || !router.draining || router.active != a.expected || g.draining == nil || g.draining != a.commitDrain || g.draining.expected != a.expected {
		return errDirectReconfigureUnavailable
	}
	select {
	case <-g.draining.done:
	default:
		return errRTSPGateDrainIncomplete
	}
	reservation := m.directBackendReservations[runtime]
	if reservation == nil || reservation.stopping || runtime.proc == nil || runtime.stopped || runtime.retiring {
		return errDirectReconfigureUnavailable
	}
	select {
	case <-reservation.started:
	default:
		return errDirectReconfigureUnavailable
	}
	if reservation.startErr != nil {
		return reservation.startErr
	}
	registered, registeredOK := o.deliveryGenerations[a.descriptor.Generation]
	ready := o.generationReadinessState[a.descriptor.Generation]
	old, oldCompleted := o.completions[a.expected.generation]
	_, candidateCompleted := o.completions[a.descriptor.Generation]
	if o.sealed || !registeredOK || registered != a.descriptor || !ready.PublisherReady || !ready.MediaReady ||
		!oldCompleted || !old.Started || !old.ExitConfirmed || candidateCompleted {
		return errDirectReconfigureUnavailable
	}
	if a.backend.descriptor != a.descriptor || a.backend.route.runtime != runtime || a.backend.route.generation != a.descriptor.Generation ||
		a.backend.route.sessionID != a.descriptor.SessionID || a.backend.route.sessionEpoch != a.descriptor.SessionEpoch || a.backend.route.requestID != a.descriptor.RequestID {
		return airplaycontract.ErrDeliveryGenerationMismatch
	}
	if !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	if err := c.ledger.Commit(a.expected.generation, a.descriptor); err != nil {
		return err
	}
	router.active, router.draining, router.failed = a.backend.route, false, false
	g.draining = nil
	contract := OBSActiveSessionContract{}
	if m.current.ActiveContract != nil {
		contract = *m.current.ActiveContract
	}
	contract.IngestURL = runtime.directPublishURL()
	contract.Port = runtime.cfg.Ports.BackendRTSP
	contract.LatencyProfile, contract.QualityPreset = a.plan.Profile, a.plan.qualityPreset
	m.current.ActiveContract = &contract
	m.current.Recording = a.descriptor.ArtifactPaths.Recording
	o.candidateGeneration = a.descriptor.Generation
	m.status.Latency = a.plan.Profile
	m.status.Connected, m.status.Publishing = true, true
	if m.rtspEndpoint != nil {
		endpoint := directRTSPEndpoint(runtime, a.descriptor.SessionID)
		endpoint.Generation = m.rtspEndpoint.Generation
		m.rtspEndpoint = &endpoint
	}
	c.active, c.pending = a.descriptor, nil
	c.activePlan = a.plan
	c.publishDeliveryStateLocked()
	return nil
}

func (c *directDeliveryCoordinator) PrepareDirectReconfigure(expected uint64, plan DirectDeliveryPlan) (*directReconfigureAttempt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prepareDirectReconfigureLocked(expected, plan)
}

func (c *directDeliveryCoordinator) prepareDirectReconfigureLocked(expected uint64, plan DirectDeliveryPlan) (*directReconfigureAttempt, error) {
	if c.pending != nil {
		return nil, airplaycontract.ErrDeliveryGenerationPending
	}
	if err := validateDirectDeliveryPlan(plan); err != nil {
		return nil, err
	}
	if expected != c.active.Generation || plan.SessionID != c.active.SessionID {
		return nil, airplaycontract.ErrDeliveryGenerationStale
	}
	route, err := c.activeRoute()
	if err != nil {
		return nil, err
	}
	profile := directMediaMTXConfig(route.runtime.cfg.Path, "", "", route.runtime.cfg.Ports, "", "", plan.Profile)
	descriptor, err := c.ledger.Allocate(c.active.SessionID, c.active.SessionEpoch, expected, plan.RequestID,
		airplaycontract.DeliveryProfile{Mode: plan.Profile.Mode, Transport: plan.Profile.Transport,
			HLSVariant: profile.HLSVariant, HLSSegmentCount: profile.HLSSegmentCount, HLSSegmentDuration: profile.HLSSegmentDuration},
		airplaycontract.DeliveryOutput(plan.Output))
	if err != nil {
		return nil, err
	}
	attempt := &directReconfigureAttempt{descriptor: descriptor, plan: plan, expected: route}
	c.pending = attempt
	fail := func(err error) (*directReconfigureAttempt, error) {
		return nil, errors.Join(err, c.abortLocked(attempt))
	}
	if err := c.observer.PrepareDeliveryGeneration(c.ctx, descriptor); err != nil {
		return fail(err)
	}
	ports, err := allocMediaMTXPorts(route.runtime.cfg.Ports)
	if err != nil {
		return fail(err)
	}
	attempt.backend, err = newDirectBackendGeneration(route.runtime.exe, descriptor, route.runtime.cfg, ports)
	if err != nil {
		return fail(err)
	}
	if _, err := c.activeRoute(); err != nil {
		return fail(err)
	}
	return attempt, nil
}

func (c *directDeliveryCoordinator) AbortDirectReconfigure(attempt *directReconfigureAttempt) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.abortLocked(attempt)
}

func (c *directDeliveryCoordinator) StartDirectReconfigureBackend(attempt *directReconfigureAttempt) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if attempt == nil || c.pending != attempt || attempt.backend == nil {
		return "", airplaycontract.ErrDeliveryGenerationStale
	}
	if attempt.backendStartAttempted {
		return "", errDirectBackendGenerationUsed
	}
	fail := func(err error) (string, error) { return "", errors.Join(err, c.abortLocked(attempt)) }
	route, err := c.activeRoute()
	if err != nil {
		return fail(err)
	}
	if route != attempt.expected {
		return fail(airplaycontract.ErrDeliveryGenerationStale)
	}
	attempt.backendStartAttempted = true
	if err := attempt.backend.startWithOwner(c.ctx, c.manager); err != nil {
		return fail(err)
	}
	route, err = c.activeRoute()
	if err != nil {
		return fail(err)
	}
	if route != attempt.expected {
		return fail(airplaycontract.ErrDeliveryGenerationStale)
	}
	return attempt.backend.runtime.directPublishURL(), nil
}

// Abort closes only the candidate backend. The publisher owner must confirm
// publisher exit separately before any compensation attempt may start. A
// backend stop timeout retains both pending state and manager ownership.
func (c *directDeliveryCoordinator) abortLocked(attempt *directReconfigureAttempt) error {
	if attempt == nil || c.pending != attempt {
		return airplaycontract.ErrDeliveryGenerationStale
	}
	if attempt.backend != nil {
		if err := c.manager.stopOwnedDirectBackend(attempt.backend.runtime, 5*time.Second); err != nil {
			return err
		}
	}
	if attempt.commitDrain != nil {
		if err := c.gate.abortDrain(attempt.commitDrain); err != nil {
			return err
		}
		attempt.commitDrain = nil
	}
	err := c.ledger.Abort(attempt.descriptor)
	if err != nil && !errors.Is(err, airplaycontract.ErrDeliveryLedgerTerminal) {
		return err
	}
	c.pending = nil
	return nil
}

// c.mu is held by the serial transaction. This check does not authorize a
// later commit: callers must recheck after slow work and at commit itself.
func (c *directDeliveryCoordinator) activeRoute() (directBackendRoute, error) {
	if c.ctx == nil || c.manager == nil || c.gate == nil || c.gate.backendRouter == nil || c.ledger == nil || c.observer == nil || c.observer.deliveryLedger != c.ledger {
		return directBackendRoute{}, errDirectReconfigureUnavailable
	}
	if err := c.ctx.Err(); err != nil {
		return directBackendRoute{}, err
	}
	c.manager.mu.Lock()
	defer c.manager.mu.Unlock()
	router := c.gate.backendRouter
	router.mu.RLock()
	defer router.mu.RUnlock()
	m := c.manager
	route := router.active
	if !m.running || !m.directPublishing || m.rtspGate != c.gate || router.terminal || router.draining ||
		m.directHandle.ID != c.active.SessionID || m.directHandle.Generation != c.active.SessionEpoch || m.listenerGeneration != c.active.SessionEpoch ||
		route.sessionID != c.active.SessionID || route.sessionEpoch != c.active.SessionEpoch || route.generation != c.active.Generation || route.requestID != c.active.RequestID || route.runtime == nil {
		return directBackendRoute{}, errDirectReconfigureUnavailable
	}
	snapshot := c.ledger.Snapshot()
	if snapshot.TerminalEpoch != 0 || snapshot.ActiveGeneration != c.active.Generation || snapshot.SessionID != c.active.SessionID || snapshot.SessionEpoch != c.active.SessionEpoch {
		return directBackendRoute{}, airplaycontract.ErrDeliveryGenerationStale
	}
	return route, nil
}

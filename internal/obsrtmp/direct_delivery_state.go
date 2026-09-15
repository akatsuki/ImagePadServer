package obsrtmp

// DirectDeliveryState contains only immutable last-committed settings. It is
// not proof of current media readiness or permission to commit another change.
type DirectDeliveryState struct {
	Handle        DirectSessionHandle
	Plan          DirectDeliveryPlan
	Generation    uint64
	ChangePending bool
}

func (m *Manager) ManagedDirectDeliveryState() (DirectDeliveryState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.directDelivery
	if !m.running || !m.directPublishing || c == nil || c.ctx.Err() != nil {
		return DirectDeliveryState{}, false
	}
	state := c.publicState.Load()
	if state == nil || state.Handle != m.directHandle {
		return DirectDeliveryState{}, false
	}
	return *state, true
}

// Caller holds coordinator.mu (or has not exposed a newly created owner yet).
// Never acquire coordinator.mu in the HTTP state path: it can be held while
// waiting for an old public connection to drain. Store is safe under commit's
// existing Manager lock and publishes the plan only after the ledger commit.
func (c *directDeliveryCoordinator) publishDeliveryStateLocked() {
	c.publicState.Store(&DirectDeliveryState{
		Handle: DirectSessionHandle{ID: c.active.SessionID, Generation: c.active.SessionEpoch},
		Plan:   c.activePlan, Generation: c.active.Generation,
		ChangePending: c.transactionActive,
	})
}

func (m *Manager) notifyManagedDeliveryChanged() {
	m.mu.Lock()
	callback := m.cb.OnDeliveryChanged
	onReady := m.cb.OnRTSPReady
	endpoint := m.prepareManagedRTSPPublicationLocked()
	m.mu.Unlock()
	if endpoint != nil && onReady != nil {
		onReady(*endpoint)
	}
	if callback != nil {
		callback()
	}
}

// HLS already has a local gate, but must not request external publication.
// Only the first committed RTSP selection announces that unchanged gate.
// Caller holds Manager.mu; callbacks must run after releasing it.
func (m *Manager) prepareManagedRTSPPublicationLocked() *RTSPEndpoint {
	c := m.directDelivery
	if !m.running || !m.directPublishing || !m.status.Publishing || m.current == nil ||
		m.rtspEndpoint != nil || !m.currentSessionUsesRTSPTLocked() || c == nil || c.ctx.Err() != nil ||
		m.rtspGate == nil || m.rtspGate != c.gate || m.rtspGate.backendRouter == nil {
		return nil
	}
	state := c.publicState.Load()
	if state == nil || state.ChangePending || state.Handle != m.directHandle ||
		state.Handle.ID != m.current.ID || state.Handle.Generation != m.listenerGeneration {
		return nil
	}
	router := m.rtspGate.backendRouter
	router.mu.RLock()
	defer router.mu.RUnlock()
	route := router.active
	if router.terminal || router.draining || router.failed || route.runtime == nil ||
		route.sessionID != state.Handle.ID || route.sessionEpoch != state.Handle.Generation || route.generation != state.Generation {
		return nil
	}
	endpoint := directRTSPEndpoint(route.runtime, m.current.ID)
	endpoint.Generation = m.current.Generation
	m.rtspEndpoint = &endpoint
	m.status.Message = "RTSP TCPストリームを準備しました。外部公開を待っています。"
	return &endpoint
}

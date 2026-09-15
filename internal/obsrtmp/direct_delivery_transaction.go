package obsrtmp

import (
	"context"
	"errors"

	"imagepadserver/internal/airplaycontract"
)

type DirectDeliveryExecutor interface {
	ApplySourceClockDelivery(context.Context, airplaycontract.CandidateDelivery) error
	RecoverSourceClockDelivery(context.Context, airplaycontract.CandidateDelivery) error
}

type DirectDeliveryChangeResult struct {
	// Plan/Generation describe the last committed contract, not a claim that
	// a failed delivery is still producing media. Restored keeps the original
	// request error: the requested settings were not applied.
	Applied    bool
	Restored   bool
	Generation uint64
	Plan       DirectDeliveryPlan
}

// ReconfigureManagedDirectDelivery serializes planned changes and one optional
// restoration in a receiver-session-owned transaction. It is intentionally not
// the crash-retry entry point and does not restart the AirPlay receiver.
func (m *Manager) ReconfigureManagedDirectDelivery(waitContext context.Context, handle DirectSessionHandle, plan DirectDeliveryPlan, ffprobe string, executor DirectDeliveryExecutor) (DirectDeliveryChangeResult, error) {
	if waitContext == nil {
		waitContext = context.Background()
	}
	if err := waitContext.Err(); err != nil {
		return DirectDeliveryChangeResult{}, err
	}
	if err := validateDirectDeliveryPlan(plan); err != nil {
		return DirectDeliveryChangeResult{}, err
	}
	m.mu.Lock()
	c := m.directDelivery
	valid := m.running && m.directPublishing && m.directHandle == handle && c != nil && executor != nil
	m.mu.Unlock()
	if !valid {
		return DirectDeliveryChangeResult{}, errDirectReconfigureUnavailable
	}
	c.mu.Lock()
	if err := waitContext.Err(); err != nil {
		c.mu.Unlock()
		return DirectDeliveryChangeResult{}, err
	}
	if _, err := c.activeRoute(); err != nil {
		c.mu.Unlock()
		return DirectDeliveryChangeResult{}, err
	}
	if plan.SessionID != c.active.SessionID {
		c.mu.Unlock()
		return DirectDeliveryChangeResult{}, airplaycontract.ErrDeliveryGenerationStale
	}
	if c.transactionActive || c.pending != nil {
		c.mu.Unlock()
		return DirectDeliveryChangeResult{}, airplaycontract.ErrDeliveryGenerationPending
	}
	if plan.Output == c.activePlan.Output && plan.Profile == c.activePlan.Profile && plan.RequestedQualityMode == c.activePlan.RequestedQualityMode {
		ready, known := c.observer.generationReadiness(c.active)
		_, completed := c.observer.publisherCompletion(c.active.Generation)
		if completed || !known || !ready.PublisherReady || !ready.MediaReady || !c.gate.backendRouter.canAccept() {
			c.mu.Unlock()
			return DirectDeliveryChangeResult{}, errDirectReconfigureUnavailable
		}
		result := DirectDeliveryChangeResult{Applied: true, Generation: c.active.Generation, Plan: c.activePlan}
		c.mu.Unlock()
		return result, nil
	}
	expected := c.active.Generation
	c.transactionActive = true
	c.publishDeliveryStateLocked()
	c.mu.Unlock()
	type completion struct {
		result DirectDeliveryChangeResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		applied, restored, err := c.runDirectDeliveryChange(expected, plan, ffprobe, executor)
		c.mu.Lock()
		result := DirectDeliveryChangeResult{Applied: applied, Restored: restored, Generation: c.active.Generation, Plan: c.activePlan}
		c.transactionActive = false
		c.publishDeliveryStateLocked()
		c.mu.Unlock()
		m.notifyManagedDeliveryChanged()
		done <- completion{result, err}
	}()
	m.notifyManagedDeliveryChanged()
	// Only this wait belongs to the HTTP request. In particular, cancellation
	// here must not abort a publisher that the monitor has already accepted.
	select {
	case result := <-done:
		return result.result, result.err
	case <-waitContext.Done():
		return DirectDeliveryChangeResult{}, waitContext.Err()
	}
}

func (c *directDeliveryCoordinator) runDirectDeliveryChange(expected uint64, plan DirectDeliveryPlan, ffprobe string, executor DirectDeliveryExecutor) (bool, bool, error) {
	candidate, err := c.PrepareCandidateDelivery(expected, plan, ffprobe)
	if err != nil {
		return false, false, err
	}
	err = executor.ApplySourceClockDelivery(c.ctx, candidate)
	if c.deliveryCandidateCommitted(candidate) {
		return true, false, err
	}
	if err == nil {
		err = errDirectReconfigureUnavailable
	}
	// Abort is idempotent after monitor-confirmed cleanup, but refuses to
	// release a claimed publisher whose completion has not been confirmed.
	if cleanupErr := candidate.Abort(); cleanupErr != nil {
		return false, false, errors.Join(err, cleanupErr)
	}
	if c.ctx.Err() != nil {
		return false, false, errors.Join(err, c.ctx.Err())
	}
	recovery, restoreErr := c.PrepareCompensationDelivery(candidate, ffprobe)
	if restoreErr != nil {
		return false, false, errors.Join(err, restoreErr)
	}
	restoreErr = executor.RecoverSourceClockDelivery(c.ctx, recovery)
	if c.deliveryCandidateCommitted(recovery) {
		return false, true, errors.Join(err, restoreErr)
	}
	if restoreErr == nil {
		restoreErr = errDirectReconfigureUnavailable
	}
	return false, false, errors.Join(err, restoreErr, recovery.Abort())
}

func (c *directDeliveryCoordinator) deliveryCandidateCommitted(candidate airplaycontract.CandidateDelivery) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	descriptor := candidate.Binding().Descriptor
	return c.pending == nil && c.active == descriptor && c.ledger.Snapshot().ActiveGeneration == descriptor.Generation
}

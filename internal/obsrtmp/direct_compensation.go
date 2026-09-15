package obsrtmp

import (
	"os/exec"

	"imagepadserver/internal/airplaycontract"
)

// Compensation is a fresh generation, never a rollback of artifacts or a
// second try at the requested settings. Only this coordinator can authorize
// one restoration after publisher AND backend cleanup is confirmed.
func (c *directDeliveryCoordinator) PrepareCompensationDelivery(failed airplaycontract.CandidateDelivery, ffprobe string) (airplaycontract.CandidateDelivery, error) {
	d, ok := failed.(*directCandidateDelivery)
	if !ok || d == nil {
		return nil, errDirectReconfigureUnavailable
	}
	probePath, err := exec.LookPath(ffprobe)
	if err != nil {
		return nil, err
	}
	attempt, err := c.prepareDirectCompensation(d)
	if err != nil {
		return nil, err
	}
	if _, err := c.StartDirectReconfigureBackend(attempt); err != nil {
		return nil, err
	}
	return &directCandidateDelivery{owner: c, attempt: attempt, ffprobe: probePath, command: exec.CommandContext}, nil
}

func (c *directDeliveryCoordinator) prepareDirectCompensation(failed *directCandidateDelivery) (*directReconfigureAttempt, error) {
	if failed == nil {
		return nil, errDirectReconfigureUnavailable
	}
	failed.mu.Lock()
	defer failed.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if failed.owner != c || failed.attempt == nil || !failed.aborted || failed.compensationUsed || failed.attempt.compensation || c.pending != nil {
		return nil, errDirectReconfigureUnavailable
	}
	route, err := c.activeRoute()
	if err != nil {
		return nil, err
	}
	if route != failed.attempt.expected || c.ledger.Snapshot().LastAllocatedGeneration != failed.attempt.descriptor.Generation {
		return nil, airplaycontract.ErrDeliveryGenerationStale
	}
	old, exists := c.observer.publisherCompletion(c.active.Generation)
	if !exists || !old.Started || !old.ExitConfirmed || old.Artifacts != c.active.ArtifactPaths {
		return nil, airplaycontract.ErrCandidatePublisherExitUnconfirmed
	}
	plan := c.activePlan
	if err := validateDirectDeliveryPlan(plan); err != nil {
		return nil, err
	}
	if plan.SessionID != c.active.SessionID || plan.RequestID != c.active.RequestID || plan.Output != DirectOutputSettings(c.active.Output) {
		return nil, airplaycontract.ErrDeliveryGenerationMismatch
	}
	plan.RequestID = sessionID()
	// Burn the sole compensation authorization before allocation/startup. Even
	// an allocation or backend failure must not turn into an unbounded loop.
	failed.compensationUsed = true
	attempt, err := c.prepareDirectReconfigureLocked(c.active.Generation, plan)
	if err != nil {
		return nil, err
	}
	attempt.compensation = true
	return attempt, nil
}

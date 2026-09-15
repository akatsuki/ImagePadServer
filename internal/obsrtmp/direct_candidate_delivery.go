package obsrtmp

import (
	"context"
	"os/exec"
	"sync"
	"time"

	"imagepadserver/internal/airplaycontract"
)

type directCandidateDelivery struct {
	mu                  sync.Mutex
	owner               *directDeliveryCoordinator
	attempt             *directReconfigureAttempt
	ffprobe             string
	command             directRecordingProbeCommandFunc
	validationAttempted bool
	validated           bool
	publisherClaimed    bool
	aborted             bool
	compensationUsed    bool
}

var _ airplaycontract.CandidateDelivery = (*directCandidateDelivery)(nil)

func (c *directDeliveryCoordinator) PrepareCandidateDelivery(expected uint64, plan DirectDeliveryPlan, ffprobe string) (airplaycontract.CandidateDelivery, error) {
	// Capture the probe executable before starting any candidate resource. No
	// HTTP context owns this work: the coordinator's receiver-session context
	// controls startup, validation and cleanup.
	probePath, err := exec.LookPath(ffprobe)
	if err != nil {
		return nil, err
	}
	attempt, err := c.PrepareDirectReconfigure(expected, plan)
	if err != nil {
		return nil, err
	}
	if _, err := c.StartDirectReconfigureBackend(attempt); err != nil {
		return nil, err
	}
	return &directCandidateDelivery{owner: c, attempt: attempt, ffprobe: probePath, command: exec.CommandContext}, nil
}

func (d *directCandidateDelivery) Binding() airplaycontract.CandidateDeliveryBinding {
	return airplaycontract.CandidateDeliveryBinding{ExpectedActiveGeneration: d.attempt.expected.generation, Descriptor: d.attempt.descriptor, PublishURL: d.attempt.backend.runtime.directPublishURL()}
}

func (d *directCandidateDelivery) ClaimPublisher() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.publisherClaimed || d.validationAttempted || d.aborted {
		return airplaycontract.ErrDeliveryGenerationStale
	}
	c := d.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != d.attempt || !d.attempt.backendStartAttempted || c.ledger.Snapshot().PreparedGeneration != d.attempt.descriptor.Generation {
		return airplaycontract.ErrDeliveryGenerationStale
	}
	route, err := c.activeRoute()
	if err != nil {
		return err
	}
	if route != d.attempt.expected {
		return airplaycontract.ErrDeliveryGenerationStale
	}
	d.publisherClaimed = true
	return nil
}

func (d *directCandidateDelivery) Validate(ctx context.Context, old, candidate []airplaycontract.Event) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.validationAttempted || d.aborted {
		return airplaycontract.ErrDeliveryGenerationStale
	}
	d.validationAttempted = true
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stop := context.AfterFunc(d.owner.ctx, cancel)
	defer stop()
	a := d.attempt
	if _, err := airplaycontract.NewSourceClockPostWatermarkProof(old, candidate, a.descriptor.SessionID, a.expected.generation, a.descriptor.Generation); err != nil {
		return err
	}
	// An encoded IDR is not proof that rtspclientsink has finished ANNOUNCE /
	// RECORD. Wait for this owned private path, inside the same output deadline,
	// before the one decoder probe; an immediate DESCRIBE otherwise returns 404.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a.backend.runtime.pathReady(ctx) {
			break
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	proof, err := probeDirectBackendGeneration(ctx, d.ffprobe, a.descriptor, a.backend.route, d.command)
	if err != nil {
		return err
	}
	for _, event := range candidate {
		d.owner.observer.ObservePublisher(event)
	}
	deadline, _ := ctx.Deadline()
	if err := d.owner.prepareDirectCommit(ctx, a, old, candidate, proof, deadline); err != nil {
		return err
	}
	d.validated = true
	return nil
}

func (d *directCandidateDelivery) Commit(deadline time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.validated {
		return errDirectReconfigureUnavailable
	}
	d.validated = false
	return d.owner.commitPreparedDirectReconfigure(d.attempt, deadline)
}

func (d *directCandidateDelivery) Abort() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.aborted {
		return nil
	}
	if d.publisherClaimed {
		completion, exists := d.owner.observer.publisherCompletion(d.attempt.descriptor.Generation)
		if !exists || completion.Artifacts != d.attempt.descriptor.ArtifactPaths || (completion.Started && !completion.ExitConfirmed) {
			return airplaycontract.ErrCandidatePublisherExitUnconfirmed
		}
	}
	d.validated = false
	err := d.owner.AbortDirectReconfigure(d.attempt)
	if err == nil {
		d.aborted = true
	}
	return err
}

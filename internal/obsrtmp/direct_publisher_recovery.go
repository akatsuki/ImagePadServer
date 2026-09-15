package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

func (c *directDeliveryCoordinator) RecoverPublisher(ctx context.Context, old airplaycontract.PublisherArtifacts, executor airplaycontract.PublisherRecoveryExecutor) error {
	if ctx == nil || executor == nil {
		return errDirectReconfigureUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if _, err := c.activeRoute(); err != nil {
		c.mu.Unlock()
		return err
	}
	if c.transactionActive || c.pending != nil {
		c.mu.Unlock()
		return airplaycontract.ErrDeliveryGenerationPending
	}
	if old != c.active.ArtifactPaths || c.recoveryAttempted[old.Generation] {
		c.mu.Unlock()
		return airplaycontract.ErrDeliveryGenerationStale
	}
	completion, exists := c.observer.publisherCompletion(old.Generation)
	if !exists || !completion.Started || !completion.ExitConfirmed || completion.Artifacts != old {
		c.mu.Unlock()
		return airplaycontract.ErrCandidatePublisherExitUnconfirmed
	}
	plan := c.activePlan
	if err := validateDirectDeliveryPlan(plan); err != nil {
		c.mu.Unlock()
		return err
	}
	permission := c.recoveryBudget.Next(time.Now())
	if !permission.Allowed {
		c.mu.Unlock()
		return fmt.Errorf("managed publisher recovery: %s", permission.Reason)
	}
	if c.recoveryAttempted == nil {
		c.recoveryAttempted = make(map[uint64]bool)
	}
	c.recoveryAttempted[old.Generation] = true
	c.transactionActive = true
	c.publishDeliveryStateLocked()
	c.mu.Unlock()
	c.manager.notifyManagedDeliveryChanged()
	defer func() {
		c.mu.Lock()
		c.transactionActive = false
		c.publishDeliveryStateLocked()
		c.mu.Unlock()
		c.manager.notifyManagedDeliveryChanged()
	}()
	timer := time.NewTimer(permission.Delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ffprobe, err := video.ExistingFFprobePath()
	if err != nil {
		return err
	}
	plan.RequestID = sessionID()
	candidate, err := c.PrepareCandidateDelivery(old.Generation, plan, ffprobe)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, candidate.Abort())
	}
	err = executor.RecoverSourceClockDelivery(c.ctx, candidate)
	if c.deliveryCandidateCommitted(candidate) {
		return err
	}
	if err == nil {
		err = errDirectReconfigureUnavailable
	}
	return errors.Join(err, candidate.Abort())
}

func (c *directDeliveryCoordinator) PublisherHealthy(artifacts airplaycontract.PublisherArtifacts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.activeRoute(); err != nil {
		return
	}
	if artifacts != c.active.ArtifactPaths || c.transactionActive || c.pending != nil {
		return
	}
	ready, known := c.observer.generationReadiness(c.active)
	_, completed := c.observer.publisherCompletion(artifacts.Generation)
	if !known || !ready.PublisherReady || !ready.MediaReady || completed {
		return
	}
	c.recoveryBudget.ResetAfterHealthy()
}

func (o *directPublisherObserver) ManagedRecoveryOwner() (airplaycontract.PublisherRecoveryOwner, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.recoveryOwner, o.deliveryLedger != nil
}

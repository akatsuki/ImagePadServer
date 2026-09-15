package airplay

import (
	"context"
	"errors"
	"time"

	"imagepadserver/internal/airplaycontract"
)

var errSourceClockCandidateValidationUnconfirmed = errors.New("candidate output validation exit is unconfirmed")

// The monitor remains the sole consumer of process completion and lifecycle
// observations. Only the bounded downstream probe runs in the worker. Joining
// that worker precedes any backend abort, so late validation cannot reopen or
// drain a gate after cleanup has returned.
func validateSourceClockCandidateOutput(ctx context.Context, candidate airplaycontract.CandidateDelivery, old, events []airplaycontract.Event, done <-chan error, priority func() error) (result error, consumed, validationFinished bool) {
	if candidate == nil {
		return ErrDeliveryReconfigureEvidenceInsufficient, false, true
	}
	ctx, cancel := context.WithTimeout(ctx, sourceClockReadyTimeout)
	defer cancel()
	check := func() (error, bool) {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		if priority != nil {
			if err := priority(); err != nil {
				return err, false
			}
		}
		if err := ctx.Err(); err != nil {
			return err, false
		}
		select {
		case waitErr := <-done:
			return &sourceClockPublisherExitedBeforeReadyError{waitErr: waitErr}, true
		default:
		}
		return nil, false
	}
	if err, exited := check(); err != nil {
		return err, exited, true
	}
	finished := make(chan error, 1)
	go func() { finished <- candidate.Validate(ctx, old, events) }()
	defer func() {
		cancel()
		if validationFinished {
			return
		}
		timer := time.NewTimer(sourceClockReadyTimeout)
		defer timer.Stop()
		select {
		case err := <-finished:
			validationFinished = true
			result = errors.Join(result, err)
		case <-timer.C:
			result = errors.Join(result, errSourceClockCandidateValidationUnconfirmed)
		}
	}()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err, exited := check(); err != nil {
			return err, exited, false
		}
		select {
		case err := <-finished:
			validationFinished = true
			if priorityErr, exited := check(); priorityErr != nil {
				return errors.Join(priorityErr, err), exited, true
			}
			return err, false, true
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// Commit is non-blocking process-wise: the slow probe and gate drain already
// finished. Hold the receiver state lock across the downstream atomic commit
// so an accepted user stop/stale request cannot publish a new active snapshot.
// CandidateDelivery.Commit must not call back into the AirPlay manager.
func (m *Manager) commitSourceClockCandidate(ctx context.Context, command sourceClockDeliveryCommand, deadline time.Time) error {
	request := command.request
	if err := validateSourceClockDeliveryRequest(request); err != nil {
		return err
	}
	if request.CandidateDelivery == nil {
		return ErrDeliveryReconfigureEvidenceInsufficient
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !m.running || !m.status.ReceiverRunning || m.stopInitiator != "" ||
		m.deliverySessionID != request.Artifacts.SessionID || m.deliveryActiveGeneration != request.ExpectedActiveGeneration ||
		!m.deliveryReconfigurePending || command.identity == nil || m.deliveryReconfigureRequestID != request.RequestID || m.deliveryReconfigureReply != command.reply {
		return ErrDeliveryReconfigureStale
	}
	if deadline.IsZero() || !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	if err := request.CandidateDelivery.Commit(deadline); err != nil {
		return err
	}
	m.deliveryActiveGeneration = request.CandidateGeneration
	m.deliveryActivePublishURL = request.PublishURL
	m.deliveryActiveOutput = request.Output
	m.status.BridgeRunning, m.status.MediaReadyKnown, m.status.MediaReady = true, true, true
	m.status.Message = "AirPlay接続を維持したまま配信設定を変更しました。"
	return nil
}

func sourceClockCandidateCommitDeadline(lifecycle SessionLifecycle) time.Time {
	if lifecycle.Timeout <= 0 {
		return time.Time{}
	}
	last := lifecycle.FirstDecodedAt
	if last.IsZero() {
		last = lifecycle.StartedAt
	} else {
		if lifecycle.LastVideoAt.After(last) {
			last = lifecycle.LastVideoAt
		}
		if lifecycle.LastAudioAt.After(last) {
			last = lifecycle.LastAudioAt
		}
	}
	if last.IsZero() {
		return time.Time{}
	}
	return last.Add(lifecycle.Timeout)
}

package airplay

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"imagepadserver/internal/airplaycontract"
)

// Call only after the old publisher's Wait is consumed. Never read a still-open
// old event log as a final watermark, even if a plausible event already exists.
func preparePlannedSourceClockCandidate(current []string, old airplaycontract.PublisherArtifacts, request SourceClockDeliveryRequest, waitErr error) ([]string, []airplaycontract.Event, error) {
	if errors.Is(waitErr, errDirectGStreamerProcessExitUnconfirmed) {
		return nil, nil, waitErr
	}
	if exit := sourceClockProcessExitFromError(waitErr); exit.Known && exit.Code == 20 {
		return nil, nil, ErrDeliveryReconfigureUnavailable
	}
	if old.SessionID != request.Artifacts.SessionID || old.Generation != request.ExpectedActiveGeneration {
		return nil, nil, ErrDeliveryReconfigureStale
	}
	events, err := readSourceClockPublisherEvents(old.EventLog)
	if err != nil {
		return nil, nil, fmt.Errorf("read final source-clock watermark: %w", err)
	}
	watermark, err := sourceClockVideoWatermarkFinalEvent(events, old.SessionID, old.Generation)
	if err != nil {
		return nil, nil, err
	}
	// The native wire/CLI are uint32; do not truncate uint64 JSON evidence.
	if watermark.SourceSessionGeneration > uint64(^uint32(0)) || *watermark.SourceVideoSequence > uint64(^uint32(0)) {
		return nil, nil, errSourceClockPostWatermarkEvidence
	}
	args, err := buildPlannedSourceClockPublisherArgs(current, request)
	if err != nil {
		return nil, nil, err
	}
	args = append(args, "--proof-source-generation", strconv.FormatUint(watermark.SourceSessionGeneration, 10),
		"--proof-video-watermark", strconv.FormatUint(*watermark.SourceVideoSequence, 10))
	return args, events, nil
}

// One consumer owns done. User stop and the session-owned deadline are checked
// before AND after reading the file, before returning any successful proof.
// A complete native chain is not RTSP acceptance or active-promotion authority.
func waitSourceClockCandidateProof(ctx context.Context, path string, old []airplaycontract.Event, sessionID string, oldGeneration, candidateGeneration uint64, done <-chan error, timeout time.Duration, priority func() error) (sourceClockPostWatermarkProof, error, bool) {
	empty := sourceClockPostWatermarkProof{}
	if timeout <= 0 {
		return empty, errors.New("invalid candidate proof timeout"), false
	}
	if _, err := sourceClockVideoWatermarkFinalEvent(old, sessionID, oldGeneration); err != nil {
		return empty, err, false
	}
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
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
		if !time.Now().Before(deadline) {
			return fmt.Errorf("source-clock candidate proof timeout: %w", errors.Join(errSourceClockPostWatermarkEvidence, lastErr)), false
		}
		return nil, false
	}
	for {
		if err, consumed := check(); err != nil {
			return empty, err, consumed
		}
		events, err := readSourceClockPublisherEvents(path)
		lastErr = err
		if err == nil {
			proof, proofErr := newSourceClockPostWatermarkProof(old, events, sessionID, oldGeneration, candidateGeneration)
			lastErr = proofErr
			if proofErr == nil {
				if err, consumed := check(); err != nil {
					return empty, err, consumed
				}
				return proof, nil, false
			}
		}
		// Poll done at the next check (<=25ms), keeping priority ordering and
		// single-consumer ownership explicit even when several signals are ready.
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-ticker.C:
		}
	}
}

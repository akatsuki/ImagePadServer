package airplay

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"imagepadserver/internal/airplaycontract"
)

const sourceClockPublisherHealthyDuration = 30 * time.Second

func readSourceClockPublisherReady(path, sessionID string, publisherGeneration uint64) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read source-clock publisher-ready file: %w", err)
	}
	var event airplaycontract.Event
	if err := json.Unmarshal(data, &event); err != nil {
		return false, fmt.Errorf("parse source-clock publisher-ready file: %w", err)
	}
	return event.ReadyFor(sessionID, publisherGeneration), nil
}

func sourceClockPublisherHealthObservationFromState(readyPath, sessionID string, publisherGeneration uint64, mediaStarted bool, stats sourceClockMetricsGateStats) (sourceClockPublisherHealthObservation, error) {
	ready, err := readSourceClockPublisherReady(readyPath, sessionID, publisherGeneration)
	if err != nil {
		return sourceClockPublisherHealthObservation{}, err
	}
	latest := stats.Latest
	return sourceClockPublisherHealthObservation{
		Generation:     publisherGeneration,
		PublisherReady: ready,
		MediaStarted:   mediaStarted,
		OutputCounter:  latest.SentFrames,
		FailureCounter: latest.FailureEvents,
		ErrorFree: stats.HasMetrics && latest.HasFailureEvents &&
			latest.ConnectedConsumers > 0 &&
			latest.LastSocketError == 0 &&
			latest.LastFailureReason == "",
	}, nil
}

func sourceClockPublisherHealthTick(tracker *sourceClockPublisherHealthTracker, budget *sourceClockRetryBudget, readyPath, sessionID string, publisherGeneration uint64, mediaStarted bool, stats sourceClockMetricsGateStats, now time.Time) (bool, error) {
	if tracker == nil || budget == nil {
		return false, fmt.Errorf("source-clock publisher health state is nil")
	}
	observation, err := sourceClockPublisherHealthObservationFromState(readyPath, sessionID, publisherGeneration, mediaStarted, stats)
	if err != nil {
		tracker.resetCandidate()
		return false, err
	}
	if !tracker.Observe(now, observation) {
		return false, nil
	}
	budget.ResetAfterHealthy()
	return true, nil
}

func sourceClockPublisherHealthTickIfRunning(pipelineDone <-chan error, tracker *sourceClockPublisherHealthTracker, budget *sourceClockRetryBudget, readyPath, sessionID string, publisherGeneration uint64, mediaStarted bool, stats sourceClockMetricsGateStats, now time.Time) (reset bool, pipelineErr error, exited bool, healthErr error) {
	if pipelineDone == nil {
		return false, nil, false, nil
	}
	select {
	case pipelineErr = <-pipelineDone:
		if tracker != nil {
			tracker.resetCandidate()
		}
		return false, pipelineErr, true, nil
	default:
	}
	reset, healthErr = sourceClockPublisherHealthTick(tracker, budget, readyPath, sessionID, publisherGeneration, mediaStarted, stats, now)
	return reset, nil, false, healthErr
}

package airplay

import (
	"errors"
	"time"
)

// sourceClockLifecycleTracker admits receiver activity only after the current
// session has produced a validated decoded-video notification. Publisher
// generations may change, but this receiver-session state is kept intact.
type sourceClockLifecycleTracker struct {
	lifecycle SessionLifecycle
}

func newSourceClockLifecycleTracker(timeout time.Duration) *sourceClockLifecycleTracker {
	return newSourceClockLifecycleTrackerAt(timeout, time.Now())
}

func newSourceClockLifecycleTrackerAt(timeout time.Duration, startedAt time.Time) *sourceClockLifecycleTracker {
	return &sourceClockLifecycleTracker{
		lifecycle: SessionLifecycle{StartedAt: startedAt, Timeout: timeout},
	}
}

func (t *sourceClockLifecycleTracker) Observe(mediaReady bool, observedAt time.Time, stats sourceClockMetricsGateStats) {
	if mediaReady && t.lifecycle.FirstDecodedAt.IsZero() {
		// Use the local observation clock instead of the native event's wall
		// timestamp so later receiver metric upper bounds share one clock.
		t.lifecycle.ObserveVideoDecoded(observedAt)
	}
	if t.lifecycle.FirstDecodedAt.IsZero() {
		return
	}
	t.lifecycle.ObserveVideoDecoded(stats.LastVideoInputUpperBound)
	t.lifecycle.ObserveAudioDecoded(stats.LastAudioInputUpperBound)
}

func (t *sourceClockLifecycleTracker) NoSignalExpired(now time.Time) bool {
	return t.lifecycle.NoSignalExpired(now)
}

func (t *sourceClockLifecycleTracker) NoSignalDecision(now time.Time) sourceClockTimeoutDecision {
	return t.lifecycle.NoSignalDecision(now)
}

func (t *sourceClockLifecycleTracker) Snapshot() SessionLifecycle {
	return t.lifecycle
}

func sourceClockLifecycleTick(tracker *sourceClockLifecycleTracker, gate *sourceClockMetricsGate, mediaReadyPath, sessionID string, publisherGeneration uint64, now time.Time) (bool, error) {
	if tracker == nil {
		return false, errors.New("source-clock lifecycle tracker is nil")
	}
	if gate == nil {
		return false, errors.New("source-clock metrics gate is nil")
	}
	mediaReady := !tracker.Snapshot().FirstDecodedAt.IsZero()
	if !mediaReady {
		var err error
		mediaReady, err = readSourceClockMediaReady(mediaReadyPath, sessionID, publisherGeneration)
		if err != nil {
			return false, err
		}
	}
	tracker.Observe(mediaReady, now, gate.snapshot())
	return tracker.NoSignalExpired(now), nil
}

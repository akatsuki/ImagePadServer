package airplay

import "time"

const sourceClockPublisherHealthMaxObservationGap = 2 * time.Second

type sourceClockPublisherHealthObservation struct {
	Generation     uint64
	PublisherReady bool
	MediaStarted   bool
	OutputCounter  uint64
	FailureCounter uint64
	ErrorFree      bool
}

type sourceClockPublisherHealthTracker struct {
	requiredDuration time.Duration
	generation       uint64
	mediaStarted     bool
	candidateActive  bool
	candidateSince   time.Time
	baselineOutput   uint64
	lastOutput       uint64
	lastFailure      uint64
	lastObservedAt   time.Time
	lastOutputAt     time.Time
	healthyReported  bool
}

func newSourceClockPublisherHealthTracker(requiredDuration time.Duration) *sourceClockPublisherHealthTracker {
	return &sourceClockPublisherHealthTracker{requiredDuration: requiredDuration}
}

func (t *sourceClockPublisherHealthTracker) Observe(now time.Time, observation sourceClockPublisherHealthObservation) bool {
	if t.requiredDuration <= 0 {
		t.resetCandidate()
		return false
	}
	if observation.Generation == 0 {
		t.resetCandidate()
		return false
	}
	if t.generation != observation.Generation {
		t.generation = observation.Generation
		t.resetCandidate()
	}
	if observation.MediaStarted && !t.mediaStarted {
		t.mediaStarted = true
		t.resetCandidate()
	}
	if !observation.PublisherReady || (t.mediaStarted && !observation.ErrorFree) {
		t.resetCandidate()
		return false
	}
	if !t.candidateActive {
		t.startCandidate(now, observation.OutputCounter, observation.FailureCounter)
		return false
	}
	if now.Before(t.lastObservedAt) || now.Sub(t.lastObservedAt) > sourceClockPublisherHealthMaxObservationGap {
		t.startCandidate(now, observation.OutputCounter, observation.FailureCounter)
		return false
	}
	if observation.FailureCounter != t.lastFailure || (t.mediaStarted && observation.OutputCounter < t.lastOutput) {
		t.startCandidate(now, observation.OutputCounter, observation.FailureCounter)
		return false
	}
	outputAdvanced := observation.OutputCounter > t.lastOutput
	t.lastOutput = observation.OutputCounter
	t.lastObservedAt = now
	if t.mediaStarted {
		if outputAdvanced {
			t.lastOutputAt = now
		} else if t.lastOutputAt.IsZero() || now.Sub(t.lastOutputAt) > sourceClockPublisherHealthMaxObservationGap {
			t.startCandidate(now, observation.OutputCounter, observation.FailureCounter)
			return false
		}
	}
	if now.Before(t.candidateSince.Add(t.requiredDuration)) {
		return false
	}
	if t.mediaStarted && observation.OutputCounter <= t.baselineOutput {
		return false
	}
	return t.reportHealthy()
}

func (t *sourceClockPublisherHealthTracker) startCandidate(now time.Time, outputCounter, failureCounter uint64) {
	t.candidateActive = true
	t.candidateSince = now
	t.baselineOutput = outputCounter
	t.lastOutput = outputCounter
	t.lastFailure = failureCounter
	t.lastObservedAt = now
	t.lastOutputAt = time.Time{}
	t.healthyReported = false
}

func (t *sourceClockPublisherHealthTracker) resetCandidate() {
	t.candidateActive = false
	t.candidateSince = time.Time{}
	t.baselineOutput = 0
	t.lastOutput = 0
	t.lastFailure = 0
	t.lastObservedAt = time.Time{}
	t.lastOutputAt = time.Time{}
	t.healthyReported = false
}

func (t *sourceClockPublisherHealthTracker) reportHealthy() bool {
	if t.healthyReported {
		return false
	}
	t.healthyReported = true
	return true
}

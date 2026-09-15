package obsrtmp

import (
	"sync"
	"time"

	"imagepadserver/internal/airplaycontract"
)

const (
	streamTerminationInitiatorUnknown         = "unknown"
	streamTerminationInitiatorUserStop        = airplaycontract.TerminationReasonUserStop
	streamTerminationInitiatorNoInitialMedia  = airplaycontract.TerminationReasonNoInitialMedia
	streamTerminationInitiatorNoSignalTimeout = airplaycontract.TerminationReasonNoSignalTimeout
	streamTerminationInitiatorServerShutdown  = airplaycontract.TerminationReasonServerShutdown
)

// StreamTermination records where a stream stop was first observed without
// claiming that observation was also the cause. Initiator remains unknown
// unless a local, explicit operation marks it.
type StreamTermination struct {
	SessionID              string    `json:"sessionId"`
	PublisherGeneration    uint64    `json:"publisherGeneration"`
	At                     time.Time `json:"at"`
	FirstObservedComponent string    `json:"firstObservedComponent"`
	Initiator              string    `json:"initiator"`
	Evidence               string    `json:"evidence"`
	ExitCode               int       `json:"exitCode"`
	Reason                 string    `json:"reason"`
}

type streamTerminationTracker struct {
	mu         sync.Mutex
	sessionID  string
	generation uint64
	initiator  string
	observed   bool
	value      StreamTermination
}

func newStreamTerminationTracker(sessionID string, generation uint64) *streamTerminationTracker {
	return &streamTerminationTracker{
		sessionID:  sessionID,
		generation: generation,
		initiator:  streamTerminationInitiatorUnknown,
	}
}

func (t *streamTerminationTracker) Observe(observation airplaycontract.TerminationObservation) bool {
	if t == nil || !observation.ValidFor(t.sessionID, t.generation) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.observed {
		return false
	}
	t.observed = true
	t.value = StreamTermination{
		SessionID:              t.sessionID,
		PublisherGeneration:    t.generation,
		At:                     observation.At.UTC(),
		FirstObservedComponent: observation.Component,
		Initiator:              t.initiator,
		Evidence:               observation.Evidence,
		ExitCode:               observation.ExitCode,
		Reason:                 observation.Reason,
	}
	return true
}

func (t *streamTerminationTracker) MarkInitiator(initiator string) bool {
	if t == nil || !explicitStreamTerminationInitiator(initiator) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.initiator != streamTerminationInitiatorUnknown {
		return false
	}
	t.initiator = initiator
	if t.observed {
		t.value.Initiator = initiator
	}
	return true
}

func (t *streamTerminationTracker) Snapshot() (StreamTermination, bool) {
	if t == nil {
		return StreamTermination{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.value, t.observed
}

func explicitStreamTerminationInitiator(initiator string) bool {
	switch initiator {
	case streamTerminationInitiatorUserStop,
		streamTerminationInitiatorNoInitialMedia,
		streamTerminationInitiatorNoSignalTimeout,
		streamTerminationInitiatorServerShutdown:
		return true
	default:
		return false
	}
}

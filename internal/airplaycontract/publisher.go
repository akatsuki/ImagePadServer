package airplaycontract

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// PublisherArtifacts names files owned by one source-clock publisher
// generation. Session-level state and listener ownership use separate IDs.
type PublisherArtifacts struct {
	SessionID   string
	Generation  uint64
	Recording   string
	Ready       string
	MediaReady  string
	EventLog    string
	ProcessLog  string
	StopRequest string
}

// RecordingOutcome is reported independently from session shutdown. ProbeOK
// is never inferred from process exit or file existence alone.
type RecordingOutcome struct {
	Artifacts    PublisherArtifacts
	Closed       bool
	HasRealVideo bool
	ProbeOK      bool
	DurationNS   uint64
	Reason       string
}

// PublisherCompletion is emitted after one registered publisher generation
// has either failed to start or has had its process wait and event-log read
// completed. It does not imply that the recording is valid.
type PublisherCompletion struct {
	Artifacts     PublisherArtifacts
	Started       bool
	ExitConfirmed bool
	EventLogError string
	ProcessID     int
	CompletedAt   time.Time
	ExitCode      int
	ExitCodeKnown bool
}

// PublisherObserver serializes generation registration, event observation,
// and recording completion at the session owner.
type PublisherObserver interface {
	PreparePublisher(context.Context, PublisherArtifacts) error
	ObservePublisher(Event)
	CompletePublisher(PublisherCompletion)
	SealPublishers()
	FinishRecording(RecordingOutcome)
}

// TerminationInitiatorObserver is an optional extension used only when the
// application itself starts a known shutdown operation. Process arrival order
// must never be promoted to an initiator through this interface.
type TerminationInitiatorObserver interface {
	ObserveTerminationInitiator(sessionID string, generation uint64, reason string)
}

// NewPublisherArtifacts derives paths only. The caller owns root and id and
// must reserve the generation before creating any of these files.
func NewPublisherArtifacts(root, id string, generation uint64) PublisherArtifacts {
	stem := filepath.Join(root, "airplay", id, fmt.Sprintf("publisher-%04d", generation))
	return PublisherArtifacts{
		SessionID:   id,
		Generation:  generation,
		Recording:   stem + ".mp4",
		Ready:       stem + ".ready.json",
		MediaReady:  stem + ".media-ready.json",
		EventLog:    stem + ".events.jsonl",
		ProcessLog:  stem + ".log",
		StopRequest: stem + ".stop",
	}
}

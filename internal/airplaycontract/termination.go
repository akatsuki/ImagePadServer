package airplaycontract

import "time"

const (
	TerminationComponentMediaMTX  = "mediamtx"
	TerminationComponentRTSPEOF   = "rtsp-eof"
	TerminationComponentPublisher = "publisher"

	TerminationEvidenceProcessExit   = "process-exit"
	TerminationEvidenceRTSPEOF       = "rtsp-eof"
	TerminationEvidencePublisherExit = "publisher-exit"

	TerminationReasonProcessExited   = "process-exited"
	TerminationReasonRTSPEOF         = "rtsp-eof"
	TerminationReasonPublisherExited = "publisher-exited"
	TerminationReasonUserStop        = "user-stop"
	TerminationReasonNoInitialMedia  = "no-initial-media"
	TerminationReasonNoSignalTimeout = "no-signal-timeout"
	TerminationReasonServerShutdown  = "server-shutdown"
)

// TerminationObservation is a correlation-safe, non-causal report that one
// component observed a stream ending. Evidence and Reason are stable labels;
// raw process output, URLs, tokens, and credentials do not belong here.
type TerminationObservation struct {
	Schema              int       `json:"schema"`
	SessionID           string    `json:"sessionId"`
	PublisherGeneration uint64    `json:"publisherGeneration"`
	At                  time.Time `json:"at"`
	Component           string    `json:"component"`
	ProcessID           int       `json:"processId"`
	Sequence            uint64    `json:"sequence"`
	Evidence            string    `json:"evidence"`
	ExitCode            int       `json:"exitCode"`
	Reason              string    `json:"reason"`
}

// ValidFor reports whether the observation is safe to correlate with the
// expected source-clock publisher generation.
func (o TerminationObservation) ValidFor(sessionID string, generation uint64) bool {
	return o.Schema == 2 &&
		sessionID != "" && o.SessionID == sessionID &&
		generation != 0 && o.PublisherGeneration == generation &&
		!o.At.IsZero() && o.ProcessID > 0 && o.Sequence > 0 &&
		terminationEvidenceMatches(o.Component, o.Evidence) &&
		validTerminationReason(o.Reason)
}

func terminationEvidenceMatches(component, evidence string) bool {
	switch component {
	case TerminationComponentMediaMTX:
		return evidence == TerminationEvidenceProcessExit
	case TerminationComponentRTSPEOF:
		return evidence == TerminationEvidenceRTSPEOF
	case TerminationComponentPublisher:
		return evidence == TerminationEvidencePublisherExit
	default:
		return false
	}
}

func validTerminationReason(reason string) bool {
	switch reason {
	case TerminationReasonProcessExited,
		TerminationReasonRTSPEOF,
		TerminationReasonPublisherExited,
		TerminationReasonUserStop,
		TerminationReasonNoInitialMedia,
		TerminationReasonNoSignalTimeout,
		TerminationReasonServerShutdown:
		return true
	default:
		return false
	}
}

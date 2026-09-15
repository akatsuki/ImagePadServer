package airplaycontract

import (
	"testing"
	"time"
)

func TestTerminationObservationValidForAcceptsKnownSafeObservations(t *testing.T) {
	at := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		component string
		evidence  string
	}{
		{name: "MediaMTX process exit", component: TerminationComponentMediaMTX, evidence: TerminationEvidenceProcessExit},
		{name: "RTSP EOF", component: TerminationComponentRTSPEOF, evidence: TerminationEvidenceRTSPEOF},
		{name: "publisher exit", component: TerminationComponentPublisher, evidence: TerminationEvidencePublisherExit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := TerminationObservation{
				Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 3,
				At: at, Component: tt.component, ProcessID: 1234, Sequence: 1,
				Evidence: tt.evidence, ExitCode: 1, Reason: "process-exited",
			}
			if !observation.ValidFor("0123456789abcdef", 3) {
				t.Fatalf("valid observation was rejected: %+v", observation)
			}
		})
	}
}

func TestTerminationObservationValidForRejectsUncorrelatedOrUnsafeObservations(t *testing.T) {
	canonical := TerminationObservation{
		Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 3,
		At:        time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
		Component: TerminationComponentPublisher, ProcessID: 1234, Sequence: 1,
		Evidence: TerminationEvidencePublisherExit, ExitCode: 1, Reason: "publisher-exited",
	}
	tests := []struct {
		name       string
		mutate     func(*TerminationObservation)
		sessionID  string
		generation uint64
	}{
		{name: "old schema", mutate: func(o *TerminationObservation) { o.Schema = 1 }, sessionID: canonical.SessionID, generation: 3},
		{name: "wrong session", mutate: func(o *TerminationObservation) { o.SessionID = "fedcba9876543210" }, sessionID: canonical.SessionID, generation: 3},
		{name: "old generation", mutate: func(o *TerminationObservation) { o.PublisherGeneration = 2 }, sessionID: canonical.SessionID, generation: 3},
		{name: "empty expected session", mutate: func(*TerminationObservation) {}, sessionID: "", generation: 3},
		{name: "zero expected generation", mutate: func(*TerminationObservation) {}, sessionID: canonical.SessionID, generation: 0},
		{name: "zero timestamp", mutate: func(o *TerminationObservation) { o.At = time.Time{} }, sessionID: canonical.SessionID, generation: 3},
		{name: "zero process ID", mutate: func(o *TerminationObservation) { o.ProcessID = 0 }, sessionID: canonical.SessionID, generation: 3},
		{name: "zero sequence", mutate: func(o *TerminationObservation) { o.Sequence = 0 }, sessionID: canonical.SessionID, generation: 3},
		{name: "unknown component", mutate: func(o *TerminationObservation) { o.Component = "decoder" }, sessionID: canonical.SessionID, generation: 3},
		{name: "mismatched evidence", mutate: func(o *TerminationObservation) { o.Evidence = TerminationEvidenceRTSPEOF }, sessionID: canonical.SessionID, generation: 3},
		{name: "empty reason", mutate: func(o *TerminationObservation) { o.Reason = "" }, sessionID: canonical.SessionID, generation: 3},
		{name: "reason containing credentials", mutate: func(o *TerminationObservation) { o.Reason = "token=secret" }, sessionID: canonical.SessionID, generation: 3},
		{name: "reason containing URL", mutate: func(o *TerminationObservation) { o.Reason = "rtsp://user:pass@example.invalid/live" }, sessionID: canonical.SessionID, generation: 3},
		{name: "unlisted opaque token", mutate: func(o *TerminationObservation) { o.Reason = "deadbeefdeadbeef" }, sessionID: canonical.SessionID, generation: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := canonical
			tt.mutate(&observation)
			if observation.ValidFor(tt.sessionID, tt.generation) {
				t.Fatalf("invalid observation was accepted: %+v", observation)
			}
		})
	}
}

func TestTerminationObservationValidForAcceptsSessionCauseReasons(t *testing.T) {
	base := TerminationObservation{
		Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 3,
		At:        time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
		Component: TerminationComponentPublisher, ProcessID: 1234, Sequence: 1,
		Evidence: TerminationEvidencePublisherExit, ExitCode: 20,
	}
	for _, reason := range []string{
		TerminationReasonUserStop,
		TerminationReasonNoInitialMedia,
		TerminationReasonNoSignalTimeout,
	} {
		t.Run(reason, func(t *testing.T) {
			observation := base
			observation.Reason = reason
			if !observation.ValidFor(base.SessionID, base.PublisherGeneration) {
				t.Fatalf("session cause reason was rejected: %q", reason)
			}
		})
	}
}

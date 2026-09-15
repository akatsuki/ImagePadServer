package obsrtmp

import (
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func TestStreamTerminationTrackerDoesNotInferInitiatorFromObservationOrder(t *testing.T) {
	components := []string{
		airplaycontract.TerminationComponentMediaMTX,
		airplaycontract.TerminationComponentRTSPEOF,
		airplaycontract.TerminationComponentPublisher,
	}
	permutations := [][]string{
		{components[0], components[1], components[2]},
		{components[0], components[2], components[1]},
		{components[1], components[0], components[2]},
		{components[1], components[2], components[0]},
		{components[2], components[0], components[1]},
		{components[2], components[1], components[0]},
	}
	base := time.Date(2026, 9, 7, 3, 0, 0, 0, time.FixedZone("test", 9*60*60))
	for _, order := range permutations {
		tracker := newStreamTerminationTracker("session-1", 4)
		for index, component := range order {
			tracker.Observe(airplaycontract.TerminationObservation{
				Schema: 2, SessionID: "session-1", PublisherGeneration: 4,
				At: base.Add(time.Duration(index) * time.Millisecond), Component: component,
				ProcessID: 100 + index, Sequence: uint64(index + 1),
				Evidence: terminationEvidenceForTest(component), ExitCode: index + 10, Reason: terminationReasonForTest(component),
			})
		}
		got, ok := tracker.Snapshot()
		if !ok {
			t.Fatalf("order %v produced no termination", order)
		}
		if got.SessionID != "session-1" || got.PublisherGeneration != 4 || got.FirstObservedComponent != order[0] {
			t.Fatalf("order %v snapshot = %+v", order, got)
		}
		if got.Initiator != streamTerminationInitiatorUnknown {
			t.Fatalf("order %v inferred initiator %q", order, got.Initiator)
		}
		if !got.At.Equal(base.UTC()) || got.At.Location() != time.UTC || got.Evidence != terminationEvidenceForTest(order[0]) || got.ExitCode != 10 || got.Reason != terminationReasonForTest(order[0]) {
			t.Fatalf("order %v replaced first observation: %+v", order, got)
		}
	}
}

func TestStreamTerminationTrackerAcceptsOnlyExplicitInitiatorWithoutReplacingFirstObservation(t *testing.T) {
	base := time.Date(2026, 9, 7, 4, 0, 0, 0, time.UTC)
	tracker := newStreamTerminationTracker("session-2", 7)
	tracker.MarkInitiator(streamTerminationInitiatorUserStop)
	tracker.Observe(airplaycontract.TerminationObservation{Schema: 2, SessionID: "session-2", PublisherGeneration: 7, At: base, Component: airplaycontract.TerminationComponentPublisher, ProcessID: 200, Sequence: 1, Evidence: airplaycontract.TerminationEvidencePublisherExit, ExitCode: 0, Reason: "publisher-exited"})
	tracker.Observe(airplaycontract.TerminationObservation{Schema: 2, SessionID: "session-2", PublisherGeneration: 7, At: base.Add(time.Second), Component: airplaycontract.TerminationComponentMediaMTX, ProcessID: 201, Sequence: 2, Evidence: airplaycontract.TerminationEvidenceProcessExit, ExitCode: 1, Reason: "process-exited"})

	got, ok := tracker.Snapshot()
	if !ok {
		t.Fatal("explicitly initiated termination produced no observation")
	}
	if got.Initiator != streamTerminationInitiatorUserStop || got.FirstObservedComponent != airplaycontract.TerminationComponentPublisher || got.Evidence != airplaycontract.TerminationEvidencePublisherExit || got.Reason != "publisher-exited" {
		t.Fatalf("explicit initiator or first observation was lost: %+v", got)
	}

	late := newStreamTerminationTracker("session-3", 8)
	late.Observe(airplaycontract.TerminationObservation{Schema: 2, SessionID: "session-3", PublisherGeneration: 8, At: base, Component: airplaycontract.TerminationComponentRTSPEOF, ProcessID: 300, Sequence: 1, Evidence: airplaycontract.TerminationEvidenceRTSPEOF, ExitCode: 12, Reason: "rtsp-eof"})
	if late.MarkInitiator("publisher") {
		t.Fatal("observed component was accepted as a causal initiator")
	}
	if !late.MarkInitiator(streamTerminationInitiatorNoSignalTimeout) {
		t.Fatal("late explicit initiator was not accepted")
	}
	lateSnapshot, ok := late.Snapshot()
	if !ok || lateSnapshot.Initiator != streamTerminationInitiatorNoSignalTimeout || lateSnapshot.FirstObservedComponent != airplaycontract.TerminationComponentRTSPEOF || lateSnapshot.Evidence != airplaycontract.TerminationEvidenceRTSPEOF || lateSnapshot.ExitCode != 12 {
		t.Fatalf("late explicit initiator replaced first observation: %+v", lateSnapshot)
	}
}

func TestStreamTerminationTrackerAcceptsInitialMediaTimeoutAsExplicitInitiator(t *testing.T) {
	tracker := newStreamTerminationTracker("0123456789abcdef", 3)
	if !tracker.MarkInitiator(streamTerminationInitiatorNoInitialMedia) {
		t.Fatal("no-initial-media initiator was rejected")
	}
	if tracker.MarkInitiator(streamTerminationInitiatorNoSignalTimeout) {
		t.Fatal("first explicit initiator was replaced")
	}
}

func TestStreamTerminationTrackerRejectsWrongSessionOrGeneration(t *testing.T) {
	tracker := newStreamTerminationTracker("session-current", 9)
	at := time.Date(2026, 9, 7, 4, 30, 0, 0, time.UTC)
	for _, observation := range []airplaycontract.TerminationObservation{
		{Schema: 2, SessionID: "session-old", PublisherGeneration: 9, At: at, Component: airplaycontract.TerminationComponentPublisher, ProcessID: 400, Sequence: 1, Evidence: airplaycontract.TerminationEvidencePublisherExit, Reason: "publisher-exited"},
		{Schema: 2, SessionID: "session-current", PublisherGeneration: 8, At: at, Component: airplaycontract.TerminationComponentPublisher, ProcessID: 400, Sequence: 2, Evidence: airplaycontract.TerminationEvidencePublisherExit, Reason: "publisher-exited"},
	} {
		if tracker.Observe(observation) {
			t.Fatalf("uncorrelated observation was accepted: %+v", observation)
		}
	}
	if _, ok := tracker.Snapshot(); ok {
		t.Fatal("uncorrelated observation created a termination snapshot")
	}
}

func TestStreamTerminationTrackerRequiresExactSessionIdentity(t *testing.T) {
	tracker := newStreamTerminationTracker(" session-current ", 9)
	accepted := tracker.Observe(airplaycontract.TerminationObservation{
		Schema: 2, SessionID: "session-current", PublisherGeneration: 9,
		At: time.Date(2026, 9, 7, 5, 0, 0, 0, time.UTC), Component: airplaycontract.TerminationComponentPublisher,
		ProcessID: 500, Sequence: 1, Evidence: airplaycontract.TerminationEvidencePublisherExit, Reason: "publisher-exited",
	})
	if accepted {
		t.Fatal("trimmed observation matched a different expected session ID")
	}
}

func TestStreamTerminationTrackerKeepsFirstArrivalWhenLaterMetadataIsOlder(t *testing.T) {
	tracker := newStreamTerminationTracker("session-order", 10)
	base := time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)
	first := airplaycontract.TerminationObservation{Schema: 2, SessionID: "session-order", PublisherGeneration: 10, At: base, Component: airplaycontract.TerminationComponentPublisher, ProcessID: 600, Sequence: 2, Evidence: airplaycontract.TerminationEvidencePublisherExit, Reason: "publisher-exited"}
	laterButOlder := airplaycontract.TerminationObservation{Schema: 2, SessionID: "session-order", PublisherGeneration: 10, At: base.Add(-time.Second), Component: airplaycontract.TerminationComponentRTSPEOF, ProcessID: 601, Sequence: 1, Evidence: airplaycontract.TerminationEvidenceRTSPEOF, Reason: "rtsp-eof"}
	if !tracker.Observe(first) || tracker.Observe(laterButOlder) {
		t.Fatal("arrival-order contract was not preserved")
	}
	got, ok := tracker.Snapshot()
	if !ok || got.FirstObservedComponent != airplaycontract.TerminationComponentPublisher || !got.At.Equal(base) {
		t.Fatalf("later metadata replaced first arrival: %+v", got)
	}
}

func TestStreamTerminationTrackerConcurrentObservationAcceptsExactlyOne(t *testing.T) {
	tracker := newStreamTerminationTracker("session-race", 11)
	base := time.Date(2026, 9, 7, 7, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	accepted := make(chan bool, 3)
	for index, component := range []string{airplaycontract.TerminationComponentMediaMTX, airplaycontract.TerminationComponentRTSPEOF, airplaycontract.TerminationComponentPublisher} {
		go func(index int, component string) {
			<-start
			accepted <- tracker.Observe(airplaycontract.TerminationObservation{
				Schema: 2, SessionID: "session-race", PublisherGeneration: 11,
				At: base.Add(time.Duration(index) * time.Millisecond), Component: component,
				ProcessID: 700 + index, Sequence: uint64(index + 1), Evidence: terminationEvidenceForTest(component), Reason: terminationReasonForTest(component),
			})
		}(index, component)
	}
	close(start)
	acceptedCount := 0
	for range 3 {
		if <-accepted {
			acceptedCount++
		}
	}
	if acceptedCount != 1 {
		t.Fatalf("accepted observations = %d, want 1", acceptedCount)
	}
	if _, ok := tracker.Snapshot(); !ok {
		t.Fatal("concurrent observations produced no snapshot")
	}
}

func terminationEvidenceForTest(component string) string {
	switch component {
	case airplaycontract.TerminationComponentMediaMTX:
		return airplaycontract.TerminationEvidenceProcessExit
	case airplaycontract.TerminationComponentRTSPEOF:
		return airplaycontract.TerminationEvidenceRTSPEOF
	default:
		return airplaycontract.TerminationEvidencePublisherExit
	}
}

func terminationReasonForTest(component string) string {
	switch component {
	case airplaycontract.TerminationComponentMediaMTX:
		return "process-exited"
	case airplaycontract.TerminationComponentRTSPEOF:
		return "rtsp-eof"
	default:
		return "publisher-exited"
	}
}

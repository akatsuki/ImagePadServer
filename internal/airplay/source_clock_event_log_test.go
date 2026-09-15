package airplay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func sourceClockEventForWatermarkTest(eventName string, sessionID string, publisherGeneration, sourceSessionGeneration, sourceVideoSequence uint64) airplaycontract.Event {
	sequence := sourceVideoSequence
	return airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: publisherGeneration,
		Event: eventName, At: time.Unix(300, 0).UTC(),
		SourceSessionGeneration: sourceSessionGeneration, SourceVideoSequence: &sequence,
	}
}

func sourceClockPublisherReadyForWatermarkTest(sessionID string, publisherGeneration uint64) airplaycontract.Event {
	return airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: publisherGeneration,
		Event: "publisher-ready", At: time.Unix(301, 0).UTC(),
		ProtocolVersion: 1, VideoListenPort: 46001, AudioListenPort: 46002,
		PipelineStartAccepted: true,
	}
}

func TestSourceClockVideoWatermarkFinalRequiresUniqueExactIdentity(t *testing.T) {
	const sessionID = "0123456789abcdef"
	const publisherGeneration = uint64(7)
	watermark := sourceClockEventForWatermarkTest("video-watermark-final", sessionID, publisherGeneration, 4, 100)

	sequence, err := sourceClockVideoWatermarkFinal([]airplaycontract.Event{
		sourceClockPublisherReadyForWatermarkTest(sessionID, publisherGeneration), watermark,
	}, sessionID, publisherGeneration)
	if err != nil || sequence != 100 {
		t.Fatalf("watermark sequence=%d error=%v, want 100/nil", sequence, err)
	}

	tests := []struct {
		name   string
		events []airplaycontract.Event
	}{
		{name: "missing", events: []airplaycontract.Event{sourceClockPublisherReadyForWatermarkTest(sessionID, publisherGeneration)}},
		{name: "duplicate", events: []airplaycontract.Event{watermark, watermark}},
		{name: "foreign session", events: []airplaycontract.Event{watermark, sourceClockEventForWatermarkTest("rtsp-eof", "other", publisherGeneration, 4, 100)}},
		{name: "foreign publisher", events: []airplaycontract.Event{watermark, sourceClockEventForWatermarkTest("rtsp-eof", sessionID, publisherGeneration+1, 4, 100)}},
		{name: "zero source session", events: []airplaycontract.Event{sourceClockEventForWatermarkTest("video-watermark-final", sessionID, publisherGeneration, 0, 100)}},
		{name: "missing sequence", events: []airplaycontract.Event{func() airplaycontract.Event {
			event := watermark
			event.SourceVideoSequence = nil
			return event
		}()}},
		{name: "zero sequence", events: []airplaycontract.Event{sourceClockEventForWatermarkTest("video-watermark-final", sessionID, publisherGeneration, 4, 0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := sourceClockVideoWatermarkFinal(tt.events, sessionID, publisherGeneration); err == nil {
				t.Fatal("invalid watermark evidence was accepted")
			}
		})
	}
}

func TestNewSourceClockPostWatermarkProofRequiresExactSequenceEvidence(t *testing.T) {
	const sessionID = "0123456789abcdef"
	const oldPublisherGeneration = uint64(1)
	const candidatePublisherGeneration = uint64(2)
	oldEvents := []airplaycontract.Event{
		sourceClockPublisherReadyForWatermarkTest(sessionID, oldPublisherGeneration),
		sourceClockEventForWatermarkTest("video-watermark-final", sessionID, oldPublisherGeneration, 4, 100),
	}
	candidate := []airplaycontract.Event{
		sourceClockPublisherReadyForWatermarkTest(sessionID, candidatePublisherGeneration),
		sourceClockEventForWatermarkTest("video-input-idr", sessionID, candidatePublisherGeneration, 4, 101),
		sourceClockEventForWatermarkTest("video-decoded", sessionID, candidatePublisherGeneration, 4, 101),
		sourceClockEventForWatermarkTest("video-encoded-idr", sessionID, candidatePublisherGeneration, 4, 101),
	}

	proof, err := newSourceClockPostWatermarkProof(oldEvents, candidate, sessionID, oldPublisherGeneration, candidatePublisherGeneration)
	if err != nil {
		t.Fatalf("proof error=%v", err)
	}
	if proof.SessionID != sessionID || proof.PublisherGeneration != candidatePublisherGeneration ||
		proof.SourceSessionGeneration != 4 || proof.WatermarkSequence != 100 || proof.Sequence != 101 {
		t.Fatalf("proof=%+v", proof)
	}
	if _, err := newSourceClockPostWatermarkProof(oldEvents, candidate, sessionID, oldPublisherGeneration, oldPublisherGeneration); err == nil {
		t.Fatal("same old/candidate generation was accepted")
	}
	if _, err := newSourceClockPostWatermarkProof(oldEvents, candidate, sessionID, candidatePublisherGeneration, oldPublisherGeneration); err == nil {
		t.Fatal("reverse old/candidate generation was accepted")
	}

	tests := []struct {
		name   string
		mutate func([]airplaycontract.Event) []airplaycontract.Event
	}{
		{name: "missing input", mutate: func(events []airplaycontract.Event) []airplaycontract.Event { return events[:1] }},
		{name: "duplicate decoded", mutate: func(events []airplaycontract.Event) []airplaycontract.Event { return append(events, events[2]) }},
		{name: "sequence mismatch", mutate: func(events []airplaycontract.Event) []airplaycontract.Event {
			events[2] = sourceClockEventForWatermarkTest("video-decoded", sessionID, candidatePublisherGeneration, 4, 102)
			return events
		}},
		{name: "sequence at watermark", mutate: func(events []airplaycontract.Event) []airplaycontract.Event {
			events[0] = sourceClockPublisherReadyForWatermarkTest(sessionID, candidatePublisherGeneration)
			events[1] = sourceClockEventForWatermarkTest("video-input-idr", sessionID, candidatePublisherGeneration, 4, 100)
			events[2] = sourceClockEventForWatermarkTest("video-decoded", sessionID, candidatePublisherGeneration, 4, 100)
			events[3] = sourceClockEventForWatermarkTest("video-encoded-idr", sessionID, candidatePublisherGeneration, 4, 100)
			return events
		}},
		{name: "source session mismatch", mutate: func(events []airplaycontract.Event) []airplaycontract.Event {
			events[2] = sourceClockEventForWatermarkTest("video-decoded", sessionID, candidatePublisherGeneration, 5, 101)
			return events
		}},
		{name: "encoded is not idr", mutate: func(events []airplaycontract.Event) []airplaycontract.Event {
			events[3] = sourceClockEventForWatermarkTest("video-encoded", sessionID, candidatePublisherGeneration, 4, 101)
			return events
		}},
		{name: "foreign candidate identity", mutate: func(events []airplaycontract.Event) []airplaycontract.Event {
			events[2] = sourceClockEventForWatermarkTest("video-decoded", "other", candidatePublisherGeneration, 4, 101)
			return events
		}},
		{name: "candidate event has old generation", mutate: func(events []airplaycontract.Event) []airplaycontract.Event {
			events[2] = sourceClockEventForWatermarkTest("video-decoded", sessionID, oldPublisherGeneration, 4, 101)
			return events
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := make([]airplaycontract.Event, len(candidate))
			copy(mutated, candidate)
			if _, err := newSourceClockPostWatermarkProof(oldEvents, tt.mutate(mutated), sessionID, oldPublisherGeneration, candidatePublisherGeneration); err == nil {
				t.Fatal("invalid post-watermark proof was accepted")
			}
		})
	}

	oldWithCandidateGeneration := append([]airplaycontract.Event(nil), oldEvents...)
	oldWithCandidateGeneration[1].PublisherGeneration = candidatePublisherGeneration
	if _, err := newSourceClockPostWatermarkProof(oldWithCandidateGeneration, candidate, sessionID, oldPublisherGeneration, candidatePublisherGeneration); err == nil {
		t.Fatal("old watermark with candidate generation was accepted")
	}
}

func TestNewSourceClockPostWatermarkProofDoesNotUseEventTimes(t *testing.T) {
	const sessionID = "0123456789abcdef"
	const oldPublisherGeneration = uint64(1)
	const candidatePublisherGeneration = uint64(2)
	oldWatermark := sourceClockEventForWatermarkTest("video-watermark-final", sessionID, oldPublisherGeneration, 4, 100)
	candidate := []airplaycontract.Event{
		sourceClockPublisherReadyForWatermarkTest(sessionID, candidatePublisherGeneration),
		sourceClockEventForWatermarkTest("video-input-idr", sessionID, candidatePublisherGeneration, 4, 101),
		sourceClockEventForWatermarkTest("video-decoded", sessionID, candidatePublisherGeneration, 4, 101),
		sourceClockEventForWatermarkTest("video-encoded-idr", sessionID, candidatePublisherGeneration, 4, 101),
	}
	for index := range candidate {
		candidate[index].At = time.Unix(int64(400-index), 0).UTC()
		candidate[index].RunningTimeNS = nil
	}
	if _, err := newSourceClockPostWatermarkProof(
		[]airplaycontract.Event{oldWatermark}, candidate, sessionID, oldPublisherGeneration, candidatePublisherGeneration,
	); err != nil {
		t.Fatalf("time-independent proof rejected: %v", err)
	}
}

func TestReadSourceClockPublisherEventsReadsCompleteJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publisher.events.jsonl")
	first := airplaycontract.Event{
		Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 1,
		Event: "publisher-ready", At: time.Unix(100, 0).UTC(),
	}
	second := airplaycontract.Event{
		Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 1,
		Event: "recording-finalized", At: time.Unix(101, 0).UTC(),
		RecordingPath: "recording.mp4", RecordingClosed: true,
	}
	var lines []string
	for _, event := range []airplaycontract.Event{first, second} {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	events, err := readSourceClockPublisherEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != first || events[1] != second {
		t.Fatalf("events=%+v", events)
	}
}

func TestWaitSourceClockPublisherReadinessStopsImmediatelyWhenCandidateExits(t *testing.T) {
	done := make(chan error, 1)
	done <- errors.New("exit status 22")
	started := time.Now()
	readinessErr, consumed := waitSourceClockPublisherReadinessWithDone(
		context.Background(), filepath.Join(t.TempDir(), "missing.events"),
		"0123456789abcdef", 5, started, done, 2*time.Second,
	)
	if !consumed {
		t.Fatal("candidate exit was not consumed by the readiness waiter")
	}
	var exited *sourceClockPublisherExitedBeforeReadyError
	if !errors.As(readinessErr, &exited) {
		t.Fatalf("error=%v, want candidate-exit error", readinessErr)
	}
	if !errors.Is(readinessErr, errors.New("exit status 22")) && exited.waitErr == nil {
		t.Fatalf("candidate exit reason was lost: %v", readinessErr)
	}
}

func TestReadSourceClockPublisherEventsStopsAtMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publisher.events.jsonl")
	valid := `{"schema":2,"sessionId":"0123456789abcdef","publisherGeneration":1,"event":"publisher-ready","at":"1970-01-01T00:01:40Z"}`
	late := `{"schema":2,"sessionId":"0123456789abcdef","publisherGeneration":1,"event":"recording-finalized","at":"1970-01-01T00:01:41Z","recordingPath":"recording.mp4","recordingClosed":true}`
	if err := os.WriteFile(path, []byte(valid+"\n{partial\n"+late+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	events, err := readSourceClockPublisherEvents(path)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error=%v, want malformed line 2", err)
	}
	if len(events) != 1 || events[0].Event != "publisher-ready" {
		t.Fatalf("valid prefix events=%+v", events)
	}
}

func TestReadSourceClockPublisherEventsStopsAtOversizedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publisher.events.jsonl")
	valid := `{"schema":2,"sessionId":"0123456789abcdef","publisherGeneration":1,"event":"publisher-ready","at":"1970-01-01T00:01:40Z"}`
	late := `{"schema":2,"sessionId":"0123456789abcdef","publisherGeneration":1,"event":"recording-finalized","at":"1970-01-01T00:01:41Z","recordingPath":"recording.mp4","recordingClosed":true}`
	data := valid + "\n" + strings.Repeat("x", sourceClockPublisherEventMaxBytes+1) + "\n" + late + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	events, err := readSourceClockPublisherEvents(path)
	if err == nil || !strings.Contains(err.Error(), "after line 1") {
		t.Fatalf("error=%v, want oversized line after valid prefix", err)
	}
	if len(events) != 1 || events[0].Event != "publisher-ready" {
		t.Fatalf("valid prefix events=%+v", events)
	}
}

func TestObserveSourceClockPublisherEventLogDeliversValidPrefix(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := os.MkdirAll(filepath.Dir(artifacts.EventLog), 0700); err != nil {
		t.Fatal(err)
	}
	valid := `{"schema":2,"sessionId":"0123456789abcdef","publisherGeneration":1,"event":"publisher-ready","at":"1970-01-01T00:01:40Z"}`
	if err := os.WriteFile(artifacts.EventLog, []byte(valid+"\n{partial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}

	err := observeSourceClockPublisherEventLog(observer, artifacts)
	if err == nil {
		t.Fatal("malformed suffix was not reported")
	}
	events := observer.eventSnapshot()
	if len(events) != 1 || events[0].Event != "publisher-ready" {
		t.Fatalf("observed events=%+v", events)
	}
}

func TestCompleteSourceClockPublisherCarriesRTSPEOFFromEventLog(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := os.MkdirAll(filepath.Dir(artifacts.EventLog), 0700); err != nil {
		t.Fatal(err)
	}
	writeSourceClockEventForTest(t, artifacts.EventLog, airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "rtsp-eof", At: time.Unix(102, 0).UTC(), ProcessID: 4321,
	})
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}

	if err := completeSourceClockPublisherProcess(observer, artifacts, true, 4321, time.Unix(103, 0), errors.New("exit status 22")); err != nil {
		t.Fatal(err)
	}
	events := observer.eventSnapshot()
	completions, _ := observer.completionSnapshot()
	if len(events) != 1 || !events[0].RTSPEOFFor(sessionID, 1) || len(completions) != 1 || completions[0].ProcessID != 4321 {
		t.Fatalf("rtsp-eof events=%+v completions=%+v", events, completions)
	}
}

func TestObserveSourceClockPublisherEventLogRejectsForeignIdentity(t *testing.T) {
	const sessionID = "0123456789abcdef"
	valid := airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "rtsp-eof", At: time.Unix(104, 0).UTC(), ProcessID: 4321,
	}
	tests := map[string]func(*airplaycontract.Event){
		"generation": func(event *airplaycontract.Event) { event.PublisherGeneration = 2 },
		"session":    func(event *airplaycontract.Event) { event.SessionID = "fedcba9876543210" },
		"schema":     func(event *airplaycontract.Event) { event.Schema = 1 },
		"time":       func(event *airplaycontract.Event) { event.At = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
			if err := os.MkdirAll(filepath.Dir(artifacts.EventLog), 0700); err != nil {
				t.Fatal(err)
			}
			event := valid
			mutate(&event)
			writeSourceClockEventForTest(t, artifacts.EventLog, event)
			observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
			if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
				t.Fatal(err)
			}

			err := observeSourceClockPublisherEventLog(observer, artifacts)
			if err == nil || !strings.Contains(err.Error(), "identity") {
				t.Fatalf("foreign-identity error=%v", err)
			}
			if events := observer.eventSnapshot(); len(events) != 0 {
				t.Fatalf("foreign-identity events=%+v", events)
			}
		})
	}
}

func TestObserveSourceClockPublisherEventLogAllowsMissingLog(t *testing.T) {
	observer := &sourceClockArtifactObserverForTest{root: t.TempDir(), session: "0123456789abcdef"}
	artifacts := airplaycontract.NewPublisherArtifacts(observer.root, observer.session, 1)
	if err := observeSourceClockPublisherEventLog(observer, artifacts); err != nil {
		t.Fatalf("missing event log must be an empty generation, got %v", err)
	}
	if events := observer.eventSnapshot(); len(events) != 0 {
		t.Fatalf("events=%+v, want none", events)
	}
}

func TestObserveSourceClockPublisherEventLogAfterExitRejectsUnconfirmedExit(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := os.MkdirAll(filepath.Dir(artifacts.EventLog), 0700); err != nil {
		t.Fatal(err)
	}
	writeSourceClockEventForTest(t, artifacts.EventLog, airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "recording-finalized", At: time.Unix(105, 0).UTC(),
		RecordingPath: artifacts.Recording, RecordingClosed: true,
	})
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}

	err := observeSourceClockPublisherEventLogAfterExit(observer, artifacts, errDirectGStreamerProcessExitUnconfirmed)
	if !errors.Is(err, errDirectGStreamerProcessExitUnconfirmed) {
		t.Fatalf("error=%v, want unconfirmed exit", err)
	}
	if events := observer.eventSnapshot(); len(events) != 0 {
		t.Fatalf("unconfirmed-exit events=%+v", events)
	}
}

func TestObserveSourceClockPublisherEventLogAfterExitAcceptsConfirmedErrorExit(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := os.MkdirAll(filepath.Dir(artifacts.EventLog), 0700); err != nil {
		t.Fatal(err)
	}
	writeSourceClockEventForTest(t, artifacts.EventLog, airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "recording-finalized", At: time.Unix(106, 0).UTC(),
		RecordingPath: artifacts.Recording, RecordingClosed: true,
	})
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}

	if err := observeSourceClockPublisherEventLogAfterExit(observer, artifacts, errors.New("exit status 22")); err != nil {
		t.Fatal(err)
	}
	events := observer.eventSnapshot()
	if len(events) != 1 || !events[0].RecordingFinalizedFor(sessionID, 1, artifacts.Recording) {
		t.Fatalf("confirmed-error-exit events=%+v", events)
	}
}

func TestMonitorSourceClockObservesFinalEventAfterGracefulStop(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	if err := os.WriteFile(counterPath, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_WRITE_FINALIZED_ON_STOP", "1")
	stopEventAck := filepath.Join(t.TempDir(), "publisher-event-inspected.ack")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_STOP_EVENT_ACK", stopEventAck)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
	if err := observer.PreparePublisher(ctx, artifacts); err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{
		"-test.run=TestSourceClockPublisherProcess$", "fixture",
		"--session-id", sessionID,
		"--publisher-generation", "1",
		"--video-listen-port", "46001",
		"--audio-listen-port", "46002",
		"--recording", artifacts.Recording,
		"--event-log", artifacts.EventLog,
		"--stop-file", artifacts.StopRequest,
	}, artifacts.StopRequest)
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-event-stop-")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	gate, err := newSourceClockMetricsGate("receiver-event-stop-test")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	callbackSawSeal := make(chan bool, 1)
	go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, gate,
		artifacts.Ready, artifacts.MediaReady, sessionID, 1, root, observer,
		sourceClockNoSignalTimeout, nil, nil, tempDir, func() {
			_, sealed := observer.completionSnapshot()
			callbackSawSeal <- sealed
		})

	stopResult := make(chan bool, 1)
	go func() {
		stopResult <- manager.StopForUser(5 * time.Second)
	}()
	eventDeadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(artifacts.EventLog); err == nil {
			break
		}
		if time.Now().After(eventDeadline) {
			t.Fatal("publisher did not write its final event before exit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if events := observer.eventSnapshot(); len(events) != 0 {
		_ = os.WriteFile(stopEventAck, []byte("release\n"), 0600)
		t.Fatalf("events were observed before publisher exit: %+v", events)
	}
	if err := os.WriteFile(stopEventAck, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if receiver.Process != nil {
			_ = receiver.Process.Kill()
		}
		t.Fatal("source-clock monitor did not finish after cancellation")
	}
	select {
	case stopped := <-stopResult:
		if !stopped {
			t.Fatal("explicit user stop timed out")
		}
	case <-time.After(time.Second):
		t.Fatal("explicit user stop did not return")
	}
	events := observer.eventSnapshot()
	if len(events) != 1 || !events[0].RecordingFinalizedFor(sessionID, 1, artifacts.Recording) {
		t.Fatalf("graceful-stop events=%+v", events)
	}
	completions, sealed := observer.completionSnapshot()
	if len(completions) != 1 || completions[0].Artifacts != artifacts || !completions[0].Started || !completions[0].ExitConfirmed ||
		completions[0].ProcessID <= 0 || completions[0].CompletedAt.IsZero() || completions[0].CompletedAt.Location() != time.UTC ||
		!completions[0].ExitCodeKnown || completions[0].ExitCode != 0 || !sealed {
		t.Fatalf("graceful-stop completions=%+v sealed=%t", completions, sealed)
	}
	if initiators := observer.initiatorSnapshot(); len(initiators) != 1 || initiators[0].sessionID != sessionID || initiators[0].generation != 1 || initiators[0].reason != airplaycontract.TerminationReasonUserStop {
		t.Fatalf("graceful-stop initiators=%+v", initiators)
	}
	if calls := observer.callSnapshot(); len(calls) != 4 || calls[0] != "observe" || calls[1] != "complete" || calls[2] != "initiator" || calls[3] != "seal" {
		t.Fatalf("publisher observer call order=%v", calls)
	}
	select {
	case sealed := <-callbackSawSeal:
		if !sealed {
			t.Fatal("stop callback ran before publisher observer was sealed")
		}
	default:
		t.Fatal("stop callback was not invoked")
	}
}

func TestCompleteSourceClockPublisherRejectsUnconfirmedExitWithoutReadingEvents(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := os.MkdirAll(filepath.Dir(artifacts.EventLog), 0700); err != nil {
		t.Fatal(err)
	}
	writeSourceClockEventForTest(t, artifacts.EventLog, airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "recording-finalized", At: time.Unix(120, 0).UTC(),
		RecordingPath: artifacts.Recording, RecordingClosed: true,
	})
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	err := completeSourceClockPublisher(observer, artifacts, true, errDirectGStreamerProcessExitUnconfirmed)
	if !errors.Is(err, errDirectGStreamerProcessExitUnconfirmed) {
		t.Fatalf("error=%v", err)
	}
	if events := observer.eventSnapshot(); len(events) != 0 {
		t.Fatalf("unconfirmed exit read events=%+v", events)
	}
	completions, sealed := observer.completionSnapshot()
	if len(completions) != 1 || completions[0].Artifacts != artifacts || !completions[0].Started || completions[0].ExitConfirmed || sealed {
		t.Fatalf("completions=%+v sealed=%t", completions, sealed)
	}
}

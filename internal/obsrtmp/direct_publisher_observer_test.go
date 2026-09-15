package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

func TestDirectPublisherObserverStoresFinalizedEventOnlyForRegisteredDescriptor(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	first := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	second := airplaycontract.NewPublisherArtifacts(root, sessionID, 2)
	if err := observer.PreparePublisher(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := observer.PreparePublisher(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if !observer.claimCurrentForPublication(second) {
		t.Fatal("generation 2 publication claim failed")
	}
	now := time.Date(2026, 9, 5, 0, 0, 25, 0, time.UTC)
	validFirst := airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "recording-finalized", At: now,
		RecordingPath: first.Recording, RecordingClosed: true,
	}
	observer.ObservePublisher(validFirst)
	if got, ok := observer.recordingFinalizedEvent(1); !ok || got != validFirst {
		t.Fatalf("generation 1 finalized event = %+v ok=%t", got, ok)
	}

	invalidSecond := validFirst
	invalidSecond.PublisherGeneration = 2
	invalidSecond.RecordingPath = first.Recording
	observer.ObservePublisher(invalidSecond)
	if got, ok := observer.recordingFinalizedEvent(2); ok {
		t.Fatalf("wrong-path generation 2 event was stored: %+v", got)
	}

	validSecond := invalidSecond
	validSecond.RecordingPath = second.Recording
	observer.ObservePublisher(validSecond)
	if got, ok := observer.recordingFinalizedEvent(2); !ok || got != validSecond {
		t.Fatalf("generation 2 finalized event = %+v ok=%t", got, ok)
	}
	observer.ObservePublisher(invalidSecond)
	if got, ok := observer.recordingFinalizedEvent(2); !ok || got != validSecond {
		t.Fatalf("invalid event replaced valid generation 2 finalize: %+v ok=%t", got, ok)
	}

	unregistered := validSecond
	unregistered.PublisherGeneration = 3
	unregistered.RecordingPath = airplaycontract.NewPublisherArtifacts(root, sessionID, 3).Recording
	observer.ObservePublisher(unregistered)
	if got, ok := observer.recordingFinalizedEvent(3); ok {
		t.Fatalf("unregistered generation event was stored: %+v", got)
	}
	if current, ok := observer.currentArtifacts(); !ok || current != second {
		t.Fatalf("old generation finalize changed current descriptor: %+v ok=%t", current, ok)
	}
	if observer.publishedGeneration != 2 || observer.candidateGeneration != 2 {
		t.Fatalf("finalize changed publication state: published=%d candidate=%d", observer.publishedGeneration, observer.candidateGeneration)
	}
}

func TestDirectPublisherObserverStoresFirstRegisteredCompletion(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	first := airplaycontract.PublisherCompletion{Artifacts: artifacts, Started: true, ExitConfirmed: true}
	observer.CompletePublisher(first)
	observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: artifacts, Started: true, EventLogError: "late duplicate"})
	got, ok := observer.publisherCompletion(1)
	if !ok || got != first {
		t.Fatalf("completion=%+v ok=%t, want first=%+v", got, ok, first)
	}

	foreign := airplaycontract.NewPublisherArtifacts(root, sessionID, 2)
	observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: foreign, Started: true, ExitConfirmed: true})
	if _, ok := observer.publisherCompletion(2); ok {
		t.Fatal("unregistered completion was stored")
	}
}

func TestDirectPublisherObserverSealRejectsNewGenerationAndLateCompletion(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	first := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	observer.SealPublishers()
	if err := observer.PreparePublisher(t.Context(), airplaycontract.NewPublisherArtifacts(root, sessionID, 2)); err == nil {
		t.Fatal("sealed observer accepted a new generation")
	}
	observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: first, Started: true, ExitConfirmed: true})
	if _, ok := observer.publisherCompletion(1); ok {
		t.Fatal("sealed observer accepted a late completion")
	}
}

func TestDirectPublisherObserverSealClosesRegistryAndCleansOnlyEventEvidence(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{artifacts.Ready, artifacts.MediaReady, artifacts.EventLog, artifacts.Recording, artifacts.ProcessLog, artifacts.StopRequest} {
		if err := os.WriteFile(path, []byte("evidence"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	observer.SealPublishers()
	observer.mu.Lock()
	rootClosed := observer.rootFS == nil
	observer.mu.Unlock()
	if !rootClosed {
		t.Fatal("publisher registry root remained open after seal")
	}
	for _, path := range []string{artifacts.Ready, artifacts.MediaReady, artifacts.EventLog} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("event evidence survived seal: path=%q err=%v", path, err)
		}
	}
	for _, path := range []string{artifacts.Recording, artifacts.ProcessLog, artifacts.StopRequest} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("durable publisher artifact was removed at seal: path=%q err=%v", path, err)
		}
	}
	if err := observer.Close(); err != nil {
		t.Fatalf("Close after SealPublishers: %v", err)
	}
}

func TestDirectPublisherObserverRejectsLateEventAfterSeal(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	observer.ObservePublisher(airplaycontract.Event{
		Schema:              2,
		SessionID:           sessionID,
		PublisherGeneration: 1,
		Event:               "recording-finalized",
		At:                  time.Now().UTC(),
		RecordingPath:       artifacts.Recording,
		RecordingClosed:     true,
	})
	if _, ok := observer.recordingFinalizedEvent(1); ok {
		t.Fatal("publisher observer accepted a late event after seal")
	}
}

func TestDirectPublisherObserverSealRacesSafelyAndKeepsRecordingOutcomes(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	first := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	second := airplaycontract.NewPublisherArtifacts(root, sessionID, 2)
	for _, artifacts := range []airplaycontract.PublisherArtifacts{first, second} {
		if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < 100; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			if index%2 == 0 {
				observer.ObservePublisher(airplaycontract.Event{
					Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
					Event: "recording-finalized", At: time.Unix(int64(index+1), 0).UTC(),
					RecordingPath: first.Recording, RecordingClosed: true,
				})
				return
			}
			observer.CompletePublisher(airplaycontract.PublisherCompletion{
				Artifacts: first, Started: true, ExitConfirmed: true,
			})
		}(index)
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		observer.SealPublishers()
	}()
	close(start)
	workers.Wait()
	observer.SealPublishers()

	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 2,
		Event: "recording-finalized", At: time.Unix(200, 0).UTC(),
		RecordingPath: second.Recording, RecordingClosed: true,
	})
	observer.CompletePublisher(airplaycontract.PublisherCompletion{
		Artifacts: second, Started: true, ExitConfirmed: true,
	})
	if _, ok := observer.recordingFinalizedEvent(2); ok {
		t.Fatal("sealed observer accepted a late generation-2 event")
	}
	if _, ok := observer.publisherCompletion(2); ok {
		t.Fatal("sealed observer accepted a late generation-2 completion")
	}

	outcome := airplaycontract.RecordingOutcome{
		Artifacts: second, Closed: true, HasRealVideo: true, ProbeOK: true,
		DurationNS: uint64(time.Second), Reason: "verified",
	}
	observer.FinishRecording(outcome)
	observer.mu.Lock()
	stored, ok := observer.outcomes[second.Generation]
	observer.mu.Unlock()
	if !ok || stored != outcome {
		t.Fatalf("recording outcome after seal was not retained: got=%+v ok=%t", stored, ok)
	}
}

func TestDirectPublisherObserverSealStartsNonBlockingRecordingProbe(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	runningTime := uint64(1)
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "video-decoded", At: time.Now().UTC(),
		VideoDecoded: true, RunningTimeNS: &runningTime,
	})
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "recording-finalized", At: time.Now().UTC(),
		RecordingPath: artifacts.Recording, RecordingClosed: true,
	})
	observer.CompletePublisher(airplaycontract.PublisherCompletion{
		Artifacts: artifacts, Started: true, ExitConfirmed: true,
	})
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	err = observer.configureRecordingProbe(time.Second, 1280, 720, func(ctx context.Context, path string) (video.MediaProbe, error) {
		if path != artifacts.Recording {
			t.Errorf("probe path=%q, want %q", path, artifacts.Recording)
		}
		close(probeStarted)
		select {
		case <-releaseProbe:
			return video.MediaProbe{Duration: 2, Streams: []video.MediaStream{{CodecType: "video", Width: 1280, Height: 720}}}, nil
		case <-ctx.Done():
			return video.MediaProbe{}, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	probeOutcome := make(chan airplaycontract.RecordingOutcome, 1)
	if err := observer.configureRecordingOutcomeHandler(func(outcome airplaycontract.RecordingOutcome) {
		probeOutcome <- outcome
	}); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	observer.SealPublishers()
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("SealPublishers blocked on recording probe for %s", elapsed)
	}
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("recording probe worker did not start")
	}
	observer.mu.Lock()
	_, completedEarly := observer.outcomes[1]
	observer.mu.Unlock()
	if completedEarly {
		t.Fatal("recording outcome completed before probe release")
	}
	close(releaseProbe)
	if err := observer.waitRecordingProbes(t.Context()); err != nil {
		t.Fatal(err)
	}
	observer.mu.Lock()
	outcome, ok := observer.outcomes[1]
	observer.mu.Unlock()
	if !ok || !outcome.ProbeOK || outcome.Reason != recordingReasonVerified {
		t.Fatalf("outcome=%+v ok=%t", outcome, ok)
	}
	select {
	case notified := <-probeOutcome:
		if notified != outcome {
			t.Fatalf("notified outcome=%+v, want %+v", notified, outcome)
		}
	default:
		t.Fatal("asynchronous recording outcome was not forwarded")
	}
}

func TestDirectPublisherObserverProbeAdmissionRequiresPerGenerationEvidence(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := make([]airplaycontract.PublisherArtifacts, 3)
	for index := range artifacts {
		artifacts[index] = airplaycontract.NewPublisherArtifacts(root, sessionID, uint64(index+1))
		if err := observer.PreparePublisher(t.Context(), artifacts[index]); err != nil {
			t.Fatal(err)
		}
	}
	runningTime := uint64(1)
	for _, generation := range []uint64{2, 3} {
		observer.ObservePublisher(airplaycontract.Event{
			Schema: 2, SessionID: sessionID, PublisherGeneration: generation,
			Event: "video-decoded", At: time.Now().UTC(),
			VideoDecoded: true, RunningTimeNS: &runningTime,
		})
	}
	observer.CompletePublisher(airplaycontract.PublisherCompletion{
		Artifacts: artifacts[1], Started: true, ExitConfirmed: true,
	})
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 3,
		Event: "recording-finalized", At: time.Now().UTC(),
		RecordingPath: artifacts[2].Recording, RecordingClosed: true,
	})
	probeCalls := 0
	if err := observer.configureRecordingProbe(time.Second, 1280, 720, func(context.Context, string) (video.MediaProbe, error) {
		probeCalls++
		return video.MediaProbe{}, errors.New("probe should not run")
	}); err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	if err := observer.waitRecordingProbes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 0 {
		t.Fatalf("probe calls=%d, want 0", probeCalls)
	}
	observer.mu.Lock()
	first := observer.outcomes[1]
	second := observer.outcomes[2]
	third := observer.outcomes[3]
	observer.mu.Unlock()
	if first.Reason != recordingReasonNoRealVideo {
		t.Fatalf("generation 1 outcome=%+v", first)
	}
	if second.Reason != recordingReasonCloseNotConfirmed {
		t.Fatalf("generation 2 outcome=%+v", second)
	}
	if third.Reason != recordingReasonCloseNotConfirmed {
		t.Fatalf("generation 3 unconfirmed completion outcome=%+v", third)
	}
}

func TestDirectPublisherObserverNotifiesRecordingOutcomesDoneAfterEveryGeneration(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for generation := uint64(1); generation <= 2; generation++ {
		artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, generation)
		if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
			t.Fatal(err)
		}
	}
	if err := observer.configureRecordingProbe(time.Second, 1280, 720, func(context.Context, string) (video.MediaProbe, error) {
		t.Fatal("probe must not run without per-generation admission evidence")
		return video.MediaProbe{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	individual := make(chan uint64, 2)
	if err := observer.configureRecordingOutcomeHandler(func(outcome airplaycontract.RecordingOutcome) {
		individual <- outcome.Artifacts.Generation
	}); err != nil {
		t.Fatal(err)
	}
	type doneNotice struct {
		sessionID string
		outcomes  int
	}
	done := make(chan doneNotice, 2)
	if err := observer.configureRecordingOutcomesDoneHandler(func(gotSessionID string) {
		observer.mu.Lock()
		count := len(observer.outcomes)
		observer.mu.Unlock()
		done <- doneNotice{sessionID: gotSessionID, outcomes: count}
	}); err != nil {
		t.Fatal(err)
	}

	observer.SealPublishers()
	if err := observer.waitRecordingProbes(t.Context()); err != nil {
		t.Fatal(err)
	}
	for wantGeneration := uint64(1); wantGeneration <= 2; wantGeneration++ {
		select {
		case gotGeneration := <-individual:
			if gotGeneration != wantGeneration {
				t.Fatalf("outcome order=%d, want %d", gotGeneration, wantGeneration)
			}
		default:
			t.Fatalf("missing generation %d outcome", wantGeneration)
		}
	}
	select {
	case notice := <-done:
		if notice.sessionID != sessionID || notice.outcomes != 2 {
			t.Fatalf("done notice=%+v", notice)
		}
	default:
		t.Fatal("recording outcomes done was not notified")
	}
	select {
	case notice := <-done:
		t.Fatalf("duplicate recording outcomes done notice=%+v", notice)
	default:
	}
}

func TestDirectPublisherObserverBatchDoneWaitsForConcurrentIndividualCallback(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	if err := observer.configureRecordingProbe(time.Second, 1280, 720, func(context.Context, string) (video.MediaProbe, error) {
		t.Fatal("probe must not run without admission evidence")
		return video.MediaProbe{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	individualStarted := make(chan struct{})
	releaseIndividual := make(chan struct{})
	if err := observer.configureRecordingOutcomeHandler(func(airplaycontract.RecordingOutcome) {
		close(individualStarted)
		<-releaseIndividual
	}); err != nil {
		t.Fatal(err)
	}
	batchDone := make(chan string, 1)
	if err := observer.configureRecordingOutcomesDoneHandler(func(gotSessionID string) {
		batchDone <- gotSessionID
	}); err != nil {
		t.Fatal(err)
	}
	externalDone := make(chan struct{})
	go func() {
		observer.FinishRecording(airplaycontract.RecordingOutcome{Artifacts: artifacts, Reason: recordingReasonNoRealVideo})
		close(externalDone)
	}()
	select {
	case <-individualStarted:
	case <-time.After(time.Second):
		t.Fatal("individual callback did not start")
	}
	observer.SealPublishers()
	waitCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := observer.waitRecordingProbes(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe worker completed before individual callback: %v", err)
	}
	select {
	case sessionID := <-batchDone:
		t.Fatalf("batch done ran before individual callback for %q", sessionID)
	default:
	}
	close(releaseIndividual)
	select {
	case <-externalDone:
	case <-time.After(time.Second):
		t.Fatal("individual callback did not finish")
	}
	if err := observer.waitRecordingProbes(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case gotSessionID := <-batchDone:
		if gotSessionID != sessionID {
			t.Fatalf("batch done session=%q", gotSessionID)
		}
	default:
		t.Fatal("batch done was not delivered after individual callback")
	}
}

func TestDirectPublisherObserverDoesNotNotifyEmptyRecordingBatch(t *testing.T) {
	observer, err := newDirectPublisherObserver(t.TempDir(), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.configureRecordingProbe(time.Second, 1280, 720, func(context.Context, string) (video.MediaProbe, error) {
		t.Fatal("empty batch must not probe")
		return video.MediaProbe{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	if err := observer.configureRecordingOutcomesDoneHandler(func(sessionID string) { done <- sessionID }); err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	if err := observer.waitRecordingProbes(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case sessionID := <-done:
		t.Fatalf("empty recording batch notified %q", sessionID)
	default:
	}
}

func TestDirectPublisherObserverConcurrentSealNotifiesRecordingBatchOnce(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	if err := observer.configureRecordingProbe(time.Second, 1280, 720, func(context.Context, string) (video.MediaProbe, error) {
		t.Fatal("probe must not run without admission evidence")
		return video.MediaProbe{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 2)
	if err := observer.configureRecordingOutcomesDoneHandler(func(gotSessionID string) { done <- gotSessionID }); err != nil {
		t.Fatal(err)
	}
	if err := observer.configureRecordingOutcomesDoneHandler(func(string) {}); err == nil {
		t.Fatal("duplicate recording outcomes done handler was accepted")
	}
	var sealers sync.WaitGroup
	for index := 0; index < 20; index++ {
		sealers.Add(1)
		go func() {
			defer sealers.Done()
			observer.SealPublishers()
		}()
	}
	sealers.Wait()
	if err := observer.waitRecordingProbes(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case gotSessionID := <-done:
		if gotSessionID != sessionID {
			t.Fatalf("batch done session=%q", gotSessionID)
		}
	default:
		t.Fatal("batch done was not delivered")
	}
	select {
	case gotSessionID := <-done:
		t.Fatalf("duplicate batch done session=%q", gotSessionID)
	default:
	}
}

func TestDirectPublisherObserverRejectsRecordingProbeConfigurationAfterSeal(t *testing.T) {
	observer, err := newDirectPublisherObserver(t.TempDir(), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	if err := observer.configureRecordingProbe(time.Second, 1280, 720, func(context.Context, string) (video.MediaProbe, error) {
		return video.MediaProbe{}, nil
	}); err == nil {
		t.Fatal("sealed observer accepted recording probe configuration")
	}
	if err := observer.configureRecordingOutcomesDoneHandler(func(string) {}); err == nil {
		t.Fatal("sealed observer accepted recording outcomes done handler")
	}
}

func TestDirectPublisherObserverFinishesRecordingExactlyOnceAfterSeal(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	notified := make(chan airplaycontract.RecordingOutcome, 2)
	if err := observer.configureRecordingOutcomeHandler(func(outcome airplaycontract.RecordingOutcome) {
		notified <- outcome
	}); err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	first := airplaycontract.RecordingOutcome{
		Artifacts: artifacts, Closed: true, HasRealVideo: true, ProbeOK: true,
		DurationNS: uint64(time.Second), Reason: recordingReasonVerified,
	}
	duplicate := first
	duplicate.ProbeOK = false
	duplicate.Reason = recordingReasonProbeFailed
	observer.FinishRecording(first)
	observer.FinishRecording(duplicate)
	foreign := first
	foreign.Artifacts.Generation = 2
	observer.FinishRecording(foreign)

	select {
	case got := <-notified:
		if got != first {
			t.Fatalf("notified outcome=%+v, want %+v", got, first)
		}
	default:
		t.Fatal("recording outcome handler was not called")
	}
	select {
	case extra := <-notified:
		t.Fatalf("recording outcome handler was called more than once: %+v", extra)
	default:
	}
	observer.mu.Lock()
	stored := observer.outcomes[1]
	observer.mu.Unlock()
	if stored != first {
		t.Fatalf("stored outcome=%+v, want first %+v", stored, first)
	}
}

func TestDirectPublisherObserverCandidateIsolationAndReadinessPerGeneration(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	ledger, err := airplaycontract.NewDeliveryLedger(root, sessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := newDirectPublisherObserverWithLedger(root, ledger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	first, err := ledger.Allocate(sessionID, 1, 0, "initial", testContractDeliveryProfile(), testContractDeliveryOutput(1280, 720))
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.PrepareDeliveryGeneration(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Commit(0, first); err != nil {
		t.Fatal(err)
	}
	second, err := ledger.Allocate(sessionID, 1, 1, "planned", testContractDeliveryProfile(), testContractDeliveryOutput(640, 360))
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.PrepareDeliveryGeneration(t.Context(), second); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: second.Generation,
		Event: "publisher-ready", At: now, ProtocolVersion: 1,
		VideoListenPort: 5000, AudioListenPort: 5001, PipelineStartAccepted: true,
	})
	runningTime := uint64(1)
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: second.Generation,
		Event: "video-decoded", At: now.Add(time.Millisecond), VideoDecoded: true, RunningTimeNS: &runningTime,
	})
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: second.Generation,
		Event: "diagnostic", At: now.Add(2 * time.Millisecond),
	})

	readiness, ok := observer.generationReadiness(second)
	if !ok || !readiness.PublisherReady || !readiness.MediaReady {
		t.Fatalf("candidate readiness=%+v ok=%t", readiness, ok)
	}
	if got := ledger.Snapshot().ActiveGeneration; got != first.Generation {
		t.Fatalf("active generation=%d, want %d", got, first.Generation)
	}
	if current, ok := observer.currentArtifacts(); ok {
		t.Fatalf("strict candidate leaked into legacy current artifacts: %+v", current)
	}
	if observer.claimCurrentForPublication(second.ArtifactPaths) {
		t.Fatal("strict candidate was publishable before commit")
	}
}

func TestDirectPublisherObserverUsesGenerationRecordingDimensions(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	ledger, err := airplaycontract.NewDeliveryLedger(root, sessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := newDirectPublisherObserverWithLedger(root, ledger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	first, err := ledger.Allocate(sessionID, 1, 0, "initial", testContractDeliveryProfile(), testContractDeliveryOutput(1280, 720))
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.PrepareDeliveryGeneration(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Commit(0, first); err != nil {
		t.Fatal(err)
	}
	second, err := ledger.Allocate(sessionID, 1, 1, "quality-360", testContractDeliveryProfile(), testContractDeliveryOutput(640, 360))
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.PrepareDeliveryGeneration(t.Context(), second); err != nil {
		t.Fatal(err)
	}

	runningTime := uint64(1)
	for _, descriptor := range []airplaycontract.DeliveryGeneration{first, second} {
		observer.ObservePublisher(airplaycontract.Event{
			Schema: 2, SessionID: sessionID, PublisherGeneration: descriptor.Generation,
			Event: "video-decoded", At: time.Now().UTC(), VideoDecoded: true, RunningTimeNS: &runningTime,
		})
		observer.ObservePublisher(airplaycontract.Event{
			Schema: 2, SessionID: sessionID, PublisherGeneration: descriptor.Generation,
			Event: "recording-finalized", At: time.Now().UTC(),
			RecordingPath: descriptor.ArtifactPaths.Recording, RecordingClosed: true,
		})
		observer.CompletePublisher(airplaycontract.PublisherCompletion{
			Artifacts: descriptor.ArtifactPaths, Started: true, ExitConfirmed: true,
		})
	}
	if err := observer.configureRecordingProbe(time.Second, 1920, 1080, func(_ context.Context, path string) (video.MediaProbe, error) {
		switch path {
		case first.ArtifactPaths.Recording:
			return video.MediaProbe{Duration: 2, Streams: []video.MediaStream{{CodecType: "video", Width: 1280, Height: 720}}}, nil
		case second.ArtifactPaths.Recording:
			return video.MediaProbe{Duration: 2, Streams: []video.MediaStream{{CodecType: "video", Width: 640, Height: 360}}}, nil
		default:
			return video.MediaProbe{}, errors.New("unexpected recording path")
		}
	}); err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	if err := observer.waitRecordingProbes(t.Context()); err != nil {
		t.Fatal(err)
	}
	observer.mu.Lock()
	firstOutcome := observer.outcomes[first.Generation]
	secondOutcome := observer.outcomes[second.Generation]
	observer.mu.Unlock()
	if !firstOutcome.ProbeOK || !secondOutcome.ProbeOK {
		t.Fatalf("generation outcomes: first=%+v second=%+v", firstOutcome, secondOutcome)
	}
}

func testContractDeliveryProfile() airplaycontract.DeliveryProfile {
	return airplaycontract.DeliveryProfile{Mode: "rtsp-ultra", Transport: "rtsp", HLSVariant: "lowLatency", HLSSegmentCount: 7, HLSSegmentDuration: "1s"}
}

func testContractDeliveryOutput(width, height int) airplaycontract.DeliveryOutput {
	return airplaycontract.DeliveryOutput{
		Width: width, Height: height, SourceFPS: 60, OutputFPS: 60,
		VideoBitrateKbps: 4500, MaxRateKbps: 5200, BufferSizeKbps: 9000,
		AudioBitrateBps: 160000, GOPFrames: 60,
	}
}

func TestDirectPublisherObserverRejectsOutcomeHandlerAfterSeal(t *testing.T) {
	observer, err := newDirectPublisherObserver(t.TempDir(), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	if err := observer.configureRecordingOutcomeHandler(func(airplaycontract.RecordingOutcome) {}); err == nil {
		t.Fatal("sealed observer accepted recording outcome handler")
	}
}

func TestDirectPublisherObserverConcurrentFinishNotifiesOnce(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	notified := make(chan airplaycontract.RecordingOutcome, 100)
	if err := observer.configureRecordingOutcomeHandler(func(outcome airplaycontract.RecordingOutcome) {
		notified <- outcome
	}); err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	outcome := airplaycontract.RecordingOutcome{Artifacts: artifacts, Reason: recordingReasonNoRealVideo}
	var workers sync.WaitGroup
	for index := 0; index < 100; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			observer.FinishRecording(outcome)
		}()
	}
	workers.Wait()
	if got := len(notified); got != 1 {
		t.Fatalf("outcome notifications=%d, want 1", got)
	}
}

func TestDirectPublisherObserverOutcomeCallbackRunsAfterStoredAndUnlocked(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	outcome := airplaycontract.RecordingOutcome{Artifacts: artifacts, Reason: recordingReasonNoRealVideo}
	callbackDone := make(chan bool, 1)
	if err := observer.configureRecordingOutcomeHandler(func(got airplaycontract.RecordingOutcome) {
		observer.mu.Lock()
		stored, ok := observer.outcomes[got.Artifacts.Generation]
		observer.mu.Unlock()
		callbackDone <- ok && stored == got
	}); err != nil {
		t.Fatal(err)
	}
	observer.SealPublishers()
	go observer.FinishRecording(outcome)
	select {
	case storedFirst := <-callbackDone:
		if !storedFirst {
			t.Fatal("outcome callback ran before the result was stored")
		}
	case <-time.After(time.Second):
		t.Fatal("outcome callback could not re-enter observer after mutex release")
	}
}

func TestDirectPublisherObserverPreparesOnlyFreshCanonicalGeneration(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	first := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(context.Background(), first); err != nil {
		t.Fatalf("prepare generation 1: %v", err)
	}
	if current, ok := observer.currentArtifacts(); !ok || current != first {
		t.Fatalf("current after generation 1=%+v ok=%t", current, ok)
	}
	if info, err := os.Stat(filepath.Dir(first.Recording)); err != nil || !info.IsDir() {
		t.Fatalf("generation directory was not prepared: info=%v err=%v", info, err)
	}
	for _, path := range publisherArtifactPaths(first) {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("prepare created or reused artifact %q: %v", path, err)
		}
	}

	second := airplaycontract.NewPublisherArtifacts(root, sessionID, 2)
	if err := os.WriteFile(second.Recording, []byte("stale-generation-2"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := observer.PreparePublisher(context.Background(), second); err == nil {
		t.Fatal("generation 2 reused an existing recording")
	}
	data, err := os.ReadFile(second.Recording)
	if err != nil || string(data) != "stale-generation-2" {
		t.Fatalf("existing artifact was modified: data=%q err=%v", data, err)
	}
	if current, ok := observer.currentArtifacts(); !ok || current != first {
		t.Fatalf("failed generation changed current: %+v ok=%t", current, ok)
	}

	third := airplaycontract.NewPublisherArtifacts(root, sessionID, 3)
	if err := observer.PreparePublisher(context.Background(), third); err != nil {
		t.Fatalf("prepare skipped generation 3: %v", err)
	}
	if current, ok := observer.currentArtifacts(); !ok || current != third {
		t.Fatalf("current after generation 3=%+v ok=%t", current, ok)
	}
	if err := observer.PreparePublisher(context.Background(), first); err == nil {
		t.Fatal("previously registered generation was reused")
	}
}

func TestDirectPublisherObserverRejectsForeignPathsAndCanceledPrepare(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	foreign := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	foreign.StopRequest = filepath.Join(root, "outside.stop")
	if err := observer.PreparePublisher(context.Background(), foreign); err == nil {
		t.Fatal("foreign stop path was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canonical := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(ctx, canonical); err == nil {
		t.Fatal("canceled prepare was accepted")
	}
	if _, ok := observer.currentArtifacts(); ok {
		t.Fatal("rejected prepare registered a current generation")
	}
}

func TestDirectPublisherObserverCancellationWhileWaitingDoesNotRegister(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	entered := make(chan struct{})
	release := make(chan struct{})
	observer.beforePrepareLock = func() {
		close(entered)
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- observer.PreparePublisher(ctx, airplaycontract.NewPublisherArtifacts(root, sessionID, 1))
	}()
	<-entered
	cancel()
	close(release)
	if err := <-result; err == nil {
		t.Fatal("prepare ignored cancellation while waiting")
	}
	if _, ok := observer.currentArtifacts(); ok {
		t.Fatal("canceled prepare registered a generation")
	}
}

func TestDirectPublisherObserverRootRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	const sessionID = "0123456789abcdef"
	if err := os.Mkdir(filepath.Join(root, "airplay"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "airplay", sessionID)); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	if err := observer.PreparePublisher(context.Background(), airplaycontract.NewPublisherArtifacts(root, sessionID, 1)); err == nil {
		t.Fatal("root-escaping session symlink was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("prepare wrote through escaping symlink: %v", entries)
	}
}

func TestNewDirectPublisherObserverRejectsUnsafeSessionID(t *testing.T) {
	for _, id := range []string{"", " ", ".", "..", "../escape", `..\escape`, "nested/session", "CON", "aux.txt", "0123456789abcde.", "0123456789ABCDEf", "0123456789abcdeg", "abc:def"} {
		t.Run(id, func(t *testing.T) {
			if _, err := newDirectPublisherObserver(t.TempDir(), id); err == nil {
				t.Fatalf("unsafe session ID %q was accepted", id)
			}
		})
	}
}

func TestDirectPublisherObserverConvertsConfirmedPublisherExitToTerminationObservation(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 7, 21, 0, 0, 0, time.FixedZone("test", 9*60*60))
	completion := airplaycontract.PublisherCompletion{
		Artifacts: artifacts, Started: true, ExitConfirmed: true,
		ProcessID: 4321, CompletedAt: at, ExitCode: 17, ExitCodeKnown: true,
	}
	observer.CompletePublisher(completion)

	got, ok := observer.terminationSnapshot(1)
	if !ok {
		t.Fatal("confirmed publisher exit produced no termination observation")
	}
	if got.SessionID != sessionID || got.PublisherGeneration != 1 ||
		got.FirstObservedComponent != airplaycontract.TerminationComponentPublisher ||
		got.Initiator != streamTerminationInitiatorUnknown ||
		got.Evidence != airplaycontract.TerminationEvidencePublisherExit ||
		got.ExitCode != 17 || got.Reason != airplaycontract.TerminationReasonPublisherExited {
		t.Fatalf("termination snapshot = %+v", got)
	}
	if !got.At.Equal(at.UTC()) || got.At.Location() != time.UTC {
		t.Fatalf("termination time = %v, want UTC %v", got.At, at.UTC())
	}
	observations := observer.terminationObservations(1)
	if len(observations) != 1 || observations[0].ProcessID != 4321 || observations[0].Sequence != 1 || !observations[0].ValidFor(sessionID, 1) {
		t.Fatalf("termination observations = %+v", observations)
	}

	observer.CompletePublisher(completion)
	if observations := observer.terminationObservations(1); len(observations) != 1 {
		t.Fatalf("duplicate completion appended termination observations = %+v", observations)
	}
}

func TestDirectPublisherObserverRecordsNativeRTSPEOFBeforePublisherExit(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	runningTime := uint64(0)
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "video-decoded", At: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
		VideoDecoded: true, RunningTimeNS: &runningTime,
	})
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "rtsp-eof", At: time.Date(2026, 9, 7, 12, 0, 1, 0, time.UTC),
		ProcessID: 4321,
	})
	observer.CompletePublisher(airplaycontract.PublisherCompletion{
		Artifacts: artifacts, Started: true, ExitConfirmed: true,
		ProcessID: 4321, CompletedAt: time.Date(2026, 9, 7, 12, 0, 2, 0, time.UTC),
		ExitCode: 22, ExitCodeKnown: true,
	})

	got, ok := observer.terminationSnapshot(1)
	if !ok || got.FirstObservedComponent != airplaycontract.TerminationComponentRTSPEOF ||
		got.Initiator != streamTerminationInitiatorUnknown ||
		got.Evidence != airplaycontract.TerminationEvidenceRTSPEOF ||
		got.Reason != airplaycontract.TerminationReasonRTSPEOF {
		t.Fatalf("termination snapshot = %+v, ok=%t", got, ok)
	}
	observations := observer.terminationObservations(1)
	if len(observations) != 2 || observations[0].Component != airplaycontract.TerminationComponentRTSPEOF ||
		observations[0].ProcessID != 4321 || observations[0].Sequence != 1 ||
		observations[1].Component != airplaycontract.TerminationComponentPublisher || observations[1].Sequence != 2 {
		t.Fatalf("ordered termination observations = %+v", observations)
	}
	observer.mu.Lock()
	mediaEvent := observer.events[1]
	observer.mu.Unlock()
	if !mediaEvent.MediaReadyFor(sessionID, 1) {
		t.Fatalf("RTSP EOF replaced the media-ready event: %+v", mediaEvent)
	}
}

func TestDirectPublisherObserverRejectsInvalidRTSPEOFWithoutReplacingMediaReady(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	runningTime := uint64(0)
	media := airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "video-decoded", At: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
		VideoDecoded: true, RunningTimeNS: &runningTime,
	}
	observer.ObservePublisher(media)
	invalid := airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "rtsp-eof", At: time.Date(2026, 9, 7, 12, 0, 1, 0, time.UTC),
	}
	observer.ObservePublisher(invalid)

	observer.mu.Lock()
	stored := observer.events[1]
	observer.mu.Unlock()
	if stored != media {
		t.Fatalf("invalid RTSP EOF replaced media-ready event: %+v", stored)
	}
	if observations := observer.terminationObservations(1); len(observations) != 0 {
		t.Fatalf("invalid RTSP EOF created termination observations: %+v", observations)
	}
}

func TestDirectPublisherObserverAppliesOnlyExplicitMatchingInitiator(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}

	observer.ObserveTerminationInitiator("other-session", 1, airplaycontract.TerminationReasonUserStop)
	observer.ObserveTerminationInitiator(sessionID, 2, airplaycontract.TerminationReasonUserStop)
	observer.ObserveTerminationInitiator(sessionID, 1, airplaycontract.TerminationReasonPublisherExited)
	observer.ObserveTerminationInitiator(sessionID, 1, airplaycontract.TerminationReasonUserStop)
	observer.ObserveTerminationInitiator(sessionID, 1, airplaycontract.TerminationReasonServerShutdown)
	observer.CompletePublisher(airplaycontract.PublisherCompletion{
		Artifacts: artifacts, Started: true, ExitConfirmed: true,
		ProcessID: 4321, CompletedAt: time.Date(2026, 9, 7, 12, 0, 2, 0, time.UTC),
		ExitCode: 0, ExitCodeKnown: true,
	})

	got, ok := observer.terminationSnapshot(1)
	if !ok || got.Initiator != airplaycontract.TerminationReasonUserStop {
		t.Fatalf("termination snapshot = %+v, ok=%t", got, ok)
	}
}

func TestDirectPublisherObserverImplementsTerminationInitiatorObserver(t *testing.T) {
	var _ airplaycontract.TerminationInitiatorObserver = (*directPublisherObserver)(nil)
}

func TestDirectPublisherObserverLogsOrderedTerminationMetadataWithoutEndpoints(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	var lines []string
	observer.terminationLog = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	observer.ObservePublisher(airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "rtsp-eof", At: time.Date(2026, 9, 7, 12, 0, 1, 0, time.UTC), ProcessID: 4321,
	})
	observer.CompletePublisher(airplaycontract.PublisherCompletion{
		Artifacts: artifacts, Started: true, ExitConfirmed: true,
		ProcessID: 4321, CompletedAt: time.Date(2026, 9, 7, 12, 0, 2, 0, time.UTC), ExitCode: 22, ExitCodeKnown: true,
	})
	observer.ObserveTerminationInitiator(sessionID, 1, airplaycontract.TerminationReasonUserStop)

	if len(lines) != 3 {
		t.Fatalf("termination log lines=%v", lines)
	}
	wants := []string{
		"event=rtsp-eof sequence=1 process_id=4321",
		"event=publisher sequence=2 process_id=4321",
		"event=initiator sequence=0 process_id=0",
	}
	for index, want := range wants {
		if !strings.Contains(lines[index], "session=0123456789abcdef publisher_generation=1") || !strings.Contains(lines[index], want) || !strings.Contains(lines[index], " at_utc=") || !strings.Contains(lines[index], " reason=") {
			t.Fatalf("termination log[%d]=%q, want metadata %q", index, lines[index], want)
		}
		for _, forbidden := range []string{"rtsp://", "session-token", "credential", "password"} {
			if strings.Contains(strings.ToLower(lines[index]), forbidden) {
				t.Fatalf("termination log[%d] leaked endpoint or credential field: %q", index, lines[index])
			}
		}
	}
}

func TestDirectPublisherObserverDoesNotTreatUnconfirmedPublisherExitAsObservedTermination(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	base := airplaycontract.PublisherCompletion{
		Artifacts: artifacts, Started: true, ProcessID: 4321,
		CompletedAt: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), ExitCode: 17, ExitCodeKnown: true,
	}
	tests := []struct {
		name   string
		mutate func(*airplaycontract.PublisherCompletion)
	}{
		{name: "exit not confirmed", mutate: func(*airplaycontract.PublisherCompletion) {}},
		{name: "process not started", mutate: func(c *airplaycontract.PublisherCompletion) { c.ExitConfirmed = true; c.Started = false }},
		{name: "missing process ID", mutate: func(c *airplaycontract.PublisherCompletion) { c.ExitConfirmed = true; c.ProcessID = 0 }},
		{name: "missing completion time", mutate: func(c *airplaycontract.PublisherCompletion) { c.ExitConfirmed = true; c.CompletedAt = time.Time{} }},
		{name: "unknown exit code", mutate: func(c *airplaycontract.PublisherCompletion) { c.ExitConfirmed = true; c.ExitCodeKnown = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := base
			tt.mutate(&candidate)
			observer.CompletePublisher(candidate)
			if _, ok := observer.terminationSnapshot(1); ok {
				t.Fatalf("unconfirmed completion created termination: %+v", candidate)
			}
			observer.mu.Lock()
			delete(observer.completions, 1)
			observer.mu.Unlock()
		})
	}
}

func TestDirectPublisherObserverKeepsMediaMTXAsFirstObservationAndAppendsPublisherExit(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 7, 13, 0, 0, 0, time.UTC)
	if !observer.observeMediaMTXExit(8123, base, 23, true) {
		t.Fatal("known MediaMTX exit was rejected")
	}
	observer.CompletePublisher(airplaycontract.PublisherCompletion{
		Artifacts: artifacts, Started: true, ExitConfirmed: true,
		ProcessID: 8456, CompletedAt: base.Add(time.Millisecond), ExitCode: 17, ExitCodeKnown: true,
	})

	got, ok := observer.terminationSnapshot(1)
	if !ok || got.FirstObservedComponent != airplaycontract.TerminationComponentMediaMTX ||
		got.Initiator != streamTerminationInitiatorUnknown || got.ExitCode != 23 ||
		got.Evidence != airplaycontract.TerminationEvidenceProcessExit || got.Reason != airplaycontract.TerminationReasonProcessExited {
		t.Fatalf("termination snapshot = %+v", got)
	}
	observations := observer.terminationObservations(1)
	if len(observations) != 2 || observations[0].Component != airplaycontract.TerminationComponentMediaMTX || observations[0].Sequence != 1 ||
		observations[1].Component != airplaycontract.TerminationComponentPublisher || observations[1].Sequence != 2 {
		t.Fatalf("ordered termination observations = %+v", observations)
	}
}

func TestDirectPublisherObserverRejectsUncorrelatedMediaMTXExit(t *testing.T) {
	observer, err := newDirectPublisherObserver(t.TempDir(), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	at := time.Date(2026, 9, 7, 13, 30, 0, 0, time.UTC)
	if observer.observeMediaMTXExit(8123, at, 23, true) {
		t.Fatal("MediaMTX exit without a publisher generation was accepted")
	}
	artifacts := airplaycontract.NewPublisherArtifacts(observer.root, "0123456789abcdef", 1)
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []struct {
		pid   int
		at    time.Time
		known bool
	}{
		{pid: 0, at: at, known: true},
		{pid: 8123, at: time.Time{}, known: true},
		{pid: 8123, at: at, known: false},
	} {
		if observer.observeMediaMTXExit(candidate.pid, candidate.at, 23, candidate.known) {
			t.Fatalf("invalid MediaMTX exit was accepted: %+v", candidate)
		}
	}
	if observations := observer.terminationObservations(1); len(observations) != 0 {
		t.Fatalf("invalid MediaMTX exits were stored: %+v", observations)
	}
}

package airplay

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func TestSourceClockPublisherHealthObservationFromState(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "publisher-ready.json")
	writeSourceClockEventForTest(t, readyPath, airplaycontract.Event{
		Schema: 2, SessionID: "health-session", PublisherGeneration: 3,
		Event: "publisher-ready", At: time.Unix(100, 0).UTC(), ProtocolVersion: 1,
		VideoListenPort: 41001, AudioListenPort: 41002, PipelineStartAccepted: true,
	})
	stats := sourceClockMetricsGateStats{
		HasMetrics: true,
		Latest: ReceiverMetrics{
			ConnectedConsumers: 2,
			SentFrames:         44,
			FailureEvents:      7,
			HasFailureEvents:   true,
		},
	}
	got, err := sourceClockPublisherHealthObservationFromState(readyPath, "health-session", 3, true, stats)
	if err != nil {
		t.Fatal(err)
	}
	want := (sourceClockPublisherHealthObservation{
		Generation: 3, PublisherReady: true, MediaStarted: true, OutputCounter: 44, FailureCounter: 7, ErrorFree: true,
	})
	if got != want {
		t.Fatalf("health observation=%+v, want %+v", got, want)
	}
}

func TestSourceClockPublisherHealthObservationRequiresCurrentReadyAndCleanMetrics(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "publisher-ready.json")
	writeSourceClockEventForTest(t, readyPath, airplaycontract.Event{
		Schema: 2, SessionID: "health-session", PublisherGeneration: 2,
		Event: "publisher-ready", At: time.Unix(100, 0).UTC(), ProtocolVersion: 1,
		VideoListenPort: 41001, AudioListenPort: 41002, PipelineStartAccepted: true,
	})
	baseStats := sourceClockMetricsGateStats{
		HasMetrics: true,
		Latest:     ReceiverMetrics{ConnectedConsumers: 1, SentFrames: 10, HasFailureEvents: true},
	}
	got, err := sourceClockPublisherHealthObservationFromState(readyPath, "health-session", 3, false, baseStats)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublisherReady {
		t.Fatal("stale publisher generation became ready")
	}

	tests := []struct {
		name   string
		mutate func(*sourceClockMetricsGateStats)
	}{
		{name: "no metrics", mutate: func(stats *sourceClockMetricsGateStats) { stats.HasMetrics = false }},
		{name: "legacy metrics", mutate: func(stats *sourceClockMetricsGateStats) { stats.Latest.HasFailureEvents = false }},
		{name: "no consumer", mutate: func(stats *sourceClockMetricsGateStats) { stats.Latest.ConnectedConsumers = 0 }},
		{name: "socket error", mutate: func(stats *sourceClockMetricsGateStats) { stats.Latest.LastSocketError = 10054 }},
		{name: "failure reason", mutate: func(stats *sourceClockMetricsGateStats) { stats.Latest.LastFailureReason = "send" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stats := baseStats
			test.mutate(&stats)
			observation, err := sourceClockPublisherHealthObservationFromState(readyPath, "health-session", 2, true, stats)
			if err != nil {
				t.Fatal(err)
			}
			if observation.ErrorFree {
				t.Fatalf("unclean metrics became error-free: %+v", observation)
			}
		})
	}
}

func TestReadSourceClockPublisherReadyTreatsMissingAsWaitingAndMalformedAsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publisher-ready.json")
	ready, err := readSourceClockPublisherReady(path, "health-session", 1)
	if err != nil || ready {
		t.Fatalf("missing ready file = %t, %v", ready, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSourceClockPublisherReady(path, "health-session", 1); err == nil {
		t.Fatal("malformed ready file was accepted")
	}
}

func TestSourceClockPublisherHealthTickReadErrorBreaksContinuousWindow(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "publisher-ready.json")
	writeReady := func() {
		t.Helper()
		writeSourceClockEventForTest(t, readyPath, airplaycontract.Event{
			Schema: 2, SessionID: "health-session", PublisherGeneration: 2,
			Event: "publisher-ready", At: time.Unix(100, 0).UTC(), ProtocolVersion: 1,
			VideoListenPort: 41001, AudioListenPort: 41002, PipelineStartAccepted: true,
		})
	}
	writeReady()
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	var budget sourceClockRetryBudget
	base := time.Unix(1000, 0)
	if permission := budget.Next(base); !permission.Allowed || permission.Attempt != 1 {
		t.Fatalf("initial retry permission=%+v", permission)
	}
	for second := 0; second <= 28; second++ {
		reset, err := sourceClockPublisherHealthTick(tracker, &budget, readyPath, "health-session", 2, false, sourceClockMetricsGateStats{}, base.Add(time.Duration(second)*time.Second))
		if err != nil || reset {
			t.Fatalf("healthy prefix second=%d reset=%t err=%v", second, reset, err)
		}
	}
	if err := os.WriteFile(readyPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if reset, err := sourceClockPublisherHealthTick(tracker, &budget, readyPath, "health-session", 2, false, sourceClockMetricsGateStats{}, base.Add(29*time.Second)); err == nil || reset {
		t.Fatalf("malformed ready reset=%t err=%v", reset, err)
	}
	writeReady()
	for second := 30; second <= 60; second++ {
		reset, err := sourceClockPublisherHealthTick(tracker, &budget, readyPath, "health-session", 2, false, sourceClockMetricsGateStats{}, base.Add(time.Duration(second)*time.Second))
		wantReset := second == 60
		if err != nil || reset != wantReset {
			t.Fatalf("replacement window second=%d reset=%t want=%t err=%v", second, reset, wantReset, err)
		}
	}
	if permission := budget.Next(base.Add(61 * time.Second)); !permission.Allowed || permission.Attempt != 1 {
		t.Fatalf("budget was not reset after replacement window: %+v", permission)
	}
}

func TestSourceClockPublisherHealthTickIfRunningPrioritizesPendingExit(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "publisher-ready.json")
	writeSourceClockEventForTest(t, readyPath, airplaycontract.Event{
		Schema: 2, SessionID: "health-session", PublisherGeneration: 2,
		Event: "publisher-ready", At: time.Unix(100, 0).UTC(), ProtocolVersion: 1,
		VideoListenPort: 41001, AudioListenPort: 41002, PipelineStartAccepted: true,
	})
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	var budget sourceClockRetryBudget
	base := time.Unix(2000, 0)
	if permission := budget.Next(base); !permission.Allowed || permission.Attempt != 1 {
		t.Fatalf("initial retry permission=%+v", permission)
	}
	for second := 0; second <= 29; second++ {
		reset, err := sourceClockPublisherHealthTick(tracker, &budget, readyPath, "health-session", 2, false, sourceClockMetricsGateStats{}, base.Add(time.Duration(second)*time.Second))
		if err != nil || reset {
			t.Fatalf("healthy prefix second=%d reset=%t err=%v", second, reset, err)
		}
	}
	wantExit := errors.New("publisher exited")
	pipelineDone := make(chan error, 1)
	pipelineDone <- wantExit
	reset, pipelineErr, exited, healthErr := sourceClockPublisherHealthTickIfRunning(pipelineDone, tracker, &budget, readyPath, "health-session", 2, false, sourceClockMetricsGateStats{}, base.Add(30*time.Second))
	if healthErr != nil || reset || !exited || !errors.Is(pipelineErr, wantExit) {
		t.Fatalf("simultaneous exit outcome reset=%t exited=%t pipelineErr=%v healthErr=%v", reset, exited, pipelineErr, healthErr)
	}
	if permission := budget.Next(base.Add(31 * time.Second)); !permission.Allowed || permission.Attempt != 2 {
		t.Fatalf("pending exit incorrectly reset budget: %+v", permission)
	}
}

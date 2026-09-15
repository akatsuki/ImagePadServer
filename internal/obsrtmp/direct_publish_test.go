package obsrtmp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

type countingDirectReadinessProber struct {
	pathCalls int
	hlsCalls  int
}

func (p *countingDirectReadinessProber) pathReady(context.Context) bool {
	p.pathCalls++
	return true
}

func (p *countingDirectReadinessProber) hlsReady(context.Context, LatencyProfile) bool {
	p.hlsCalls++
	return true
}

type fixedDirectReadinessProber struct {
	pathReadyValue bool
	hlsReadyValue  bool
	pathCalls      int
	hlsCalls       int
}

type directRoundTripFunc func(*http.Request) (*http.Response, error)

func (f directRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (p *fixedDirectReadinessProber) pathReady(context.Context) bool {
	p.pathCalls++
	return p.pathReadyValue
}

func (p *fixedDirectReadinessProber) hlsReady(context.Context, LatencyProfile) bool {
	p.hlsCalls++
	return p.hlsReadyValue
}

func TestDirectSessionReadinessRequiresHLSMediaArtifacts(t *testing.T) {
	tests := []struct {
		name       string
		pathReady  bool
		hlsReady   bool
		mediaReady bool
		want       bool
	}{
		{name: "publisher only", pathReady: true, hlsReady: false, mediaReady: false, want: false},
		{name: "placeholder HLS without AirPlay media", pathReady: true, hlsReady: true, mediaReady: false, want: false},
		{name: "AirPlay media before HLS", pathReady: true, hlsReady: false, mediaReady: true, want: false},
		{name: "HLS without publisher", pathReady: false, hlsReady: true, mediaReady: true, want: false},
		{name: "publisher, AirPlay media, and playable HLS", pathReady: true, hlsReady: true, mediaReady: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := directSessionReady(tt.pathReady, tt.hlsReady, tt.mediaReady); got != tt.want {
				t.Fatalf("directSessionReady(%v, %v, %v) = %v, want %v", tt.pathReady, tt.hlsReady, tt.mediaReady, got, tt.want)
			}
		})
	}
}

func TestDirectReadinessProbeStopsAfterSessionConnects(t *testing.T) {
	prober := &countingDirectReadinessProber{}
	if directReadinessProbe(context.Background(), true, prober, NormalizeLatencyProfile(LatencyModeRTSPUltra), true) {
		t.Fatal("connected direct session unexpectedly requested another readiness transition")
	}
	if prober.pathCalls != 0 || prober.hlsCalls != 0 {
		t.Fatalf("connected direct session repeated readiness probes: path=%d hls=%d", prober.pathCalls, prober.hlsCalls)
	}
}

func TestDirectReadinessProbeSkipsArtifactProbesBeforeValidatedMedia(t *testing.T) {
	prober := &countingDirectReadinessProber{}
	if directReadinessProbe(context.Background(), false, prober, NormalizeLatencyProfile(LatencyModeHLS), false) {
		t.Fatal("direct session became ready before validated media arrived")
	}
	if prober.pathCalls != 0 || prober.hlsCalls != 0 {
		t.Fatalf("unvalidated media reached readiness probes: path=%d hls=%d, want both 0", prober.pathCalls, prober.hlsCalls)
	}
}

func TestDirectReadinessProbeChecksArtifactsBeforeFirstConnection(t *testing.T) {
	prober := &countingDirectReadinessProber{}
	if !directReadinessProbe(context.Background(), false, prober, NormalizeLatencyProfile(LatencyModeRTSPUltra), true) {
		t.Fatal("unconnected direct session did not become ready")
	}
	if prober.pathCalls != 1 || prober.hlsCalls != 0 {
		t.Fatalf("first direct readiness probe counts: path=%d hls=%d", prober.pathCalls, prober.hlsCalls)
	}
}

func TestDirectReadinessProbeRTSPDoesNotWaitForHLS(t *testing.T) {
	prober := &fixedDirectReadinessProber{pathReadyValue: true, hlsReadyValue: false}
	if !directReadinessProbe(context.Background(), false, prober, NormalizeLatencyProfile(LatencyModeRTSPUltra), true) {
		t.Fatal("RTSP direct session waited for the optional HLS preview")
	}
	if prober.pathCalls != 1 || prober.hlsCalls != 0 {
		t.Fatalf("RTSP readiness probe counts: path=%d hls=%d, want path=1 hls=0", prober.pathCalls, prober.hlsCalls)
	}
}

func TestDirectReadinessProbeHLSWaitsForPlayableHLS(t *testing.T) {
	prober := &fixedDirectReadinessProber{pathReadyValue: true, hlsReadyValue: false}
	if directReadinessProbe(context.Background(), false, prober, NormalizeLatencyProfile(LatencyModeHLS), true) {
		t.Fatal("HLS direct session became ready before its preview was playable")
	}
	if prober.pathCalls != 1 || prober.hlsCalls != 1 {
		t.Fatalf("HLS readiness probe counts: path=%d hls=%d, want path=1 hls=1", prober.pathCalls, prober.hlsCalls)
	}
}

func TestDirectMediaReadinessRejectsNonEventMarker(t *testing.T) {
	recording := filepath.Join(t.TempDir(), "recording.mp4")
	writeDirectMonitorSnapshots(t, recording, "not an event")
	if directMediaArrived(recording, "session-1") {
		t.Fatal("non-empty non-event marker was accepted as decoded media")
	}
}

func TestDirectMediaReadinessRequiresMatchingSchema2Snapshots(t *testing.T) {
	const validReady = `{"schema":2,"sessionId":"session-1","publisherGeneration":1,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":50000,"audioListenPort":50001,"pipelineStartAccepted":true}`
	const validMediaAtZero = `{"schema":2,"sessionId":"session-1","publisherGeneration":1,"event":"video-decoded","at":"2026-09-05T00:00:01Z","videoDecoded":true,"runningTimeNs":0}`
	tests := []struct {
		name  string
		ready string
		media string
		want  bool
	}{
		{name: "matching snapshots accept PTS zero", ready: validReady, media: validMediaAtZero, want: true},
		{name: "missing ready", media: validMediaAtZero, want: false},
		{name: "malformed ready", ready: `{`, media: validMediaAtZero, want: false},
		{name: "schema 1 ready", ready: `{"schema":1,"sessionId":"session-1","publisherGeneration":1,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":50000,"audioListenPort":50001,"pipelineStartAccepted":true}`, media: validMediaAtZero, want: false},
		{name: "wrong ready session", ready: `{"schema":2,"sessionId":"session-2","publisherGeneration":1,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":50000,"audioListenPort":50001,"pipelineStartAccepted":true}`, media: validMediaAtZero, want: false},
		{name: "zero ready generation", ready: `{"schema":2,"sessionId":"session-1","publisherGeneration":0,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":50000,"audioListenPort":50001,"pipelineStartAccepted":true}`, media: validMediaAtZero, want: false},
		{name: "other ready generation", ready: `{"schema":2,"sessionId":"session-1","publisherGeneration":2,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":50000,"audioListenPort":50001,"pipelineStartAccepted":true}`, media: validMediaAtZero, want: false},
		{name: "wrong ready event", ready: `{"schema":2,"sessionId":"session-1","publisherGeneration":1,"event":"video-decoded","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":50000,"audioListenPort":50001,"pipelineStartAccepted":true}`, media: validMediaAtZero, want: false},
		{name: "schema 1 media", ready: validReady, media: `{"schema":1,"sessionId":"session-1","publisherGeneration":1,"event":"video-decoded","at":"2026-09-05T00:00:01Z","videoDecoded":true,"runningTimeNs":0}`, want: false},
		{name: "wrong media session", ready: validReady, media: `{"schema":2,"sessionId":"session-2","publisherGeneration":1,"event":"video-decoded","at":"2026-09-05T00:00:01Z","videoDecoded":true,"runningTimeNs":0}`, want: false},
		{name: "zero media generation", ready: validReady, media: `{"schema":2,"sessionId":"session-1","publisherGeneration":0,"event":"video-decoded","at":"2026-09-05T00:00:01Z","videoDecoded":true,"runningTimeNs":0}`, want: false},
		{name: "other media generation", ready: validReady, media: `{"schema":2,"sessionId":"session-1","publisherGeneration":2,"event":"video-decoded","at":"2026-09-05T00:00:01Z","videoDecoded":true,"runningTimeNs":0}`, want: false},
		{name: "AU only event", ready: validReady, media: `{"schema":2,"sessionId":"session-1","publisherGeneration":1,"event":"video-access-unit","at":"2026-09-05T00:00:01Z","videoDecoded":true,"runningTimeNs":0}`, want: false},
		{name: "missing running time", ready: validReady, media: `{"schema":2,"sessionId":"session-1","publisherGeneration":1,"event":"video-decoded","at":"2026-09-05T00:00:01Z","videoDecoded":true}`, want: false},
		{name: "malformed media", ready: validReady, media: `{`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recording := filepath.Join(t.TempDir(), "recording.mp4")
			paths, err := airplaycontract.FixedPathsForRecording(recording)
			if err != nil {
				t.Fatal(err)
			}
			if tt.ready != "" {
				if err := os.WriteFile(paths.Ready, []byte(tt.ready), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(paths.MediaReady, []byte(tt.media), 0600); err != nil {
				t.Fatal(err)
			}
			if got := directMediaArrived(recording, "session-1"); got != tt.want {
				t.Fatalf("directMediaArrived() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDirectMonitorHasNoPublicationSideEffectsBeforeValidatedMedia(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(context.Background(), artifacts); err != nil {
		t.Fatal(err)
	}
	running := uint64(0)
	readyData, err := json.Marshal(airplaycontract.Event{Schema: 2, SessionID: sessionID, PublisherGeneration: 1, Event: "publisher-ready", At: time.Unix(100, 0).UTC(), ProtocolVersion: 1, VideoListenPort: 50000, AudioListenPort: 50001, PipelineStartAccepted: true, RunningTimeNS: &running})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts.Ready, readyData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts.MediaReady, []byte("not an event"), 0600); err != nil {
		t.Fatal(err)
	}
	recording := artifacts.Recording
	paths := airplaycontract.FixedPaths{Ready: artifacts.Ready, MediaReady: artifacts.MediaReady, EventLog: artifacts.EventLog}
	if err := os.WriteFile(recording, []byte("recording body"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.EventLog, []byte("event log"), 0600); err != nil {
		t.Fatal(err)
	}

	var pathCalls atomic.Int32
	var startCalls atomic.Int32
	var readyCalls atomic.Int32
	runtime := newDirectMonitorTestRuntime(&pathCalls)
	gate := newRTSPGate(rtspGateConfig{})
	close(gate.done)
	m := &Manager{
		running:            true,
		listenerGeneration: 1,
		status:             Status{Publishing: true},
		cb: Callbacks{
			OnStart:     func(Session) { startCalls.Add(1) },
			OnRTSPReady: func(RTSPEndpoint) { readyCalls.Add(1) },
		},
	}
	session := Session{ID: sessionID, Recording: recording, ActiveContract: &OBSActiveSessionContract{SessionID: sessionID, LatencyProfile: NormalizeLatencyProfile(LatencyModeRTSPUltra)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go m.monitorDirectPublishing(ctx, cancel, 1, session, runtime, gate, root, done, observer)

	timer := time.NewTimer(350 * time.Millisecond)
	<-timer.C
	if got := pathCalls.Load(); got != 0 {
		t.Fatalf("path probe calls before validated media = %d, want 0", got)
	}
	if got := startCalls.Load(); got != 0 {
		t.Fatalf("OnStart calls before validated media = %d, want 0", got)
	}
	if got := readyCalls.Load(); got != 0 {
		t.Fatalf("OnRTSPReady calls before validated media = %d, want 0", got)
	}
	m.mu.Lock()
	mediaGeneration := m.mediaGeneration
	current := m.current
	endpoint := m.rtspEndpoint
	m.mu.Unlock()
	if mediaGeneration != 0 || current != nil || endpoint != nil {
		t.Fatalf("publication state changed before validated media: mediaGeneration=%d current=%v endpoint=%v", mediaGeneration, current, endpoint)
	}
	if status := video.CurrentStatusForID(filepath.Dir(recording), session.ID); status.HLS {
		t.Fatalf("BeginExternalHLS side effect appeared before validated media: %+v", status)
	}

	cancel()
	waitDirectMonitorDone(t, done)
	for _, path := range []string{paths.Ready, paths.MediaReady, paths.EventLog} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("direct monitor removed publisher evidence before producer seal: path=%q err=%v", path, err)
		}
	}
	observer.SealPublishers()
	assertDirectSnapshotsRemovedAndRecordingPreserved(t, paths, recording)
}

func TestDirectMonitorRecordsMediaMTXExitAgainstCurrentPublisherGeneration(t *testing.T) {
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
	var pathCalls atomic.Int32
	runtime := newDirectMonitorTestRuntime(&pathCalls)
	proc := runtime.proc.(*fakeProcess)
	proc.processID = 9123
	gate := newRTSPGate(rtspGateConfig{})
	close(gate.done)
	m := &Manager{running: true, directPublishing: true, listenerGeneration: 77, status: Status{Publishing: true}}
	session := Session{ID: sessionID, Recording: artifacts.Recording, ActiveContract: &OBSActiveSessionContract{SessionID: sessionID, LatencyProfile: NormalizeLatencyProfile(LatencyModeRTSPUltra)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go m.monitorDirectPublishing(ctx, cancel, 77, session, runtime, gate, root, done, observer)
	proc.finish(nil)
	waitDirectMonitorDone(t, done)

	got, ok := observer.terminationSnapshot(1)
	if !ok || got.PublisherGeneration != 1 || got.FirstObservedComponent != airplaycontract.TerminationComponentMediaMTX || got.ExitCode != 0 {
		t.Fatalf("MediaMTX runtime exit termination = %+v, ok=%t", got, ok)
	}
	observations := observer.terminationObservations(1)
	if len(observations) != 1 || observations[0].ProcessID != 9123 || observations[0].PublisherGeneration != 1 {
		t.Fatalf("MediaMTX runtime exit observations = %+v", observations)
	}
}

func TestDirectMonitorCommitsPublicationOnceForDuplicateSnapshots(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	artifacts := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(context.Background(), artifacts); err != nil {
		t.Fatal(err)
	}
	writeDirectArtifactSnapshots(t, artifacts)
	recording := artifacts.Recording
	paths := airplaycontract.FixedPaths{Ready: artifacts.Ready, MediaReady: artifacts.MediaReady, EventLog: artifacts.EventLog}
	if err := os.WriteFile(recording, []byte("recording body"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.EventLog, []byte("event log"), 0600); err != nil {
		t.Fatal(err)
	}

	var pathCalls atomic.Int32
	var startCalls atomic.Int32
	var readyCalls atomic.Int32
	started := make(chan struct{}, 1)
	runtime := newDirectMonitorTestRuntime(&pathCalls)
	gate := newRTSPGate(rtspGateConfig{})
	close(gate.done)
	m := &Manager{
		running:            true,
		listenerGeneration: 1,
		status:             Status{Publishing: true},
		cb: Callbacks{
			OnStart: func(Session) {
				startCalls.Add(1)
				select {
				case started <- struct{}{}:
				default:
				}
			},
			OnRTSPReady: func(RTSPEndpoint) { readyCalls.Add(1) },
		},
	}
	session := Session{ID: sessionID, Recording: recording, ActiveContract: &OBSActiveSessionContract{SessionID: sessionID, LatencyProfile: NormalizeLatencyProfile(LatencyModeRTSPUltra)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go m.monitorDirectPublishing(ctx, cancel, 1, session, runtime, gate, root, done, observer)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatal("direct monitor did not publish matching Ready and MediaReady snapshots")
	}
	timer := time.NewTimer(250 * time.Millisecond)
	<-timer.C
	cancel()
	waitDirectMonitorDone(t, done)

	if got := pathCalls.Load(); got != 1 {
		t.Fatalf("path probe calls for duplicate snapshots = %d, want 1", got)
	}
	if got := startCalls.Load(); got != 1 {
		t.Fatalf("OnStart/history publication calls for duplicate snapshots = %d, want 1", got)
	}
	if got := readyCalls.Load(); got != 1 {
		t.Fatalf("OnRTSPReady URL calls for duplicate snapshots = %d, want 1", got)
	}
	if m.mediaGeneration != 1 {
		t.Fatalf("accepted media generations for duplicate snapshots = %d, want 1", m.mediaGeneration)
	}
	for _, path := range []string{paths.Ready, paths.MediaReady, paths.EventLog} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("direct monitor removed publisher evidence before producer seal: path=%q err=%v", path, err)
		}
	}
	observer.SealPublishers()
	assertDirectSnapshotsRemovedAndRecordingPreserved(t, paths, recording)
}

func TestLegacyDirectMonitorPublishesWithoutSourceClockSnapshots(t *testing.T) {
	recording := filepath.Join(t.TempDir(), "legacy-recording.mp4")
	var pathCalls atomic.Int32
	started := make(chan Session, 1)
	runtime := newDirectMonitorTestRuntime(&pathCalls)
	gate := newRTSPGate(rtspGateConfig{})
	close(gate.done)
	m := &Manager{
		running: true, listenerGeneration: 1, status: Status{Publishing: true},
		cb: Callbacks{OnStart: func(session Session) { started <- session }},
	}
	session := directMonitorTestSession(recording)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go m.monitorDirectPublishing(ctx, cancel, 1, session, runtime, gate, filepath.Dir(recording), done, nil)
	select {
	case published := <-started:
		if published.Recording != recording {
			t.Fatalf("legacy recording changed: %q", published.Recording)
		}
	case <-time.After(2 * time.Second):
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatal("legacy direct session waited for source-clock snapshots")
	}
	cancel()
	waitDirectMonitorDone(t, done)
}

func writeDirectMonitorSnapshots(t *testing.T, recording, media string) airplaycontract.FixedPaths {
	t.Helper()
	paths, err := airplaycontract.FixedPathsForRecording(recording)
	if err != nil {
		t.Fatal(err)
	}
	const ready = `{"schema":2,"sessionId":"session-1","publisherGeneration":1,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":50000,"audioListenPort":50001,"pipelineStartAccepted":true}`
	if err := os.WriteFile(paths.Ready, []byte(ready), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.MediaReady, []byte(media), 0600); err != nil {
		t.Fatal(err)
	}
	return paths
}

func newDirectMonitorTestRuntime(pathCalls *atomic.Int32) *mediaMTXRuntime {
	proc := newFakeProcess()
	proc.exitOnStop = true
	return &mediaMTXRuntime{
		cfg: mediaMTXSessionConfig{
			Path:          "obs_session_1",
			AdvertiseHost: "192.0.2.10",
			Ports:         mediaMTXPorts{API: 9999, RTSP: 8554, RTP: 9000, RTCP: 9001, BackendRTSP: 18554},
		},
		httpClient: &http.Client{Transport: directRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			pathCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ready":true}`)),
				Request:    req,
			}, nil
		})},
		stopGrace: time.Millisecond,
		proc:      proc,
	}
}

func directMonitorTestSession(recording string) Session {
	return Session{
		ID:        "session-1",
		Recording: recording,
		ActiveContract: &OBSActiveSessionContract{
			SessionID:      "session-1",
			LatencyProfile: NormalizeLatencyProfile(LatencyModeRTSPUltra),
		},
	}
}

func waitDirectMonitorDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("direct monitor did not stop")
	}
}

func assertDirectSnapshotsRemovedAndRecordingPreserved(t *testing.T, paths airplaycontract.FixedPaths, recording string) {
	t.Helper()
	for _, path := range []string{paths.Ready, paths.MediaReady, paths.EventLog} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("event snapshot was not removed: path=%q err=%v", path, err)
		}
	}
	if _, err := os.Stat(recording); err != nil {
		t.Fatalf("recording body was removed during snapshot cleanup: %v", err)
	}
}

func TestDirectMonitorUsesOnlyCurrentPublisherArtifactsAndCommitsOnce(t *testing.T) {
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
	writeDirectArtifactSnapshots(t, first)

	var pathCalls atomic.Int32
	var startCalls atomic.Int32
	started := make(chan Session, 1)
	runtime := newDirectMonitorTestRuntime(&pathCalls)
	gate := newRTSPGate(rtspGateConfig{})
	close(gate.done)
	m := &Manager{
		running:            true,
		listenerGeneration: 1,
		status:             Status{Publishing: true},
		cb: Callbacks{OnStart: func(session Session) {
			startCalls.Add(1)
			select {
			case started <- session:
			default:
			}
		}},
	}
	session := Session{
		ID: sessionID,
		ActiveContract: &OBSActiveSessionContract{
			SessionID:      sessionID,
			LatencyProfile: NormalizeLatencyProfile(LatencyModeRTSPUltra),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go m.monitorDirectPublishing(ctx, cancel, 1, session, runtime, gate, root, done, observer)

	time.Sleep(350 * time.Millisecond)
	if got := startCalls.Load(); got != 0 {
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatalf("stale generation 1 published after generation 2 registration: starts=%d", got)
	}
	writeDirectArtifactSnapshots(t, second)
	select {
	case published := <-started:
		if published.Recording != second.Recording {
			t.Fatalf("published recording=%q, want current %q", published.Recording, second.Recording)
		}
	case <-time.After(2 * time.Second):
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatal("current generation 2 did not publish")
	}
	writeDirectArtifactSnapshots(t, first)
	time.Sleep(250 * time.Millisecond)
	if got := startCalls.Load(); got != 1 {
		t.Fatalf("delayed old generation duplicated publication: starts=%d", got)
	}
	cancel()
	waitDirectMonitorDone(t, done)
}

func TestDirectMonitorRejectsGenerationThatBecomesStaleDuringProbe(t *testing.T) {
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
	writeDirectArtifactSnapshots(t, first)

	var pathCalls atomic.Int32
	probeEntered := make(chan struct{}, 1)
	releaseProbe := make(chan struct{})
	runtime := newDirectMonitorTestRuntime(&pathCalls)
	runtime.httpClient.Transport = directRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		pathCalls.Add(1)
		select {
		case probeEntered <- struct{}{}:
		default:
		}
		<-releaseProbe
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ready":true}`)), Request: req}, nil
	})
	gate := newRTSPGate(rtspGateConfig{})
	close(gate.done)
	started := make(chan Session, 1)
	m := &Manager{
		running: true, listenerGeneration: 1, status: Status{Publishing: true},
		cb: Callbacks{OnStart: func(session Session) { started <- session }},
	}
	session := Session{ID: sessionID, ActiveContract: &OBSActiveSessionContract{SessionID: sessionID, LatencyProfile: NormalizeLatencyProfile(LatencyModeRTSPUltra)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go m.monitorDirectPublishing(ctx, cancel, 1, session, runtime, gate, root, done, observer)
	select {
	case <-probeEntered:
	case <-time.After(2 * time.Second):
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatal("generation 1 did not enter readiness probe")
	}
	if err := observer.PreparePublisher(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	close(releaseProbe)
	select {
	case stale := <-started:
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatalf("stale generation committed publication: %+v", stale)
	case <-time.After(250 * time.Millisecond):
	}
	writeDirectArtifactSnapshots(t, second)
	select {
	case published := <-started:
		if published.Recording != second.Recording {
			t.Fatalf("published recording=%q, want %q", published.Recording, second.Recording)
		}
	case <-time.After(2 * time.Second):
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatal("current generation did not publish after stale probe rejection")
	}
	cancel()
	waitDirectMonitorDone(t, done)
}

func TestDirectMonitorUpdatesRecordingCandidateAfterPublication(t *testing.T) {
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	first := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	second := airplaycontract.NewPublisherArtifacts(root, sessionID, 2)
	third := airplaycontract.NewPublisherArtifacts(root, sessionID, 3)
	if err := observer.PreparePublisher(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	writeDirectArtifactSnapshots(t, first)
	var pathCalls atomic.Int32
	var startCalls atomic.Int32
	started := make(chan struct{}, 1)
	doneSession := make(chan Session, 1)
	runtime := newDirectMonitorTestRuntime(&pathCalls)
	gate := newRTSPGate(rtspGateConfig{})
	close(gate.done)
	m := &Manager{
		running: true, directPublishing: true, listenerGeneration: 1, status: Status{Publishing: true},
		cb: Callbacks{
			OnStart: func(Session) { startCalls.Add(1); started <- struct{}{} },
			OnDone:  func(session Session) { doneSession <- session },
		},
	}
	session := Session{ID: sessionID, ActiveContract: &OBSActiveSessionContract{SessionID: sessionID, LatencyProfile: NormalizeLatencyProfile(LatencyModeRTSPUltra)}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go m.monitorDirectPublishing(ctx, cancel, 1, session, runtime, gate, root, done, observer)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatal("generation 1 did not publish")
	}
	if err := observer.PreparePublisher(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	candidateClaimEntered := make(chan struct{})
	releaseCandidateClaim := make(chan struct{})
	observer.setBeforeCandidateClaimForTest(func() {
		close(candidateClaimEntered)
		<-releaseCandidateClaim
	})
	writeDirectArtifactSnapshots(t, second)
	select {
	case <-candidateClaimEntered:
	case <-time.After(2 * time.Second):
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatal("generation 2 did not enter recording candidate claim")
	}
	if err := observer.PreparePublisher(context.Background(), third); err != nil {
		t.Fatal(err)
	}
	close(releaseCandidateClaim)
	time.Sleep(250 * time.Millisecond)
	m.mu.Lock()
	staleCandidate := ""
	if m.current != nil {
		staleCandidate = m.current.Recording
	}
	m.mu.Unlock()
	if staleCandidate != first.Recording {
		cancel()
		waitDirectMonitorDone(t, done)
		t.Fatalf("stale generation 2 became recording candidate: %q", staleCandidate)
	}
	writeDirectArtifactSnapshots(t, third)
	candidateDeadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		currentRecording := ""
		if m.current != nil {
			currentRecording = m.current.Recording
		}
		m.mu.Unlock()
		if currentRecording == third.Recording {
			break
		}
		if time.Now().After(candidateDeadline) {
			cancel()
			waitDirectMonitorDone(t, done)
			t.Fatalf("accepted session recording stayed at %q", currentRecording)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := startCalls.Load(); got != 1 {
		t.Fatalf("replacement generation duplicated publication: starts=%d", got)
	}
	cancel()
	waitDirectMonitorDone(t, done)
	select {
	case finished := <-doneSession:
		if finished.Recording != third.Recording {
			t.Fatalf("OnDone recording=%q, want latest candidate %q", finished.Recording, third.Recording)
		}
	case <-time.After(time.Second):
		t.Fatal("connected direct session did not emit OnDone")
	}
}

func TestManagerStoresDirectRecordingOutcomesAndSelectsLatestVerified(t *testing.T) {
	const sessionID = "0123456789abcdef"
	callbackCount := 0
	manager := &Manager{}
	manager.cb.OnRecordingDone = func(outcome airplaycontract.RecordingOutcome) {
		callbackCount++
		stored := manager.DirectRecordingOutcomes(outcome.Artifacts.SessionID)
		if len(stored) == 0 {
			t.Error("recording callback ran before Manager stored the outcome")
		}
	}
	makeOutcome := func(generation uint64, verified bool) airplaycontract.RecordingOutcome {
		artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), sessionID, generation)
		outcome := airplaycontract.RecordingOutcome{
			Artifacts: artifacts, Closed: true, HasRealVideo: true,
			DurationNS: uint64(generation) * uint64(time.Second),
			Reason:     recordingReasonProbeFailed,
		}
		if verified {
			outcome.ProbeOK = true
			outcome.Reason = recordingReasonVerified
		}
		return outcome
	}
	second := makeOutcome(2, false)
	first := makeOutcome(1, true)
	third := makeOutcome(3, true)
	manager.notifyDirectRecordingOutcome(second)
	manager.notifyDirectRecordingOutcome(first)
	manager.notifyDirectRecordingOutcome(third)
	duplicate := third
	duplicate.ProbeOK = false
	duplicate.Reason = recordingReasonProbeFailed
	manager.notifyDirectRecordingOutcome(duplicate)
	manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{})

	stored := manager.DirectRecordingOutcomes(sessionID)
	if len(stored) != 3 || stored[0] != first || stored[1] != second || stored[2] != third {
		t.Fatalf("stored outcomes=%+v", stored)
	}
	representative, ok := manager.DirectRepresentativeRecording(sessionID)
	if !ok || representative != third.Artifacts.Recording {
		t.Fatalf("representative=%q ok=%t, want %q", representative, ok, third.Artifacts.Recording)
	}
	if callbackCount != 3 {
		t.Fatalf("recording callbacks=%d, want 3", callbackCount)
	}
}

func TestManagerRecordingOutcomesDoneCallbackRunsAfterStoredOutcomesWithoutLock(t *testing.T) {
	const sessionID = "0123456789abcdef"
	manager := &Manager{}
	artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), sessionID, 1)
	manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{
		Artifacts: artifacts, ProbeOK: true, Closed: true, HasRealVideo: true,
		DurationNS: uint64(time.Second), Reason: recordingReasonVerified,
	})
	called := make(chan string, 1)
	manager.cb.OnRecordingOutcomesDone = func(gotSessionID string) {
		if outcomes := manager.DirectRecordingOutcomes(gotSessionID); len(outcomes) != 1 {
			t.Errorf("done callback observed outcomes=%+v", outcomes)
		}
		called <- gotSessionID
	}
	manager.notifyDirectRecordingOutcomesDone(sessionID)
	select {
	case gotSessionID := <-called:
		if gotSessionID != sessionID {
			t.Fatalf("done session=%q", gotSessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("recording outcomes done callback deadlocked")
	}
}

func TestManagerDirectRepresentativeRecordingRejectsUnverifiedOnlySession(t *testing.T) {
	const sessionID = "0123456789abcdef"
	manager := &Manager{}
	artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), sessionID, 1)
	manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{
		Artifacts: artifacts, Closed: true, HasRealVideo: true,
		Reason: recordingReasonProbeFailed,
	})
	if representative, ok := manager.DirectRepresentativeRecording(sessionID); ok || representative != "" {
		t.Fatalf("unverified recording became representative: %q ok=%t", representative, ok)
	}
	copy := manager.DirectRecordingOutcomes(sessionID)
	copy[0].Reason = "mutated"
	if got := manager.DirectRecordingOutcomes(sessionID)[0].Reason; got != recordingReasonProbeFailed {
		t.Fatalf("caller mutated Manager outcome storage: %q", got)
	}
}

func TestManagerDirectRepresentativeRecordingDoesNotFallBackPastFailedLatestGeneration(t *testing.T) {
	const sessionID = "0123456789abcdef"
	manager := &Manager{}
	first := airplaycontract.NewPublisherArtifacts(t.TempDir(), sessionID, 1)
	second := airplaycontract.NewPublisherArtifacts(t.TempDir(), sessionID, 2)
	manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{
		Artifacts: first, ProbeOK: true, Closed: true, HasRealVideo: true,
		DurationNS: uint64(time.Second), Reason: recordingReasonVerified,
	})
	manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{
		Artifacts: second, Closed: true, HasRealVideo: true,
		DurationNS: uint64(time.Second), Reason: recordingReasonProbeFailed,
	})
	if representative, ok := manager.DirectRepresentativeRecording(sessionID); ok || representative != "" {
		t.Fatalf("failed latest generation fell back to old recording: %q ok=%t", representative, ok)
	}
}

func TestManagerDirectRepresentativeRecordingRequiresEveryLatestGenerationPredicate(t *testing.T) {
	const sessionID = "0123456789abcdef"
	tests := []struct {
		name   string
		mutate func(*airplaycontract.RecordingOutcome)
	}{
		{name: "probe-ok", mutate: func(outcome *airplaycontract.RecordingOutcome) { outcome.ProbeOK = false }},
		{name: "closed", mutate: func(outcome *airplaycontract.RecordingOutcome) { outcome.Closed = false }},
		{name: "real-video", mutate: func(outcome *airplaycontract.RecordingOutcome) { outcome.HasRealVideo = false }},
		{name: "positive-duration", mutate: func(outcome *airplaycontract.RecordingOutcome) { outcome.DurationNS = 0 }},
		{name: "verified-reason", mutate: func(outcome *airplaycontract.RecordingOutcome) { outcome.Reason = recordingReasonProbeFailed }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{}
			first := airplaycontract.NewPublisherArtifacts(t.TempDir(), sessionID, 1)
			latest := airplaycontract.NewPublisherArtifacts(t.TempDir(), sessionID, 2)
			manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{
				Artifacts: first, ProbeOK: true, Closed: true, HasRealVideo: true,
				DurationNS: uint64(time.Second), Reason: recordingReasonVerified,
			})
			latestOutcome := airplaycontract.RecordingOutcome{
				Artifacts: latest, ProbeOK: true, Closed: true, HasRealVideo: true,
				DurationNS: uint64(time.Second), Reason: recordingReasonVerified,
			}
			test.mutate(&latestOutcome)
			manager.notifyDirectRecordingOutcome(latestOutcome)
			if representative, ok := manager.DirectRepresentativeRecording(sessionID); ok || representative != "" {
				t.Fatalf("latest generation missing %s fell back to old recording: %q ok=%t", test.name, representative, ok)
			}
		})
	}
}

func TestManagerBoundsDirectRecordingOutcomeSessions(t *testing.T) {
	manager := &Manager{}
	for index := 0; index <= directRecordingOutcomeSessionLimit; index++ {
		sessionID := fmt.Sprintf("%016x", index)
		artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), sessionID, 1)
		manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{
			Artifacts: artifacts, Reason: recordingReasonNoRealVideo,
		})
	}
	if got := manager.DirectRecordingOutcomes("0000000000000000"); got != nil {
		t.Fatalf("oldest session was not pruned: %+v", got)
	}
	latest := fmt.Sprintf("%016x", directRecordingOutcomeSessionLimit)
	if got := manager.DirectRecordingOutcomes(latest); len(got) != 1 {
		t.Fatalf("latest session outcomes=%+v", got)
	}
	retained := "0000000000000001"
	secondGeneration := airplaycontract.NewPublisherArtifacts(t.TempDir(), retained, 2)
	manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{
		Artifacts: secondGeneration, Reason: recordingReasonNoRealVideo,
	})
	if got := manager.DirectRecordingOutcomes(retained); len(got) != 2 || got[0].Artifacts.Generation != 1 || got[1].Artifacts.Generation != 2 {
		t.Fatalf("retained multi-generation session was corrupted: %+v", got)
	}
}

func TestManagerConcurrentDelayedRecordingOutcomesDoNotMutateCurrentSession(t *testing.T) {
	const oldSessionID = "0123456789abcdef"
	manager := &Manager{
		current: &Session{ID: "fedcba9876543210", Recording: "current.mp4"},
		status:  Status{MediaID: "current-media", RTSPTURL: "rtsp://current"},
	}
	root := t.TempDir()
	var workers sync.WaitGroup
	for generation := uint64(1); generation <= 100; generation++ {
		generation := generation
		workers.Add(2)
		go func() {
			defer workers.Done()
			artifacts := airplaycontract.NewPublisherArtifacts(root, oldSessionID, generation)
			manager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{
				Artifacts: artifacts, Reason: recordingReasonNoRealVideo,
			})
		}()
		go func() {
			defer workers.Done()
			_ = manager.DirectRecordingOutcomes(oldSessionID)
		}()
	}
	workers.Wait()
	if got := manager.DirectRecordingOutcomes(oldSessionID); len(got) != 100 {
		t.Fatalf("old session outcomes=%d, want 100", len(got))
	}
	manager.mu.Lock()
	current := cloneSession(*manager.current)
	status := manager.status
	manager.mu.Unlock()
	if current.ID != "fedcba9876543210" || current.Recording != "current.mp4" || status.MediaID != "current-media" || status.RTSPTURL != "rtsp://current" {
		t.Fatalf("delayed outcome mutated current state: session=%+v status=%+v", current, status)
	}
	var nilManager *Manager
	nilManager.notifyDirectRecordingOutcome(airplaycontract.RecordingOutcome{})
	if nilManager.DirectRecordingOutcomes(oldSessionID) != nil {
		t.Fatal("nil Manager returned recording outcomes")
	}
	if recording, ok := nilManager.DirectRepresentativeRecording(oldSessionID); ok || recording != "" {
		t.Fatalf("nil Manager representative=%q ok=%t", recording, ok)
	}
}

func writeDirectArtifactSnapshots(t *testing.T, artifacts airplaycontract.PublisherArtifacts) {
	t.Helper()
	running := uint64(0)
	for path, event := range map[string]airplaycontract.Event{
		artifacts.Ready: {
			Schema: 2, SessionID: artifacts.SessionID, PublisherGeneration: artifacts.Generation,
			Event: "publisher-ready", At: time.Unix(100, 0).UTC(), ProtocolVersion: 1,
			VideoListenPort: 50000, AudioListenPort: 50001, PipelineStartAccepted: true,
		},
		artifacts.MediaReady: {
			Schema: 2, SessionID: artifacts.SessionID, PublisherGeneration: artifacts.Generation,
			Event: "video-decoded", At: time.Unix(101, 0).UTC(), VideoDecoded: true, RunningTimeNS: &running,
		},
	} {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDirectMediaMTXConfigFollowsSelectedProfile(t *testing.T) {
	tests := []struct {
		mode     string
		variant  string
		count    int
		duration string
	}{
		{mode: LatencyModeHLSHigh, variant: "fmp4", count: 6, duration: "4s"},
		{mode: LatencyModeHLS, variant: "fmp4", count: 8, duration: "1s"},
		{mode: LatencyModeRTSPLow, variant: "lowLatency", count: 7, duration: "2s"},
		{mode: LatencyModeRTSPUltra, variant: "lowLatency", count: 7, duration: "1s"},
		{mode: LatencyModeRTSPRealtime, variant: "lowLatency", count: 16, duration: "0.5s"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			cfg := directMediaMTXConfig("obs_airplay", "publisher", "secret", mediaMTXPorts{}, "192.0.2.10", "", NormalizeLatencyProfile(tt.mode))
			if cfg.RTSPTCPOnly {
				t.Fatal("direct AirPlay MediaMTX ingest must allow UDP publishing")
			}
			if cfg.UDPReadBufferSize != 64*1024*1024 {
				t.Fatalf("direct AirPlay MediaMTX UDP read buffer = %d, want 64 MiB", cfg.UDPReadBufferSize)
			}
			if !cfg.HLSAlwaysRemux {
				t.Fatal("direct AirPlay MediaMTX must remux HLS before the preview URL is exposed")
			}
			if cfg.HLSVariant != tt.variant || cfg.HLSSegmentCount != tt.count || cfg.HLSSegmentDuration != tt.duration {
				t.Fatalf("direct AirPlay MediaMTX config = %+v, want variant=%q count=%d duration=%q", cfg, tt.variant, tt.count, tt.duration)
			}
		})
	}
}

func TestDirectMediaMTXConfigMeetsLowLatencyHLSMinimum(t *testing.T) {
	for _, mode := range []string{LatencyModeRTSPLow, LatencyModeRTSPUltra, LatencyModeRTSPRealtime} {
		t.Run(mode, func(t *testing.T) {
			cfg := directMediaMTXConfig("obs_airplay", "publisher", "secret", mediaMTXPorts{}, "192.0.2.10", "", NormalizeLatencyProfile(mode))
			if cfg.HLSVariant != "lowLatency" {
				t.Fatalf("direct AirPlay MediaMTX variant = %q, want lowLatency", cfg.HLSVariant)
			}
			if cfg.HLSSegmentCount < 7 {
				t.Fatalf("direct AirPlay low-latency HLS segment count = %d, want at least 7", cfg.HLSSegmentCount)
			}
		})
	}
}

func TestMediaMTXRuntimeDirectPublishURLUsesBackendRTSPAndCredentials(t *testing.T) {
	runtime := newMediaMTXRuntime("mediamtx", mediaMTXSessionConfig{
		Path:        "obs_airplay123",
		PublishUser: "pub-user",
		PublishPass: "pub-pass",
		Ports:       mediaMTXPorts{RTSP: 8554, BackendRTSP: 18554},
	})
	got := runtime.directPublishURL()
	want := "rtsp://pub-user:pub-pass@127.0.0.1:18554/obs_airplay123"
	if got != want {
		t.Fatalf("directPublishURL() = %q, want %q", got, want)
	}
	if strings.Contains(got, "rtmp://") {
		t.Fatalf("direct publish URL must not be RTMP: %q", got)
	}
}

func TestDirectLatencyProfilePreservesEverySelectableMode(t *testing.T) {
	for _, mode := range []string{
		LatencyModeHLSHigh,
		LatencyModeHLS,
		LatencyModeRTSPLow,
		LatencyModeRTSPUltra,
		LatencyModeRTSPRealtime,
	} {
		t.Run(mode, func(t *testing.T) {
			profile := directLatencyProfile(NormalizeLatencyProfile(mode))
			if profile.Mode != mode {
				t.Fatalf("directLatencyProfile(%q).Mode = %q, want %q", mode, profile.Mode, mode)
			}
		})
	}
}

func TestDirectOutputSettingsFollowEverySelectableProfile(t *testing.T) {
	base := video.ResolveQuality("1080", 0)
	tests := []struct {
		mode        string
		width       int
		height      int
		sourceFPS   int
		outputFPS   int
		videoKbps   int
		maxRateKbps int
		bufferKbps  int
		audioBps    int
		gop         int
	}{
		{mode: LatencyModeHLSHigh, width: 1920, height: 1080, sourceFPS: 60, outputFPS: 30, videoKbps: 4500, maxRateKbps: 5200, bufferKbps: 9000, audioBps: 160000, gop: 120},
		{mode: LatencyModeHLS, width: 1920, height: 1080, sourceFPS: 60, outputFPS: 30, videoKbps: 4500, maxRateKbps: 5200, bufferKbps: 9000, audioBps: 160000, gop: 30},
		{mode: LatencyModeRTSPLow, width: 1920, height: 1080, sourceFPS: 60, outputFPS: 30, videoKbps: 4500, maxRateKbps: 5200, bufferKbps: 9000, audioBps: 160000, gop: 60},
		{mode: LatencyModeRTSPUltra, width: 1920, height: 1080, sourceFPS: 60, outputFPS: 30, videoKbps: 9000, maxRateKbps: 10400, bufferKbps: 18000, audioBps: 160000, gop: 30},
		{mode: LatencyModeRTSPRealtime, width: 1280, height: 720, sourceFPS: 60, outputFPS: 30, videoKbps: 2500, maxRateKbps: 3000, bufferKbps: 5000, audioBps: 128000, gop: 15},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			got := directOutputSettings(NormalizeLatencyProfile(tt.mode), base)
			if got.Width != tt.width || got.Height != tt.height ||
				got.SourceFPS != tt.sourceFPS || got.OutputFPS != tt.outputFPS ||
				got.VideoBitrateKbps != tt.videoKbps || got.MaxRateKbps != tt.maxRateKbps ||
				got.BufferSizeKbps != tt.bufferKbps || got.AudioBitrateBps != tt.audioBps ||
				got.GOPFrames != tt.gop {
				t.Fatalf("directOutputSettings(%q) = %+v", tt.mode, got)
			}
		})
	}
}

func TestDirectOutputPlanQualityMatrix(t *testing.T) {
	type qualityCase struct {
		mode                    string
		preset                  video.QualityPreset
		height, video, max, vbv int
		audio                   int
	}
	qualities := []qualityCase{
		{mode: "auto", preset: video.ResolveQualityForUpload("auto", 100, 20), height: 1080, video: 4500, max: 5200, vbv: 9000, audio: 160000},
		{mode: "360", preset: video.ResolveQuality("360", 0), height: 360, video: 900, max: 1100, vbv: 1800, audio: 96000},
		{mode: "720", preset: video.ResolveQuality("720", 0), height: 720, video: 2500, max: 3000, vbv: 5000, audio: 128000},
		{mode: "1080", preset: video.ResolveQuality("1080", 0), height: 1080, video: 4500, max: 5200, vbv: 9000, audio: 160000},
	}
	profiles := []struct {
		mode       string
		gop        int
		multiplier int
		maxHeight  int
	}{
		{mode: LatencyModeHLSHigh, gop: 120, multiplier: 1, maxHeight: 1080},
		{mode: LatencyModeHLS, gop: 30, multiplier: 1, maxHeight: 1080},
		{mode: LatencyModeRTSPLow, gop: 60, multiplier: 1, maxHeight: 1080},
		{mode: LatencyModeRTSPUltra, gop: 30, multiplier: 2, maxHeight: 1080},
		{mode: LatencyModeRTSPRealtime, gop: 15, multiplier: 1, maxHeight: 720},
	}

	for _, profileCase := range profiles {
		for _, quality := range qualities {
			t.Run(profileCase.mode+"/"+quality.mode, func(t *testing.T) {
				plan, err := newDirectDeliveryPlan(
					"0123456789abcdef",
					"fedcba9876543210",
					NormalizeLatencyProfile(profileCase.mode),
					quality.mode,
					quality.preset,
				)
				if err != nil {
					t.Fatal(err)
				}
				wantHeight := quality.height
				wantVideo, wantMax, wantVBV, wantAudio := quality.video, quality.max, quality.vbv, quality.audio
				if wantHeight > profileCase.maxHeight {
					capped := video.ResolveQuality("720", 0)
					wantHeight = capped.Height
					wantVideo = directBitrateKbps(capped.VideoBitrate)
					wantMax = directBitrateKbps(capped.MaxRate)
					wantVBV = directBitrateKbps(capped.BufferSize)
					wantAudio = directBitrateKbps(capped.AudioBitrate) * 1000
				}
				if wantHeight == 720 && profileCase.mode != LatencyModeRTSPRealtime && wantVideo < airPlay720VideoBitrateKbps {
					wantVideo = airPlay720VideoBitrateKbps
					wantMax = airPlay720MaxRateKbps
					wantVBV = airPlay720BufferSizeKbps
				}
				wantVideo *= profileCase.multiplier
				wantMax *= profileCase.multiplier
				wantVBV *= profileCase.multiplier
				wantWidth := wantHeight * 16 / 9
				if wantWidth%2 != 0 {
					wantWidth++
				}
				if plan.SessionID != "0123456789abcdef" || plan.RequestID != "fedcba9876543210" || plan.RequestedQualityMode != quality.mode {
					t.Fatalf("plan identity = %+v", plan)
				}
				if plan.Profile.Mode != profileCase.mode || plan.EffectiveHeight != wantHeight || plan.Output.Height != wantHeight || plan.Output.Width != wantWidth {
					t.Fatalf("plan dimensions/profile = %+v, want profile=%s %dx%d", plan, profileCase.mode, wantWidth, wantHeight)
				}
				if plan.Output.SourceFPS != 60 || plan.Output.OutputFPS != 30 || plan.Output.GOPFrames != profileCase.gop {
					t.Fatalf("plan cadence = %+v, want source/output/gop=60/30/%d", plan.Output, profileCase.gop)
				}
				if plan.Output.VideoBitrateKbps != wantVideo || plan.Output.MaxRateKbps != wantMax || plan.Output.BufferSizeKbps != wantVBV || plan.Output.AudioBitrateBps != wantAudio {
					t.Fatalf("plan rates = %+v, want video/max/vbv/audio=%d/%d/%d/%d", plan.Output, wantVideo, wantMax, wantVBV, wantAudio)
				}
			})
		}
	}
}

func TestDirectAirPlay720QualityUsesScreenBitrateFloor(t *testing.T) {
	requested := video.ResolveQuality("720", 0)
	plan, err := newDirectDeliveryPlan(
		"0123456789abcdef",
		"fedcba9876543210",
		NormalizeLatencyProfile(LatencyModeHLS),
		"720",
		requested,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Output.VideoBitrateKbps != 5000 || plan.Output.MaxRateKbps != 5000 || plan.Output.BufferSizeKbps != 5000 {
		t.Fatalf("720p AirPlay CRF output rates = %+v, want 5000/5000/5000 kbps", plan.Output)
	}
}

func TestDirectAirPlayRealtime720KeepsLowLatencyBitrateContract(t *testing.T) {
	plan, err := newDirectDeliveryPlan(
		"0123456789abcdef",
		"fedcba9876543210",
		NormalizeLatencyProfile(LatencyModeRTSPRealtime),
		"720",
		video.ResolveQuality("720", 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Output.VideoBitrateKbps != 2500 || plan.Output.MaxRateKbps != 3000 || plan.Output.BufferSizeKbps != 5000 {
		t.Fatalf("realtime 720p AirPlay output rates = %+v, want 2500/3000/5000 kbps", plan.Output)
	}
}

func TestDirectOutputPlanQualityAutoUsesCapturedUploadDecision(t *testing.T) {
	preset := video.ResolveQualityForUpload("auto", 100, 4)
	plan, err := newDirectDeliveryPlan(
		"0123456789abcdef",
		"fedcba9876543210",
		NormalizeLatencyProfile(LatencyModeHLS),
		"auto",
		preset,
	)
	if err != nil {
		t.Fatal(err)
	}
	preset = video.ResolveQualityForUpload("auto", 100, 100)
	if plan.EffectiveHeight != 360 || plan.Output.Height != 360 {
		t.Fatalf("captured auto plan changed after bandwidth input changed: %+v; later=%+v", plan, preset)
	}
}

func TestDirectOutputPlanQualityRejectsIncompleteIdentity(t *testing.T) {
	preset := video.ResolveQuality("720", 0)
	for _, test := range []struct{ sessionID, requestID string }{{"", "fedcba9876543210"}, {"0123456789abcdef", ""}} {
		if _, err := newDirectDeliveryPlan(test.sessionID, test.requestID, NormalizeLatencyProfile(LatencyModeHLS), "720", preset); err == nil {
			t.Fatalf("incomplete identity session=%q request=%q was accepted", test.sessionID, test.requestID)
		}
	}
}

func TestDirectOutputPlanQualityRejectsMutation(t *testing.T) {
	plan, err := newDirectDeliveryPlan(
		"0123456789abcdef",
		"fedcba9876543210",
		NormalizeLatencyProfile(LatencyModeHLS),
		"720",
		video.ResolveQuality("720", 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	mutatedOutput := plan
	mutatedOutput.Output.VideoBitrateKbps++
	if err := validateDirectDeliveryPlan(mutatedOutput); err == nil {
		t.Fatal("mutated output was accepted")
	}
	mutatedProfile := plan
	mutatedProfile.Profile.GOPFrames = "999"
	if err := validateDirectDeliveryPlan(mutatedProfile); err == nil {
		t.Fatal("mutated profile was accepted")
	}
}

func TestDirectRecordingPlanQualityDimensionsMatchOutput(t *testing.T) {
	plan, err := newDirectDeliveryPlan(
		"0123456789abcdef",
		"fedcba9876543210",
		NormalizeLatencyProfile(LatencyModeRTSPRealtime),
		"1080",
		video.ResolveQuality("1080", 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := newDirectPublisherObserver(t.TempDir(), plan.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	if err := configureDirectRecordingProbeForSession(observer, plan.Output, "", fmt.Errorf("missing test ffprobe")); err != nil {
		t.Fatal(err)
	}
	observer.mu.Lock()
	width, height := observer.recordingProbeWidth, observer.recordingProbeHeight
	observer.mu.Unlock()
	if width != plan.Output.Width || height != plan.Output.Height || height != plan.EffectiveHeight {
		t.Fatalf("recording probe=%dx%d, output=%dx%d effective=%d", width, height, plan.Output.Width, plan.Output.Height, plan.EffectiveHeight)
	}
}

func TestDirectRTSPEndpointUsesMediaMTXPorts(t *testing.T) {
	runtime := newMediaMTXRuntime("mediamtx", mediaMTXSessionConfig{
		Path:          "obs_direct123",
		AdvertiseHost: "192.0.2.10",
		Ports:         mediaMTXPorts{RTSP: 8554, RTP: 9000, RTCP: 9001, BackendRTSP: 18554},
	})
	got := directRTSPEndpoint(runtime, "direct123")
	if got.SessionID != "direct123" || got.Host != "192.0.2.10" || got.Port != 8554 || got.RTPPort != 9000 || got.RTCPPort != 9001 {
		t.Fatalf("directRTSPEndpoint() = %+v", got)
	}
	if got.Path != "obs_direct123" || got.LocalURL != "rtsp://192.0.2.10:8554/obs_direct123" || got.BackendURL != "rtsp://127.0.0.1:18554/obs_direct123" {
		t.Fatalf("directRTSPEndpoint URLs = %+v", got)
	}
}

func TestStopDirectIgnoresStaleGeneration(t *testing.T) {
	cancelled := make(chan struct{})
	m := &Manager{
		directPublishing:   true,
		listenerGeneration: 2,
		stop: func() {
			close(cancelled)
		},
		done: make(chan struct{}),
	}
	if m.StopDirect(DirectSessionHandle{ID: "old", Generation: 1}, time.Millisecond) {
		t.Fatal("stale direct session unexpectedly stopped")
	}
	select {
	case <-cancelled:
		t.Fatal("stale direct session invoked cancel")
	default:
	}
}

func TestStopCurrentDirectStopsTheOwnedSession(t *testing.T) {
	stopped := make(chan struct{})
	m := &Manager{
		directPublishing:   true,
		directHandle:       DirectSessionHandle{ID: "current", Generation: 3},
		listenerGeneration: 3,
		stop: func() {
			close(stopped)
		},
		done: make(chan struct{}),
	}
	close(m.done)

	if !m.StopCurrentDirect(time.Second) {
		t.Fatal("current direct session was not stopped")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("current direct session did not invoke cancel")
	}
}

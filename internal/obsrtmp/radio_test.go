package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

type fakeRadioRuntime struct {
	mu            sync.Mutex
	stopped       bool
	pathReadyFunc func(context.Context) bool
	hlsReadyFunc  func(context.Context, LatencyProfile) bool
	exit          chan error
	exitOnce      sync.Once
}

func (f *fakeRadioRuntime) start(context.Context) error { return nil }
func (f *fakeRadioRuntime) stop(time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}
func (f *fakeRadioRuntime) wasStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}
func (f *fakeRadioRuntime) wait() <-chan error { return f.exit }
func (f *fakeRadioRuntime) finish(err error) {
	f.exitOnce.Do(func() {
		f.exit <- err
		close(f.exit)
	})
}
func (f *fakeRadioRuntime) rtmpPublishURL() string {
	return "rtmp://127.0.0.1:9999/radio?user=u&pass=p"
}
func (f *fakeRadioRuntime) publishURL() string {
	return "rtsp://u:p@127.0.0.1:8554/radio"
}
func (f *fakeRadioRuntime) rtspURL() string { return "rtsp://192.168.0.10:8554/radio" }
func (f *fakeRadioRuntime) proxyHLS(w http.ResponseWriter, _ *http.Request, name string) {
	w.Header().Set("X-Proxied", name)
	w.WriteHeader(http.StatusOK)
}
func (f *fakeRadioRuntime) llhlsReady(context.Context) bool { return true }
func (f *fakeRadioRuntime) hlsReady(ctx context.Context, profile LatencyProfile) bool {
	if f.hlsReadyFunc != nil {
		return f.hlsReadyFunc(ctx, profile)
	}
	return true
}
func (f *fakeRadioRuntime) pathReady(ctx context.Context) bool {
	if f.pathReadyFunc != nil {
		return f.pathReadyFunc(ctx)
	}
	return true
}

type fakeGate struct {
	mu      sync.Mutex
	stopped bool
}

func (g *fakeGate) start(context.Context) error { return nil }
func (g *fakeGate) stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopped = true
	return nil
}

type fakePublisher struct {
	exit chan error
	once sync.Once
}

func (p *fakePublisher) sink() io.Writer    { return io.Discard }
func (p *fakePublisher) done() <-chan error { return p.exit }
func (p *fakePublisher) close()             {}
func (p *fakePublisher) finish(err error) {
	p.once.Do(func() {
		p.exit <- err
		close(p.exit)
	})
}

type radioEvent struct {
	kind       string
	trackID    string
	err        error
	radioError RadioError
}

type radioHarness struct {
	mu        sync.Mutex
	events    []radioEvent
	eventCh   chan radioEvent
	runtime   *fakeRadioRuntime
	gate      *fakeGate
	publisher *fakePublisher
}

func (h *radioHarness) record(e radioEvent) {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
	h.eventCh <- e
}

func (h *radioHarness) waitEvent(t *testing.T, kind string) radioEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-h.eventCh:
			if e.kind == kind {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event %q; got %+v", kind, h.events)
		}
	}
}

// wrapNext adapts the older 3-value test scripts to the next() signature.
func wrapNext(next func() (string, string, bool)) func() (string, string, int, bool) {
	return func() (string, string, int, bool) {
		mediaPath, trackID, ok := next()
		return mediaPath, trackID, 0, ok
	}
}

// newRadioHarness wires a RadioManager with fake runtime/gate and a scripted
// pusher. push blocks until ctx is cancelled when the track ID is in blockers.
func newRadioHarness(t *testing.T, next func() (string, string, bool), pushErr map[string]error, blockers map[string]bool) (*RadioManager, *radioHarness) {
	t.Helper()
	h := &radioHarness{eventCh: make(chan radioEvent, 64), runtime: &fakeRadioRuntime{exit: make(chan error, 1)}, gate: &fakeGate{}}
	m := NewRadioManager(t.TempDir(), "192.168.0.10", wrapNext(next), RadioCallbacks{
		OnTrackStart: func(id string) { h.record(radioEvent{kind: "start", trackID: id}) },
		OnTrackEnd:   func(id string, err error) { h.record(radioEvent{kind: "end", trackID: id, err: err}) },
		OnIdle:       func() { h.record(radioEvent{kind: "idle"}) },
		OnRTSPReady:  func(RTSPEndpoint) { h.record(radioEvent{kind: "rtsp-ready"}) },
		OnError:      func(e RadioError) { h.record(radioEvent{kind: "error", radioError: e}) },
		OnStopped:    func() { h.record(radioEvent{kind: "stopped"}) },
	})
	m.buildRuntime = func(context.Context, RadioActiveSessionContract) (radioRuntime, radioGate, RTSPEndpoint, error) {
		return h.runtime, h.gate, RTSPEndpoint{SessionID: "s1", Host: "192.168.0.10", Port: 8554, Path: "radio", LocalURL: h.runtime.rtspURL()}, nil
	}
	m.startPublisher = func(context.Context, string) (radioPublisher, error) {
		h.publisher = &fakePublisher{exit: make(chan error, 1)}
		return h.publisher, nil
	}
	m.runFeeder = func(ctx context.Context, mediaPath string, _ int, loop bool, _ float64, _ io.Writer) error {
		id := mediaPath // tests pass trackID as mediaPath for simplicity
		if blockers[id] {
			<-ctx.Done()
			return nil
		}
		return pushErr[id]
	}
	m.runFallbackFeeder = func(ctx context.Context, _ RadioActiveSessionContract, _ float64, _ io.Writer) (float64, error) {
		<-ctx.Done()
		return 0, nil
	}
	t.Cleanup(func() { m.Stop(3 * time.Second) })
	return m, h
}

func TestRadioPlaysTracksThenIdles(t *testing.T) {
	tracks := []string{"t1", "t2"}
	i := 0
	next := func() (string, string, bool) {
		if i >= len(tracks) {
			return "", "", false
		}
		id := tracks[i]
		i++
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if e := h.waitEvent(t, "start"); e.trackID != "t1" {
		t.Fatalf("first start = %+v", e)
	}
	if e := h.waitEvent(t, "end"); e.trackID != "t1" || e.err != nil {
		t.Fatalf("first end = %+v", e)
	}
	if e := h.waitEvent(t, "start"); e.trackID != "t2" {
		t.Fatalf("second start = %+v", e)
	}
	h.waitEvent(t, "idle")
	if !m.Running() {
		t.Fatal("radio must stay running while idle")
	}
}

func TestRadioPublishesThroughRTMPIngestPath(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	gotURL := make(chan string, 1)
	m.startPublisher = func(_ context.Context, publishURL string) (radioPublisher, error) {
		gotURL <- publishURL
		return &fakePublisher{exit: make(chan error)}, nil
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case got := <-gotURL:
		if got != "rtmp://127.0.0.1:9999/radio?user=u&pass=p" {
			t.Fatalf("publisher URL = %q, want RTMP ingest URL", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publisher was not started")
	}
}

func TestRadioRTSPReadyWaitsForMediaPathReady(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	ready := make(chan struct{})
	h.runtime.pathReadyFunc = func(ctx context.Context) bool {
		select {
		case <-ready:
			return true
		case <-ctx.Done():
			return false
		default:
			return false
		}
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case e := <-h.eventCh:
			if e.kind != "rtsp-ready" {
				continue
			}
			t.Fatal("RTSP ready was announced before MediaMTX path was ready")
		case <-deadline:
			goto release
		}
	}
release:
	close(ready)
	deadline = time.After(3 * time.Second)
	for {
		select {
		case e := <-h.eventCh:
			if e.kind == "rtsp-ready" {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for RTSP ready after path became ready; got %+v", h.events)
		}
	}
}

func TestRadioWakeResumesFromIdle(t *testing.T) {
	var mu sync.Mutex
	queue := []string{}
	next := func() (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		if len(queue) == 0 {
			return "", "", false
		}
		id := queue[0]
		queue = queue[1:]
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "idle")
	mu.Lock()
	queue = append(queue, "t9")
	mu.Unlock()
	m.Wake()
	if e := h.waitEvent(t, "start"); e.trackID != "t9" {
		t.Fatalf("resumed track = %+v", e)
	}
}

func TestRadioSkipCancelsCurrentPush(t *testing.T) {
	var mu sync.Mutex
	queue := []string{"blocked", "t2"}
	next := func() (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		if len(queue) == 0 {
			return "", "", false
		}
		id := queue[0]
		queue = queue[1:]
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, map[string]bool{"blocked": true})
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "start")
	m.SkipCurrent()
	if e := h.waitEvent(t, "end"); e.trackID != "blocked" || e.err != nil {
		t.Fatalf("skipped track must end without error: %+v", e)
	}
	if e := h.waitEvent(t, "start"); e.trackID != "t2" {
		t.Fatalf("after skip = %+v", e)
	}
}

func TestRadioTrackGenerationCompletionSignalsExactTrackAndSessionStop(t *testing.T) {
	var queueMu sync.Mutex
	queue := []string{"blocked"}
	next := func() (string, string, bool) {
		queueMu.Lock()
		defer queueMu.Unlock()
		if len(queue) == 0 {
			return "", "", false
		}
		id := queue[0]
		queue = queue[1:]
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, map[string]bool{"blocked": true})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	completion := m.CurrentTrackGeneration()
	if completion.Generation == 0 || completion.TrackID != "blocked" {
		t.Fatalf("active generation = %+v", completion)
	}
	select {
	case <-completion.Completed:
		t.Fatal("active generation completed before skip")
	default:
	}
	m.SkipCurrent()
	select {
	case <-completion.Completed:
	case <-time.After(time.Second):
		t.Fatal("skipped generation did not complete")
	}

	queueMu.Lock()
	queue = []string{"blocked"}
	queueMu.Unlock()
	m.Wake()
	h.waitEvent(t, "start")
	stopping := m.CurrentTrackGeneration()
	m.Stop(3 * time.Second)
	select {
	case <-stopping.SessionCanceled:
	case <-time.After(time.Second):
		t.Fatal("generation waiter did not terminate when the session context was canceled")
	}
	select {
	case <-stopping.SessionDone:
	case <-time.After(time.Second):
		t.Fatal("generation waiter did not terminate when the session stopped")
	}
}

func TestRadioClaimPublishesGenerationBeforeHandlersCanObserveClaim(t *testing.T) {
	claimEntered := make(chan struct{})
	releaseClaim := make(chan struct{})
	next := func() (string, string, bool) {
		close(claimEntered)
		<-releaseClaim
		return "blocked", "blocked", true
	}
	m, h := newRadioHarness(t, next, nil, map[string]bool{"blocked": true})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	<-claimEntered
	observed := make(chan TrackGeneration, 1)
	go func() { observed <- m.CurrentTrackGeneration() }()
	select {
	case generation := <-observed:
		t.Fatalf("handler observed claim before generation publication: %+v", generation)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseClaim)
	select {
	case generation := <-observed:
		if generation.Generation == 0 || generation.TrackID != "blocked" {
			t.Fatalf("published generation = %+v", generation)
		}
	case <-time.After(time.Second):
		t.Fatal("generation was not published after claim completed")
	}
	h.waitEvent(t, "start")
}

func TestRadioCancelGenerationDoesNotCancelNewerTrack(t *testing.T) {
	queue := []string{"a", "b"}
	next := func() (string, string, bool) {
		if len(queue) == 0 {
			return "", "", false
		}
		id := queue[0]
		queue = queue[1:]
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, map[string]bool{"a": true, "b": true})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	a := m.CurrentTrackGeneration()
	if !m.CancelGeneration(a.Generation) {
		t.Fatal("CancelGeneration must cancel the captured active generation")
	}
	select {
	case <-a.Completed:
	case <-time.After(time.Second):
		t.Fatal("generation A did not complete")
	}
	h.waitEvent(t, "end")
	h.waitEvent(t, "start")
	b := m.CurrentTrackGeneration()
	if b.Generation <= a.Generation || b.TrackID != "b" {
		t.Fatalf("generation B = %+v after A = %+v", b, a)
	}
	if m.CancelGeneration(a.Generation) {
		t.Fatal("CancelGeneration(A) must not cancel newer generation B")
	}
	select {
	case <-b.Completed:
		t.Fatal("generation B was canceled by a stale cancellation")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestRadioTrackErrorPropagatesAndContinues(t *testing.T) {
	queue := []string{"bad", "good"}
	i := 0
	next := func() (string, string, bool) {
		if i >= len(queue) {
			return "", "", false
		}
		id := queue[i]
		i++
		return id, id, true
	}
	m, h := newRadioHarness(t, next, map[string]error{"bad": context.DeadlineExceeded}, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if e := h.waitEvent(t, "end"); e.trackID != "bad" || e.err == nil {
		t.Fatalf("failed push must surface error: %+v", e)
	}
	if e := h.waitEvent(t, "start"); e.trackID != "good" {
		t.Fatalf("radio must continue after failure: %+v", e)
	}
}

func TestRadioStopShutsDownRuntimeAndGate(t *testing.T) {
	next := func() (string, string, bool) { return "", "", false }
	m, h := newRadioHarness(t, next, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "idle")
	m.Stop(3 * time.Second)
	h.waitEvent(t, "stopped")
	if m.Running() {
		t.Fatal("Running must be false after Stop")
	}
	if !h.runtime.wasStopped() {
		t.Fatal("runtime must be stopped")
	}
	h.gate.mu.Lock()
	gateStopped := h.gate.stopped
	h.gate.mu.Unlock()
	if !gateStopped {
		t.Fatal("gate must be stopped")
	}
	if st := m.Status(); st.Running || st.CurrentTrackID != "" {
		t.Fatalf("status after stop = %+v", st)
	}
}

func TestRadioFillerLoopsWhileIdleAndWakeInterrupts(t *testing.T) {
	var mu sync.Mutex
	queue := []string{}
	next := func() (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		if len(queue) == 0 {
			return "", "", false
		}
		id := queue[0]
		queue = queue[1:]
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, nil)
	fillerStarted := make(chan struct{}, 8)
	m.runFallbackFeeder = func(ctx context.Context, _ RadioActiveSessionContract, _ float64, _ io.Writer) (float64, error) {
		select {
		case fillerStarted <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return 0, nil
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "idle")
	select {
	case <-fillerStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("filler feeder did not start while idle")
	}
	mu.Lock()
	queue = append(queue, "t1")
	mu.Unlock()
	m.Wake()
	if e := h.waitEvent(t, "start"); e.trackID != "t1" {
		t.Fatalf("after wake, expected t1, got %+v", e)
	}
}

func TestRadioDoesNotStartFillerWhileTrackFeederIsRunning(t *testing.T) {
	queue := []string{"blocked"}
	next := func() (string, string, bool) {
		if len(queue) == 0 {
			return "", "", false
		}
		id := queue[0]
		queue = queue[1:]
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, nil)
	fillerStarted := make(chan struct{}, 1)
	trackStarted := make(chan struct{}, 1)
	m.runFeeder = func(ctx context.Context, mediaPath string, _ int, loop bool, _ float64, _ io.Writer) error {
		if mediaPath == "blocked" {
			trackStarted <- struct{}{}
			<-ctx.Done()
		}
		return nil
	}
	m.runFallbackFeeder = func(ctx context.Context, _ RadioActiveSessionContract, _ float64, _ io.Writer) (float64, error) {
		fillerStarted <- struct{}{}
		<-ctx.Done()
		return 0, nil
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "start")
	select {
	case <-trackStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("track feeder did not start")
	}
	select {
	case <-fillerStarted:
		t.Fatal("filler must not start while a track feeder is still running")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRadioKeepsTrackFeederTimestampsLocalAcrossTracks(t *testing.T) {
	tracks := []string{"t1", "t2"}
	i := 0
	next := func() (string, string, bool) {
		if i >= len(tracks) {
			return "", "", false
		}
		id := tracks[i]
		i++
		return id, id, true
	}
	m, _ := newRadioHarness(t, next, nil, nil)
	offsets := make(chan float64, 2)
	m.runFeeder = func(_ context.Context, mediaPath string, _ int, loop bool, timestampOffset float64, _ io.Writer) error {
		if loop {
			return nil
		}
		offsets <- timestampOffset
		if mediaPath == "t1" {
			time.Sleep(700 * time.Millisecond)
		}
		return nil
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := <-offsets
	second := <-offsets
	if first != 0 {
		t.Fatalf("first feeder offset = %f, want 0", first)
	}
	if second != 0 {
		t.Fatalf("second feeder offset = %f, want local track timeline 0", second)
	}
}

func TestRadioHLSSettingsFollowOBSProfiles(t *testing.T) {
	tests := []struct {
		profile  string
		variant  string
		segments int
		duration string
	}{
		{"hls-high", "fmp4", 6, "4s"},
		{"hls", "fmp4", 8, "1s"},
		{"rtsp-ultra", "lowLatency", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.profile, func(t *testing.T) {
			variant, segments, duration := radioHLSSettings(NormalizeLatencyProfile(tc.profile))
			if variant != tc.variant || segments != tc.segments || duration != tc.duration {
				t.Fatalf("radioHLSSettings(%q) = (%q, %d, %q)", tc.profile, variant, segments, duration)
			}
		})
	}
}

func TestRadioActiveSessionContractFreezesDesiredCallbacksAcrossTrackFallbackTrack(t *testing.T) {
	var queueMu sync.Mutex
	queue := []string{"track-a"}
	next := func() (string, string, bool) {
		queueMu.Lock()
		defer queueMu.Unlock()
		if len(queue) == 0 {
			return "", "", false
		}
		trackID := queue[0]
		queue = queue[1:]
		return trackID, trackID, true
	}
	m, h := newRadioHarness(t, next, nil, nil)

	profileA := NormalizeLatencyProfile(LatencyModeHLSHigh)
	profileB := NormalizeLatencyProfile(LatencyModeHLS)
	presetA := video.MusicRadioQualityPreset("720", 0, 0)
	presetB := video.MusicRadioQualityPreset("480", 0, 0)
	var latencyCalls, presetCalls int
	m.SetLatencyProfile(func() LatencyProfile {
		latencyCalls++
		return profileA
	})
	m.SetFallbackPreset(func() video.QualityPreset {
		presetCalls++
		return presetA
	})

	fallbackContracts := make(chan RadioActiveSessionContract, 1)
	m.runFeeder = func(ctx context.Context, mediaPath string, _ int, _ bool, _ float64, _ io.Writer) error {
		if mediaPath == "track-b" {
			<-ctx.Done()
		}
		return nil
	}
	m.runFallbackFeeder = func(ctx context.Context, contract RadioActiveSessionContract, _ float64, _ io.Writer) (float64, error) {
		fallbackContracts <- contract
		<-ctx.Done()
		return 0, nil
	}

	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if latencyCalls != 1 || presetCalls != 1 {
		t.Fatalf("Start callbacks = latency %d, preset %d; want one call each", latencyCalls, presetCalls)
	}
	if event := h.waitEvent(t, "start"); event.trackID != "track-a" {
		t.Fatalf("first track = %+v, want track-a", event)
	}
	h.waitEvent(t, "idle")

	var fallback RadioActiveSessionContract
	select {
	case fallback = <-fallbackContracts:
	case <-time.After(3 * time.Second):
		t.Fatal("fallback did not receive the active session contract")
	}
	if fallback.LatencyProfile != profileA {
		t.Fatalf("fallback latency = %+v, want %+v", fallback.LatencyProfile, profileA)
	}
	if fallback.FallbackPreset != presetA {
		t.Fatalf("fallback preset = %+v, want %+v", fallback.FallbackPreset, presetA)
	}

	m.SetLatencyProfile(func() LatencyProfile {
		latencyCalls++
		return profileB
	})
	m.SetFallbackPreset(func() video.QualityPreset {
		presetCalls++
		return presetB
	})
	queueMu.Lock()
	queue = append(queue, "track-b")
	queueMu.Unlock()
	m.Wake()
	if event := h.waitEvent(t, "start"); event.trackID != "track-b" {
		t.Fatalf("resumed track = %+v, want track-b", event)
	}
	if latencyCalls != 1 || presetCalls != 1 {
		t.Fatalf("running session re-read desired callbacks: latency %d, preset %d", latencyCalls, presetCalls)
	}
	status := m.Status()
	if status.ActiveSession == nil || *status.ActiveSession != fallback {
		t.Fatalf("active status contract = %+v, want %+v", status.ActiveSession, fallback)
	}
}

func TestRadioStartDoesNotExposeOrContinueHalfSessionWhenStopRacesContractCapture(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	captureEntered := make(chan struct{})
	releaseCapture := make(chan struct{})
	buildStarted := make(chan struct{}, 1)
	m.SetLatencyProfile(func() LatencyProfile {
		close(captureEntered)
		<-releaseCapture
		return NormalizeLatencyProfile(LatencyModeRTSPUltra)
	})
	m.buildRuntime = func(context.Context, RadioActiveSessionContract) (radioRuntime, radioGate, RTSPEndpoint, error) {
		buildStarted <- struct{}{}
		return nil, nil, RTSPEndpoint{}, errors.New("runtime must not start after Stop")
	}

	startDone := make(chan error, 1)
	go func() { startDone <- m.Start() }()
	select {
	case <-captureEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not enter blocked desired callback")
	}

	if status := m.Status(); status.Running || status.Phase != RadioPhaseStopped || status.ActiveSession != nil {
		t.Fatalf("blocked capture exposed half session: %+v", status)
	}
	stopDone := make(chan struct{})
	go func() {
		m.Stop(50 * time.Millisecond)
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked behind contract capture")
	}

	close(releaseCapture)
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("Start after concurrent Stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after concurrent Stop")
	}
	select {
	case <-buildStarted:
		t.Fatal("Start built a runtime after Stop canceled the provisional session")
	default:
	}
	if status := m.Status(); status.Running || status.Phase != RadioPhaseStopped || status.ActiveSession != nil {
		t.Fatalf("stopped provisional session leaked status: %+v", status)
	}
}

func TestRadioStopDuringProvisionalCaptureAllowsImmediateReplacementStart(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	captureAEntered := make(chan struct{})
	releaseA := make(chan struct{})
	profileA := NormalizeLatencyProfile(LatencyModeHLSHigh)
	profileB := NormalizeLatencyProfile(LatencyModeRTSPUltra)
	presetB := video.MusicRadioQualityPreset("480", 0, 0)
	m.SetLatencyProfile(func() LatencyProfile {
		close(captureAEntered)
		<-releaseA
		return profileA
	})

	startA := make(chan error, 1)
	go func() { startA <- m.Start() }()
	select {
	case <-captureAEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Start A did not enter blocked callback")
	}
	m.Stop(50 * time.Millisecond)
	m.SetLatencyProfile(func() LatencyProfile { return profileB })
	m.SetFallbackPreset(func() video.QualityPreset { return presetB })
	if err := m.Start(); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	statusB := m.Status()
	if !statusB.Running || statusB.ActiveSession == nil || statusB.ActiveSession.LatencyProfile != profileB || statusB.ActiveSession.FallbackPreset != presetB {
		t.Fatalf("Start B status = %+v", statusB)
	}

	close(releaseA)
	select {
	case err := <-startA:
		if err != nil {
			t.Fatalf("Start A after replacement: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start A did not return after replacement")
	}
	status := m.Status()
	if !status.Running || status.ActiveSession == nil || status.ActiveSession.SessionID != statusB.ActiveSession.SessionID || status.ActiveSession.LatencyProfile != profileB || status.ActiveSession.FallbackPreset != presetB {
		t.Fatalf("released Start A overrode Start B: %+v", status)
	}
}

func TestRadioStopAfterPromotionAllowsImmediateReplacementStart(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	profileA := NormalizeLatencyProfile(LatencyModeHLSHigh)
	profileB := NormalizeLatencyProfile(LatencyModeRTSPUltra)
	presetB := video.MusicRadioQualityPreset("480", 0, 0)
	m.SetLatencyProfile(func() LatencyProfile { return profileA })

	promotedA := make(chan struct{})
	releaseA := make(chan struct{})
	var promoteMu sync.Mutex
	blocked := false
	m.afterSessionPromote = func(uint64) {
		promoteMu.Lock()
		if blocked {
			promoteMu.Unlock()
			return
		}
		blocked = true
		close(promotedA)
		promoteMu.Unlock()
		<-releaseA
	}
	m.buildRuntime = func(_ context.Context, contract RadioActiveSessionContract) (radioRuntime, radioGate, RTSPEndpoint, error) {
		runtime := &fakeRadioRuntime{exit: make(chan error, 1)}
		path := "a"
		if contract.LatencyProfile == profileB {
			path = "b"
		}
		return runtime, &fakeGate{}, RTSPEndpoint{SessionID: contract.SessionID, Path: path, LocalURL: "rtsp://127.0.0.1:8554/" + path}, nil
	}

	startA := make(chan error, 1)
	go func() { startA <- m.Start() }()
	select {
	case <-promotedA:
	case <-time.After(3 * time.Second):
		t.Fatal("Start A did not publish its active session")
	}

	// A is active but deliberately paused before runtime setup. Stop must revoke
	// that ownership before a replacement Start observes m.done.
	m.Stop(50 * time.Millisecond)
	m.SetLatencyProfile(func() LatencyProfile { return profileB })
	m.SetFallbackPreset(func() video.QualityPreset { return presetB })
	if err := m.Start(); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	statusB := m.Status()
	if !statusB.Running || statusB.ActiveSession == nil || statusB.ActiveSession.LatencyProfile != profileB || statusB.ActiveSession.FallbackPreset != presetB || statusB.Path != "b" {
		t.Fatalf("Start B status = %+v", statusB)
	}

	close(releaseA)
	select {
	case err := <-startA:
		if err != nil {
			t.Fatalf("Start A after replacement: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start A did not return after replacement")
	}
	status := m.Status()
	if !status.Running || status.ActiveSession == nil || status.ActiveSession.SessionID != statusB.ActiveSession.SessionID || status.Path != "b" {
		t.Fatalf("released Start A overrode Start B: %+v", status)
	}
}

func TestRadioStopDoesNotWaitForBlockedStoppedCallback(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	m.cb.OnStopped = func() {
		close(callbackEntered)
		<-releaseCallback
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}

	stopped := make(chan struct{})
	go func() {
		m.Stop(10 * time.Millisecond)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Stop waited for a blocked OnStopped callback")
	}
	select {
	case <-callbackEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("OnStopped was not invoked asynchronously")
	}
	close(releaseCallback)
}

func TestRadioStopPublishesGenerationBoundRTSPDone(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	done := make(chan RTSPEndpoint, 1)
	m.cb.OnRTSPDone = func(endpoint RTSPEndpoint) { done <- endpoint }
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	active := m.Status().ActiveSession
	if active == nil {
		t.Fatal("radio did not publish an active session")
	}
	m.Stop(time.Second)
	select {
	case endpoint := <-done:
		if endpoint.SessionID != "s1" || endpoint.Generation == 0 {
			t.Fatalf("RTSP done endpoint = %+v", endpoint)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("radio Stop did not publish RTSP done")
	}
}

func TestRadioKeepsImmediateInterruptFeederTimestampsLocal(t *testing.T) {
	tracks := []string{"t1", "t2"}
	i := 0
	next := func() (string, string, bool) {
		if i >= len(tracks) {
			return "", "", false
		}
		id := tracks[i]
		i++
		return id, id, true
	}
	m, _ := newRadioHarness(t, next, nil, nil)
	offsets := make(chan float64, 2)
	m.runFeeder = func(_ context.Context, _ string, _ int, loop bool, timestampOffset float64, _ io.Writer) error {
		if loop {
			return nil
		}
		offsets <- timestampOffset
		return nil
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := <-offsets
	second := <-offsets
	if first != 0 {
		t.Fatalf("first feeder offset = %f, want 0", first)
	}
	if second != 0 {
		t.Fatalf("immediate interrupt feeder offset = %f, want local track timeline 0", second)
	}
}

func TestRadioDoesNotAddFallbackDurationToNextTrackHead(t *testing.T) {
	var mu sync.Mutex
	queue := []string{}
	next := func() (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		if len(queue) == 0 {
			return "", "", false
		}
		id := queue[0]
		queue = queue[1:]
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, nil)
	fallbackStarted := make(chan struct{}, 1)
	m.runFallbackFeeder = func(ctx context.Context, _ RadioActiveSessionContract, _ float64, _ io.Writer) (float64, error) {
		fallbackStarted <- struct{}{}
		<-ctx.Done()
		time.Sleep(250 * time.Millisecond)
		return 0.5, nil
	}
	offsets := make(chan float64, 1)
	m.runFeeder = func(_ context.Context, mediaPath string, _ int, _ bool, timestampOffset float64, _ io.Writer) error {
		if mediaPath == "t1" {
			offsets <- timestampOffset
		}
		return nil
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "idle")
	select {
	case <-fallbackStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("fallback feeder did not start")
	}
	mu.Lock()
	queue = append(queue, "t1")
	mu.Unlock()
	m.Wake()
	got := <-offsets
	if got != 0 {
		t.Fatalf("track offset after fallback = %.3f, want local track timeline 0", got)
	}
}

func TestRadioDoesNotAddRetimedFallbackDurationToNextTrackHead(t *testing.T) {
	var mu sync.Mutex
	queue := []string{}
	next := func() (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		if len(queue) == 0 {
			return "", "", false
		}
		id := queue[0]
		queue = queue[1:]
		return id, id, true
	}
	m, h := newRadioHarness(t, next, nil, nil)
	fallbackStarted := make(chan struct{}, 1)
	m.runFallbackFeeder = func(ctx context.Context, _ RadioActiveSessionContract, _ float64, _ io.Writer) (float64, error) {
		fallbackStarted <- struct{}{}
		<-ctx.Done()
		return 0.501, nil
	}
	offsets := make(chan float64, 1)
	m.runFeeder = func(_ context.Context, mediaPath string, _ int, _ bool, timestampOffset float64, _ io.Writer) error {
		if mediaPath == "t1" {
			offsets <- timestampOffset
		}
		return nil
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "idle")
	select {
	case <-fallbackStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("fallback feeder did not start")
	}
	mu.Lock()
	queue = append(queue, "t1")
	mu.Unlock()
	m.Wake()
	got := <-offsets
	if got != 0 {
		t.Fatalf("track offset after fallback = %.6f, want local track timeline 0", got)
	}
}

func TestRadioRetimingUsesSlowerWallClockAsCorrection(t *testing.T) {
	got := retimeRadioFallbackDuration(0.100, 0.351)
	want := 11.0 / 30.0
	if math.Abs(got-want) > 0.0001 {
		t.Fatalf("retimed duration = %.6f, want wall-clock corrected frame boundary %.6f", got, want)
	}
}

func TestRadioPublisherStartFailureIsTerminalAndSanitized(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.startPublisher = func(context.Context, string) (radioPublisher, error) {
		return nil, errors.New("publish rtmp://alice:secret@127.0.0.1:9999/radio?token=private failed")
	}
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	e := h.waitEvent(t, "error").radioError
	if e.Stage != RadioErrorStagePublisher || e.Recoverable {
		t.Fatalf("publisher error = %+v", e)
	}
	if strings.Contains(e.Message, "alice") || strings.Contains(e.Message, "secret") || strings.Contains(e.Message, "private") || strings.Contains(e.Message, "127.0.0.1") {
		t.Fatalf("publisher error leaked private URL: %+v", e)
	}
	h.waitEvent(t, "stopped")
	st := m.Status()
	if st.Phase != RadioPhaseFailed || st.Running || st.LastError == "" || st.StoppedAt.IsZero() {
		t.Fatalf("terminal status = %+v", st)
	}
}

func TestRadioPublisherExitIsImmediatelyFatal(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "idle")
	h.publisher.finish(errors.New("publisher exited unexpectedly"))
	e := h.waitEvent(t, "error").radioError
	if e.Stage != RadioErrorStagePublisher || e.Recoverable {
		t.Fatalf("publisher exit = %+v", e)
	}
	h.waitEvent(t, "stopped")
	if got := m.Status(); got.Phase != RadioPhaseFailed || got.RetryCount != 0 {
		t.Fatalf("status after publisher exit = %+v", got)
	}
}

func TestRadioMediaMTXEarlyExitIsTerminal(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.buildRuntime = func(context.Context, RadioActiveSessionContract) (radioRuntime, radioGate, RTSPEndpoint, error) {
		return nil, nil, RTSPEndpoint{}, errors.New("MediaMTX exited before becoming healthy")
	}
	if err := m.Start(); err == nil {
		t.Fatal("Start succeeded after MediaMTX early exit")
	}
	e := h.waitEvent(t, "error").radioError
	if e.Stage != RadioErrorStageMediaMTX || e.Recoverable {
		t.Fatalf("early MediaMTX error = %+v", e)
	}
	if got := m.Status(); got.Phase != RadioPhaseFailed || got.LastError == "" {
		t.Fatalf("status after early MediaMTX exit = %+v", got)
	}
}

func TestRadioMediaMTXUnexpectedExitReleasesGenerationWaiters(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "blocked", "blocked", true }, nil, map[string]bool{"blocked": true})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	generation := m.CurrentTrackGeneration()
	h.runtime.finish(errors.New("MediaMTX crashed"))
	e := h.waitEvent(t, "error").radioError
	if e.Stage != RadioErrorStageMediaMTX || e.Recoverable {
		t.Fatalf("unexpected MediaMTX error = %+v", e)
	}
	select {
	case <-generation.Completed:
	case <-time.After(time.Second):
		t.Fatal("active generation did not complete after runtime failure")
	}
	select {
	case <-generation.SessionDone:
	case <-time.After(time.Second):
		t.Fatal("session waiter did not release after runtime failure")
	}
	h.waitEvent(t, "stopped")
	if !h.runtime.wasStopped() {
		t.Fatal("runtime.stop was not called after MediaMTX exit")
	}
}

func TestRadioChildExitPrefersMediaMTXWhenBothAreReady(t *testing.T) {
	for i := 0; i < 32; i++ {
		got := make(chan RadioError, 1)
		m := NewRadioManager(t.TempDir(), "127.0.0.1", func() (string, string, int, bool) { return "", "", 0, false }, RadioCallbacks{OnError: func(e RadioError) { got <- e }})
		runtime := &fakeRadioRuntime{exit: make(chan error, 1)}
		publisher := &fakePublisher{exit: make(chan error, 1)}
		runtime.finish(errors.New("MediaMTX crashed"))
		publisher.finish(errors.New("publisher crashed"))
		m.watchRadioChildren(context.Background(), runtime, publisher, func() {}, make(chan struct{}))
		if e := <-got; e.Stage != RadioErrorStageMediaMTX {
			t.Fatalf("iteration %d chose %q, want %q", i, e.Stage, RadioErrorStageMediaMTX)
		}
	}
}

func TestRadioFallbackRetriesWithBoundedBackoffThenStops(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	var calls int
	var delays []time.Duration
	m.runFallbackFeeder = func(context.Context, RadioActiveSessionContract, float64, io.Writer) (float64, error) {
		calls++
		return 0, errors.New("fallback rtmp://user:pass@127.0.0.1/radio failed")
	}
	m.waitFallbackRetry = func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return true
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 5; attempt++ {
		e := h.waitEvent(t, "error").radioError
		if e.Stage != RadioErrorStageFallback || e.Recoverable != (attempt < 5) {
			t.Fatalf("fallback attempt %d error = %+v", attempt, e)
		}
	}
	h.waitEvent(t, "stopped")
	if calls != 5 {
		t.Fatalf("fallback calls = %d, want 5", calls)
	}
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("retry delays = %v, want %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("retry delay[%d] = %s, want %s", i, delays[i], want[i])
		}
	}
	if got := m.Status(); got.Phase != RadioPhaseFailed || got.RetryCount != 4 || got.LastError == "" {
		t.Fatalf("fallback terminal status = %+v", got)
	}
}

func TestRadioFallbackPublisherDownDoesNotRetry(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	var calls, retries int
	m.runFallbackFeeder = func(context.Context, RadioActiveSessionContract, float64, io.Writer) (float64, error) {
		calls++
		return 0, errPublisherDown
	}
	m.waitFallbackRetry = func(context.Context, time.Duration) bool { retries++; return true }
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	e := h.waitEvent(t, "error").radioError
	if e.Stage != RadioErrorStagePublisher || e.Recoverable {
		t.Fatalf("publisher-down fallback error = %+v", e)
	}
	h.waitEvent(t, "stopped")
	if calls != 1 || retries != 0 {
		t.Fatalf("publisher-down calls/retries = %d/%d", calls, retries)
	}
}

func TestRadioTrackErrorReportsRecoverableTrackStage(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "bad", "bad", true }, map[string]error{"bad": errors.New("track failed")}, nil)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	e := h.waitEvent(t, "error").radioError
	if e.Stage != RadioErrorStageTrack || !e.Recoverable {
		t.Fatalf("track error = %+v", e)
	}
}

func TestRadioFallbackTransientFailureRecovers(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	secondAttempt := make(chan struct{})
	thirdAttempt := make(chan struct{})
	allowSecondSuccess := make(chan struct{})
	var calls int
	m.runFallbackFeeder = func(ctx context.Context, _ RadioActiveSessionContract, _ float64, _ io.Writer) (float64, error) {
		calls++
		if calls == 1 {
			return 0, errors.New("temporary fallback failure")
		}
		if calls == 2 {
			close(secondAttempt)
			<-allowSecondSuccess
			return 1, nil
		}
		close(thirdAttempt)
		<-ctx.Done()
		return 0, nil
	}
	m.waitFallbackRetry = func(context.Context, time.Duration) bool { return true }
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	e := h.waitEvent(t, "error").radioError
	if e.Stage != RadioErrorStageFallback || !e.Recoverable {
		t.Fatalf("transient failure = %+v", e)
	}
	select {
	case <-secondAttempt:
	case <-time.After(time.Second):
		t.Fatal("fallback did not restart after transient failure")
	}
	if got := m.Status(); !got.Running || got.Phase != RadioPhaseRunning || got.RetryCount != 1 {
		t.Fatalf("retrying status = %+v", got)
	}
	close(allowSecondSuccess)
	select {
	case <-thirdAttempt:
	case <-time.After(time.Second):
		t.Fatal("fallback did not continue after successful retry")
	}
	if got := m.Status(); !got.Running || got.Phase != RadioPhaseRunning || got.RetryCount != 0 {
		t.Fatalf("recovered status = %+v", got)
	}
}

func TestRadioTrackEndSanitizesErrorAndPreservesCause(t *testing.T) {
	cause := errors.New("Authorization: Bearer bearer-secret password: colon-secret token=equals-secret rtmp://alice:url-secret@127.0.0.1/radio?key=query-secret")
	m, h := newRadioHarness(t, func() (string, string, bool) { return "bad", "bad", true }, map[string]error{"bad": cause}, nil)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	e := h.waitEvent(t, "end")
	if !errors.Is(e.err, cause) {
		t.Fatalf("track end error lost its cause: %v", e.err)
	}
	for _, secret := range []string{"bearer-secret", "colon-secret", "equals-secret", "alice", "url-secret", "127.0.0.1", "query-secret"} {
		if strings.Contains(e.err.Error(), secret) {
			t.Fatalf("track end error leaked %q: %v", secret, e.err)
		}
	}
}

func TestSanitizeRadioErrorRedactsUnsafeOuterWrapper(t *testing.T) {
	nested := SanitizeRadioError(errors.New("rtmp://alice:inner-secret@127.0.0.1/radio?token=inner-query"))
	outer := fmt.Errorf("Authorization: Bearer outer-secret: %w", nested)
	got := SanitizeRadioError(outer)
	if !errors.Is(got, outer) {
		t.Fatal("sanitized error did not preserve the original outer cause")
	}
	for _, secret := range []string{"outer-secret", "alice", "inner-secret", "127.0.0.1", "inner-query"} {
		if strings.Contains(got.Error(), secret) {
			t.Fatalf("sanitized outer wrapper leaked %q: %v", secret, got)
		}
	}
}

func TestSanitizeRadioErrorRetainsDirectSanitizedValue(t *testing.T) {
	direct := sanitizedRadioError{cause: errors.New("cause"), message: "already safe"}
	if got := SanitizeRadioError(direct); got.Error() != direct.Error() || !errors.Is(got, direct.cause) {
		t.Fatalf("direct sanitized value changed: %v", got)
	}
}

func TestRadioInitialStatusIsStopped(t *testing.T) {
	m := NewRadioManager(t.TempDir(), "127.0.0.1", func() (string, string, int, bool) { return "", "", 0, false }, RadioCallbacks{})
	if got := m.Status(); got.Phase != RadioPhaseStopped || got.Running || !got.StoppedAt.IsZero() {
		t.Fatalf("initial status = %+v", got)
	}
}

func TestRadioTerminalStatusPersistsUntilSuccessfulStart(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.startPublisher = func(context.Context, string) (radioPublisher, error) { return nil, errors.New("publisher failed") }
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "error")
	h.waitEvent(t, "stopped")
	failed := m.Status()
	if failed.Phase != RadioPhaseFailed || failed.LastError == "" || failed.StoppedAt.IsZero() {
		t.Fatalf("retained terminal status = %+v", failed)
	}
	m.startPublisher = func(context.Context, string) (radioPublisher, error) {
		h.publisher = &fakePublisher{exit: make(chan error, 1)}
		return h.publisher, nil
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if recovered := m.Status(); recovered.Phase != RadioPhaseRunning || recovered.LastError != "" || !recovered.StoppedAt.IsZero() {
		t.Fatalf("successful restart did not clear terminal error: %+v", recovered)
	}
}

func TestNewRadioErrorSanitizesCredentialsAndPrivateURLs(t *testing.T) {
	e := newRadioError(RadioErrorStageTrack, errors.New("read rtsp://alice:secret@192.168.0.10:8554/radio?token=private failed"), true)
	for _, private := range []string{"alice", "secret", "192.168.0.10", "private"} {
		if strings.Contains(e.Message, private) {
			t.Fatalf("sanitized error leaked %q: %+v", private, e)
		}
	}
}

func TestRadioStatusWhileIdle(t *testing.T) {
	next := func() (string, string, bool) { return "", "", false }
	m, h := newRadioHarness(t, next, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "idle")
	st := m.Status()
	if !st.Running || st.Path != "radio" {
		t.Fatalf("Status = %+v", st)
	}
}

func TestRadioSetRTSPURLPublishesCurrentSessionOnly(t *testing.T) {
	next := func() (string, string, bool) { return "", "", false }
	m, h := newRadioHarness(t, next, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "idle")
	assertRadioReadiness(t, m, true, true)

	if m.SetRTSPURL("stale", "rtsp://8.8.8.8:52000/stale", "stale") {
		t.Fatal("stale session must not update radio RTSP URL")
	}
	if got, want := m.Status().RTSPURL, ""; got != want {
		t.Fatalf("RTSPURL after stale update = %q, want %q", got, want)
	}
	if !m.SetRTSPURL("s1", "rtsp://8.8.8.8:52000/radio", "published") {
		t.Fatal("current session should update radio RTSP URL")
	}
	if got, want := m.Status().RTSPURL, "rtsp://8.8.8.8:52000/radio"; got != want {
		t.Fatalf("RTSPURL after current update = %q, want %q", got, want)
	}
	if !m.Status().RTSPPublic {
		t.Fatal("RTSPPublic should be true after public RTSP URL update")
	}
	if !m.SetRTSPURL("s1", "", "UPnP failed") {
		t.Fatal("current session should accept UPnP failure update")
	}
	if st := m.Status(); st.RTSPPublic || st.RTSPURL != "" {
		t.Fatalf("UPnP failure status = %+v, want no public RTSP URL", st)
	}
}

func TestRadioReadinessCachesProbeResultsAndWithdrawsURLsOnLoss(t *testing.T) {
	var pathReady, hlsReady atomic.Bool
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	h.runtime.pathReadyFunc = func(context.Context) bool { return pathReady.Load() }
	h.runtime.hlsReadyFunc = func(context.Context, LatencyProfile) bool { return hlsReady.Load() }

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "idle")
	assertRadioReadiness(t, m, false, false)
	if m.SetRTSPURL("s1", "rtsp://198.51.100.20:8554/radio", "premature") {
		t.Fatal("RTSP URL must not publish before cached path readiness")
	}

	pathReady.Store(true)
	hlsReady.Store(true)
	assertRadioReadiness(t, m, true, true)
	if !m.SetRTSPURL("s1", "rtsp://198.51.100.20:8554/radio", "published") {
		t.Fatal("current ready session must accept its public RTSP URL")
	}

	pathReady.Store(false)
	hlsReady.Store(false)
	assertRadioReadiness(t, m, false, false)
	if st := m.Status(); st.RTSPURL != "" || st.RTSPPublic {
		t.Fatalf("lost readiness must withdraw RTSP URL: %+v", st)
	}
}

func TestRadioReadinessUsesFrozenActiveProfileAndStatusDoesNotProbe(t *testing.T) {
	var probes atomic.Int32
	profiles := make(chan LatencyProfile, 4)
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.SetLatencyProfile(func() LatencyProfile { return NormalizeLatencyProfile(LatencyModeHLS) })
	h.runtime.hlsReadyFunc = func(_ context.Context, profile LatencyProfile) bool {
		probes.Add(1)
		select {
		case profiles <- profile:
		default:
		}
		return true
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "idle")
	select {
	case got := <-profiles:
		if got.Mode != LatencyModeHLS {
			t.Fatalf("readiness profile = %q, want frozen %q", got.Mode, LatencyModeHLS)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readiness monitor did not probe the active session")
	}
	m.SetLatencyProfile(func() LatencyProfile { return NormalizeLatencyProfile(LatencyModeRTSPUltra) })
	before := probes.Load()
	for i := 0; i < 5; i++ {
		_ = m.Status()
	}
	_ = m.HLSReady(context.Background())
	if got := probes.Load(); got != before {
		t.Fatalf("Status performed readiness probes: before=%d after=%d", before, got)
	}
	if st := m.Status(); st.ActiveSession == nil || st.ActiveSession.LatencyProfile.Mode != LatencyModeHLS {
		t.Fatalf("active profile changed after desired mutation: %+v", st.ActiveSession)
	}
}

func assertRadioReadiness(t *testing.T, m *RadioManager, wantRTSP, wantHLS bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := m.Status()
		if st.RTSPReady == wantRTSP && st.HLSReady == wantHLS {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("readiness = %+v, want RTSP=%v HLS=%v", m.Status(), wantRTSP, wantHLS)
}

package obsrtmp

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

type fakeRadioRuntime struct {
	mu      sync.Mutex
	stopped bool
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
func (f *fakeRadioRuntime) publishURL() string { return "rtsp://user:pass@127.0.0.1:9999/radio" }
func (f *fakeRadioRuntime) rtspURL() string    { return "rtsp://192.168.0.10:8554/radio" }
func (f *fakeRadioRuntime) proxyHLS(w http.ResponseWriter, _ *http.Request, name string) {
	w.Header().Set("X-Proxied", name)
	w.WriteHeader(http.StatusOK)
}
func (f *fakeRadioRuntime) llhlsReady(context.Context) bool { return true }

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

type radioEvent struct {
	kind    string
	trackID string
	err     error
}

type radioHarness struct {
	mu      sync.Mutex
	events  []radioEvent
	eventCh chan radioEvent
	runtime *fakeRadioRuntime
	gate    *fakeGate
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
	h := &radioHarness{eventCh: make(chan radioEvent, 64), runtime: &fakeRadioRuntime{}, gate: &fakeGate{}}
	m := NewRadioManager(t.TempDir(), "192.168.0.10", wrapNext(next), RadioCallbacks{
		OnTrackStart: func(id string) { h.record(radioEvent{kind: "start", trackID: id}) },
		OnTrackEnd:   func(id string, err error) { h.record(radioEvent{kind: "end", trackID: id, err: err}) },
		OnIdle:       func() { h.record(radioEvent{kind: "idle"}) },
		OnStopped:    func() { h.record(radioEvent{kind: "stopped"}) },
	})
	m.buildRuntime = func(context.Context) (radioRuntime, radioGate, RTSPEndpoint, error) {
		return h.runtime, h.gate, RTSPEndpoint{SessionID: "s1", Host: "192.168.0.10", Port: 8554, Path: "radio", LocalURL: h.runtime.rtspURL()}, nil
	}
	m.runPush = func(ctx context.Context, mediaPath string, _ int, publishURL string) error {
		id := mediaPath // tests pass trackID as mediaPath for simplicity
		if blockers[id] {
			<-ctx.Done()
			return ctx.Err()
		}
		return pushErr[id]
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

func TestRadioStatusWhileIdle(t *testing.T) {
	next := func() (string, string, bool) { return "", "", false }
	m, h := newRadioHarness(t, next, nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.waitEvent(t, "idle")
	st := m.Status()
	if !st.Running || st.RTSPURL != "rtsp://192.168.0.10:8554/radio" || st.Path != "radio" {
		t.Fatalf("Status = %+v", st)
	}
}

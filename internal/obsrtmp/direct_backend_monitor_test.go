package obsrtmp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

type backendMonitorProcess struct {
	*fakeProcess
	subscribed chan struct{}
	once       sync.Once
}

func TestDirectBackendMonitorExitEvidenceStaysWithOldGeneration(t *testing.T) {
	root := t.TempDir()
	const id = "0123456789abcdef"
	observer, err := newDirectPublisherObserver(root, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	for _, gen := range []uint64{1, 2} {
		if err := observer.PreparePublisher(t.Context(), airplaycontract.NewPublisherArtifacts(root, id, gen)); err != nil {
			t.Fatal(err)
		}
	}
	var probes atomic.Int32
	runtime := newDirectMonitorTestRuntime(&probes)
	proc := runtime.proc.(*fakeProcess)
	proc.processID = 9876
	route := directBackendRoute{sessionID: id, sessionEpoch: 3, generation: 1, requestID: "old", privateRTSPPort: 18554, runtime: runtime}
	watch := monitorDirectBackend(t.Context(), route, observer, false)
	proc.finish(nil)
	<-watch.done
	if got := observer.terminationObservations(1); len(got) != 1 || got[0].ProcessID != 9876 {
		t.Fatalf("missing old exit: %+v", got)
	}
	if got := observer.terminationObservations(2); len(got) != 0 {
		t.Fatalf("old exit attributed to current publisher: %+v", got)
	}
}

func TestDirectBackendActiveExitRetainsSessionAndRejectsPublicReads(t *testing.T) {
	var probes atomic.Int32
	runtime := newDirectMonitorTestRuntime(&probes)
	route := directBackendRoute{sessionID: "session", sessionEpoch: 3, generation: 2, requestID: "active", privateRTSPPort: 18554, runtime: runtime}
	router := mustNewDirectBackendRouter(t, route)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := &Manager{running: true, directPublishing: true, listenerGeneration: 3, directHandle: DirectSessionHandle{ID: "session", Generation: 3}, stop: cancel,
		status: Status{Connected: true, Publishing: true}}
	event := directBackendExit{route: route, err: errors.New("backend failed")}
	stale := event
	stale.route.generation = 1
	if m.observeDirectBackendExit(router, stale) || !router.canAccept() || !m.status.Connected {
		t.Fatal("stale exit changed current delivery")
	}
	if !m.observeDirectBackendExit(router, event) || router.canAccept() || m.status.Connected || m.status.Publishing {
		t.Fatal("active exit not isolated as delivery failure")
	}
	if ctx.Err() != nil || !m.running || !m.DirectPublishing() {
		t.Fatal("active backend failure stopped receiver/session")
	}
	// Recovery still requires the normal drain/CAS boundary.
	next := route
	next.generation, next.requestID = 3, "recovery"
	if err := router.beginDrain(route); err != nil {
		t.Fatal(err)
	}
	if err := router.commitDrained(route, next); err != nil {
		t.Fatal(err)
	}
	if !router.canAccept() {
		t.Fatal("committed recovery is still unavailable")
	}
	runtime.proc.(*fakeProcess).finish(nil)
}

func TestDirectBackendPendingCleanupBlocksNewDirectAndOBSStartup(t *testing.T) {
	for _, api := range []string{"direct", "OBS"} {
		t.Run(api, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel() // bound any old direct startup before launching a real process
			var starts atomic.Int32
			m := &Manager{key: "test", outDir: t.TempDir(), directRetiringBackends: []*mediaMTXRuntime{{}},
				loopRunner: func(context.Context, uint64) { starts.Add(1) }}
			if api == "direct" {
				_, err := m.StartDirectPublishing(ctx)
				if err == nil || !strings.Contains(err.Error(), "cleanup") {
					t.Fatalf("startup ignored pending cleanup: %v", err)
				}
			} else {
				m.Start()
				m.mu.Lock()
				running, done := m.running, m.done
				m.mu.Unlock()
				if done != nil {
					<-done
				}
				if running || starts.Load() != 0 {
					t.Fatal("OBS restarted while direct backend exit was unconfirmed")
				}
			}
		})
	}
}

func TestDirectBackendMonitorRetainsUnconfirmedRuntime(t *testing.T) {
	var probes atomic.Int32
	runtime := newDirectMonitorTestRuntime(&probes)
	runtime.stopGrace = 20 * time.Millisecond
	proc := runtime.proc.(*fakeProcess)
	proc.exitOnStop, proc.exitOnKill = false, false
	route := directBackendRoute{sessionID: "session", sessionEpoch: 1, generation: 1, requestID: "old", privateRTSPPort: 18554, runtime: runtime}
	m := &Manager{}
	ctx, cancel := context.WithCancel(t.Context())
	watch, err := m.watchDirectBackend(ctx, route, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	finished := make(chan struct{})
	go func() {
		m.finishDirectBackendMonitors(map[directBackendRoute]*directBackendMonitor{route: watch})
		close(finished)
	}()
	t.Cleanup(func() { proc.finish(nil); <-finished; _ = runtime.stop(time.Second) })
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("generation cleanup ignored its stop bound")
	}
	if m.directBackendMonitorCount != 0 || len(m.directRetiringBackends) != 1 || m.directRetiringBackends[0] != runtime {
		t.Fatal("unconfirmed process ownership discarded")
	}
}

func TestDirectBackendSupervisorCleanupCancelsEveryOwnedWatch(t *testing.T) {
	var probes atomic.Int32
	runtime := newDirectMonitorTestRuntime(&probes)
	proc := runtime.proc.(*fakeProcess)
	route := directBackendRoute{sessionID: "session", sessionEpoch: 1, generation: 1, requestID: "initial", privateRTSPPort: 18554, runtime: runtime}
	ctx, cancel := context.WithCancel(t.Context())
	m := &Manager{}
	watch, err := m.watchDirectBackend(ctx, route, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		m.finishDirectBackendMonitors(map[directBackendRoute]*directBackendMonitor{route: watch})
		close(done)
	}()
	t.Cleanup(func() { cancel(); proc.finish(nil); <-done })
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor cleanup waited on an uncanceled initial watch")
	}
	if ctx.Err() != nil {
		t.Fatal("generation cleanup canceled parent session")
	}
	proc.mu.Lock()
	closed := proc.closed
	proc.mu.Unlock()
	if !closed || m.directBackendMonitorCount != 0 {
		t.Fatal("owned generation was not stopped")
	}
}

func (p *backendMonitorProcess) done() <-chan error {
	p.once.Do(func() { close(p.subscribed) })
	return p.fakeProcess.done()
}

func TestDirectBackendMonitorOldExitDoesNotEndCommittedSession(t *testing.T) {
	root := t.TempDir()
	var probes atomic.Int32
	oldRuntime := newDirectMonitorTestRuntime(&probes)
	newRuntime := newDirectMonitorTestRuntime(&probes)
	newProc := &backendMonitorProcess{fakeProcess: newRuntime.proc.(*fakeProcess), subscribed: make(chan struct{})}
	newRuntime.proc = newProc
	newRuntime.cfg.Ports.BackendRTSP = 18555
	session := directMonitorTestSession(filepath.Join(root, "recording.mp4"))
	active := directBackendRoute{sessionID: session.ID, sessionEpoch: 1, generation: 1, requestID: "initial", privateRTSPPort: 18554, runtime: oldRuntime}
	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: 18554})
	gate.backendRouter = mustNewDirectBackendRouter(t, active)
	close(gate.done)
	var gateStops, finalCalls atomic.Int32
	gate.cancel = func() { gateStops.Add(1) }
	started := make(chan struct{}, 1)
	m := &Manager{running: true, directPublishing: true, listenerGeneration: 1,
		directHandle: DirectSessionHandle{ID: session.ID, Generation: 1},
		mtx:          oldRuntime, rtspGate: gate, status: Status{Publishing: true},
		cb: Callbacks{OnStart: func(Session) { started <- struct{}{} }, OnDone: func(Session) { finalCalls.Add(1) }}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		oldRuntime.proc.(*fakeProcess).finish(nil)
		newProc.finish(nil)
		waitDirectMonitorDone(t, done)
	})
	go m.monitorDirectPublishing(ctx, cancel, 1, session, oldRuntime, gate, root, done, nil)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial publication missing")
	}
	candidate := active
	candidate.generation, candidate.requestID, candidate.privateRTSPPort, candidate.runtime = 2, "candidate", 18555, newRuntime
	drain, err := gate.beginDrain(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := drain.wait(0); err != nil {
		t.Fatal(err)
	}
	if err := gate.commitDrained(active, candidate); err != nil {
		t.Fatal(err)
	}
	oldRuntime.proc.(*fakeProcess).finish(nil)
	select {
	case <-done:
		t.Fatal("old backend exit ended the newly committed session")
	case <-newProc.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not follow committed backend")
	}
	if gateStops.Load() != 0 || finalCalls.Load() != 0 || !m.DirectPublishing() || !video.CurrentStatusForID(root, session.ID).HLS {
		t.Fatal("old generation cleanup destroyed session-owned publication")
	}
	newProc.finish(errors.New("active backend failed"))
	deadline := time.NewTimer(time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
waitFailure:
	for {
		select {
		case <-done:
			t.Fatal("active backend exit ended receiver/session lifetime")
		case <-deadline.C:
			t.Fatal("active backend exit was not reflected")
		case <-tick.C:
			m.mu.Lock()
			failed := !m.status.Connected && !m.status.Publishing
			m.mu.Unlock()
			if failed {
				break waitFailure
			}
		}
	}
	if ctx.Err() != nil || gateStops.Load() != 0 || finalCalls.Load() != 0 || !m.DirectPublishing() || gate.backendRouter.canAccept() {
		t.Fatal("delivery failure escaped generation boundary")
	}
	cancel()
	waitDirectMonitorDone(t, done)
	if gateStops.Load() != 1 || finalCalls.Load() != 1 {
		t.Fatalf("session cleanup gate=%d final=%d, want one each", gateStops.Load(), finalCalls.Load())
	}
	newProc.mu.Lock()
	closed := newProc.closed
	newProc.mu.Unlock()
	if !closed {
		t.Fatal("session stop left active backend running")
	}
}

func TestDirectBackendMonitorRetiresHealthyOldRuntimeAfterCommit(t *testing.T) {
	root := t.TempDir()
	var probes atomic.Int32
	oldRuntime, nextRuntime := newDirectMonitorTestRuntime(&probes), newDirectMonitorTestRuntime(&probes)
	oldProc := oldRuntime.proc.(*fakeProcess)
	oldProc.exitOnStop = true
	nextProc := nextRuntime.proc.(*fakeProcess)
	nextProc.exitOnStop = true
	nextRuntime.cfg.Ports.BackendRTSP = 18555
	session := directMonitorTestSession(filepath.Join(root, "old.mp4"))
	active := directBackendRoute{sessionID: session.ID, sessionEpoch: 1, generation: 1, requestID: "initial", privateRTSPPort: 18554, runtime: oldRuntime}
	next := directBackendRoute{sessionID: session.ID, sessionEpoch: 1, generation: 2, requestID: "candidate", privateRTSPPort: 18555, runtime: nextRuntime}
	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: 18554})
	gate.backendRouter = mustNewDirectBackendRouter(t, active)
	close(gate.done)
	started := make(chan struct{}, 1)
	m := &Manager{running: true, directPublishing: true, listenerGeneration: 1, directHandle: DirectSessionHandle{ID: session.ID, Generation: 1}, mtx: oldRuntime, rtspGate: gate, status: Status{Publishing: true}, cb: Callbacks{OnStart: func(Session) { started <- struct{}{} }}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); oldProc.finish(nil); nextProc.finish(nil); waitDirectMonitorDone(t, done) })
	go m.monitorDirectPublishing(ctx, cancel, 1, session, oldRuntime, gate, root, done, nil)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial session not ready")
	}
	drain, err := gate.beginDrain(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := drain.wait(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := gate.commitDrained(active, next); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldProc.done():
	case <-time.After(time.Second):
		t.Fatal("healthy superseded MediaMTX kept running until session stop")
	}
	if oldProc.stopCalls.Load() != 1 || nextProc.stopCalls.Load() != 0 || !m.DirectPublishing() {
		t.Fatal("retirement escaped old generation")
	}
	select {
	case <-done:
		t.Fatal("retirement ended public session")
	default:
	}
}

func TestDirectBackendReadinessCannotPublishAfterRouteChangeOrStop(t *testing.T) {
	for _, action := range []string{"route-change", "user-stop"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			var probes, starts atomic.Int32
			oldRuntime := newDirectMonitorTestRuntime(&probes)
			candidateRuntime := newDirectMonitorTestRuntime(&probes)
			candidateRuntime.cfg.Ports.BackendRTSP = 18555
			session := directMonitorTestSession(filepath.Join(root, "recording.mp4"))
			route := directBackendRoute{sessionID: session.ID, sessionEpoch: 1, generation: 1, requestID: "old", privateRTSPPort: 18554, runtime: oldRuntime}
			gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: 18554})
			gate.backendRouter = mustNewDirectBackendRouter(t, route)
			close(gate.done)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			var transitionErr error
			oldRuntime.httpClient.Transport = directRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if action == "user-stop" {
					cancel()
				} else {
					next := route
					next.generation, next.requestID, next.privateRTSPPort, next.runtime = 2, "next", 18555, candidateRuntime
					transitionErr = gate.backendRouter.beginDrain(route)
					if transitionErr == nil {
						transitionErr = gate.backendRouter.commitDrained(route, next)
					}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ready":true}`)), Request: req}, nil
			})
			candidateProbed := make(chan struct{}, 1)
			candidateRuntime.httpClient.Transport = directRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				select {
				case candidateProbed <- struct{}{}:
				default:
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ready":false}`)), Request: req}, nil
			})
			m := &Manager{running: true, directPublishing: true, listenerGeneration: 1,
				directHandle: DirectSessionHandle{ID: session.ID, Generation: 1}, mtx: oldRuntime, rtspGate: gate,
				status: Status{Publishing: true}, cb: Callbacks{OnStart: func(Session) { starts.Add(1) }}}
			t.Cleanup(func() {
				cancel()
				oldRuntime.proc.(*fakeProcess).finish(nil)
				candidateRuntime.proc.(*fakeProcess).finish(nil)
				waitDirectMonitorDone(t, done)
			})
			go m.monitorDirectPublishing(ctx, cancel, 1, session, oldRuntime, gate, root, done, nil)
			if action == "user-stop" {
				waitDirectMonitorDone(t, done)
			} else {
				select {
				case <-candidateProbed:
				case <-time.After(500 * time.Millisecond):
				}
				cancel()
				waitDirectMonitorDone(t, done)
			}
			if transitionErr != nil {
				t.Fatal(transitionErr)
			}
			if starts.Load() != 0 || m.mediaGeneration != 0 {
				t.Fatal("stale backend readiness registered public history")
			}
		})
	}
}

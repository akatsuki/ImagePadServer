package obsrtmp

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectBackendOwnershipRegisteredBeforeProcessLaunch(t *testing.T) {
	m := &Manager{}
	runtime := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	proc.exitOnStop = true
	registered := false
	runtime.startProcess = func(context.Context, string, string) (managedProcess, error) {
		m.mu.Lock()
		registered = m.directBackendReservations[runtime] != nil
		m.mu.Unlock()
		return proc, nil
	}
	runtime.checkHealth = func(context.Context, string) error { return nil }
	t.Cleanup(func() { proc.finish(nil); _ = m.stopOwnedDirectBackend(runtime, time.Second) })
	if err := m.startOwnedDirectBackend(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	if !registered || m.directBackendReservations[runtime] == nil {
		t.Fatal("backend process launched without a retained owner")
	}
	config := runtime.configPath
	if err := m.stopOwnedDirectBackend(runtime, time.Second); err != nil {
		t.Fatal(err)
	}
	if m.directBackendReservations[runtime] != nil {
		t.Fatal("confirmed owner not released")
	}
	if _, err := os.Stat(config); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned config survived confirmed stop")
	}
}

func TestDirectBackendOwnershipFailedStartupKeepsUnconfirmedProcess(t *testing.T) {
	m := &Manager{}
	runtime := testRuntime(defaultTestConfig())
	runtime.stopGrace = 20 * time.Millisecond
	proc := newFakeProcess()
	proc.exitOnKill = false
	runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	runtime.checkHealth = func(context.Context, string) error { return errors.New("not ready") }
	t.Cleanup(func() { proc.finish(nil); _ = m.stopOwnedDirectBackend(runtime, time.Second) })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := m.startOwnedDirectBackend(ctx, runtime); !errors.Is(err, errMediaMTXProcessExitUnconfirmed) {
		t.Fatalf("missing stop uncertainty: %v", err)
	}
	if m.directBackendReservations[runtime] == nil {
		t.Fatal("startup failure discarded the live backend owner")
	}
	if err := m.stopOwnedDirectBackend(runtime, time.Millisecond); !errors.Is(err, errMediaMTXProcessExitUnconfirmed) {
		t.Fatalf("unconfirmed exit accepted: %v", err)
	}
	if m.directBackendReservations[runtime] == nil {
		t.Fatal("failed cleanup discarded owner")
	}
	proc.finish(nil)
	if err := m.stopOwnedDirectBackend(runtime, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(m.directBackendReservations) != 0 {
		t.Fatal("confirmed failed-start owner not released")
	}
}

func TestDirectBackendOwnershipStopDuringSpawnRetainsUntilLateExit(t *testing.T) {
	m := &Manager{}
	runtime := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	proc.exitOnStop = true
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	runtime.startProcess = func(context.Context, string, string) (managedProcess, error) {
		close(entered)
		<-release
		return proc, nil
	}
	runtime.checkHealth = func(context.Context, string) error { return nil }
	result := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		unblock()
		proc.finish(nil)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("test startup worker did not terminate")
		}
		_ = m.stopOwnedDirectBackend(runtime, time.Second)
	})
	go func() { defer close(finished); result <- m.startOwnedDirectBackend(t.Context(), runtime) }()
	<-entered
	if err := m.startOwnedDirectBackend(t.Context(), runtime); err == nil {
		t.Fatal("duplicate startup permitted")
	}
	if err := m.stopOwnedDirectBackend(runtime, 10*time.Millisecond); !errors.Is(err, errMediaMTXProcessExitUnconfirmed) {
		t.Fatalf("late spawn accepted as stopped: %v", err)
	}
	m.mu.Lock()
	retained := m.directBackendReservations[runtime] != nil
	m.mu.Unlock()
	if !retained {
		t.Fatal("late spawn owner was lost")
	}
	unblock()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late spawn returned ready: %v", err)
		}
	case <-time.After(time.Second):
		proc.finish(nil)
		t.Fatal("canceled startup did not retire late process")
	}
	if len(m.directBackendReservations) != 0 || !proc.closed {
		t.Fatal("late spawned process was not confirmed and released")
	}
}

func TestDirectBackendOwnershipReapOnlyConfirmedExitsWithoutNewSignals(t *testing.T) {
	for _, kind := range []string{"startup", "monitor"} {
		t.Run(kind, func(t *testing.T) {
			m := &Manager{}
			runtime := testRuntime(defaultTestConfig())
			runtime.stopGrace = 10 * time.Millisecond
			proc := newFakeProcess()
			proc.exitOnKill = false
			runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
			runtime.checkHealth = func(context.Context, string) error { return errors.New("not ready") }
			t.Cleanup(func() { proc.finish(nil); _ = runtime.stop(time.Second) })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			if err := m.startOwnedDirectBackend(ctx, runtime); !errors.Is(err, errMediaMTXProcessExitUnconfirmed) {
				t.Fatal(err)
			}
			if kind == "monitor" {
				delete(m.directBackendReservations, runtime)
				m.directRetiringBackends = []*mediaMTXRuntime{runtime}
			}
			// stop() dispatches bounded shutdown calls asynchronously. Under
			// scheduler load its deadline may expire before a dispatch runs.
			// Establish that baseline before attributing later calls to reap.
			deadline := time.After(time.Second)
			ticks := time.NewTicker(time.Millisecond)
			defer ticks.Stop()
			for proc.stopCalls.Load() == 0 || proc.killCalls.Load() == 0 {
				select {
				case <-deadline:
					t.Fatal("initial stop/kill dispatch was not observed")
				case <-ticks.C:
				}
			}
			stops, kills := proc.stopCalls.Load(), proc.killCalls.Load()
			if stops != 1 || kills != 1 {
				t.Fatalf("initial stop/kill repeated: %d/%d", stops, kills)
			}
			m.reapDirectBackendCleanup()
			if len(m.directBackendReservations)+len(m.directRetiringBackends) != 1 {
				t.Fatal("live unconfirmed owner was discarded")
			}
			proc.finish(nil)
			m.reapDirectBackendCleanup()
			if len(m.directBackendReservations)+len(m.directRetiringBackends) != 0 {
				t.Fatal("confirmed exit still blocks new startup")
			}
			if proc.stopCalls.Load() != stops || proc.killCalls.Load() != kills {
				t.Fatal("reap sent a new stop or kill")
			}
		})
	}
}

func TestDirectBackendOwnershipCleanupStopsUnpublishedCandidatesOnly(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	var sessionStops atomic.Int32
	m := &Manager{stop: func() { sessionStops.Add(1) }}
	for i := 0; i < 2; i++ {
		runtime := testRuntime(defaultTestConfig())
		proc := newFakeProcess()
		proc.exitOnStop = true
		runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
		runtime.checkHealth = func(context.Context, string) error { return nil }
		t.Cleanup(func() { proc.finish(nil); _ = runtime.stop(time.Second) })
		if err := m.startOwnedDirectBackend(parent, runtime); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.cleanupDirectBackendReservations(time.Second); err != nil {
		t.Fatal(err)
	}
	if len(m.directBackendReservations) != 0 {
		t.Fatal("unpublished candidate survived session cleanup")
	}
	if sessionStops.Load() != 0 || parent.Err() != nil {
		t.Fatal("candidate cleanup canceled receiver/session")
	}
}

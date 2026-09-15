package obsrtmp

import (
	"context"
	"testing"
	"time"
)

func TestDirectBackendTransferMovesReservationToMonitorOnce(t *testing.T) {
	m := &Manager{}
	runtime := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	proc.exitOnStop = true
	runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	runtime.checkHealth = func(context.Context, string) error { return nil }
	t.Cleanup(func() {
		proc.finish(nil)
		_ = m.stopOwnedDirectBackend(runtime, time.Second)
		_ = runtime.stop(time.Second)
	})
	if err := m.startOwnedDirectBackend(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	route := directBackendRoute{sessionID: "session", sessionEpoch: 1, generation: 2, requestID: "candidate", privateRTSPPort: 18554, runtime: runtime}
	watch, err := m.watchDirectBackend(t.Context(), route, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.finishDirectBackendMonitors(map[directBackendRoute]*directBackendMonitor{route: watch})
	if len(m.directBackendReservations) != 0 || m.directBackendMonitorCount != 1 {
		t.Fatal("candidate retains two cleanup owners after transfer")
	}
	if err := m.stopOwnedDirectBackend(runtime, time.Second); err != nil {
		t.Fatal(err)
	}
	if proc.stopCalls.Load() != 0 {
		t.Fatal("stale reservation cleanup stopped monitored generation")
	}
}

func TestDirectBackendTransferRejectsStoppingReservation(t *testing.T) {
	m := &Manager{}
	runtime := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	proc.exitOnKill = false
	runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	runtime.checkHealth = func(context.Context, string) error { return nil }
	t.Cleanup(func() { proc.finish(nil); _ = m.stopOwnedDirectBackend(runtime, time.Second) })
	if err := m.startOwnedDirectBackend(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	if err := m.stopOwnedDirectBackend(runtime, time.Millisecond); err == nil {
		t.Fatal("test process exited unexpectedly")
	}
	route := directBackendRoute{sessionID: "session", sessionEpoch: 1, generation: 2, requestID: "candidate", privateRTSPPort: 18554, runtime: runtime}
	watch, err := m.watchDirectBackend(t.Context(), route, nil, false)
	if watch != nil {
		defer m.finishDirectBackendMonitors(map[directBackendRoute]*directBackendMonitor{route: watch})
	}
	if err == nil || watch != nil {
		t.Fatal("a stopping reservation was adopted as active")
	}
	if len(m.directBackendReservations) != 1 || m.directBackendMonitorCount != 0 {
		t.Fatal("rejected transfer lost ownership")
	}
}

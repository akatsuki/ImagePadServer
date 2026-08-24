package obsrtmp

import (
	"testing"
	"time"
)

func newPublishingContinuityTestManager(t *testing.T) *Manager {
	t.Helper()
	manager := New(t.TempDir(), "127.0.0.1", 1935, "test-key", nil, nil, Callbacks{})
	manager.mu.Lock()
	manager.running = true
	manager.listenerGeneration = 1
	manager.mu.Unlock()
	t.Cleanup(manager.Stop)
	return manager
}

func finalizePublishingContinuityTestSession(t *testing.T, manager *Manager, id string) {
	t.Helper()
	session := &Session{ID: id, StartedAt: time.Now().UTC()}
	armed, accepted := manager.acceptSession(session, 1)
	if !accepted || !armed {
		t.Fatalf("acceptSession() = armed %v, accepted %v; want true, true", armed, accepted)
	}
	session.FinishedAt = time.Now().UTC()
	if !manager.finalizeAcceptedSession(session, 1) {
		t.Fatal("finalizeAcceptedSession() returned false")
	}
}

func TestPublishingDisconnectTimeoutDisarms(t *testing.T) {
	manager := newPublishingContinuityTestManager(t)
	timedOut := make(chan struct{}, 1)
	manager.cb.OnContinuousPublishingTimeout = func() { timedOut <- struct{}{} }
	manager.StartContinuousPublishing()
	finalizePublishingContinuityTestSession(t, manager, "first")

	if status := manager.Status(); !status.Publishing || status.Connected {
		t.Fatalf("after disconnect status = connected %v, publishing %v; want false, true", status.Connected, status.Publishing)
	}

	manager.mu.Lock()
	manager.schedulePublishDisconnectTimeoutLocked(20 * time.Millisecond)
	manager.mu.Unlock()

	deadline := time.Now().Add(time.Second)
	for manager.Status().Publishing && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if status := manager.Status(); status.Publishing {
		t.Fatalf("after disconnect timeout publishing = true; message = %q", status.Message)
	}
	select {
	case <-timedOut:
	case <-time.After(time.Second):
		t.Fatal("continuous publishing timeout callback was not called")
	}
}

func TestPublishingOneShotStillDisarmsImmediately(t *testing.T) {
	manager := newPublishingContinuityTestManager(t)
	manager.StartPublishing()
	finalizePublishingContinuityTestSession(t, manager, "one-shot")

	status := manager.Status()
	if status.Publishing || status.Connected {
		t.Fatalf("after one-shot disconnect status = connected %v, publishing %v; want false, false", status.Connected, status.Publishing)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.continuousPublishing || manager.publishDisconnectTimer != nil {
		t.Fatal("one-shot disconnect left continuous mode or reconnect timer armed")
	}
}

func TestPublishingReconnectCancelsDisconnectTimeout(t *testing.T) {
	manager := newPublishingContinuityTestManager(t)
	timedOut := make(chan struct{}, 1)
	manager.cb.OnContinuousPublishingTimeout = func() { timedOut <- struct{}{} }
	manager.StartContinuousPublishing()
	finalizePublishingContinuityTestSession(t, manager, "first")

	manager.mu.Lock()
	manager.schedulePublishDisconnectTimeoutLocked(20 * time.Millisecond)
	manager.mu.Unlock()

	reconnected := &Session{ID: "second", StartedAt: time.Now().UTC()}
	armed, accepted := manager.acceptSession(reconnected, 1)
	if !accepted || !armed {
		t.Fatalf("reconnect acceptSession() = armed %v, accepted %v; want true, true", armed, accepted)
	}
	time.Sleep(50 * time.Millisecond)
	if status := manager.Status(); !status.Publishing || !status.Connected {
		t.Fatalf("after reconnect status = connected %v, publishing %v; want true, true", status.Connected, status.Publishing)
	}
	select {
	case <-timedOut:
		t.Fatal("stale disconnect timeout callback fired after reconnect")
	default:
	}
}

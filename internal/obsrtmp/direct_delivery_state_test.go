package obsrtmp

import (
	"testing"
	"time"
)

func TestManagedDirectDeliveryStateChangesOnlyAtCommit(t *testing.T) {
	c, a, old, next, output, _ := directReconfigureReadyFixture(t)
	c.manager.directDelivery = c
	c.publishDeliveryStateLocked()
	before, ok := c.manager.ManagedDirectDeliveryState()
	if !ok || before.Generation != 1 || before.Handle != c.manager.directHandle {
		t.Fatalf("initial state=%+v managed=%v", before, ok)
	}
	if before.Plan == a.plan {
		t.Fatal("uncommitted candidate was exposed as active")
	}
	c.mu.Lock()
	c.transactionActive = true
	c.publishDeliveryStateLocked()
	done := make(chan DirectDeliveryState, 1)
	go func() { state, _ := c.manager.ManagedDirectDeliveryState(); done <- state }()
	select {
	case state := <-done:
		if !state.ChangePending || state.Plan != before.Plan || state.Generation != 1 {
			t.Errorf("pending state changed committed contract: %+v", state)
		}
	case <-time.After(time.Second):
		t.Error("status waited for coordinator/process work")
	}
	c.mu.Unlock()
	if err := c.CommitDirectReconfigure(a, old, next, output, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after, ok := c.manager.ManagedDirectDeliveryState()
	if !ok || after.Generation != 2 || after.Plan != a.plan || !after.ChangePending {
		t.Fatalf("committed state=%+v managed=%v", after, ok)
	}
	c.manager.mu.Lock()
	c.manager.directHandle.Generation++
	c.manager.mu.Unlock()
	if _, ok := c.manager.ManagedDirectDeliveryState(); ok {
		t.Fatal("stale coordinator exposed to a newer session epoch")
	}
}

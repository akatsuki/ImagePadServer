package obsrtmp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

type observedAdmissionContext struct {
	context.Context
	seen chan struct{}
	once sync.Once
}

func (c *observedAdmissionContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.seen) })
	return err
}

func TestManagedDirectChangeCancelledDuringAdmission(t *testing.T) {
	c, a, old, next, output, _ := directReconfigureReadyFixture(t)
	if err := c.CommitDirectReconfigure(a, old, next, output, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	c.manager.directDelivery = c
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wait := &observedAdmissionContext{Context: ctx, seen: make(chan struct{})}
	done := make(chan error, 1)
	c.mu.Lock()
	go func() {
		_, err := c.manager.ReconfigureManagedDirectDelivery(wait, c.manager.directHandle, a.plan, "unused", directDeliveryExecutorFixture{})
		done <- err
	}()
	<-wait.seen // The first cancellation check returned nil, then admission waits.
	cancel()
	c.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission returned %v", err)
	}
}

func TestManagedDirectChangeNoopRequiresLiveCommittedPublisher(t *testing.T) {
	for _, state := range []string{"live", "already-exited", "no-ready", "backend-failed"} {
		t.Run(state, func(t *testing.T) {
			c, a, old, next, output, _ := directReconfigureReadyFixture(t)
			if err := c.CommitDirectReconfigure(a, old, next, output, time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			c.manager.directDelivery = c
			if state == "already-exited" {
				c.observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: a.descriptor.ArtifactPaths, Started: true, ExitConfirmed: true, CompletedAt: time.Now()})
			}
			if state == "no-ready" {
				delete(c.observer.generationReadinessState, 2)
			}
			if state == "backend-failed" {
				c.gate.backendRouter.mu.Lock()
				c.gate.backendRouter.failed = true
				c.gate.backendRouter.mu.Unlock()
			}
			executor := directDeliveryExecutorFixture{apply: func(context.Context, airplaycontract.CandidateDelivery) error {
				t.Error("same settings dispatched a publisher")
				return nil
			}}
			result, err := c.manager.ReconfigureManagedDirectDelivery(t.Context(), c.manager.directHandle, a.plan, "unused", executor)
			if state != "live" {
				if err == nil || result.Applied {
					t.Fatal("dead publisher reported a successful live no-op")
				}
			} else if err != nil || !result.Applied || result.Generation != 2 {
				t.Fatalf("live no-op result=%+v err=%v", result, err)
			}
			if c.ledger.Snapshot().LastAllocatedGeneration != 2 {
				t.Fatal("no-op allocated a generation")
			}
		})
	}
}

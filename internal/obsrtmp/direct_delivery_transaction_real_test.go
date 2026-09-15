package obsrtmp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

type directDeliveryExecutorFixture struct {
	apply   func(context.Context, airplaycontract.CandidateDelivery) error
	recover func(context.Context, airplaycontract.CandidateDelivery) error
}

func (f directDeliveryExecutorFixture) ApplySourceClockDelivery(ctx context.Context, d airplaycontract.CandidateDelivery) error {
	return f.apply(ctx, d)
}
func (f directDeliveryExecutorFixture) RecoverSourceClockDelivery(ctx context.Context, d airplaycontract.CandidateDelivery) error {
	return f.recover(ctx, d)
}

func TestManagedDirectChangeRealDetachedWaitAndSingleRestoration(t *testing.T) {
	if os.Getenv("IMAGEPAD_DIRECT_BACKEND_OUTPUT_REAL") != "1" {
		t.Skip("isolated real backend opt-in required")
	}
	exe, err := ResolveMediaMTX()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_MEDIAMTX", exe)
	t.Setenv("IMAGEPAD_MEDIAMTX_DEBUG_LOG", "")
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	m := &Manager{outDir: t.TempDir(), host: "127.0.0.1"}
	initialPlan, err := newDirectDeliveryPlan("0123456789abcdef", "transaction-initial", NormalizeLatencyProfile("rtsp-ultra"), "720", video.ResolveQuality("720", 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	info, err := m.StartManagedDirectPublishingWithPlan(ctx, initialPlan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.StopDirect(info.Handle, 7*time.Second); info.PublisherObserver.SealPublishers() })
	c := m.directDelivery
	initial := airplaycontract.NewPublisherArtifacts(info.PublisherArtifactRoot, info.Session.ID, 1)
	if err := info.PublisherObserver.PreparePublisher(ctx, initial); err != nil {
		t.Fatal(err)
	}
	plan, err := newDirectDeliveryPlan(info.Session.ID, "wanted-360", NormalizeLatencyProfile("hls"), "360", video.ResolveQuality("360", 0))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	restored := make(chan airplaycontract.CandidateDeliveryBinding, 1)
	fixtureErr := errors.New("fixture output rejected")
	executor := directDeliveryExecutorFixture{
		apply: func(ownerCtx context.Context, d airplaycontract.CandidateDelivery) error {
			close(entered)
			select {
			case <-release:
			case <-ownerCtx.Done():
				return ownerCtx.Err()
			}
			if err := ownerCtx.Err(); err != nil {
				return err
			}
			info.PublisherObserver.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: initial, Started: true, ExitConfirmed: true, CompletedAt: time.Now()})
			if err := d.ClaimPublisher(); err != nil {
				return err
			}
			info.PublisherObserver.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: d.Binding().Descriptor.ArtifactPaths, Started: true, ExitConfirmed: true, CompletedAt: time.Now()})
			return fixtureErr
		},
		recover: func(ownerCtx context.Context, d airplaycontract.CandidateDelivery) error {
			if err := ownerCtx.Err(); err != nil {
				return err
			}
			restored <- d.Binding()
			return fixtureErr
		},
	}
	waitCtx, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()
	result := make(chan error, 1)
	go func() {
		_, err := m.ReconfigureManagedDirectDelivery(waitCtx, info.Handle, plan, os.Args[0], executor)
		result <- err
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("transaction never dispatched: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stopWaiting()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP wait did not cancel")
	}
	if _, err := m.ReconfigureManagedDirectDelivery(ctx, info.Handle, plan, os.Args[0], executor); err == nil {
		t.Fatal("concurrent request entered the same transaction")
	}
	// Release the session-owned executor after the HTTP waiter has departed.
	release <- struct{}{}
	select {
	case binding := <-restored:
		if binding.ExpectedActiveGeneration != 1 || binding.Descriptor.Generation != 3 || binding.Descriptor.Output.Width != 1280 || binding.Descriptor.Output.Height != 720 || binding.Descriptor.Profile.Mode != "rtsp-ultra" || binding.PublishURL == info.PublishURL {
			t.Fatalf("invalid restoration binding: generation=%d output=%+v", binding.Descriptor.Generation, binding.Descriptor.Output)
		}
	case <-ctx.Done():
		t.Fatal("detached transaction did not attempt restoration")
	}
	for {
		c.mu.Lock()
		pending := c.pending
		active := c.transactionActive
		c.mu.Unlock()
		if pending == nil && !active {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("failed restoration retained candidate")
		case <-time.After(10 * time.Millisecond):
		}
	}
	snapshot := c.ledger.Snapshot()
	if snapshot.ActiveGeneration != 1 || snapshot.LastAllocatedGeneration != 3 || snapshot.PreparedGeneration != 0 {
		t.Fatalf("restoration loop or false commit: %+v", snapshot)
	}
	m.mu.Lock()
	owners := len(m.directBackendReservations)
	alive := m.running
	m.mu.Unlock()
	if owners != 0 || !alive {
		t.Fatalf("candidate cleanup/receiver session: owners=%d sessionAlive=%t", owners, alive)
	}
	t.Log("REAL backend startup/cleanup and session-owned transaction: caller canceled; candidate2 failed; one 720p restoration3 failed; no generation4. Native executor=FIXTURE, device=NOT_RUN")
}

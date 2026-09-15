package obsrtmp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

// Start with a committed 360p generation, then fail a requested 720p change.
// The restoration must use the committed snapshot, not current desired settings.
func directCompensationFixture(t *testing.T) (*directDeliveryCoordinator, *directCandidateDelivery) {
	t.Helper()
	c, committed, old, next, output, _ := directReconfigureReadyFixture(t)
	if err := c.CommitDirectReconfigure(committed, old, next, output, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	plan, err := newDirectDeliveryPlan(c.active.SessionID, "wanted-720", NormalizeLatencyProfile("hls"), "720", video.ResolveQuality("720", 0))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := c.PrepareDirectReconfigure(2, plan)
	if err != nil {
		t.Fatal(err)
	}
	d := &directCandidateDelivery{owner: c, attempt: attempt}
	if err := d.Abort(); err != nil {
		t.Fatal(err)
	}
	c.observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: committed.descriptor.ArtifactPaths, Started: true, ExitConfirmed: true, CompletedAt: time.Now()})
	return c, d
}

func TestDirectCompensationRestoresLastCommittedPlanOnce(t *testing.T) {
	c, failed := directCompensationFixture(t)
	route := c.gate.backendRouter.snapshot()
	recovery, err := c.prepareDirectCompensation(failed)
	if err != nil {
		t.Fatalf("prepare restoration: %v", err)
	}
	if recovery.descriptor.Generation != 4 || recovery.expected != route || recovery.plan.Output.Width != 640 || recovery.plan.Output.Height != 360 || recovery.plan.Profile.Mode != "rtsp-ultra" || recovery.plan.RequestedQualityMode != "360" {
		t.Fatalf("restoration did not retain last committed 360p/RTSP plan: %+v", recovery.plan)
	}
	if recovery.plan.Output != DirectOutputSettings(c.active.Output) || recovery.plan.RequestID == failed.attempt.plan.RequestID || recovery.plan.RequestID == c.active.RequestID {
		t.Fatal("restoration changed committed output or reused request identity")
	}
	if recovery.descriptor.ArtifactPaths == failed.attempt.descriptor.ArtifactPaths || recovery.descriptor.ArtifactPaths == c.active.ArtifactPaths || recovery.backend.runtime.directPublishURL() == route.runtime.directPublishURL() {
		t.Fatal("restoration reused prior artifacts or backend endpoint")
	}
	if c.ledger.Snapshot().ActiveGeneration != 2 || c.gate.backendRouter.snapshot() != route {
		t.Fatal("preparation promoted recovery before readiness")
	}
	if err := c.AbortDirectReconfigure(recovery); err != nil {
		t.Fatal(err)
	}
	if _, err := c.prepareDirectCompensation(failed); err == nil {
		t.Fatal("same failed change caused a second restoration")
	}
	if c.ledger.Snapshot().LastAllocatedGeneration != 4 {
		t.Fatal("duplicate restoration allocated resources")
	}
}

func TestDirectCompensationRejectsUnconfirmedAndStaleOwnership(t *testing.T) {
	for _, cause := range []string{"publisher-unconfirmed", "backend-pending", "foreign-owner", "session-canceled"} {
		t.Run(cause, func(t *testing.T) {
			c, failed := directCompensationFixture(t)
			switch cause {
			case "publisher-unconfirmed":
				delete(c.observer.completions, 2)
			case "backend-pending":
				c.pending = failed.attempt
			case "foreign-owner":
				failed.owner = &directDeliveryCoordinator{}
			case "session-canceled":
				ctx, cancel := context.WithCancel(c.ctx)
				cancel()
				c.ctx = ctx
			}
			if _, err := c.prepareDirectCompensation(failed); err == nil {
				t.Fatal("unsafe restoration accepted")
			}
			if c.ledger.Snapshot().LastAllocatedGeneration != 3 {
				t.Fatal("rejected restoration allocated resources")
			}
		})
	}
}

func TestDirectCompensationCannotRecursivelyRestoreOrRetryFailedStartup(t *testing.T) {
	t.Run("recursive", func(t *testing.T) {
		c, failed := directCompensationFixture(t)
		recovery, err := c.prepareDirectCompensation(failed)
		if err != nil {
			t.Fatal(err)
		}
		d := &directCandidateDelivery{owner: c, attempt: recovery}
		if err := d.Abort(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.prepareDirectCompensation(d); err == nil {
			t.Fatal("failed restoration started another restoration chain")
		}
		if c.ledger.Snapshot().LastAllocatedGeneration != 4 {
			t.Fatal("recursive restoration allocated a generation")
		}
	})
	t.Run("backend-start-failed", func(t *testing.T) {
		c, failed := directCompensationFixture(t)
		activeRuntime := c.gate.backendRouter.snapshot().runtime
		activeOwner := c.manager.directBackendReservations[activeRuntime]
		activeRuntime.exe = filepath.Join(t.TempDir(), "missing-mediamtx.exe")
		if d, err := c.PrepareCompensationDelivery(failed, os.Args[0]); err == nil || d != nil {
			t.Fatal("missing backend issued restoration capability")
		}
		if c.pending != nil || len(c.manager.directBackendReservations) != 1 || c.manager.directBackendReservations[activeRuntime] != activeOwner {
			t.Fatal("startup failure leaked candidate ownership or altered the active backend")
		}
		if d, err := c.PrepareCompensationDelivery(failed, os.Args[0]); err == nil || d != nil {
			t.Fatal("failed restoration retried startup")
		}
		if c.ledger.Snapshot().LastAllocatedGeneration != 4 || c.ledger.Snapshot().ActiveGeneration != 2 {
			t.Fatal("failed restoration reused or promoted a generation")
		}
	})
}

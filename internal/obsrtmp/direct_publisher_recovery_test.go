package obsrtmp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/airplaycontract"
)

func TestManagedPublisherRecoveryUsesFreshGenerationAndCannotLoop(t *testing.T) {
	// Backend startup fails before probing; resolve a known fixture executable
	// so this ownership test does not depend on a prior FFmpeg installation.
	t.Setenv("IMAGEPAD_FFPROBE", os.Args[0])
	c, _ := directCompensationFixture(t)
	old := c.active.ArtifactPaths
	c.gate.backendRouter.snapshot().runtime.exe = filepath.Join(t.TempDir(), "missing-mediamtx.exe")
	executor := directDeliveryExecutorFixture{recover: func(context.Context, airplaycontract.CandidateDelivery) error {
		t.Error("missing backend started a publisher")
		return nil
	}}
	recoveryErr := c.RecoverPublisher(t.Context(), old, executor)
	if recoveryErr == nil {
		t.Fatal("missing backend recovered")
	}
	if snapshot := c.ledger.Snapshot(); snapshot.LastAllocatedGeneration != 4 || snapshot.ActiveGeneration != 2 || snapshot.PreparedGeneration != 0 {
		t.Fatalf("recovery did not burn one fresh owned generation: %+v; error=%v", snapshot, recoveryErr)
	}
	descriptor := c.observer.deliveryGenerations[4]
	if descriptor.Output.Height != 360 || descriptor.Profile.Mode != "rtsp-ultra" || descriptor.ArtifactPaths == old {
		t.Fatal("recovery lost last committed output or reused artifacts")
	}
	if err := c.RecoverPublisher(t.Context(), old, executor); err == nil {
		t.Fatal("same crashed publisher recovered twice")
	}
	if c.ledger.Snapshot().LastAllocatedGeneration != 4 || c.pending != nil {
		t.Fatal("duplicate crash created another generation")
	}
}

func TestManagedPublisherRecoveryRejectsPriorityAndUnconfirmedExit(t *testing.T) {
	for _, cause := range []string{"planned-pending", "old-running", "wrong-artifacts", "canceled"} {
		t.Run(cause, func(t *testing.T) {
			c, failed := directCompensationFixture(t)
			old := c.active.ArtifactPaths
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch cause {
			case "planned-pending":
				c.pending = failed.attempt
			case "old-running":
				delete(c.observer.completions, 2)
			case "wrong-artifacts":
				old.Recording = "different.mp4"
			case "canceled":
				cancel()
			}
			executor := directDeliveryExecutorFixture{recover: func(context.Context, airplaycontract.CandidateDelivery) error {
				t.Error("rejected recovery reached executor")
				return nil
			}}
			if err := c.RecoverPublisher(ctx, old, executor); err == nil {
				t.Fatal("ineligible recovery accepted")
			}
			if c.ledger.Snapshot().LastAllocatedGeneration != 3 {
				t.Fatal("ineligible recovery allocated a generation")
			}
		})
	}
}

package airplaycontract

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
)

func testDeliveryProfile() DeliveryProfile {
	return DeliveryProfile{
		Mode:               "rtsp-ultra",
		Transport:          "rtsp",
		HLSVariant:         "lowLatency",
		HLSSegmentCount:    7,
		HLSSegmentDuration: "1s",
	}
}

func testDeliveryOutput(width, height int) DeliveryOutput {
	return DeliveryOutput{
		Width:            width,
		Height:           height,
		SourceFPS:        60,
		OutputFPS:        60,
		VideoBitrateKbps: 4500,
		MaxRateKbps:      5200,
		BufferSizeKbps:   9000,
		AudioBitrateBps:  160000,
		GOPFrames:        60,
	}
}

func allocateTestDelivery(t *testing.T, ledger *DeliveryLedger, expected uint64, requestID string, width, height int) DeliveryGeneration {
	t.Helper()
	descriptor, err := ledger.Allocate("session-01", 1, expected, requestID, testDeliveryProfile(), testDeliveryOutput(width, height))
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	return descriptor
}

func TestDeliveryGenerationMonotonicAcrossReasons(t *testing.T) {
	root := t.TempDir()
	ledger, err := NewDeliveryLedger(root, "session-01", 1)
	if err != nil {
		t.Fatal(err)
	}

	first := allocateTestDelivery(t, ledger, 0, "initial", 1280, 720)
	if err := ledger.Prepare(first); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Commit(0, first); err != nil {
		t.Fatal(err)
	}

	planned := allocateTestDelivery(t, ledger, 1, "planned", 640, 360)
	if err := ledger.Abort(planned); err != nil {
		t.Fatal(err)
	}
	crash := allocateTestDelivery(t, ledger, 1, "crash", 1280, 720)
	if err := ledger.Prepare(crash); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Commit(1, crash); err != nil {
		t.Fatal(err)
	}
	rollback := allocateTestDelivery(t, ledger, 3, "rollback", 1280, 720)

	if got, want := []uint64{first.Generation, planned.Generation, crash.Generation, rollback.Generation}, []uint64{1, 2, 3, 4}; !equalGenerations(got, want) {
		t.Fatalf("generations = %v, want %v", got, want)
	}
	paths := map[string]bool{}
	for _, descriptor := range []DeliveryGeneration{first, planned, crash, rollback} {
		for _, path := range []string{descriptor.ArtifactPaths.Recording, descriptor.ArtifactPaths.Ready, descriptor.ArtifactPaths.MediaReady, descriptor.ArtifactPaths.EventLog, descriptor.ArtifactPaths.ProcessLog, descriptor.ArtifactPaths.StopRequest} {
			if paths[path] {
				t.Fatalf("artifact path reused: %s", path)
			}
			paths[path] = true
			if filepath.Clean(path) != path {
				t.Fatalf("artifact path is not clean: %s", path)
			}
		}
	}
}

func TestDeliveryGenerationFailedPrepareBurnsAllocation(t *testing.T) {
	ledger, err := NewDeliveryLedger(t.TempDir(), "session-01", 1)
	if err != nil {
		t.Fatal(err)
	}
	first := allocateTestDelivery(t, ledger, 0, "first", 1280, 720)
	changed := first
	changed.Output.Height = 360
	if err := ledger.Prepare(changed); !errors.Is(err, ErrDeliveryGenerationMismatch) {
		t.Fatalf("Prepare changed descriptor error = %v", err)
	}
	if err := ledger.Abort(first); err != nil {
		t.Fatal(err)
	}
	second := allocateTestDelivery(t, ledger, 0, "second", 640, 360)
	if second.Generation != 2 {
		t.Fatalf("generation = %d, want 2", second.Generation)
	}
	if second.ArtifactPaths == first.ArtifactPaths {
		t.Fatal("aborted generation artifacts were reused")
	}
}

func TestDeliveryGenerationImmutableCopies(t *testing.T) {
	ledger, err := NewDeliveryLedger(t.TempDir(), "session-01", 1)
	if err != nil {
		t.Fatal(err)
	}
	original := allocateTestDelivery(t, ledger, 0, "immutable", 1280, 720)
	mutated := original
	mutated.Profile.Mode = "hls"
	mutated.ArtifactPaths.Recording = "changed.mp4"
	mutated.ExpectedRecordingHeight = 360

	stored, ok := ledger.Lookup(original.Generation)
	if !ok || stored != original {
		t.Fatalf("stored descriptor changed: %#v", stored)
	}
	if err := ledger.Prepare(mutated); !errors.Is(err, ErrDeliveryGenerationMismatch) {
		t.Fatalf("Prepare mutated descriptor error = %v", err)
	}
	if err := ledger.Prepare(original); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Commit(0, mutated); !errors.Is(err, ErrDeliveryGenerationMismatch) {
		t.Fatalf("Commit mutated descriptor error = %v", err)
	}
}

func TestDeliveryGenerationCommitRejectsStaleIdentity(t *testing.T) {
	ledger, err := NewDeliveryLedger(t.TempDir(), "session-01", 1)
	if err != nil {
		t.Fatal(err)
	}
	first := allocateTestDelivery(t, ledger, 0, "first", 1280, 720)
	if err := ledger.Commit(0, first); !errors.Is(err, ErrDeliveryGenerationNotPrepared) {
		t.Fatalf("unprepared commit error = %v", err)
	}
	if err := ledger.Prepare(first); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Commit(1, first); !errors.Is(err, ErrDeliveryGenerationStale) {
		t.Fatalf("stale commit error = %v", err)
	}
	if err := ledger.Commit(0, first); err != nil {
		t.Fatal(err)
	}
	second := allocateTestDelivery(t, ledger, 1, "second", 640, 360)
	if err := ledger.Prepare(second); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Commit(0, second); !errors.Is(err, ErrDeliveryGenerationStale) {
		t.Fatalf("old expected generation error = %v", err)
	}
	if err := ledger.Commit(1, second); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryGenerationTerminalIsIrreversible(t *testing.T) {
	ledger, err := NewDeliveryLedger(t.TempDir(), "session-01", 1)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := allocateTestDelivery(t, ledger, 0, "candidate", 1280, 720)
	if err := ledger.Prepare(descriptor); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Terminate("session-01", 1); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Terminate("session-01", 1); err != nil {
		t.Fatalf("idempotent terminate: %v", err)
	}
	if err := ledger.Commit(0, descriptor); !errors.Is(err, ErrDeliveryLedgerTerminal) {
		t.Fatalf("commit after terminal error = %v", err)
	}
	if _, err := ledger.Allocate("session-01", 1, 0, "late", testDeliveryProfile(), testDeliveryOutput(640, 360)); !errors.Is(err, ErrDeliveryLedgerTerminal) {
		t.Fatalf("allocate after terminal error = %v", err)
	}
	snapshot := ledger.Snapshot()
	if snapshot.TerminalEpoch != 1 || snapshot.PreparedGeneration != 0 || snapshot.ActiveGeneration != 0 {
		t.Fatalf("terminal snapshot = %#v", snapshot)
	}
}

func TestDeliveryGenerationAllocationOverflow(t *testing.T) {
	ledger, err := NewDeliveryLedger(t.TempDir(), "session-01", 1)
	if err != nil {
		t.Fatal(err)
	}
	ledger.lastAllocatedGeneration = math.MaxUint64
	if _, err := ledger.Allocate("session-01", 1, 0, "overflow", testDeliveryProfile(), testDeliveryOutput(1280, 720)); !errors.Is(err, ErrDeliveryGenerationOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
	if got := ledger.Snapshot(); got.LastAllocatedGeneration != math.MaxUint64 || got.PreparedGeneration != 0 || got.ActiveGeneration != 0 {
		t.Fatalf("overflow mutated ledger: %#v", got)
	}
}

func equalGenerations(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

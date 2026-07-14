package video

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMappedFrameRingRoundTrip(t *testing.T) {
	r, err := NewMappedFrameRing(filepath.Join(t.TempDir(), "frames.map"), 2, 4096); if err != nil { t.Fatal(err) }
	f := testGPUFrame(); f.Sequence = 42
	if err := r.Submit(context.Background(), f); err != nil { t.Fatal(err) }
	got, err := r.Receive(context.Background()); if err != nil { t.Fatal(err) }
	if got.Sequence != 42 { t.Fatalf("sequence %d", got.Sequence) }; r.Close(nil)
}

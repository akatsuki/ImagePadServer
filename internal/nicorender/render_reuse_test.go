package nicorender

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

func TestRenderBinaryReuseMatchesFullAcrossTransitions(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_RENDER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_RENDER_TEST=1 for the real browser test")
	}
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{VideoID: "sm9", Threads: []niconico.Thread{{ID: "t", Fork: "main", Comments: []niconico.Comment{
		{ID: "a", VposMs: 1000, Body: "流れるコメント", Commands: []string{"red"}, PostedAt: "2026-01-01T00:00:00Z"},
		{ID: "b", VposMs: 2000, Body: "上固定\n二行目", Commands: []string{"ue", "blue"}, PostedAt: "2026-01-01T00:00:00Z"},
		{ID: "c", VposMs: 2500, Body: "下固定", Commands: []string{"shita", "green"}, PostedAt: "2026-01-01T00:00:00Z"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	// Odd height also checks the in-place WebGL row flip's untouched middle row.
	options := RenderOptions{Width: 321, Height: 181, DurationMs: 8000, FPSNum: 10, FPSDen: 1, Transport: "binary"}
	full, reused := &collectingSink{}, &collectingSink{}
	if _, err := Render(ctx, snapshot, options, full); err != nil {
		t.Fatal(err)
	}
	options.ReuseUnchanged = true
	options.BatchFrames = 30
	options.SparseFrames = true
	if _, err := Render(ctx, snapshot, options, reused); err != nil {
		t.Fatal(err)
	}
	if len(full.frames) != 80 || len(reused.frames) != 80 {
		t.Fatalf("frame counts full=%d reuse=%d", len(full.frames), len(reused.frames))
	}
	visible := false
	for frame, want := range full.frames {
		if !bytes.Equal(want, reused.frames[frame]) {
			t.Fatalf("different pixels at frame %d (time %dms)", frame, frame*100)
		}
		for i := 3; i < len(want); i += 4 {
			if want[i] != 0 {
				visible = true
				break
			}
		}
	}
	if !visible {
		t.Fatal("all frames were transparent")
	}
}

//go:build nico_timeline_embedded

package nicorender

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEmbeddedTimelineCompositorSupportsNCT2(t *testing.T) {
	if !EmbeddedTimelineCompositorSupportsNCT2() {
		t.Fatal("embedded timeline compositor is not a valid NCT2 helper for this target")
	}
}

func TestTimelineRuntimeUsesEmbeddedHelper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	path, cleanup, err := PrepareTimelineCompositor(ctx, "", TimelineRuntimeOptions{Backend: "auto", ReadbackSlots: 3})
	if err != nil {
		t.Fatalf("PrepareTimelineCompositor with the embedded payload: %v", err)
	}
	if cleanup == nil {
		t.Fatal("embedded helper did not return job-owned cleanup")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("embedded helper path is not a regular file: path=%q err=%v", path, err)
	}
	dir := filepath.Dir(path)
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("embedded helper directory remains after cleanup: %q err=%v", dir, err)
	}
}

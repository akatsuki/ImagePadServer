//go:build !nico_timeline_embedded

package nicorender

import (
	"context"
	"errors"
	"testing"
)

func TestTimelineRuntimeWithoutEmbeddedPayloadIsUnavailable(t *testing.T) {
	if EmbeddedTimelineCompositorSupportsNCT2() {
		t.Fatal("build without embedded payload reported NCT2 support")
	}
	_, cleanup, err := PrepareTimelineCompositor(context.Background(), "", TimelineRuntimeOptions{})
	if cleanup != nil {
		cleanup()
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable without the nico_timeline_embedded build tag, got %v", err)
	}
}

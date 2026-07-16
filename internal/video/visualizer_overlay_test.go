package video

import (
	"context"
	"testing"
)

func TestNewScreenTextOverlayMetadataContract(t *testing.T) {
	payload := make([]byte, 512)
	o := NewScreenTextOverlayMetadata(1, 2, 256, payload, "ffmpeg-test")
	if err := o.Validate(); err != nil {
		t.Fatalf("screen overlay metadata rejected: %v", err)
	}
	if o.Kind != "screen_rgba" || o.AlphaMode != "premultiplied" || o.PixelOrigin != "top_left" {
		t.Fatalf("missing screen semantics: %+v", o)
	}
}

func TestRenderASSOverlayRGBARejectsInvalidArguments(t *testing.T) {
	if _, _, err := RenderASSOverlayRGBA(context.Background(), "", "x.ass", "fonts", 1, 1); err == nil {
		t.Fatal("expected invalid ffmpeg argument rejection")
	}
}

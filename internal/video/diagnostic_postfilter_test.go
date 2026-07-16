package video

import (
	"context"
	"testing"
)

func TestDiagnosticRenderPostFilterRGBARejectsInvalidCaptureBounds(t *testing.T) {
	input := AudioRenderInput{}
	for _, n := range []int{0, 151} {
		if _, err := DiagnosticRenderPostFilterRGBA(context.Background(), "ffmpeg", input, "test", QualityPreset{Height: 360}, n, func(int, []byte) error { return nil }); err == nil {
			t.Fatalf("maxFrames=%d: expected validation error", n)
		}
	}
}

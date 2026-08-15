package video

import (
	"context"
	"errors"
	"testing"
)

func TestRenderRadioTrackUsesCPUWhenGPUCompositorIsUnset(t *testing.T) {
	t.Setenv("IMAGEPAD_PLAYLIST_COMPOSITORD", "")
	_, err := RenderRadioTrack(context.Background(), t.TempDir(), "ffmpeg", AudioRenderInput{}, "missing-sidecar", QualityPreset{}, nil)
	if errors.Is(err, ErrGPURequired) {
		t.Fatalf("RenderRadioTrack still requires GPU when sidecar is unset: %v", err)
	}
}

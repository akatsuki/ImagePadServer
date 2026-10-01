//go:build nico_timeline_embedded && windows && amd64

package server

import "testing"

func TestNicoTimelineWorkerOptionsDefaultToEmbeddedRenderer(t *testing.T) {
	for _, key := range []string{
		"IMAGEPAD_NICO_RENDERER", "IMAGEPAD_NICO_TIMELINE_ENABLED", "IMAGEPAD_NICO_TIMELINE_COMPOSITOR",
		"IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS", "IMAGEPAD_NICO_TIMELINE_GPU_BACKEND",
	} {
		t.Setenv(key, "")
	}
	options, err := nicoTimelineWorkerRequestOptions()
	if err != nil {
		t.Fatal(err)
	}
	if !options.TimelineEnabled {
		t.Fatalf("embedded compositor build did not enable timeline by default: %+v", options)
	}
}

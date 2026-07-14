package video

import "testing"

func TestValidateGPURenderCapability(t *testing.T) {
	if err := ValidateGPURenderCapability(false, false); err != nil {
		t.Fatalf("image-only startup must remain available: %v", err)
	}
	if err := ValidateGPURenderCapability(true, false); err != ErrGPURequired {
		t.Fatalf("want gpu_required, got %v", err)
	}
	if err := ValidateGPURenderCapability(true, true); err != nil {
		t.Fatal(err)
	}
}

func TestRequireGPUFrameUsesStableError(t *testing.T) {
	if err := RequireGPUFrame(GpuFrame{}); err != ErrGPURendererUnavailable {
		t.Fatalf("want gpu_renderer_unavailable, got %v", err)
	}
}

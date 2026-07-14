package video

import "errors"

var (
	ErrGPURequired            = errors.New("gpu_required")
	ErrGPURendererUnavailable = errors.New("gpu_renderer_unavailable")
)

// ValidateGPURenderCapability gates only rendering jobs. Image-only server
// startup may proceed when renderRequested is false.
func ValidateGPURenderCapability(renderRequested, hardwareAdapter bool) error {
	if !renderRequested {
		return nil
	}
	if !hardwareAdapter {
		return ErrGPURequired
	}
	return nil
}

func RequireGPUFrame(frame GpuFrame) error {
	if err := frame.Validate(); err != nil {
		return ErrGPURendererUnavailable
	}
	return nil
}

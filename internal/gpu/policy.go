// Package gpu contains the small, process-wide rendering policy shared by
// HTTP handlers and the compositor bridge. Rendering is GPU-only; the
// environment override exists for deterministic diagnostics and CI tests.
package gpu

import (
	"errors"
	"os"
	"strings"
)

var (
	ErrRequired            = errors.New("gpu_required")
	ErrRendererUnavailable = errors.New("gpu_renderer_unavailable")
)

// RenderingAvailable reports whether a GPU renderer may be started. The
// compositor sidecar is the authoritative hardware probe once running; this
// early gate only handles an explicit unavailable result from that probe (or
// CI/diagnostic override) without ever selecting a CPU renderer.
func RenderingAvailable() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_RENDERER")), "unavailable")
}

func RequiredError() error    { return ErrRequired }
func UnavailableError() error { return ErrRendererUnavailable }

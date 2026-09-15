package airplay

import (
	"os"
	"runtime"
	"strings"
)

// PipelineMode identifies the AirPlay media egress selected for a session.
// The source-clock mode is intentionally parsed separately from the existing
// direct bridge selector so its process and lifecycle contract stays isolated.
type PipelineMode string

const (
	PipelineLegacy          PipelineMode = "legacy"
	PipelineGStreamerDirect PipelineMode = "gstreamer-direct"
	PipelineSourceClock     PipelineMode = "source-clock"
)

// CurrentPipelineMode reads the explicit AirPlay pipeline feature gate.
// Unknown values retain the legacy path rather than enabling a partial mode.
// A packaged executable can also be launched directly from Explorer, without
// the distribution command file setting IMAGEPAD_AIRPLAY_PIPELINE. In that
// case source-clock is the package-safe default even while its pinned runtime
// is being prepared. Start then fails fast with a preparation error instead of
// silently switching to the legacy decoder path.
func CurrentPipelineMode() PipelineMode {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(envAirPlayPipeline)))
	explicitlySet := value != ""
	switch value {
	case "source-clock":
		return PipelineSourceClock
	case "1", "true", "yes", "on", "gstreamer", "gst":
		return PipelineGStreamerDirect
	default:
		if !explicitlySet && runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
			return PipelineSourceClock
		}
		return PipelineLegacy
	}
}

func SourceClockPipelineEnabled() bool {
	return CurrentPipelineMode() == PipelineSourceClock
}

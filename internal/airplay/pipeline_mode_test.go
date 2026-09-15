package airplay

import (
	"runtime"
	"testing"
)

func TestCurrentPipelineModeSeparatesSourceClockFromExistingDirect(t *testing.T) {
	t.Setenv(envAirPlayPipeline, "source-clock")
	if got := CurrentPipelineMode(); got != PipelineSourceClock {
		t.Fatalf("mode=%q want=%q", got, PipelineSourceClock)
	}
	if !SourceClockPipelineEnabled() {
		t.Fatal("source-clock mode was not enabled")
	}
	if !DirectPipelineEnabled() {
		t.Fatal("source-clock mode must retain direct MediaMTX allocation")
	}
}

func TestExistingGStreamerAliasesRemainDirect(t *testing.T) {
	for _, value := range []string{"1", "true", "gstreamer", "gst"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(envAirPlayPipeline, value)
			if got := CurrentPipelineMode(); got != PipelineGStreamerDirect {
				t.Fatalf("mode=%q", got)
			}
			if SourceClockPipelineEnabled() {
				t.Fatal("existing GStreamer alias unexpectedly enabled source-clock")
			}
		})
	}
}

func TestUnknownPipelineModeFallsBackToLegacy(t *testing.T) {
	for _, value := range []string{"ffmpeg", "auto", "0", "false", "source_clock", "source-clock-v2"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(envAirPlayPipeline, value)
			if got := CurrentPipelineMode(); got != PipelineLegacy {
				t.Fatalf("mode=%q want=%q", got, PipelineLegacy)
			}
		})
	}
}

func TestEmptyPipelineModeUsesSourceClockDefaultOnSupportedWindows(t *testing.T) {
	t.Setenv(envAirPlayPipeline, "")
	want := PipelineLegacy
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		want = PipelineSourceClock
	}
	if got := CurrentPipelineMode(); got != want {
		t.Fatalf("empty mode=%q want=%q", got, want)
	}
}

package video

import (
	"strings"
	"testing"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

type nicoProduction30Profile struct {
	Render nicorender.RenderOptions
	Encode NicoEncodeOptions
}

func production30Profile(durationMs int64) nicoProduction30Profile {
	return nicoProduction30Profile{
		Render: nicorender.RenderOptions{
			Width: 1920, Height: 1080, DurationMs: durationMs, FPSNum: 30, FPSDen: 1,
			Transport: "binary", ReuseUnchanged: true, BatchFrames: 30, SparseFrames: true,
		},
		Encode: NicoEncodeOptions{
			Width: 1920, Height: 1080, DurationMs: durationMs, FPSNum: 30, FPSDen: 1,
			CRF: 26, AudioBitrate: "160k",
		},
	}
}

func TestNicoProduction30BudgetProfileContract(t *testing.T) {
	profile := production30Profile(6000)
	if profile.Render.Width != profile.Encode.Width || profile.Render.Height != profile.Encode.Height {
		t.Fatalf("render/encode geometry diverged: %+v", profile)
	}
	if profile.Render.DurationMs != profile.Encode.DurationMs || profile.Render.FPSNum != profile.Encode.FPSNum || profile.Render.FPSDen != profile.Encode.FPSDen {
		t.Fatalf("render/encode timeline diverged: %+v", profile)
	}
	clock, err := niconico.NewFrameClock(profile.Encode.FPSNum, profile.Encode.FPSDen)
	if err != nil {
		t.Fatal(err)
	}
	if got := clock.FrameCountForDurationMs(profile.Encode.DurationMs); got != 180 {
		t.Fatalf("frame count=%d, want 180", got)
	}
	args := strings.Join(nicoEncodeArgs("source.mp4", "output.mp4", profile.Encode), " ")
	for _, want := range []string{"-crf 26", "-b:a 160k", "-preset veryfast", "-t 6.000", "-framerate 30/1", "fps=30/1"} {
		if !strings.Contains(args, want) {
			t.Fatalf("production30 argv missing %q: %s", want, args)
		}
	}
}

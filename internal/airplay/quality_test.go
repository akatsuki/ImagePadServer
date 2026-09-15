package airplay

import (
	"testing"

	"imagepadserver/internal/settings"
)

func TestAirPlayQualityNormalizerMatchesSettingsCanonicalSource(t *testing.T) {
	for _, input := range []string{"", " AUTO ", "360", " 720 ", "1080", "invalid"} {
		if got, want := NormalizeAirPlayQualityMode(input), settings.NormalizeAirPlayQualityMode(input); got != want {
			t.Fatalf("NormalizeAirPlayQualityMode(%q) = %q, settings source = %q", input, got, want)
		}
	}
}

func TestNormalizeAirPlayQualityModeCanonicalValues(t *testing.T) {
	for input, want := range map[string]string{
		"auto":     "auto",
		"AUTO":     "auto",
		"  Auto  ": "auto",
		"360":      "360",
		" 720 ":    "720",
		"1080":     "1080",
	} {
		if got := NormalizeAirPlayQualityMode(input); got != want {
			t.Fatalf("NormalizeAirPlayQualityMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeAirPlayQualityModeInvalidValuesDefaultToAuto(t *testing.T) {
	for _, input := range []string{"", "   ", "1440", "1080p", "quality", "-1"} {
		if got := NormalizeAirPlayQualityMode(input); got != "auto" {
			t.Fatalf("NormalizeAirPlayQualityMode(%q) = %q, want auto", input, got)
		}
	}
}

func TestResolveAirPlayQualityAutoThresholds(t *testing.T) {
	tests := []struct {
		name                     string
		downloadMbps, uploadMbps int
		wantEffective            string
		wantHeight               int
	}{
		{name: "below five", downloadMbps: 4, wantEffective: "360", wantHeight: 360},
		{name: "five", downloadMbps: 5, wantEffective: "720", wantHeight: 720},
		{name: "twelve", downloadMbps: 12, wantEffective: "1080", wantHeight: 1080},
		{name: "positive upload overrides download", downloadMbps: 12, uploadMbps: 4, wantEffective: "360", wantHeight: 360},
		{name: "nonpositive upload falls back to download", downloadMbps: 5, uploadMbps: 0, wantEffective: "720", wantHeight: 720},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveAirPlayQuality(" AUTO ", tc.downloadMbps, tc.uploadMbps)
			if got.Effective != tc.wantEffective || got.Height != tc.wantHeight {
				t.Fatalf("ResolveAirPlayQuality() = effective %q, height %d; want %q, %d", got.Effective, got.Height, tc.wantEffective, tc.wantHeight)
			}
			if got.Mode != "auto" {
				t.Fatalf("ResolveAirPlayQuality() mode = %q, want auto", got.Mode)
			}
		})
	}
}

func TestResolveAirPlayQualityManualValues(t *testing.T) {
	for _, tc := range []struct {
		input  string
		mode   string
		height int
	}{
		{input: "360", mode: "360", height: 360},
		{input: " 720 ", mode: "720", height: 720},
		{input: "1080", mode: "1080", height: 1080},
	} {
		got := ResolveAirPlayQuality(tc.input, 1, 100)
		if got.Mode != tc.mode || got.Effective != tc.mode || got.Height != tc.height {
			t.Fatalf("ResolveAirPlayQuality(%q) = mode %q, effective %q, height %d; want %q, %q, %d", tc.input, got.Mode, got.Effective, got.Height, tc.mode, tc.mode, tc.height)
		}
	}
}

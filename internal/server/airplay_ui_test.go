package server

import (
	"strings"
	"testing"
)

func TestUIContainsAirPlayControlsInsideLiveOBSPanel(t *testing.T) {
	html := indexHTML
	panelStart := strings.Index(html, `id="obsUploadPanel"`)
	panelEnd := strings.Index(html[panelStart:], `<div class="flow-secondary">`)
	if panelStart < 0 || panelEnd < 0 {
		t.Fatal("Live OBS panel is missing")
	}
	panel := html[panelStart : panelStart+panelEnd]
	for _, want := range []string{
		`id="airplayCard"`,
		`id="airplayStartButton"`,
		`id="airplayEndButton"`,
		`id="airplayStatusText"`,
		`class="airplay-graphic"`,
	} {
		if !strings.Contains(panel, want) {
			t.Fatalf("AirPlay control is not inside the Live OBS panel: %q", want)
		}
	}
	for _, want := range []string{
		`/api/airplay/start`,
		`/api/airplay/end`,
		`applyAirPlay(data)`,
		`.airplay-card`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("AirPlay UI wiring is missing: %q", want)
		}
	}
}

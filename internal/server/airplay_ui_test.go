package server

import (
	"strings"
	"testing"
)

func TestUIContainsExclusiveOBSPlaylistAirPlayTabs(t *testing.T) {
	html := indexHTML
	obsTab := strings.Index(html, `id="obsModeButton"`)
	playlistTab := strings.Index(html, `id="playlistModeButton"`)
	airplayTab := strings.Index(html, `id="airplayModeButton"`)
	if obsTab < 0 || playlistTab < 0 || airplayTab < 0 || !(obsTab < playlistTab && playlistTab < airplayTab) {
		t.Fatal("Live input tabs must be ordered OBS, Playlist, AirPlay")
	}

	obsPanelStart := strings.Index(html, `id="obsUploadPanel"`)
	airplayPanelStart := strings.Index(html, `id="airplayUploadPanel"`)
	flowSecondaryStart := strings.Index(html, `<div class="flow-secondary">`)
	if obsPanelStart < 0 || airplayPanelStart < 0 || flowSecondaryStart < 0 || !(obsPanelStart < airplayPanelStart && airplayPanelStart < flowSecondaryStart) {
		t.Fatal("separate OBS and AirPlay tab panels are missing")
	}
	obsPanel := html[obsPanelStart:airplayPanelStart]
	airplayPanel := html[airplayPanelStart:flowSecondaryStart]
	if strings.Contains(obsPanel, `id="airplayCard"`) {
		t.Fatal("AirPlay controls must not be nested in the OBS panel")
	}
	for _, want := range []string{
		`role="tabpanel" aria-labelledby="airplayModeButton"`,
		`id="airplayCard"`,
		`id="airplayStartButton"`,
		`id="airplayEndButton"`,
		`id="airplayStatusText"`,
		`class="airplay-graphic"`,
	} {
		if !strings.Contains(airplayPanel, want) {
			t.Fatalf("AirPlay panel control is missing: %q", want)
		}
	}
	for _, want := range []string{
		`setUploadMode('airplay')`,
		`airplayUploadPanel.hidden = !airplayMode`,
		`airplayModeButton.setAttribute('aria-selected', String(airplayMode))`,
		`/api/airplay/start`,
		`/api/airplay/end`,
		`data.running && data.receiverRunning && data.bridgeRunning`,
		`iPhoneの画面を受信中です`,
		`.airplay-card`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("AirPlay UI wiring is missing: %q", want)
		}
	}
}

func TestUILiveInputTabsSynchronizeRovingFocusAndKeyboardNavigation(t *testing.T) {
	expected := []string{
		`aria-controls="obsUploadPanel" tabindex="-1" hidden`,
		`aria-controls="musicPlaylistPanel" tabindex="-1" hidden>Playlist</button>`,
		`aria-controls="airplayUploadPanel" tabindex="-1" hidden`,
		`button.tabIndex = visible && selected ? 0 : -1;`,
		`button.tabIndex = playlistActive && selected ? 0 : -1;`,
		`bindInputTabKeyboardNavigation`,
		`event.key === 'ArrowLeft'`,
		`event.key === 'ArrowRight'`,
		`event.key === 'Home'`,
		`event.key === 'End'`,
		`target.focus();`,
		`target.click();`,
	}
	for _, snippet := range expected {
		if !strings.Contains(indexHTML, snippet) {
			t.Errorf(`missing roving-focus tab behavior: %s`, snippet)
		}
	}
}

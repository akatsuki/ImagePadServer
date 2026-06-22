package server

import (
	"strings"
	"testing"
)

func getIndexHTML(t *testing.T) string {
	t.Helper()
	return indexHTML
}

func TestUIContainsToolInstallOverlay(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="toolInstallOverlay"`,
		`id="toolInstallFill"`,
		`updateToolInstall(data.toolInstall)`,
		`function updateToolInstall(`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("served HTML missing %q", want)
		}
	}
}

func TestUIRendersIngestPhase(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{"ingestPhase", "ダウンロード中", "解析中"} {
		if !strings.Contains(html, want) {
			t.Errorf("UI page missing %q", want)
		}
	}
}

func TestVideoPlayerEnabledMediaCopy(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{"画像/音声/動画", "メディアアップロード", "画像、RAW、音声、動画"} {
		if !strings.Contains(html, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestVideoPlayerEnabledModeRemoveAccept(t *testing.T) {
	html := getIndexHTML(t)
	if !strings.Contains(html, `data.enabled ? '' : imageAccept`) {
		t.Fatal("enabled mode should use empty string for accept (allow all media)")
	}
}

func TestVideoPlayerDisabledModeRestoresAccept(t *testing.T) {
	html := getIndexHTML(t)
	if !strings.Contains(html, `imageAccept = 'image/png,image/jpeg,image/gif,image/webp,image/bmp`) {
		t.Fatal("imageAccept should contain image/RAW types for disabled mode")
	}
	if !strings.Contains(html, `data.enabled ? '' : imageAccept`) {
		t.Fatal("disabled mode should restore imageAccept via ternary")
	}
}

func TestMusicModeUIIsNestedUnderVideoPlayerMode(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="musicModeRow"`,
		`id="musicModeToggle"`,
		`ミュージックモード`,
		`fetch('/api/music-mode'`,
		`musicModeRow.hidden = !data.enabled`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("music mode UI is missing %q", want)
		}
	}
}

func TestBrowserCookieSourceIsNotExposed(t *testing.T) {
	html := getIndexHTML(t)
	for _, forbidden := range []string{
		`navigator.brave`,
		`/api/browser-cookie-source`,
		`--cookies-from-browser`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("frozen browser cookie integration remains in UI: %q", forbidden)
		}
	}
}

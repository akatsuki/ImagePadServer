package server

import (
	"strings"
	"testing"
)

func getIndexHTML(t *testing.T) string {
	t.Helper()
	return indexHTML
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

//go:build airplay_runtime_embedded

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAirPlayLicensesAvailableOfflineBeforeRuntimeInstallation(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	s := &Server{}
	for _, name := range []string{"", "SOURCE-OFFER.md", "source-manifest.json", "licenses/uxplay/COPYING"} {
		w := httptest.NewRecorder()
		s.handleAirPlayLicenses(w, httptest.NewRequest(http.MethodGet, "/licenses/airplay?file="+name, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if name == "" && (!strings.Contains(w.Body.String(), "https://github.com/akatsuki/ImagePadServer/releases/download/") || !strings.Contains(w.Body.String(), "ImagePadServer-AirPlay-Sources.zip")) {
			t.Fatal("same-release source link missing")
		}
		if name == "source-manifest.json" && !json.Valid(w.Body.Bytes()) {
			t.Fatal("invalid source inventory")
		}
	}
	w := httptest.NewRecorder()
	s.handleAirPlayLicenses(w, httptest.NewRequest(http.MethodHead, "/licenses/airplay?file=sources/BUILD.md", nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD build instructions: %d %v", w.Code, w.Header())
	}
}

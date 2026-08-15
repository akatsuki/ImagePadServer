package video

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPlaylistGPUResponseParserKeepsTrackEpochAndAssetReceipt(t *testing.T) {
	var response sidecarResponse
	if err := json.Unmarshal([]byte(`{"type":"track_ready","epoch":9,"assets_hash":"abc123"}`), &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "track_ready" || response.Epoch != 9 || response.AssetsHash != "abc123" {
		t.Fatalf("track-ready response lost playlist identity: %#v", response)
	}
}

func TestLegacyPlaylistGPUDiagnosticRoutePreflightsExplicitTimelineContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source := readRouteSource(t, filepath.Join(filepath.Dir(file), "radio_render.go"))
	body := routeBody(sourceBetween(source, "func renderRadioTrackGPUDirectH264", "func directH264MuxArgs"))
	for _, want := range []string{
		"CompilePlaylistGPUTrack",
		"timeline.FrameCount",
		"timeline.Frames[frameIndex].PTSNs",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("legacy playlist GPU diagnostic route missing explicit timeline preflight %q", want)
		}
	}
}

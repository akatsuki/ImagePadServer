package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

// TestRadioEndToEnd exercises the full playlist radio chain with the real
// binaries: tone generation -> unified-input add -> visualizer render ->
// MediaMTX -> ffmpeg codec-copy push -> public LL-HLS proxy.
//
// Gated like the other MediaMTX acceptance tests: requires
// IMAGEPAD_MEDIAMTX_TEST=1 plus IMAGEPAD_MEDIAMTX / IMAGEPAD_FFMPEG /
// IMAGEPAD_FFPROBE pins so nothing is downloaded (see
// bundled-only tool policy).
func TestRadioEndToEnd(t *testing.T) {
	if os.Getenv("IMAGEPAD_MEDIAMTX_TEST") == "" {
		t.Skip("set IMAGEPAD_MEDIAMTX_TEST=1 (with IMAGEPAD_MEDIAMTX/FFMPEG/FFPROBE pinned) to run the radio E2E test")
	}
	for _, env := range []string{"IMAGEPAD_MEDIAMTX", "IMAGEPAD_FFMPEG", "IMAGEPAD_FFPROBE"} {
		if os.Getenv(env) == "" {
			t.Skipf("%s must be pinned for the radio E2E test", env)
		}
	}

	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	t.Cleanup(func() { srv.radio.Stop(10 * time.Second) })
	enableMusicMode(t)

	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	tone := filepath.Join(t.TempDir(), "radio-e2e-tone.m4a")
	gen := exec.Command(ffmpeg, "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=8",
		"-metadata", "title=Radio E2E Tone", "-c:a", "aac", "-b:a", "128k", tone)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("tone generation: %v\n%s", err, out)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/add", strings.NewReader(`{"input":`+jsonString(tone)+`}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body.String())
	}
	// Real visualizer render — allow far longer than waitTrackStatus's 5s.
	renderDeadline := time.Now().Add(3 * time.Minute)
	for {
		tracks := stateTracks(playlistState(t, mux))
		if len(tracks) > 0 {
			if status, _ := tracks[0]["status"].(string); status == "ready" {
				break
			} else if status == "failed" {
				t.Fatalf("render failed: %v", tracks[0]["error"])
			}
		}
		if time.Now().After(renderDeadline) {
			t.Fatalf("timed out waiting for render: %v", playlistState(t, mux))
		}
		time.Sleep(2 * time.Second)
	}

	// Loop keeps the 8s tone broadcasting while the HLS muxer spins up.
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/options", strings.NewReader(`{"loop":true}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("options = %d: %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/play", strings.NewReader(`{}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("play = %d: %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		req := httptest.NewRequest(http.MethodGet, "/radio/index.m3u8", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK && bytes.Contains(rec.Body.Bytes(), []byte("#EXTM3U")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("LL-HLS never became ready: last=%d %s; state=%v", rec.Code, rec.Body.String(), playlistState(t, mux))
		}
		time.Sleep(2 * time.Second)
	}

	st := playlistState(t, mux)
	if st["playing"] != true {
		t.Fatalf("radio must be playing: %v", st)
	}
	if url, _ := st["rtspUrl"].(string); !strings.Contains(url, "/radio_") {
		t.Fatalf("RTSP URL must use the radio_ path prefix: %q", url)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/stop", strings.NewReader(`{}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d: %s", rec.Code, rec.Body.String())
	}
}

func jsonString(s string) string {
	out := strings.ReplaceAll(s, `\`, `\\`)
	out = strings.ReplaceAll(out, `"`, `\"`)
	return `"` + out + `"`
}

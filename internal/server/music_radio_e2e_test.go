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

	fetchHLS := func() (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/radio/index.m3u8", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// 一時停止しても常駐 publisher がフィラーを流し続け、ストリームは切れない。
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/pause", strings.NewReader(`{}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("pause = %d: %s", rec.Code, rec.Body.String())
	}
	time.Sleep(6 * time.Second) // フィラーへの切り替えとセグメント生成を跨ぐ
	if code, body := fetchHLS(); code != http.StatusOK || !strings.Contains(body, "#EXTM3U") {
		t.Fatalf("stream must stay alive while paused: %d %s; state=%v", code, body, playlistState(t, mux))
	}
	if st := playlistState(t, mux); st["paused"] != true || st["playing"] != false {
		t.Fatalf("state must report paused: %v", st)
	}

	// 再開: 中断位置から同じ曲が流れ、playing に戻る。
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/play", strings.NewReader(`{}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body.String())
	}
	resumeDeadline := time.Now().Add(20 * time.Second)
	for {
		st := playlistState(t, mux)
		if st["playing"] == true {
			break
		}
		if time.Now().After(resumeDeadline) {
			t.Fatalf("radio did not resume: %v", st)
		}
		time.Sleep(1 * time.Second)
	}
	// フィラー→曲の切り替えでコーデックパラメータが変わると mediamtx が HLS
	// muxer を作り直すため、直後は一時的に 502/404 になり得る。
	hlsDeadline := time.Now().Add(20 * time.Second)
	for {
		code, body := fetchHLS()
		if code == http.StatusOK && strings.Contains(body, "#EXTM3U") {
			break
		}
		if time.Now().After(hlsDeadline) {
			t.Fatalf("stream must come back after resume: %d %s", code, body)
		}
		time.Sleep(1 * time.Second)
	}

	// シーク: 再生中に位置指定 → 指定オフセットから feed が張り直される。
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/seek", strings.NewReader(`{"seconds":4}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("seek = %d: %s", rec.Code, rec.Body.String())
	}
	seekDeadline := time.Now().Add(15 * time.Second)
	for {
		st := playlistState(t, mux)
		if st["playing"] == true {
			if elapsed, _ := st["elapsedSeconds"].(float64); elapsed >= 4 {
				break
			}
		}
		if time.Now().After(seekDeadline) {
			t.Fatalf("seek did not take effect: %v", playlistState(t, mux))
		}
		time.Sleep(500 * time.Millisecond)
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

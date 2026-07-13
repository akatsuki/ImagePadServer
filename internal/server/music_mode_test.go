package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

func TestUploadURLReportsDownloadingPhase(t *testing.T) {
	_, mux := testServer(t, true)
	if err := settings.Update(func(s *settings.Settings) error {
		s.MusicModeEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	oldMusic := musicURLAcquirer
	defer func() { musicURLAcquirer = oldMusic }()
	release := make(chan struct{})
	reached := make(chan struct{})
	musicURLAcquirer = func(context.Context, *Server, string) (video.AcquiredAudio, error) {
		close(reached)
		<-release
		return video.AcquiredAudio{}, errors.New("stop here")
	}

	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.youtube.com/watch?v=test"}`))
		adminJSON(t, mux, req)
	}()

	<-reached
	stateReq := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := adminJSON(t, mux, stateReq)
	close(release)

	var st map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	ingest, _ := st["ingest"].(map[string]interface{})
	if ingest == nil || ingest["active"] != true || ingest["phase"] != "downloading" {
		t.Fatalf("ingest phase = %#v, want downloading/active", ingest)
	}
}

func TestMusicModeCannotEnableWithoutVideoPlayer(t *testing.T) {
	_, mux := testServer(t, false)
	req := httptest.NewRequest(http.MethodPost, "/api/music-mode", strings.NewReader(`{"enabled":true}`))
	rec := adminJSON(t, mux, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	got, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.MusicModeEnabled {
		t.Fatal("music mode was enabled while video player support was disabled")
	}
}

func TestMusicModeRoutesPublishAndQueueURLsToAudioAcquirer(t *testing.T) {
	for _, endpoint := range []string{"/api/upload-url", "/api/upload-url-queue"} {
		t.Run(endpoint, func(t *testing.T) {
			_, mux := testServer(t, true)
			if err := settings.Update(func(s *settings.Settings) error {
				s.MusicModeEnabled = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			oldMusic := musicURLAcquirer
			oldDirect := directMediaDownloader
			defer func() {
				musicURLAcquirer = oldMusic
				directMediaDownloader = oldDirect
			}()
			musicCalled := false
			directCalled := false
			musicURLAcquirer = func(context.Context, *Server, string) (video.AcquiredAudio, error) {
				musicCalled = true
				return video.AcquiredAudio{}, errors.New("music route selected")
			}
			directMediaDownloader = func(context.Context, string, string, func(context.Context, string) (video.MediaProbe, error)) (downloadedRemoteMedia, error) {
				directCalled = true
				return downloadedRemoteMedia{}, errors.New("direct route selected")
			}

			req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"url":"https://www.youtube.com/watch?v=test"}`))
			rec := adminJSON(t, mux, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "music route selected") {
				t.Fatalf("status/body = %d %q, want music route error", rec.Code, rec.Body.String())
			}
			if !musicCalled || directCalled {
				t.Fatalf("musicCalled=%v directCalled=%v, want true/false", musicCalled, directCalled)
			}
		})
	}
}

// TestVideoModeTriesYTDLPThenDirect verifies the fallback routing: any URL is
// tried with yt-dlp first (so X/Twitter and every other yt-dlp-supported site
// works, not just an allowlist), and only when yt-dlp fails does the bounded
// direct downloader run.
func TestVideoModeTriesYTDLPThenDirect(t *testing.T) {
	oldPage := pageMediaDownloader
	oldHLS := pageHLSMediaDownloader
	oldDirect := directMediaDownloader
	oldEnsureFFmpeg := ensureFFmpeg
	t.Cleanup(func() {
		pageMediaDownloader = oldPage
		pageHLSMediaDownloader = oldHLS
		directMediaDownloader = oldDirect
		ensureFFmpeg = oldEnsureFFmpeg
	})
	ensureFFmpeg = func() (string, error) {
		return "", errors.New("ffmpeg route blocked")
	}

	t.Run("yt-dlp success skips direct", func(t *testing.T) {
		_, mux := testServer(t, true)
		pageCalled := false
		directCalled := false
		pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
			pageCalled = true
			// Succeed at the download; later processing may fail, but direct
			// must not be attempted.
			return video.DownloadedMedia{SourcePath: filepath.Join(t.TempDir(), "missing.mp4"), Name: "x.mp4"}, nil
		}
		directMediaDownloader = func(context.Context, string, string, func(context.Context, string) (video.MediaProbe, error)) (downloadedRemoteMedia, error) {
			directCalled = true
			return downloadedRemoteMedia{}, errors.New("direct route selected")
		}

		req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://x.com/u/status/1/video/1"}`))
		adminJSON(t, mux, req)
		if !pageCalled || directCalled {
			t.Fatalf("pageCalled=%v directCalled=%v, want yt-dlp tried and direct skipped", pageCalled, directCalled)
		}
	})

	t.Run("yt-dlp failure falls back to direct for non-page URLs", func(t *testing.T) {
		for _, rawURL := range []string{
			"https://example.com/clip.mp4",
			"https://example.com/song",
		} {
			t.Run(rawURL, func(t *testing.T) {
				_, mux := testServer(t, true)
				pageCalled := false
				directCalled := false
				pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
					pageCalled = true
					return video.DownloadedMedia{}, errors.New("yt-dlp route failed")
				}
				directMediaDownloader = func(context.Context, string, string, func(context.Context, string) (video.MediaProbe, error)) (downloadedRemoteMedia, error) {
					directCalled = true
					return downloadedRemoteMedia{}, errors.New("direct route failed")
				}

				req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"`+rawURL+`"}`))
				rec := adminJSON(t, mux, req)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%q, want 400", rec.Code, rec.Body.String())
				}
				if !pageCalled || !directCalled {
					t.Fatalf("pageCalled=%v directCalled=%v, want both (yt-dlp first, then direct fallback)", pageCalled, directCalled)
				}
				if !strings.Contains(rec.Body.String(), "yt-dlp route failed") {
					t.Fatalf("body %q should surface the yt-dlp error", rec.Body.String())
				}
			})
		}
	})

	t.Run("yt-dlp failure tries page HLS before direct for unknown pages", func(t *testing.T) {
		_, mux := testServer(t, true)
		pageCalled := false
		hlsCalled := false
		directCalled := false
		pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
			pageCalled = true
			return video.DownloadedMedia{}, errors.New("yt-dlp route failed")
		}
		pageHLSMediaDownloader = func(context.Context, string, string) (video.DownloadedMedia, error) {
			hlsCalled = true
			return video.DownloadedMedia{SourcePath: filepath.Join(t.TempDir(), "missing.mp4"), Name: "hls.mp4"}, nil
		}
		directMediaDownloader = func(context.Context, string, string, func(context.Context, string) (video.MediaProbe, error)) (downloadedRemoteMedia, error) {
			directCalled = true
			return downloadedRemoteMedia{}, errors.New("direct route selected")
		}

		req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://example.com/watch/123"}`))
		adminJSON(t, mux, req)
		if !pageCalled || !hlsCalled || directCalled {
			t.Fatalf("pageCalled=%v hlsCalled=%v directCalled=%v, want yt-dlp then HLS and direct skipped", pageCalled, hlsCalled, directCalled)
		}
	})

	t.Run("yt-dlp failure skips direct for page URLs", func(t *testing.T) {
		// Page URLs (YouTube, Twitter/X, SoundCloud) only return HTML to a
		// plain GET; the direct fallback must be skipped so the real yt-dlp
		// error is surfaced instead of a misleading ffprobe "Invalid data
		// found" on saved HTML. (SoundCloud is handled by an earlier branch
		// and never reaches this fallback site.)
		for _, rawURL := range []string{
			"https://www.youtube.com/watch?v=test",
			"https://youtu.be/abc123",
			"https://x.com/u/status/1/video/1",
			"https://twitter.com/u/status/1",
		} {
			t.Run(rawURL, func(t *testing.T) {
				_, mux := testServer(t, true)
				pageCalled := false
				directCalled := false
				pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
					pageCalled = true
					return video.DownloadedMedia{}, errors.New("yt-dlp route failed")
				}
				directMediaDownloader = func(context.Context, string, string, func(context.Context, string) (video.MediaProbe, error)) (downloadedRemoteMedia, error) {
					directCalled = true
					return downloadedRemoteMedia{}, errors.New("direct route failed")
				}

				req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"`+rawURL+`"}`))
				rec := adminJSON(t, mux, req)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%q, want 400", rec.Code, rec.Body.String())
				}
				if !pageCalled {
					t.Fatalf("pageCalled=%v, want yt-dlp tried", pageCalled)
				}
				if directCalled {
					t.Fatalf("directCalled=%v, want direct skipped for page URL", directCalled)
				}
				if !strings.Contains(rec.Body.String(), "yt-dlp route failed") {
					t.Fatalf("body %q should surface the yt-dlp error directly", rec.Body.String())
				}
			})
		}
	})
}

func TestVideoPlayerDisableClearsMusicMode(t *testing.T) {
	_, mux := testServer(t, true)
	if err := settings.Update(func(s *settings.Settings) error {
		s.MusicModeEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/video-player", strings.NewReader(`{"enabled":false}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	got, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.MusicModeEnabled {
		t.Fatal("music mode remained enabled after video player support was disabled")
	}

	var state map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := state["musicModeEnabled"].(bool); enabled {
		t.Fatal("video player state reported music mode as enabled")
	}
}

func TestMusicModeEndpointEnablesWithVideoPlayer(t *testing.T) {
	_, mux := testServer(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/music-mode", strings.NewReader(`{"enabled":true}`))
	rec := adminJSON(t, mux, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var state map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := state["musicModeEnabled"].(bool); !enabled {
		t.Fatalf("musicModeEnabled = %#v, want true", state["musicModeEnabled"])
	}
}

func TestMusicQualityUsesVideoQualitySetting(t *testing.T) {
	s, mux := testServer(t, true)

	req := httptest.NewRequest(http.MethodPost, "/api/video-quality", strings.NewReader(`{"mode":"360"}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	preset := s.musicQualityPreset()
	if preset.Mode != "360" || preset.Height != 360 {
		t.Fatalf("music preset = mode %q height %d, want 360/360", preset.Mode, preset.Height)
	}
}

func TestMusicPlaylistLatencyModeDefaultsToUltraAndPersists(t *testing.T) {
	_, mux := testServer(t, true)

	req := httptest.NewRequest(http.MethodGet, "/api/video-quality", nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var state map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if got := state["musicPlaylistLatencyMode"]; got != "rtsp-ultra" {
		t.Fatalf("default musicPlaylistLatencyMode = %#v, want rtsp-ultra", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/video-quality", strings.NewReader(`{"musicPlaylistLatencyMode":"rtsp-low"}`))
	rec = adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if got := state["musicPlaylistLatencyMode"]; got != "rtsp-low" {
		t.Fatalf("saved musicPlaylistLatencyMode = %#v, want rtsp-low", got)
	}
	if got := state["musicPlaylistDeliveryProfile"]; got != "rtsp-low" {
		t.Fatalf("legacy RTSP mode must surface as delivery profile, got %#v", got)
	}
	got, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.MusicPlaylistLatencyMode != "rtsp-low" {
		t.Fatalf("settings MusicPlaylistLatencyMode = %q, want rtsp-low", got.MusicPlaylistLatencyMode)
	}
}

func TestMusicPlaylistDeliveryProfileDefaultsToUltraAndPersists(t *testing.T) {
	_, mux := testServer(t, true)

	req := httptest.NewRequest(http.MethodGet, "/api/video-quality", nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var state map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if got := state["musicPlaylistDeliveryProfile"]; got != "rtsp-ultra" {
		t.Fatalf("default musicPlaylistDeliveryProfile = %#v, want rtsp-ultra", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/video-quality", strings.NewReader(`{"musicPlaylistDeliveryProfile":"hls-high"}`))
	rec = adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if got := state["musicPlaylistDeliveryProfile"]; got != "hls-high" {
		t.Fatalf("saved musicPlaylistDeliveryProfile = %#v, want hls-high", got)
	}
	if got := state["musicPlaylistLatencyMode"]; got != "rtsp-ultra" {
		t.Fatalf("HLS profile must retain safe RTSP encoder mode, got %#v", got)
	}
	got, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.MusicPlaylistDeliveryProfile != "hls-high" {
		t.Fatalf("settings MusicPlaylistDeliveryProfile = %q, want hls-high", got.MusicPlaylistDeliveryProfile)
	}
}

func TestMusicPlaylistDesiredSettingsRemainPendingUntilRestart(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.VideoQualityMode = "1080"
		appSettings.MusicPlaylistDeliveryProfile = "rtsp-ultra"
		appSettings.MusicPlaylistCanonicalHeight = 720
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv.radio = &barrierMusicRadio{status: obsrtmp.RadioStatus{
		Running: true,
		ActiveSession: &obsrtmp.RadioActiveSessionContract{
			DeliveryProfile: "rtsp-ultra",
		},
	}}

	req := httptest.NewRequest(http.MethodPost, "/api/video-quality", strings.NewReader(`{"musicPlaylistDeliveryProfile":"hls-high","musicPlaylistCanonicalHeight":1080}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var state map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if got := state["desiredDeliveryProfile"]; got != "hls-high" {
		t.Fatalf("desired delivery profile = %#v, want hls-high", got)
	}
	if got := state["activeDeliveryProfile"]; got != "rtsp-ultra" {
		t.Fatalf("active delivery profile = %#v, want rtsp-ultra", got)
	}
	if got := state["deliveryRestartRequired"]; got != true {
		t.Fatalf("delivery restart required = %#v, want true", got)
	}
	if got := state["desiredCanonicalHeight"]; got != float64(1080) {
		t.Fatalf("desired canonical height = %#v, want 1080", got)
	}
	if got := state["activeCanonicalHeight"]; got != float64(srv.activeCanonicalHeight) {
		t.Fatalf("active canonical height = %#v, want %d", got, srv.activeCanonicalHeight)
	}
	if got := state["canonicalRestartRequired"]; got != (srv.activeCanonicalHeight != 1080) {
		t.Fatalf("canonical restart required = %#v", got)
	}

	persisted, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.VideoQualityMode != "1080" {
		t.Fatalf("partial playlist update reset video quality to %q", persisted.VideoQualityMode)
	}
	if persisted.MusicPlaylistDeliveryProfile != "hls-high" || persisted.MusicPlaylistCanonicalHeight != 1080 {
		t.Fatalf("persisted playlist desired settings = %#v", persisted)
	}

	playlist := playlistState(t, mux)
	if playlist["desiredDeliveryProfile"] != "hls-high" || playlist["activeDeliveryProfile"] != "rtsp-ultra" {
		t.Fatalf("playlist state relabeled active delivery: %#v", playlist)
	}

	stateReq := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	stateRec := adminJSON(t, mux, stateReq)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("GET state = %d, want 200: %s", stateRec.Code, stateRec.Body.String())
	}
	var fullState map[string]interface{}
	if err := json.Unmarshal(stateRec.Body.Bytes(), &fullState); err != nil {
		t.Fatal(err)
	}
	quality, ok := fullState["videoQuality"].(map[string]interface{})
	if !ok {
		t.Fatalf("state videoQuality = %#v, want object", fullState["videoQuality"])
	}
	if quality["desiredDeliveryProfile"] != "hls-high" || quality["activeDeliveryProfile"] != "rtsp-ultra" {
		t.Fatalf("state quality relabeled active delivery: %#v", quality)
	}
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

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

func TestVideoModeRoutesYouTubeAndNicoThroughYTDLP(t *testing.T) {
	for _, rawURL := range []string{
		"https://www.youtube.com/watch?v=test",
		"https://music.youtube.com/watch?v=test",
		"https://www.nicovideo.jp/watch/sm9",
	} {
		t.Run(rawURL, func(t *testing.T) {
			_, mux := testServer(t, true)
			oldPage := pageMediaDownloader
			oldDirect := directMediaDownloader
			defer func() {
				pageMediaDownloader = oldPage
				directMediaDownloader = oldDirect
			}()
			pageCalled := false
			directCalled := false
			pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
				pageCalled = true
				return video.DownloadedMedia{}, errors.New("yt-dlp page route selected")
			}
			directMediaDownloader = func(context.Context, string, string, func(context.Context, string) (video.MediaProbe, error)) (downloadedRemoteMedia, error) {
				directCalled = true
				return downloadedRemoteMedia{}, errors.New("direct route selected")
			}

			req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"`+rawURL+`"}`))
			rec := adminJSON(t, mux, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "yt-dlp page route selected") {
				t.Fatalf("status/body = %d %q, want yt-dlp page route error", rec.Code, rec.Body.String())
			}
			if !pageCalled || directCalled {
				t.Fatalf("pageCalled=%v directCalled=%v, want true/false", pageCalled, directCalled)
			}
		})
	}
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

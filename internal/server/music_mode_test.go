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
	t.Run("yt-dlp success skips direct", func(t *testing.T) {
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

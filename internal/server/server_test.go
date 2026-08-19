package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/upnp"
	"imagepadserver/internal/video"
	"imagepadserver/internal/ytdlpauth"
)

func TestValidatePublicURLRejectsLocalhost(t *testing.T) {
	if _, err := validatePublicURL("http://localhost/image.png"); err == nil {
		t.Fatal("expected localhost URL to be rejected")
	}
	if _, err := validatePublicURL("http://127.0.0.1/image.png"); err == nil {
		t.Fatal("expected loopback URL to be rejected")
	}
}

func TestValidateHTTPURL(t *testing.T) {
	if err := validateHTTPURL("https://example.com/watch?v=1"); err != nil {
		t.Fatal(err)
	}
	if err := validateHTTPURL("file:///tmp/video.mp4"); err == nil {
		t.Fatal("expected non-http URL to be rejected")
	}
	if err := validateHTTPURL("http://127.0.0.1/video"); err == nil {
		t.Fatal("expected loopback URL to be rejected")
	}
	if err := validateHTTPURL("http://192.168.0.1/stream"); err == nil {
		t.Fatal("expected private network URL to be rejected")
	}
	if err := validateHTTPURL("http://100.64.0.1/internal"); err == nil {
		t.Fatal("expected CGNAT URL to be rejected")
	}
}

func TestRemoteContentTypeAllowed(t *testing.T) {
	if !remoteContentTypeAllowed("image/webp") {
		t.Fatal("expected image/webp to be allowed")
	}
	if !remoteContentTypeAllowed("image/svg+xml; charset=utf-8") {
		t.Fatal("expected image/svg+xml to be allowed")
	}
	if !remoteContentTypeAllowed("application/octet-stream") {
		t.Fatal("expected octet-stream to be allowed for RAW image downloads")
	}
	if remoteContentTypeAllowed("text/html") {
		t.Fatal("expected text/html to be rejected")
	}
}

func TestRemoteFileNameInfersRAWExtensions(t *testing.T) {
	u := mustURL("https://example.com/download?id=1&filename=sample.CR3")
	if got := remoteFileName(u, "application/octet-stream"); got != "download.cr3" {
		t.Fatalf("remoteFileName = %q, want download.cr3", got)
	}

	u = mustURL("https://example.com/raw")
	if got := remoteFileName(u, "image/x-nikon-nef"); got != "raw.nef" {
		t.Fatalf("remoteFileName = %q, want raw.nef", got)
	}
}

func TestRemoteFileNameInfersModernImageExtensions(t *testing.T) {
	u := mustURL("https://example.com/image")
	for contentType, want := range map[string]string{
		"image/avif": "image.avif",
		"image/heic": "image.heic",
		"image/heif": "image.heif",
		"image/jxl":  "image.jxl",
	} {
		if got := remoteFileName(u, contentType); got != want {
			t.Fatalf("remoteFileName(%q) = %q, want %q", contentType, got, want)
		}
	}
}

func TestHandleFFmpegChecksConfiguredBinaryWithoutEnablingVideoMode(t *testing.T) {
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.exe")
	if err := os.WriteFile(ffmpegPath, []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_FFMPEG", ffmpegPath)

	srv, mux := testServer(t, false)
	defer cleanupTestServer(srv)

	req := httptest.NewRequest(http.MethodPost, "/api/ffmpeg", nil)
	rec := adminJSON(t, mux, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if srv.videoPlayerEnabled() {
		t.Fatal("expected FFmpeg check not to enable video player mode")
	}
}

func TestIsHLSSegmentName(t *testing.T) {
	valid := []string{"current0.ts", "current12.ts", "current1779424624066091600-24.ts", "current-242352fb7167ea14-1779429230673092900-60.ts"}
	for _, name := range valid {
		if !isHLSSegmentName(name) {
			t.Fatalf("expected %s to be accepted", name)
		}
	}

	invalid := []string{"current.ts", "currentx.ts", "../current0.ts", "current0.mp4"}
	for _, name := range invalid {
		if isHLSSegmentName(name) {
			t.Fatalf("expected %s to be rejected", name)
		}
	}
}

func TestOptionsFromValuesDefaultAllows8KUpload(t *testing.T) {
	opts := optionsFromValues(func(string) string { return "" })
	if opts.MaxDimension != 2048 {
		t.Fatalf("MaxDimension = %d, want 2048", opts.MaxDimension)
	}
	if opts.MaxInputBytes != 120<<20 {
		t.Fatalf("MaxInputBytes = %d, want %d", opts.MaxInputBytes, int64(120<<20))
	}
	if opts.MaxBytes != 30<<20 {
		t.Fatalf("MaxBytes = %d, want %d", opts.MaxBytes, int64(30<<20))
	}
}

func TestStreamRequestID(t *testing.T) {
	req := adminRequest("https://example.com/stream/abc123/current-abc123.m3u8", "127.0.0.1:50000")
	if got := streamRequestID(req); got != "abc123" {
		t.Fatalf("streamRequestID = %q, want abc123", got)
	}
	req = adminRequest("https://example.com/stream/current.m3u8?v=legacy", "127.0.0.1:50000")
	if got := streamRequestID(req); got != "legacy" {
		t.Fatalf("streamRequestID = %q, want legacy", got)
	}
}

func TestIsVideoUpload(t *testing.T) {
	if !isVideoUpload("clip.mp4", "") {
		t.Fatal("expected mp4 extension to be treated as video")
	}
	if !isVideoUpload("upload.bin", "video/webm; charset=binary") {
		t.Fatal("expected video content type to be treated as video")
	}
	if isVideoUpload("photo.jpg", "image/jpeg") {
		t.Fatal("expected image upload not to be treated as video")
	}
}

func TestAdminAccessRules(t *testing.T) {
	srv := &Server{adminToken: "secret"}

	if !srv.adminAllowed(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000")) {
		t.Fatal("expected localhost admin access to be allowed")
	}
	if srv.adminAllowed(adminRequest("https://example.trycloudflare.com/?token=secret", "127.0.0.1:50000")) {
		t.Fatal("expected tunnel-host admin access to be rejected")
	}
	if !srv.adminAllowed(adminRequest("http://192.168.1.20:8080/?token=secret", "192.168.1.35:50000")) {
		t.Fatal("expected LAN admin access with token to be allowed")
	}
	if srv.adminAllowed(adminRequest("http://203.0.113.10:8080/?token=secret", "198.51.100.25:50000")) {
		t.Fatal("expected public remote admin access to be rejected")
	}
}

func TestPublicReadRules(t *testing.T) {
	if !publicReadAllowed(adminRequest("http://192.168.1.20:8080/image/current", "192.168.1.35:50000")) {
		t.Fatal("expected LAN media read to be allowed")
	}
	if publicReadAllowed(adminRequest("http://203.0.113.10:8080/image/current", "198.51.100.25:50000")) {
		t.Fatal("expected direct public media read to be rejected")
	}
	if !publicReadAllowed(adminRequest("https://example.trycloudflare.com/image/current", "127.0.0.1:50000")) {
		t.Fatal("expected tunnel media read via local origin to be allowed")
	}
}

func TestHandleEventsSendsHeartbeat(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	previousHeartbeat := stateEventHeartbeatDelay
	stateEventHeartbeatDelay = 50 * time.Millisecond
	t.Cleanup(func() { stateEventHeartbeatDelay = previousHeartbeat })
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mux := http.NewServeMux()
	srv.Register(mux)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := adminRequest("http://127.0.0.1:8080/api/events", "127.0.0.1:50000").WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(rec, req)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.Body.String(), "event: heartbeat") {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("SSE body missing heartbeat: %q", rec.Body.String())
}

func TestPrimaryShareURL(t *testing.T) {
	url, label := primaryShareURL(map[string]interface{}{
		"shareMode":  "obs",
		"obsLatency": obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPT),
		"obs": obsrtmp.Status{
			Connected: true,
			RTSPTURL:  "rtsp://8.8.8.8:52000/obs_session",
		},
		"imageURL": "https://example.com/image/current.png",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "rtsp://8.8.8.8:52000/obs_session" || label != "RTSP TCP URL" {
		t.Fatalf("share URL = %q (%s), want RTSP", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"shareMode":  "obs",
		"obsLatency": obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPT),
		"obs": obsrtmp.Status{
			Connected: true,
			RTSPTURL:  "",
		},
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "" || label != "URL" {
		t.Fatalf("share URL = %q (%s), want no URL before public RTSP is ready", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"shareMode":  "link",
		"obsLatency": obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPT),
		"obs": obsrtmp.Status{
			Connected: true,
			RTSPTURL:  "",
		},
		"hlsURL": "https://example.com/stream/abc123/current-abc123.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "https://example.com/stream/abc123/current-abc123.m3u8" || label != "HLS URL" {
		t.Fatalf("share URL = %q (%s), want link-mode HLS while public RTSP is not ready", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"shareMode":  "obs",
		"obsLatency": obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPT),
		"obs": obsrtmp.Status{
			Connected: true,
			RTSPTURL:  "",
		},
		"hlsURL": "https://example.com/stream/abc123/current-abc123.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "" || label != "URL" {
		t.Fatalf("share URL = %q (%s), want no URL in OBS RTSP mode before public RTSP is ready", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"shareMode":  "link",
		"obsLatency": obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPT),
		"obs": obsrtmp.Status{
			Connected: false,
			RTSPTURL:  "",
		},
		"hlsURL": "https://example.com/stream/abc123/current-abc123.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "https://example.com/stream/abc123/current-abc123.m3u8" || label != "HLS URL" {
		t.Fatalf("share URL = %q (%s), want recorded HLS after RTSP session ends", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"shareMode": "file",
		"current": library.CurrentImage{
			ID:   "abc123",
			Kind: "video",
		},
		"imageURL": "https://example.com/image/current.png",
		"videoURL": "https://example.com/video/current.mp4",
		"hlsURL":   "https://example.com/stream/abc123/current-abc123.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "https://example.com/stream/abc123/current-abc123.m3u8" || label != "HLS URL" {
		t.Fatalf("share URL = %q (%s), want HLS", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"shareMode": "file",
		"imageURL":  "https://example.com/image/current.png",
		"videoPlayer": map[string]interface{}{
			"enabled": false,
		},
	})
	if url != "https://example.com/image/current.png" || label != "ImagePad URL" {
		t.Fatalf("share URL = %q (%s), want image", url, label)
	}
}

func TestCopyURLPrefersCurrentImageOverStaleVideoShare(t *testing.T) {
	state := map[string]interface{}{
		"shareURL":  "https://example.com/stream/old/current-old.m3u8",
		"shareMode": "file",
		"current": library.CurrentImage{
			ID:   "image-1",
			Kind: "image",
		},
		"imageURL": "https://example.com/image/current.png",
		"hlsURL":   "https://example.com/stream/old/current-old.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	}

	const want = "https://example.com/image/current.png"
	if got := urlForClipboard(state); got != want {
		t.Fatalf("urlForClipboard() = %q, want %q", got, want)
	}
	if got := urlForCopyTarget(state, "shareURL"); got != want {
		t.Fatalf("urlForCopyTarget(shareURL) = %q, want %q", got, want)
	}
	state["shareMode"] = "link"
	if got := urlForCopyTarget(state, "shareURL"); got != want {
		t.Fatalf("urlForCopyTarget(shareURL) with link mode = %q, want %q", got, want)
	}
}

func TestCopyURLAcceptsDisplayedPlaylistShareURL(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	want := "https://public.example/radio/index.m3u8"
	req := httptest.NewRequest(http.MethodPost, "/api/copy-url", strings.NewReader(`{"target":"plShareUrl","value":"`+want+`"}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("copy playlist URL status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["copiedURL"] != want {
		t.Fatalf("copiedURL = %q, want %q", got["copiedURL"], want)
	}
}

func TestResolvedShareTargetsCentralizeModeURLs(t *testing.T) {
	state := withResolvedShareURLs(map[string]interface{}{
		"shareMode": "file",
		"current": library.CurrentImage{
			ID:   "image-1",
			Kind: "image",
		},
		"imageURL": "https://example.com/image/current.png?v=image-1",
		"hlsURL":   "https://example.com/stream/image-1/current-image-1.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	targets, ok := state["shareTargets"].(map[string]interface{})
	if !ok {
		t.Fatalf("shareTargets missing or wrong type: %#v", state["shareTargets"])
	}
	fileTarget, ok := targets["file"].(map[string]interface{})
	if !ok {
		t.Fatalf("shareTargets[file] missing: %#v", targets["file"])
	}
	if got := fileTarget["shareURL"]; got != "https://example.com/image/current.png?v=image-1" {
		t.Fatalf("shareTargets[file].shareURL = %q, want image URL", got)
	}
	linkTarget, ok := targets["link"].(map[string]interface{})
	if !ok {
		t.Fatalf("shareTargets[link] missing: %#v", targets["link"])
	}
	if got := linkTarget["shareURL"]; got != "https://example.com/image/current.png?v=image-1" {
		t.Fatalf("shareTargets[link].shareURL = %q, want image URL", got)
	}
	if got := state["shareURL"]; got != "https://example.com/image/current.png?v=image-1" {
		t.Fatalf("shareURL = %q, want image URL", got)
	}

	state = withResolvedShareURLs(map[string]interface{}{
		"shareMode": "link",
		"current": library.CurrentImage{
			ID:   "video-1",
			Kind: "video",
		},
		"hlsURL": "https://example.com/stream/video-1/current-video-1.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	targets = state["shareTargets"].(map[string]interface{})
	linkTarget = targets["link"].(map[string]interface{})
	if got := linkTarget["shareURL"]; got != "https://example.com/stream/video-1/current-video-1.m3u8" {
		t.Fatalf("video link shareURL = %q, want HLS URL", got)
	}

	state = withResolvedShareURLs(map[string]interface{}{
		"shareMode": "file",
		"current": library.CurrentImage{
			ID:         "music-1",
			Kind:       "video",
			SourceKind: "local_audio",
		},
		"hlsURL": "https://example.com/stream/music-1/current-music-1.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	targets = state["shareTargets"].(map[string]interface{})
	for _, mode := range []string{"file", "link"} {
		target := targets[mode].(map[string]interface{})
		if got := target["shareURL"]; got != "https://example.com/stream/music-1/current-music-1.m3u8" {
			t.Fatalf("music shareTargets[%s].shareURL = %q, want HLS URL", mode, got)
		}
	}
}

func TestURLEngineModeMatrix(t *testing.T) {
	const (
		imageURL = "https://example.com/image/current.png?v=image-1"
		hlsURL   = "https://example.com/stream/media-1/current-media-1.m3u8"
		rtspURL  = "rtsp://8.8.8.8:52000/obs_session"
	)
	tests := []struct {
		name      string
		current   library.CurrentImage
		mode      string
		obs       obsrtmp.Status
		wantURL   string
		wantLabel string
	}{
		{
			name:      "image file fixed to imagepad",
			current:   library.CurrentImage{ID: "image-1", Kind: "image"},
			mode:      "file",
			wantURL:   imageURL,
			wantLabel: "ImagePad URL",
		},
		{
			name:      "image link fixed to imagepad",
			current:   library.CurrentImage{ID: "image-1", Kind: "image"},
			mode:      "link",
			wantURL:   imageURL,
			wantLabel: "ImagePad URL",
		},
		{
			name:      "video file fixed to hls",
			current:   library.CurrentImage{ID: "media-1", Kind: "video"},
			mode:      "file",
			wantURL:   hlsURL,
			wantLabel: "HLS URL",
		},
		{
			name:      "video link fixed to hls",
			current:   library.CurrentImage{ID: "media-1", Kind: "video"},
			mode:      "link",
			wantURL:   hlsURL,
			wantLabel: "HLS URL",
		},
		{
			name:      "music file fixed to hls",
			current:   library.CurrentImage{ID: "media-1", Kind: "video", SourceKind: "local_audio"},
			mode:      "file",
			wantURL:   hlsURL,
			wantLabel: "HLS URL",
		},
		{
			name:      "music link fixed to hls",
			current:   library.CurrentImage{ID: "media-1", Kind: "video", SourceKind: "soundcloud"},
			mode:      "link",
			wantURL:   hlsURL,
			wantLabel: "HLS URL",
		},
		{
			name:      "obs hls mode fixed to hls",
			current:   library.CurrentImage{ID: "media-1", Kind: "video", SourceKind: "obs"},
			mode:      "obs_hls",
			wantURL:   hlsURL,
			wantLabel: "HLS URL",
		},
		{
			name:    "obs protocol prefers rtsp when ready",
			current: library.CurrentImage{ID: "media-1", Kind: "video", SourceKind: "obs"},
			mode:    "obs_rtsp",
			obs: obsrtmp.Status{
				Connected: true,
				RTSPTURL:  rtspURL,
			},
			wantURL:   rtspURL,
			wantLabel: "RTSP TCP URL",
		},
		{
			name:      "obs protocol waits before rtsp is ready",
			current:   library.CurrentImage{ID: "media-1", Kind: "video", SourceKind: "obs"},
			mode:      "obs_rtsp",
			obs:       obsrtmp.Status{Connected: true},
			wantURL:   "",
			wantLabel: "URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := map[string]interface{}{
				"shareMode":  tt.mode,
				"current":    tt.current,
				"imageURL":   imageURL,
				"hlsURL":     hlsURL,
				"obsLatency": obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPT),
				"obs":        tt.obs,
				"videoPlayer": map[string]interface{}{
					"enabled": true,
				},
			}
			gotURL, gotLabel := primaryShareURL(state)
			if gotURL != tt.wantURL || gotLabel != tt.wantLabel {
				t.Fatalf("primaryShareURL() = %q (%s), want %q (%s)", gotURL, gotLabel, tt.wantURL, tt.wantLabel)
			}
			targets := withResolvedShareURLs(state)["shareTargets"].(map[string]interface{})
			target := targets[tt.mode].(map[string]interface{})
			if target["shareURL"] != tt.wantURL || target["shareURLLabel"] != tt.wantLabel {
				t.Fatalf("shareTargets[%s] = %#v, want %q (%s)", tt.mode, target, tt.wantURL, tt.wantLabel)
			}
		})
	}
}

func TestClipboardResultUsesRequestedShareMode(t *testing.T) {
	srv, _ := testServer(t, false)
	const imageURL = "https://example.com/image/current.png?v=image-1"
	const hlsURL = "https://example.com/stream/video-1/current-video-1.m3u8"

	imageState := map[string]interface{}{
		"current": library.CurrentImage{
			ID:   "image-1",
			Kind: "image",
		},
		"imageURL": imageURL,
		"hlsURL":   hlsURL,
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	}
	req := requestWithShareMode(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"), "link")
	got := srv.withClipboardResult(req, withResolvedShareURLs(imageState))
	if copied, _ := got["copiedURL"].(string); copied != imageURL {
		t.Fatalf("image copiedURL = %q, want image URL", copied)
	}

	videoState := map[string]interface{}{
		"current": library.CurrentImage{
			ID:   "video-1",
			Kind: "video",
		},
		"imageURL": imageURL,
		"hlsURL":   hlsURL,
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	}
	req = requestWithShareMode(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"), "file")
	got = srv.withClipboardResult(req, withResolvedShareURLs(videoState))
	if copied, _ := got["copiedURL"].(string); copied != hlsURL {
		t.Fatalf("video copiedURL = %q, want HLS URL", copied)
	}

	obsState := map[string]interface{}{
		"current": library.CurrentImage{
			ID:         "obs-1",
			Kind:       "video",
			SourceKind: "obs",
		},
		"hlsURL":     hlsURL,
		"obsLatency": obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPT),
		"obs":        obsrtmp.Status{Connected: true},
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	}
	req = requestWithShareMode(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"), "obs")
	got = srv.withClipboardResult(req, withResolvedShareURLs(obsState))
	if copied, _ := got["copiedURL"].(string); copied != hlsURL {
		t.Fatalf("obs fallback copiedURL = %q, want HLS URL", copied)
	}
}

func TestStateExposesHLSURLOnlyAfterFirstSegment(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(imagePath, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(imagePath, library.CurrentImage{
		PublicName:  "current.png",
		ContentType: "image/png",
	}); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")

	playlist := filepath.Join(store.Dir(), video.PlaylistName(current.ID))
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := srv.state(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"))
	if got, _ := state["hlsURL"].(string); got != "" {
		t.Fatalf("hlsURL = %q, want empty before first segment", got)
	}

	if err := os.WriteFile(filepath.Join(store.Dir(), "current-"+current.ID+"-0.ts"), []byte("segment"), 0600); err != nil {
		t.Fatal(err)
	}
	state = srv.state(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"))
	if got, _ := state["hlsURL"].(string); !strings.Contains(got, "/stream/"+current.ID+"/") {
		t.Fatalf("hlsURL = %q, want id-scoped HLS URL after first segment", got)
	}
}

func TestStateDefaultsToImageURLForPendingStillConversion(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(imagePath, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(imagePath, library.CurrentImage{
		PublicName:  "current.png",
		ContentType: "image/png",
	}); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	video.EnqueueStillImageForID(imagePath, store.Dir(), current.ID, "input.png", video.ResolveQuality("720", 0))
	defer video.CancelQueue(store.Dir())

	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")
	state := srv.state(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"))

	if got, _ := state["hlsURL"].(string); !strings.Contains(got, "/stream/"+current.ID+"/") {
		t.Fatalf("hlsURL = %q, want pending still conversion HLS URL", got)
	}
	if got, _ := state["shareURL"].(string); !strings.Contains(got, "/image/current") || strings.Contains(got, "/stream/") {
		t.Fatalf("shareURL = %q, want image URL while current media is an image", got)
	}
	if got, _ := state["shareURLLabel"].(string); got != "ImagePad URL" {
		t.Fatalf("shareURLLabel = %q, want ImagePad URL", got)
	}
	if got := urlForClipboard(state); !strings.Contains(got, "/image/current") || strings.Contains(got, "/stream/") {
		t.Fatalf("urlForClipboard() = %q, want image URL while current media is an image", got)
	}
}

func TestHistoryStateReportsThumbnailOnlyWhenFileExists(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	thumbSource := filepath.Join(store.Dir(), "thumb-source.jpg")
	if err := os.WriteFile(thumbSource, []byte("jpeg"), 0600); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(srcPath, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}

	created, err := store.AddHistory(srcPath, library.CurrentImage{
		Kind:       "video",
		FileName:   "video.mp4",
		PublicName: "video.mp4",
		Thumbnail:  "thumb-source.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	thumbFile := filepath.Join(store.Dir(), "thumb-"+created.ID+".jpg")

	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	find := func() map[string]interface{} {
		for _, it := range srv.historyState() {
			if id, _ := it["id"].(string); id == created.ID {
				return it
			}
		}
		return nil
	}

	it := find()
	if it == nil {
		t.Fatal("history item not found")
	}
	if it["hasThumbnail"] != true {
		t.Fatalf("hasThumbnail = %v, want true while thumbnail file exists", it["hasThumbnail"])
	}
	if u, _ := it["thumbnailURL"].(string); !strings.Contains(u, "/thumbnail") {
		t.Fatalf("thumbnailURL = %q, want /thumbnail", u)
	}

	if err := os.Remove(thumbFile); err != nil {
		t.Fatal(err)
	}

	it = find()
	if it == nil {
		t.Fatal("history item not found after thumbnail removal")
	}
	if it["hasThumbnail"] != false {
		t.Fatalf("hasThumbnail = %v, want false once thumbnail file is missing", it["hasThumbnail"])
	}
	if u, _ := it["thumbnailURL"].(string); strings.Contains(u, "/thumbnail") {
		t.Fatalf("thumbnailURL = %q, want media URL fallback (no /thumbnail)", u)
	}
}

func TestPublishingVideoThenImageUsesImageURLAndCancelsVideoJob(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer video.CancelQueue(store.Dir())
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")

	videoPath := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	videoReq := requestWithShareMode(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"), "file")
	if _, err := srv.processVideoFileAndPublish(videoReq, videoPath, "clip.mp4", ""); err != nil {
		t.Fatal(err)
	}
	videoCurrent := store.Current()
	if videoCurrent == nil || videoCurrent.Kind != "video" || videoCurrent.ID == "" {
		t.Fatalf("current after video publish = %#v, want video current", videoCurrent)
	}

	imageReq := requestWithShareMode(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"), "file")
	state, err := srv.processAndPublish(imageReq, testPNGReader(t), "photo.png", "image/png", optionsFromValues(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	imageCurrent := store.Current()
	if imageCurrent == nil || imageCurrent.Kind == "video" || imageCurrent.ID == "" || imageCurrent.ID == videoCurrent.ID {
		t.Fatalf("current after image publish = %#v, want new still image current", imageCurrent)
	}
	assertURLContainsOnly(t, state, "shareURL", "/image/current", "/stream/")
	assertURLContainsOnly(t, state, "copiedURL", "/image/current", "/stream/")
	assertShareTargetContains(t, state, "file", "/image/current")
	assertShareTargetContains(t, state, "link", "/image/current")
	assertNoActiveQueueItemForMedia(t, store.Dir(), videoCurrent.ID)
}

func TestPublishingImageThenVideoUsesHLSURLAndCancelsImageJob(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer video.CancelQueue(store.Dir())
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")

	imageReq := requestWithShareMode(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"), "file")
	if _, err := srv.processAndPublish(imageReq, testPNGReader(t), "photo.png", "image/png", optionsFromValues(func(string) string { return "" })); err != nil {
		t.Fatal(err)
	}
	imageCurrent := store.Current()
	if imageCurrent == nil || imageCurrent.Kind == "video" || imageCurrent.ID == "" {
		t.Fatalf("current after image publish = %#v, want still image current", imageCurrent)
	}

	videoPath := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	videoReq := requestWithShareMode(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"), "file")
	state, err := srv.processVideoFileAndPublish(videoReq, videoPath, "clip.mp4", "")
	if err != nil {
		t.Fatal(err)
	}
	videoCurrent := store.Current()
	if videoCurrent == nil || videoCurrent.Kind != "video" || videoCurrent.ID == "" || videoCurrent.ID == imageCurrent.ID {
		t.Fatalf("current after video publish = %#v, want new video current", videoCurrent)
	}
	assertURLContainsOnly(t, state, "shareURL", "/stream/"+videoCurrent.ID+"/", "/image/current")
	assertURLContainsOnly(t, state, "copiedURL", "/stream/"+videoCurrent.ID+"/", "/image/current")
	assertShareTargetContains(t, state, "file", "/stream/"+videoCurrent.ID+"/")
	assertShareTargetContains(t, state, "link", "/stream/"+videoCurrent.ID+"/")
	assertShareTargetContains(t, state, "obs_hls", "/stream/"+videoCurrent.ID+"/")
	assertNoActiveQueueItemForMedia(t, store.Dir(), imageCurrent.ID)
}

func TestHistorySelectReturnsClipboardURL(t *testing.T) {
	srv, mux := testServer(t, false)
	source := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(source, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := srv.store.AddHistory(source, library.CurrentImage{
		PublicName:  "current.png",
		ContentType: "image/png",
		Width:       640,
		Height:      480,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/history/select", strings.NewReader(fmt.Sprintf(`{"id":%q}`, item.ID)))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	var state map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	copiedURL, _ := state["copiedURL"].(string)
	if !strings.Contains(copiedURL, "/image/current") || !strings.Contains(copiedURL, item.ID) {
		t.Fatalf("copiedURL = %q, want restored current image URL for history item", copiedURL)
	}
	if _, ok := state["clipboardCopied"].(bool); !ok {
		t.Fatalf("clipboardCopied missing or wrong type: %#v", state["clipboardCopied"])
	}
}

func TestHistorySelectReturnsRecordedHLSForOBSHistoryEvenWhenRTSPModeSelected(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		s.OBSLatencyMode = obsrtmp.LatencyModeRTSPRealtime
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "obs.mp4")
	if err := os.WriteFile(source, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(source, library.CurrentImage{
		Kind:         "video",
		SourceKind:   "obs",
		PublicName:   "obs-session.mp4",
		ContentType:  "video/mp4",
		OriginalName: "OBS session",
		Converted:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	convertedDir := filepath.Join(filepath.Dir(store.Dir()), "converted", item.ID)
	if err := os.MkdirAll(convertedDir, 0700); err != nil {
		t.Fatal(err)
	}
	playlist := video.PlaylistName(item.ID)
	if err := os.WriteFile(filepath.Join(convertedDir, playlist), []byte("#EXTM3U\n#EXTINF:1,\ncurrent-"+item.ID+"-000.ts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(convertedDir, "current-"+item.ID+"-000.ts"), []byte("segment"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")
	mux := http.NewServeMux()
	srv.Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/history/select", strings.NewReader(fmt.Sprintf(`{"id":%q}`, item.ID)))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	var state map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	shareURL, _ := state["shareURL"].(string)
	if !strings.Contains(shareURL, "/stream/"+item.ID+"/") || !strings.HasPrefix(shareURL, "https://example.trycloudflare.com/") {
		t.Fatalf("shareURL = %q, want public recorded HLS URL", shareURL)
	}
	if got, _ := state["shareURLLabel"].(string); got != "HLS URL" {
		t.Fatalf("shareURLLabel = %q, want HLS URL", got)
	}
	if got, _ := state["copiedURL"].(string); got != shareURL {
		t.Fatalf("copiedURL = %q, want shareURL %q", got, shareURL)
	}
	if got, _ := state["historyTargetMode"].(string); got != "file" {
		t.Fatalf("historyTargetMode = %q, want file", got)
	}
}

func TestHistorySelectImageClearsStaleHLSClipboardURL(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	videoSource := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(videoSource, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	videoItem, err := store.AddHistory(videoSource, library.CurrentImage{
		Kind:        "video",
		PublicName:  "clip.mp4",
		ContentType: "video/mp4",
		Converted:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	convertedDir := filepath.Join(filepath.Dir(store.Dir()), "converted", videoItem.ID)
	if err := os.MkdirAll(convertedDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(convertedDir, video.PlaylistName(videoItem.ID)), []byte("#EXTM3U\n#EXTINF:1,\ncurrent-"+videoItem.ID+"-000.ts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(convertedDir, "current-"+videoItem.ID+"-000.ts"), []byte("segment"), 0600); err != nil {
		t.Fatal(err)
	}

	imageSource := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(imageSource, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	imageItem, err := store.AddHistory(imageSource, library.CurrentImage{
		Kind:        "image",
		PublicName:  "photo.png",
		ContentType: "image/png",
		Width:       640,
		Height:      480,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")
	mux := http.NewServeMux()
	srv.Register(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/history/select", strings.NewReader(fmt.Sprintf(`{"id":%q}`, videoItem.ID)))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("video select status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	var videoState map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&videoState); err != nil {
		t.Fatal(err)
	}
	videoShareURL, _ := videoState["shareURL"].(string)
	if !strings.Contains(videoShareURL, "/stream/"+videoItem.ID+"/") {
		t.Fatalf("video shareURL = %q, want selected HLS URL", videoShareURL)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/history/select", strings.NewReader(fmt.Sprintf(`{"id":%q}`, imageItem.ID)))
	rec = adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("image select status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	var imageState map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&imageState); err != nil {
		t.Fatal(err)
	}
	imageShareURL, _ := imageState["shareURL"].(string)
	if strings.Contains(imageShareURL, "/stream/") || !strings.Contains(imageShareURL, "/image/current") || !strings.Contains(imageShareURL, imageItem.ID) {
		t.Fatalf("image shareURL = %q, want selected image URL", imageShareURL)
	}
	if got, _ := imageState["copiedURL"].(string); got != imageShareURL {
		t.Fatalf("image copiedURL = %q, want image shareURL %q", got, imageShareURL)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/copy-url", strings.NewReader(`{"target":"shareURL","mode":"file"}`))
	rec = adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("copy status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	var copyState map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&copyState); err != nil {
		t.Fatal(err)
	}
	if got, _ := copyState["copiedURL"].(string); strings.Contains(got, "/stream/") || !strings.Contains(got, "/image/current") || !strings.Contains(got, imageItem.ID) {
		t.Fatalf("copy copiedURL = %q, want selected image URL", got)
	}
}

func TestHistorySelectURLIssuingMatrix(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")
	mux := http.NewServeMux()
	srv.Register(mux)

	imageItem := addHistoryItemForURLMatrix(t, store, "photo.png", library.CurrentImage{
		Kind:        "image",
		PublicName:  "photo.png",
		ContentType: "image/png",
		Width:       640,
		Height:      480,
	})
	videoItem := addHistoryItemForURLMatrix(t, store, "clip.mp4", library.CurrentImage{
		Kind:        "video",
		PublicName:  "clip.mp4",
		ContentType: "video/mp4",
		Converted:   true,
	})
	pendingVideoItem := addHistoryItemForURLMatrix(t, store, "pending.mp4", library.CurrentImage{
		Kind:        "video",
		PublicName:  "pending.mp4",
		ContentType: "video/mp4",
	})
	musicItem := addHistoryItemForURLMatrix(t, store, "song.m4a", library.CurrentImage{
		Kind:        "video",
		SourceKind:  "local_audio",
		PublicName:  "song.m4a",
		ContentType: "audio/mp4",
		Converted:   true,
	})
	obsItem := addHistoryItemForURLMatrix(t, store, "obs.mp4", library.CurrentImage{
		Kind:        "video",
		SourceKind:  "obs",
		PublicName:  "obs-session.mp4",
		ContentType: "video/mp4",
		Converted:   true,
	})
	for _, item := range []*library.CurrentImage{videoItem, musicItem, obsItem} {
		writeConvertedHLSForHistory(t, store, item.ID)
	}

	tests := []struct {
		name               string
		id                 string
		wantMode           string
		wantShareContains  string
		wantNoStream       bool
		wantLabel          string
		wantFileContains   string
		wantLinkContains   string
		wantOBSHLSContains string
	}{
		{
			name:              "image history issues imagepad URL for every non-OBS surface",
			id:                imageItem.ID,
			wantMode:          "file",
			wantShareContains: "/image/current",
			wantNoStream:      true,
			wantLabel:         "ImagePad URL",
			wantFileContains:  "/image/current",
			wantLinkContains:  "/image/current",
		},
		{
			name:               "video history issues hls URL",
			id:                 videoItem.ID,
			wantMode:           "file",
			wantShareContains:  "/stream/" + videoItem.ID + "/",
			wantLabel:          "HLS URL",
			wantFileContains:   "/stream/" + videoItem.ID + "/",
			wantLinkContains:   "/stream/" + videoItem.ID + "/",
			wantOBSHLSContains: "/stream/" + videoItem.ID + "/",
		},
		{
			name:               "pending video history immediately issues hls URL",
			id:                 pendingVideoItem.ID,
			wantMode:           "file",
			wantShareContains:  "/stream/" + pendingVideoItem.ID + "/",
			wantLabel:          "HLS URL",
			wantFileContains:   "/stream/" + pendingVideoItem.ID + "/",
			wantLinkContains:   "/stream/" + pendingVideoItem.ID + "/",
			wantOBSHLSContains: "/stream/" + pendingVideoItem.ID + "/",
		},
		{
			name:               "music history issues hls URL",
			id:                 musicItem.ID,
			wantMode:           "link",
			wantShareContains:  "/stream/" + musicItem.ID + "/",
			wantLabel:          "HLS URL",
			wantFileContains:   "/stream/" + musicItem.ID + "/",
			wantLinkContains:   "/stream/" + musicItem.ID + "/",
			wantOBSHLSContains: "/stream/" + musicItem.ID + "/",
		},
		{
			name:               "obs recording history issues recorded hls URL",
			id:                 obsItem.ID,
			wantMode:           "file",
			wantShareContains:  "/stream/" + obsItem.ID + "/",
			wantLabel:          "HLS URL",
			wantFileContains:   "/stream/" + obsItem.ID + "/",
			wantLinkContains:   "/stream/" + obsItem.ID + "/",
			wantOBSHLSContains: "/stream/" + obsItem.ID + "/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/history/select", strings.NewReader(fmt.Sprintf(`{"id":%q}`, tt.id)))
			rec := adminJSON(t, mux, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
			}
			var state map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
				t.Fatal(err)
			}
			assertHistoryURLMatrixState(t, state, tt.wantMode, tt.wantShareContains, tt.wantLabel, tt.wantNoStream)
			assertShareTargetContains(t, state, "file", tt.wantFileContains)
			assertShareTargetContains(t, state, "link", tt.wantLinkContains)
			if tt.wantOBSHLSContains != "" {
				assertShareTargetContains(t, state, "obs_hls", tt.wantOBSHLSContains)
			}
		})
	}
}

func addHistoryItemForURLMatrix(t *testing.T, store *library.Store, filename string, info library.CurrentImage) *library.CurrentImage {
	t.Helper()
	source := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(source, []byte(filename), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(source, info)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func writeConvertedHLSForHistory(t *testing.T, store *library.Store, id string) {
	t.Helper()
	convertedDir := filepath.Join(filepath.Dir(store.Dir()), "converted", id)
	if err := os.MkdirAll(convertedDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(convertedDir, video.PlaylistName(id)), []byte("#EXTM3U\n#EXTINF:1,\ncurrent-"+id+"-000.ts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(convertedDir, "current-"+id+"-000.ts"), []byte("segment"), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertHistoryURLMatrixState(t *testing.T, state map[string]interface{}, wantMode, wantShareContains, wantLabel string, wantNoStream bool) {
	t.Helper()
	if got, _ := state["historyTargetMode"].(string); got != wantMode {
		t.Fatalf("historyTargetMode = %q, want %q", got, wantMode)
	}
	shareURL, _ := state["shareURL"].(string)
	if !strings.Contains(shareURL, wantShareContains) {
		t.Fatalf("shareURL = %q, want containing %q", shareURL, wantShareContains)
	}
	if wantNoStream && strings.Contains(shareURL, "/stream/") {
		t.Fatalf("shareURL = %q, want non-stream image URL", shareURL)
	}
	if got, _ := state["shareURLLabel"].(string); got != wantLabel {
		t.Fatalf("shareURLLabel = %q, want %q", got, wantLabel)
	}
	if copied, _ := state["copiedURL"].(string); copied != shareURL {
		t.Fatalf("copiedURL = %q, want shareURL %q", copied, shareURL)
	}
}

func assertShareTargetContains(t *testing.T, state map[string]interface{}, mode, want string) {
	t.Helper()
	targets, ok := state["shareTargets"].(map[string]interface{})
	if !ok {
		t.Fatalf("shareTargets missing or wrong type: %#v", state["shareTargets"])
	}
	target, ok := targets[mode].(map[string]interface{})
	if !ok {
		t.Fatalf("shareTargets[%s] missing: %#v", mode, targets[mode])
	}
	got, _ := target["shareURL"].(string)
	if !strings.Contains(got, want) {
		t.Fatalf("shareTargets[%s].shareURL = %q, want containing %q", mode, got, want)
	}
}

func assertURLContainsOnly(t *testing.T, state map[string]interface{}, key, want, forbidden string) {
	t.Helper()
	got, _ := state[key].(string)
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want containing %q", key, got, want)
	}
	if forbidden != "" && strings.Contains(got, forbidden) {
		t.Fatalf("%s = %q, want without %q", key, got, forbidden)
	}
}

func assertNoActiveQueueItemForMedia(t *testing.T, outDir, mediaID string) {
	t.Helper()
	for _, item := range video.QueueStatus(outDir) {
		if item.MediaID != mediaID {
			continue
		}
		if item.Status == "pending" || item.Status == "running" {
			t.Fatalf("queue item for media %s still active: %#v", mediaID, item)
		}
	}
}

func testPNGReader(t *testing.T) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 24))); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestStateIgnoresHLSConversionForDifferentCurrentMedia(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(t.TempDir(), "other.png")
	if err := os.WriteFile(otherPath, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	video.EnqueueStillImageForID(otherPath, store.Dir(), "other-media", "other.png", video.ResolveQuality("720", 0))
	defer video.CancelQueue(store.Dir())

	imagePath := filepath.Join(t.TempDir(), "current.png")
	if err := os.WriteFile(imagePath, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(imagePath, library.CurrentImage{
		PublicName:  "current.png",
		ContentType: "image/png",
	}); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")
	state := srv.state(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"))

	if got, _ := state["hlsURL"].(string); got != "" {
		t.Fatalf("hlsURL = %q, want empty for different active media", got)
	}
	if got, _ := state["shareURL"].(string); !strings.Contains(got, "/image/current") || !strings.Contains(got, current.ID) {
		t.Fatalf("shareURL = %q, want current image URL", got)
	}
}

func TestNormalizeQualityMode(t *testing.T) {
	if normalizeQualityMode("1080") != "1080" {
		t.Fatal("expected 1080 to be accepted")
	}
	if normalizeQualityMode("bad") != "auto" {
		t.Fatal("expected invalid mode to fall back to auto")
	}
}

func TestBitrateOnlyPresetKeepsActiveResolution(t *testing.T) {
	active := video.ResolveQuality("1080", 0)
	requested := video.ResolveQuality("360", 0)
	result := video.BitrateOnlyPreset(requested, active)
	if result.Height != active.Height {
		t.Fatalf("height = %d, want active height %d", result.Height, active.Height)
	}
	if result.VideoBitrate != requested.VideoBitrate {
		t.Fatalf("video bitrate = %s, want requested %s", result.VideoBitrate, requested.VideoBitrate)
	}
	if !result.BitrateOnly {
		t.Fatal("expected bitrate-only flag")
	}
}

func TestVideoQualityPresetForSourceProbeCapsToInputHeight(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoQualityMode = "1080"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	preset := srv.videoQualityPresetForSourceProbe(video.MediaProbe{Streams: []video.MediaStream{
		{CodecType: "video", Width: 1280, Height: 720},
	}})

	if preset.Height != 720 || preset.Effective != "720" {
		t.Fatalf("preset = %+v, want 720p capped by source", preset)
	}
	if preset.Mode != "1080" {
		t.Fatalf("mode = %q, want user setting preserved", preset.Mode)
	}
}

func TestVideoQualityPresetForSourceProbeMarksInterlacedInput(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoQualityMode = "1080"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	preset := srv.videoQualityPresetForSourceProbe(video.MediaProbe{Streams: []video.MediaStream{
		{CodecType: "video", Width: 1920, Height: 1080, FieldOrder: "bb"},
	}})

	if !preset.Deinterlace {
		t.Fatal("expected interlaced source to request deinterlace")
	}
}

func TestOBSRelayConfigEnablesReceiverAndReturnsConnectionInfo(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	body, err := srv.obsRelayConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if body["serverAddress"] == "" || body["streamKey"] == "" || body["rtmpURL"] == "" {
		t.Fatalf("missing OBS relay connection info: %#v", body)
	}
	if !strings.HasPrefix(body["rtmpURL"].(string), body["serverAddress"].(string)+"/") {
		t.Fatalf("rtmpURL = %q, serverAddress = %q", body["rtmpURL"], body["serverAddress"])
	}
	if enabled, _ := body["videoPlayerEnabled"].(bool); !enabled {
		t.Fatalf("videoPlayerEnabled = %#v, want true", body["videoPlayerEnabled"])
	}
	appSettings, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !appSettings.VideoPlayerEnabled {
		t.Fatal("expected relay config request to enable video player support")
	}
}

func TestHandleOBSLatencyNormalizesStorage(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.obs = nil

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/obs/latency", strings.NewReader(`{"mode":"  low  ","dvr":true}`))
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	srv.admin(srv.handleOBSLatency)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	appSettings, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if appSettings.OBSLatencyMode != obsrtmp.LatencyModeRTSPLow {
		t.Fatalf("OBSLatencyMode = %q, want %q", appSettings.OBSLatencyMode, obsrtmp.LatencyModeRTSPLow)
	}
	if appSettings.OBSDVREnabled {
		t.Fatal("DVR flag must stay disabled for OBS latency transports")
	}
}

func TestHandleEncoderModePersistsAndStateReflects(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/encoder-mode", strings.NewReader(`{"mode":"gpu"}`))
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	srv.admin(srv.handleEncoderMode)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	appSettings, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if appSettings.EncoderMode != "gpu" {
		t.Fatalf("EncoderMode = %q, want gpu", appSettings.EncoderMode)
	}
	state := srv.videoQualityState()
	if got, _ := state["encoderMode"].(string); got != "gpu" {
		t.Fatalf("state encoderMode = %#v, want gpu", state["encoderMode"])
	}
}

func TestHandleEncoderModeRejectsInvalidMode(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/encoder-mode", strings.NewReader(`{"mode":"banana"}`))
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	srv.admin(srv.handleEncoderMode)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
	}
}

func TestOBSStateIncludesLatencyCapabilities(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	status := srv.obsState()
	if len(status.Capabilities) != 5 {
		t.Fatalf("capabilities len = %d, want 5", len(status.Capabilities))
	}
	got := map[string]obsrtmp.LatencyCapability{}
	for _, capability := range status.Capabilities {
		got[capability.Mode] = capability
	}

	for _, mode := range []string{obsrtmp.LatencyModeHLSHigh, obsrtmp.LatencyModeHLS, obsrtmp.LatencyModeRTSPLow, obsrtmp.LatencyModeRTSPUltra, obsrtmp.LatencyModeRTSPRealtime} {
		if _, ok := got[mode]; !ok {
			t.Fatalf("missing capability for mode %q", mode)
		}
	}
	if got[obsrtmp.LatencyModeRTSPLow].Label != "低遅延RTSP" || got[obsrtmp.LatencyModeRTSPLow].Experimental {
		t.Fatalf("RTSP low capability = %#v, want production RTSP low label", got[obsrtmp.LatencyModeRTSPLow])
	}
	if got[obsrtmp.LatencyModeRTSPRealtime].Transport != obsrtmp.LatencyModeRTSPT {
		t.Fatalf("RTSP realtime capability = %#v, want RTSPT transport", got[obsrtmp.LatencyModeRTSPRealtime])
	}
}

func TestApplyOBSPreviewURLIncludesRTSPTransport(t *testing.T) {
	status := obsrtmp.Status{
		Connected: true,
		MediaID:   "rtsp-session",
		RTSPTURL:  "rtsp://8.8.8.8:52000/obs_rtsp-session",
		Latency:   obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPRealtime),
	}

	applyOBSPreviewURL(&status, func(path string) string {
		return "http://127.0.0.1:8080" + path
	}, func(id, name string) bool {
		return id == "rtsp-session" && name == video.PlaylistName("rtsp-session")
	})

	want := "http://127.0.0.1:8080/stream/rtsp-session/" + video.PlaylistName("rtsp-session")
	if status.PreviewURL != want {
		t.Fatalf("PreviewURL = %q, want %q", status.PreviewURL, want)
	}
	rows := obsConnectionRows(status)
	var sawHLSPreview bool
	for _, row := range rows {
		if row.Protocol == "HLS Preview" {
			sawHLSPreview = true
		}
	}
	if !sawHLSPreview {
		t.Fatalf("connection rows = %#v, want HLS Preview row", rows)
	}
}

func TestMergeOBSConnectionRowsPrefersRealReadersAndKeepsPreview(t *testing.T) {
	realRows := []obsrtmp.ConnectionStatus{{
		IP:       "192.0.2.88",
		Protocol: "RTSP/TCP",
		State:    "接続中",
	}}
	syntheticRows := []obsrtmp.ConnectionStatus{
		{IP: "8.8.8.8", Protocol: "RTSP/TCP", State: "接続中"},
		{IP: "local", Protocol: "HLS Preview", State: "接続中"},
	}

	rows := mergeOBSConnectionRows(realRows, syntheticRows)

	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want real reader plus preview: %#v", len(rows), rows)
	}
	if rows[0].IP != "192.0.2.88" {
		t.Fatalf("first row = %#v, want real reader", rows[0])
	}
	if rows[1].Protocol != "HLS Preview" {
		t.Fatalf("second row = %#v, want preview row", rows[1])
	}
	for _, row := range rows {
		if row.IP == "8.8.8.8" {
			t.Fatalf("synthetic RTSP row leaked into real rows: %#v", rows)
		}
	}
}

func TestApplyOBSPreviewURLWaitsForReadableHLS(t *testing.T) {
	status := obsrtmp.Status{
		Connected: true,
		MediaID:   "rtsp-session",
		Latency:   obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPRealtime),
	}

	applyOBSPreviewURL(&status, func(path string) string {
		return "http://127.0.0.1:8080" + path
	}, func(id, name string) bool {
		return false
	})

	if status.PreviewURL != "" {
		t.Fatalf("PreviewURL = %q, want empty until HLS is readable", status.PreviewURL)
	}
}

func TestOBSEntryPlaylistAliasDoesNotRewriteChildPlaylists(t *testing.T) {
	id := "abc123"
	for _, name := range []string{"current.m3u8", video.PlaylistName(id), ".", "/"} {
		if !isOBSEntryPlaylistAlias(id, name) {
			t.Errorf("entry alias %q was not recognized", name)
		}
	}
	for _, name := range []string{"media_0.m3u8", "stream.m3u8", "index.m3u8"} {
		if isOBSEntryPlaylistAlias(id, name) {
			t.Errorf("child playlist %q was incorrectly treated as an entry alias", name)
		}
	}
}

func TestHistoryTargetModeTreatsSavedOBSRecordingAsFile(t *testing.T) {
	mode := historyTargetMode(library.CurrentImage{
		Kind:       "video",
		SourceKind: "obs",
		PublicName: "obs-abc123.mp4",
	})
	if mode != "file" {
		t.Fatalf("historyTargetMode(saved OBS recording) = %q, want file", mode)
	}
}

func TestOBSLatencyAliasesAndCapabilitySurface(t *testing.T) {
	// Legacy aliases (and whitespace/case) normalize onto the canonical
	// transports without ever inventing a new one.
	aliases := map[string]string{
		"auto":   obsrtmp.LatencyModeHLS,
		"normal": obsrtmp.LatencyModeHLS,
		"low":    obsrtmp.LatencyModeRTSPLow,
		"ultra":  obsrtmp.LatencyModeRTSPUltra,
		" HLS ":  obsrtmp.LatencyModeHLS,
		"RTSPT":  obsrtmp.LatencyModeRTSPRealtime,
		"bogus":  obsrtmp.LatencyModeHLS,
	}
	for in, want := range aliases {
		if got := obsrtmp.NormalizeLatencyMode(in); got != want {
			t.Fatalf("NormalizeLatencyMode(%q) = %q, want %q", in, got, want)
		}
	}

	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	caps := map[string]obsrtmp.LatencyCapability{}
	for _, c := range srv.obsState().Capabilities {
		caps[c.Mode] = c
	}
	transports := map[string]string{
		obsrtmp.LatencyModeHLSHigh:      obsrtmp.LatencyModeHLS,
		obsrtmp.LatencyModeHLS:          obsrtmp.LatencyModeHLS,
		obsrtmp.LatencyModeRTSPLow:      obsrtmp.LatencyModeRTSPT,
		obsrtmp.LatencyModeRTSPUltra:    obsrtmp.LatencyModeRTSPT,
		obsrtmp.LatencyModeRTSPRealtime: obsrtmp.LatencyModeRTSPT,
	}
	for mode, transport := range transports {
		c, ok := caps[mode]
		if !ok {
			t.Fatalf("missing capability for mode %q", mode)
		}
		if !c.Available || !c.Selectable {
			t.Fatalf("%s capability must be available and selectable: %#v", mode, c)
		}
		if c.Experimental {
			t.Fatalf("%s experimental = true, want false", mode)
		}
		if c.Transport != transport {
			t.Fatalf("%s transport = %q, want %q", mode, c.Transport, transport)
		}
	}

	// With no active session, no transport leaks a preview URL.
	if url := srv.obsState().PreviewURL; url != "" {
		t.Fatalf("idle state should expose no preview URL, got %q", url)
	}
}

type fakeRTSPMapping struct {
	ip         string
	port       int
	closeCalls atomic.Int32
}

func (m *fakeRTSPMapping) ExternalIP() string {
	return m.ip
}

func (m *fakeRTSPMapping) ExternalPort() int {
	return m.port
}

func (m *fakeRTSPMapping) Close() error {
	m.closeCalls.Add(1)
	return nil
}

type rtspMapCall struct {
	protocol     string
	internalPort int
	externalPort int
	description  string
}

func waitForRTSPReadyTest(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RTSP publication update")
	}
}

func TestRTSPReadyDoesNotBlockOnUPnPMapping(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	entered := make(chan struct{})
	release := make(chan struct{})
	srv.mapRTSPPort = func(string, int, int, string) (rtspMappingHandle, upnp.Result) {
		close(entered)
		<-release
		return nil, upnp.Result{Message: "mapping released"}
	}
	srv.setRTSPURL = func(obsrtmp.RTSPEndpoint, string, string) bool { return true }
	defer close(release)

	returned := make(chan struct{})
	go func() {
		srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
			SessionID: "session",
			Port:      49152,
			Path:      "obs_session",
		})
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("handleRTSPReady blocked on UPnP mapping")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("UPnP mapping did not start asynchronously")
	}
}

func TestRTSPReadyRejectsStaleEndpointAfterReplacement(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	var activeGeneration atomic.Uint64
	activeGeneration.Store(1)
	srv.obsSessionActive = func(_ string, generation uint64) bool {
		return activeGeneration.Load() == generation
	}
	aMapStarted := make(chan struct{})
	releaseAMap := make(chan struct{})
	aMapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 52001}
	bMapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 52002}
	srv.mapRTSPPort = func(_ string, internalPort, _ int, _ string) (rtspMappingHandle, upnp.Result) {
		if internalPort == 5001 {
			close(aMapStarted)
			<-releaseAMap
			return aMapping, upnp.Result{OK: true, ExternalIP: aMapping.ip}
		}
		return bMapping, upnp.Result{OK: true, ExternalIP: bMapping.ip}
	}
	updates := make(chan string, 2)
	srv.setRTSPURL = func(endpoint obsrtmp.RTSPEndpoint, _, _ string) bool {
		updates <- endpoint.SessionID
		return true
	}

	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{SessionID: "a", Generation: 1, Port: 5001, Path: "obs_a"})
	select {
	case <-aMapStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("A mapping did not start")
	}
	activeGeneration.Store(2)
	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{SessionID: "b", Generation: 2, Port: 5002, Path: "obs_b"})
	select {
	case got := <-updates:
		if got != "b" {
			t.Fatalf("published session = %q, want B", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("B mapping was not published")
	}

	close(releaseAMap)
	deadline := time.After(2 * time.Second)
	for aMapping.closeCalls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("stale A mapping was not closed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case got := <-updates:
		t.Fatalf("stale A published %q after B", got)
	case <-time.After(100 * time.Millisecond):
	}
	srv.mu.RLock()
	if srv.rtspSessionID != "b" || srv.rtspMap == nil {
		t.Fatalf("stale A changed active mapping: session=%q mapping=%#v", srv.rtspSessionID, srv.rtspMap)
	}
	srv.mu.RUnlock()
}

func TestOBSBlockedOldStartDoesNotDelayReplacementServerCommit(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	var activeGeneration atomic.Uint64
	activeGeneration.Store(1)
	srv.obsSessionActive = func(_ string, generation uint64) bool { return activeGeneration.Load() == generation }
	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	srv.beforeOBSCommit = func(session obsrtmp.Session) {
		if session.Generation == 1 {
			close(enteredA)
			<-releaseA
		}
	}
	aDone := make(chan struct{})
	go func() {
		srv.handleOBSStreamStart(obsrtmp.Session{ID: "a", Generation: 1, Recording: "a.mp4"})
		close(aDone)
	}()
	select {
	case <-enteredA:
	case <-time.After(2 * time.Second):
		t.Fatal("A did not block before commit")
	}
	activeGeneration.Store(2)
	srv.handleOBSStreamStart(obsrtmp.Session{ID: "b", Generation: 2, Recording: "b.mp4"})
	if current := store.Current(); current == nil || current.ID != "b" {
		t.Fatalf("B commit did not complete while A was blocked: %+v", current)
	}
	close(releaseA)
	select {
	case <-aDone:
	case <-time.After(2 * time.Second):
		t.Fatal("A did not return after release")
	}
	if current := store.Current(); current == nil || current.ID != "b" {
		t.Fatalf("released A rewound B current state: %+v", current)
	}
}

func TestRTSPReadyPublishesUPnPURL(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mappings := []*fakeRTSPMapping{
		{ip: "8.8.8.8", port: 52000},
		{ip: "8.8.8.8", port: 52001},
		{ip: "8.8.8.8", port: 52002},
	}
	var calls []rtspMapCall
	srv.mapRTSPPort = func(protocol string, internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result) {
		calls = append(calls, rtspMapCall{protocol: protocol, internalPort: internalPort, externalPort: externalPort, description: description})
		mapping := mappings[len(calls)-1]
		return mapping, upnp.Result{OK: true, ExternalIP: mapping.ip}
	}
	var updatedSession, updatedURL, updatedMessage string
	updated := make(chan struct{}, 1)
	srv.setRTSPURL = func(endpoint obsrtmp.RTSPEndpoint, publicURL, message string) bool {
		updatedSession = endpoint.SessionID
		updatedURL = publicURL
		updatedMessage = message
		select {
		case updated <- struct{}{}:
		default:
		}
		return true
	}

	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
		SessionID: "new-session",
		Port:      49152,
		RTPPort:   49153,
		RTCPPort:  49154,
		Path:      "obs_new-session",
		LocalURL:  "rtsp://192.168.1.10:49152/obs_new-session",
	})
	waitForRTSPReadyTest(t, updated)

	wantCalls := []rtspMapCall{
		{protocol: "TCP", internalPort: 49152, externalPort: 49152, description: "ImagePadServer RTSP TCP"},
		{protocol: "UDP", internalPort: 49153, externalPort: 49153, description: "ImagePadServer RTSP RTP"},
		{protocol: "UDP", internalPort: 49154, externalPort: 49154, description: "ImagePadServer RTSP RTCP"},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("mapped calls = %#v, want %#v", calls, wantCalls)
	}
	for i, want := range wantCalls {
		if calls[i] != want {
			t.Fatalf("mapped call %d = %#v, want %#v", i, calls[i], want)
		}
	}
	if got, want := updatedSession, "new-session"; got != want {
		t.Fatalf("updated session = %q, want %q", got, want)
	}
	if got, want := updatedURL, "rtsp://8.8.8.8:52000/obs_new-session"; got != want {
		t.Fatalf("updated URL = %q, want %q", got, want)
	}
	if !strings.Contains(updatedMessage, "UPnP") {
		t.Fatalf("updated message = %q, want UPnP status", updatedMessage)
	}
	if srv.rtspMap == nil || srv.rtspSessionID != "new-session" {
		t.Fatalf("stored mapping/session = %#v/%q", srv.rtspMap, srv.rtspSessionID)
	}
}

func TestRadioRTSPReadyPublishesUPnPURLToRadioStatus(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 52000}
	srv.mapRTSPPort = func(protocol string, internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result) {
		return mapping, upnp.Result{OK: true, ExternalIP: mapping.ip}
	}
	var updatedSession, updatedURL, updatedMessage string
	updated := make(chan struct{}, 1)
	srv.setRadioRTSPURL = func(endpoint obsrtmp.RTSPEndpoint, publicURL, message string) bool {
		updatedSession = endpoint.SessionID
		updatedURL = publicURL
		updatedMessage = message
		select {
		case updated <- struct{}{}:
		default:
		}
		return true
	}

	srv.handleRadioRTSPReady(obsrtmp.RTSPEndpoint{
		SessionID: "radio-session",
		Port:      52000,
		Path:      "radio_session",
		LocalURL:  "rtsp://192.168.0.10:52000/radio_session",
	})
	waitForRTSPReadyTest(t, updated)

	if got, want := updatedSession, "radio-session"; got != want {
		t.Fatalf("radio updated session = %q, want %q", got, want)
	}
	if got, want := updatedURL, "rtsp://8.8.8.8:52000/radio_session"; got != want {
		t.Fatalf("radio updated URL = %q, want %q", got, want)
	}
	if !strings.Contains(updatedMessage, "UPnP") {
		t.Fatalf("radio updated message = %q, want UPnP status", updatedMessage)
	}
}

func TestRTSPReadyMappingFailureKeepsLANURL(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.mapRTSPPort = func(string, int, int, string) (rtspMappingHandle, upnp.Result) {
		return nil, upnp.Result{Message: "no UPnP gateway found"}
	}
	var updatedURL, updatedMessage string
	updated := make(chan struct{}, 1)
	srv.setRTSPURL = func(_ obsrtmp.RTSPEndpoint, publicURL, message string) bool {
		updatedURL = publicURL
		updatedMessage = message
		select {
		case updated <- struct{}{}:
		default:
		}
		return true
	}

	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
		SessionID: "session",
		Port:      49152,
		Path:      "obs_session",
		LocalURL:  "rtsp://192.168.1.10:49152/obs_session",
	})
	waitForRTSPReadyTest(t, updated)

	if got, want := updatedURL, ""; got != want {
		t.Fatalf("updated URL = %q, want %q", got, want)
	}
	if !strings.Contains(updatedMessage, "no UPnP gateway found") {
		t.Fatalf("updated message = %q", updatedMessage)
	}
	if srv.rtspMap != nil {
		t.Fatalf("failed mapping was stored: %#v", srv.rtspMap)
	}
}

func TestRTSPReadyRejectsCarrierNATAddress(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mapping := &fakeRTSPMapping{ip: "100.64.1.2", port: 49152}
	srv.mapRTSPPort = func(string, int, int, string) (rtspMappingHandle, upnp.Result) {
		return mapping, upnp.Result{OK: true, ExternalIP: mapping.ip}
	}
	var updatedURL, updatedMessage string
	updated := make(chan struct{}, 1)
	srv.setRTSPURL = func(_ obsrtmp.RTSPEndpoint, publicURL, message string) bool {
		updatedURL = publicURL
		updatedMessage = message
		select {
		case updated <- struct{}{}:
		default:
		}
		return true
	}

	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
		SessionID: "session",
		Port:      49152,
		Path:      "obs_session",
		LocalURL:  "rtsp://192.168.1.10:49152/obs_session",
	})
	waitForRTSPReadyTest(t, updated)

	if got, want := updatedURL, ""; got != want {
		t.Fatalf("updated URL = %q, want %q", got, want)
	}
	if !strings.Contains(updatedMessage, "CGNAT") {
		t.Fatalf("updated message = %q, want CGNAT explanation", updatedMessage)
	}
	if got := mapping.closeCalls.Load(); got != 1 {
		t.Fatalf("mapping close calls = %d, want 1", got)
	}
}

func TestRTSPDoneDoesNotCloseNewerMapping(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 49152}
	srv.rtspMap = mapping
	srv.rtspSource = "obs"
	srv.rtspSessionID = "new-session"
	srv.rtspGeneration = 2

	srv.handleRTSPDone(obsrtmp.RTSPEndpoint{SessionID: "new-session", Generation: 1})
	if got := mapping.closeCalls.Load(); got != 0 {
		t.Fatalf("stale done closed mapping %d times", got)
	}
	srv.rtspSource = "radio"
	srv.handleRTSPDone(obsrtmp.RTSPEndpoint{SessionID: "new-session", Generation: 2})
	if got := mapping.closeCalls.Load(); got != 0 {
		t.Fatalf("cross-source done closed mapping %d times", got)
	}
	srv.rtspSource = "obs"
	srv.handleRTSPDone(obsrtmp.RTSPEndpoint{SessionID: "new-session", Generation: 2})
	if got := mapping.closeCalls.Load(); got != 1 {
		t.Fatalf("matching done closed mapping %d times, want 1", got)
	}
	if srv.rtspMap != nil || srv.rtspSessionID != "" {
		t.Fatalf("mapping ownership not cleared: %#v/%q", srv.rtspMap, srv.rtspSessionID)
	}
}

func TestStopOBSReceiverClosesRTSPMapping(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 49152}
	srv.rtspMap = mapping
	srv.rtspSource = "obs"
	srv.rtspSessionID = "session"

	srv.StopOBSReceiver()
	if got := mapping.closeCalls.Load(); got != 1 {
		t.Fatalf("mapping close calls = %d, want 1", got)
	}
}

func TestPairingIssuesRelayDeviceAndSignedRelayAuthWorks(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	requestBody := `{"clientName":"BrowserRelayStreamer","deviceName":"Relay PC"}`
	req := httptest.NewRequest(http.MethodPost, "http://192.168.1.20:8080/api/pairing/request", strings.NewReader(requestBody))
	req.RemoteAddr = "192.168.1.50:50000"
	rr := httptest.NewRecorder()
	srv.handlePairingRequest(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("request status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var pairingResp struct {
		PairingID string `json:"pairingId"`
		Nonce     string `json:"nonce"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &pairingResp); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	pairing := srv.pairings[pairingResp.PairingID]
	srv.mu.Unlock()
	if pairing.PIN == "" {
		t.Fatal("expected pairing PIN to be stored for UI display")
	}
	proof := hmacSHA256Hex(pairing.PIN, strings.Join([]string{pairing.ID, pairing.Nonce, "BrowserRelayStreamer", "Relay PC"}, "\n"))
	confirmBody := fmt.Sprintf(`{"pairingId":%q,"clientName":"BrowserRelayStreamer","deviceName":"Relay PC","proof":%q}`, pairingResp.PairingID, proof)
	req = httptest.NewRequest(http.MethodPost, "http://192.168.1.20:8080/api/pairing/confirm", strings.NewReader(confirmBody))
	req.RemoteAddr = "192.168.1.50:50000"
	rr = httptest.NewRecorder()
	srv.handlePairingConfirm(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var device struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &device); err != nil {
		t.Fatal(err)
	}
	if device.ClientID == "" || device.ClientSecret == "" || device.Scope != relayScope {
		t.Fatalf("bad device response: %#v", device)
	}

	authReq := signedRelayRequest(t, device.ClientID, device.ClientSecret, "nonce-1")
	if !srv.relayDeviceAllowed(authReq) {
		t.Fatal("expected signed relay request to authenticate")
	}
	replayed := signedRelayRequest(t, device.ClientID, device.ClientSecret, "nonce-1")
	if srv.relayDeviceAllowed(replayed) {
		t.Fatal("expected nonce replay to be rejected")
	}
}

func TestVideoURLDownloadError(t *testing.T) {
	msg := videoURLDownloadError(fmt.Errorf("not found"))
	if !strings.Contains(msg, "yt-dlp") {
		t.Fatalf("message = %q, want yt-dlp guidance", msg)
	}
}

func TestUIKeepsActionErrorToastAcrossSuccessfulStateRefresh(t *testing.T) {
	if !strings.Contains(indexHTML, "toast.dataset.errorSource === 'sync'") {
		t.Fatal("state refresh success should only hide sync error toasts")
	}
	if !strings.Contains(indexHTML, "showToast(syncFailureMessage(error), { error: true, source: 'sync' })") {
		t.Fatal("state refresh failures should mark their toasts as sync errors")
	}
}

func TestPlaylistRTSPDisplayRequiresPublicMapping(t *testing.T) {
	if !strings.Contains(indexHTML, "plState.rtspPublic") {
		t.Fatal("playlist RTSP display must require rtspPublic so UPnP failure does not expose local RTSP")
	}
}

func TestYTDLPLoginAndCookieDeleteAPI(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	srv, mux := testServer(t, false)
	defer cleanupTestServer(srv)

	oldLauncher := ytdlpLoginLauncher
	defer func() { ytdlpLoginLauncher = oldLauncher }()
	launched := false
	ytdlpLoginLauncher = func() error {
		launched = true
		if err := os.MkdirAll(filepath.Dir(ytdlpauth.CookieFilePath()), 0700); err != nil {
			return err
		}
		return os.WriteFile(ytdlpauth.CookieFilePath(), []byte("# Netscape HTTP Cookie File\n"), 0600)
	}

	req := adminRequest("http://127.0.0.1:8080/api/ytdlp/login", "127.0.0.1:1234")
	req.Method = http.MethodPost
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%q", rec.Code, rec.Body.String())
	}
	if !launched {
		t.Fatal("expected login launcher to run")
	}
	if !strings.Contains(rec.Body.String(), `"saved":true`) {
		t.Fatalf("login body = %q, want saved true", rec.Body.String())
	}

	req = adminRequest("http://127.0.0.1:8080/api/ytdlp/cookies", "127.0.0.1:1234")
	req.Method = http.MethodDelete
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%q", rec.Code, rec.Body.String())
	}
	if ytdlpauth.Status().Saved {
		t.Fatal("cookie delete API should remove saved cookies")
	}
}

func TestSoundCloudCurrentInfoUsesVideoPresentationAndSoundCloudSource(t *testing.T) {
	media := video.DownloadedMedia{
		SourcePath: "track.m4a",
		Name:       "track.m4a",
		Kind:       "soundcloud",
	}
	info := soundCloudCurrentInfo(media, "current-video.m4a", "thumb.jpg")
	if info.Kind != "video" {
		t.Fatalf("Kind = %q, want video so existing preview/history paths treat it as media", info.Kind)
	}
	if info.SourceKind != "soundcloud" {
		t.Fatalf("SourceKind = %q, want soundcloud", info.SourceKind)
	}
	if info.Thumbnail != "thumb.jpg" {
		t.Fatalf("Thumbnail = %q, want thumb.jpg", info.Thumbnail)
	}
}

func signedRelayRequest(t *testing.T, clientID, clientSecret, nonce string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://192.168.1.20:8080/api/obs/relay-config", nil)
	req.RemoteAddr = "192.168.1.50:50000"
	timestamp := time.Now().UTC().Format(time.RFC3339)
	bodyHash := sha256.Sum256(nil)
	message := strings.Join([]string{
		req.Method,
		req.URL.RequestURI(),
		timestamp,
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	req.Header.Set("X-ImagePad-Client-Id", clientID)
	req.Header.Set("X-ImagePad-Timestamp", timestamp)
	req.Header.Set("X-ImagePad-Nonce", nonce)
	req.Header.Set("X-ImagePad-Signature", hmacSHA256Base64URL(clientSecret, message))
	return req
}

func TestAutoQualityPrefersUploadBandwidth(t *testing.T) {
	preset := video.ResolveQualityForUpload("auto", 100, 3)
	if preset.Effective != "360" {
		t.Fatalf("effective = %s, want 360 from upload bandwidth", preset.Effective)
	}
	preset = video.ResolveQualityForUpload("auto", 20, 0)
	if preset.Effective != "1080" {
		t.Fatalf("effective = %s, want download fallback", preset.Effective)
	}
}

func TestHandleNetworkCheckSurfacesSettingsSaveFailure(t *testing.T) {
	srv, mux := testServer(t, false)
	defer cleanupTestServer(srv)

	oldMeasurer := networkMeasurer
	t.Cleanup(func() { networkMeasurer = oldMeasurer })
	networkMeasurer = func() video.NetworkMeasurement {
		return video.NetworkMeasurement{UploadMbps: 12}
	}

	notDir := filepath.Join(t.TempDir(), "settings-as-file")
	if err := os.WriteFile(notDir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_DATA_DIR", notDir)

	req := httptest.NewRequest(http.MethodPost, "/api/network-check", nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when settings cannot be saved; body = %q", rec.Code, rec.Body.String())
	}
}

func adminRequest(rawURL, remoteAddr string) *http.Request {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		panic(err)
	}
	req.RemoteAddr = remoteAddr
	return req
}

func mustURL(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u
}

func slowFFmpegPath(t *testing.T) string {
	t.Helper()
	realFFmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found; install ffmpeg with your package manager or add it to PATH; you can also set IMAGEPAD_FFMPEG")
	}
	dir := t.TempDir()
	if filepath.Separator == '\\' {
		t.Setenv("IMAGEPAD_REAL_FFMPEG", realFFmpeg)
		path := filepath.Join(dir, "ffmpeg.cmd")
		if err := os.WriteFile(path, []byte("@echo off\r\nif \"%~1\"==\"-version\" (echo ffmpeg test stub & exit /b 0)\r\nping -n 6 127.0.0.1 > nul\r\n\"%IMAGEPAD_REAL_FFMPEG%\" %*\r\n"), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = \"-version\" ]; then echo 'ffmpeg test stub'; exit 0; fi\nsleep 5\nexec \"$IMAGEPAD_REAL_FFMPEG\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_REAL_FFMPEG", realFFmpeg)
	return path
}
func TestOptionsFromValuesQualityPresets(t *testing.T) {
	values := url.Values{
		"format":       {"webp"},
		"quality":      {"high"},
		"maxDimension": {"4096"},
		"maxMB":        {"60"},
	}
	opts := optionsFromValues(values.Get)
	if opts.Format != "webp" {
		t.Fatalf("Format = %q, want webp", opts.Format)
	}
	if opts.JPEGQuality != 85 {
		t.Fatalf("JPEGQuality = %d, want 85", opts.JPEGQuality)
	}
	if opts.WebPQuality != 80 {
		t.Fatalf("WebPQuality = %d, want 80", opts.WebPQuality)
	}
	if opts.PNGQuality != "high" {
		t.Fatalf("PNGQuality = %q, want high", opts.PNGQuality)
	}
	if opts.MaxDimension != 4096 {
		t.Fatalf("MaxDimension = %d, want 4096", opts.MaxDimension)
	}
	if opts.MaxBytes != 60<<20 {
		t.Fatalf("MaxBytes = %d, want %d", opts.MaxBytes, int64(60<<20))
	}
}

func TestOptionsFromValuesLegacyJPEGQuality(t *testing.T) {
	values := url.Values{"quality": {"88"}}
	opts := optionsFromValues(values.Get)
	if opts.JPEGQuality != 88 {
		t.Fatalf("JPEGQuality = %d, want 88", opts.JPEGQuality)
	}
	if opts.WebPQuality != 80 {
		t.Fatalf("WebPQuality = %d, want default 80", opts.WebPQuality)
	}
	if opts.PNGQuality != "lossless" {
		t.Fatalf("PNGQuality = %q, want default lossless", opts.PNGQuality)
	}
}

func TestOptionsFromValuesPNGLossless(t *testing.T) {
	values := url.Values{
		"format":  {"png"},
		"quality": {"lossless"},
	}
	opts := optionsFromValues(values.Get)
	if opts.Format != "png" {
		t.Fatalf("Format = %q, want png", opts.Format)
	}
	if opts.PNGQuality != "lossless" {
		t.Fatalf("PNGQuality = %q, want lossless", opts.PNGQuality)
	}
}

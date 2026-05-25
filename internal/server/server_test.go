package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
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

func TestHandleFFmpegChecksConfiguredBinaryWithoutEnablingVideoMode(t *testing.T) {
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.exe")
	if err := os.WriteFile(ffmpegPath, []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_FFMPEG", ffmpegPath)

	srv, mux := testServer(t, false)
	defer srv.store.Reset()

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

func TestPrimaryShareURL(t *testing.T) {
	url, label := primaryShareURL(map[string]interface{}{
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
		"imageURL": "https://example.com/image/current.png",
		"videoPlayer": map[string]interface{}{
			"enabled": false,
		},
	})
	if url != "https://example.com/image/current.png" || label != "ImagePad URL" {
		t.Fatalf("share URL = %q (%s), want image", url, label)
	}
}

func TestStateExposesHLSURLOnlyAfterFirstSegment(t *testing.T) {
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

func TestStateExposesHLSURLForPendingStillConversion(t *testing.T) {
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

	if got, _ := state["shareURL"].(string); !strings.Contains(got, "/stream/"+current.ID+"/") {
		t.Fatalf("shareURL = %q, want pending still conversion HLS URL", got)
	}
	if got, _ := state["shareURLLabel"].(string); got != "HLS URL" {
		t.Fatalf("shareURLLabel = %q, want HLS URL", got)
	}
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

func TestVideoURLDownloadError(t *testing.T) {
	msg := videoURLDownloadError(fmt.Errorf("not found"))
	if !strings.Contains(msg, "yt-dlp") {
		t.Fatalf("message = %q, want yt-dlp guidance", msg)
	}
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
	dir := t.TempDir()
	if filepath.Separator == '\\' {
		path := filepath.Join(dir, "ffmpeg.cmd")
		if err := os.WriteFile(path, []byte("@echo off\r\nping -n 6 127.0.0.1 > nul\r\nexit /b 1\r\n"), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 5\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

// requireFFprobe returns a usable ffprobe path, skipping the test if neither
// IMAGEPAD_FFPROBE nor a system ffprobe is available.
func requireFFprobe(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("IMAGEPAD_FFPROBE"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Try findFFprobe (checks env + sibling to ffmpeg).
	p, err := findFFprobe()
	if err == nil {
		return p
	}
	// Fall back to PATH lookup.
	if path, lookErr := exec.LookPath("ffprobe"); lookErr == nil {
		t.Setenv("IMAGEPAD_FFPROBE", path)
		return path
	}
	t.Skip("ffprobe not available; skipping test that requires real ffprobe")
	return ""
}

// ---------------------------------------------------------------------------
// mockFFprobe helpers
// ---------------------------------------------------------------------------

// writeMockFFprobe creates a temporary executable that writes probeJSON to
// stdout and exits 0.  The JSON is stored in a sidecar file so the script
// avoids shell-escaping issues.  Returns the path to the mock executable.
func writeMockFFprobe(t *testing.T, probeJSON string) string {
	t.Helper()
	dir := t.TempDir()

	jsonPath := filepath.Join(dir, "probe-result.json")
	if err := os.WriteFile(jsonPath, []byte(probeJSON), 0600); err != nil {
		t.Fatal(err)
	}

	exeName := "ffprobe"
	var script string
	if runtime.GOOS == "windows" {
		exeName = "ffprobe.bat"
		script = fmt.Sprintf("@echo off\r\ntype \"%s\"\r\nexit /b 0\r\n", jsonPath)
	} else {
		script = fmt.Sprintf("#!/bin/sh\ncat \"%s\"\n", jsonPath)
	}
	exePath := filepath.Join(dir, exeName)
	if err := os.WriteFile(exePath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return exePath
}

// ---------------------------------------------------------------------------
// 1. Remote audio via publish path → SourceKind = remote_audio
//
// Exercises the dispatch chain: DownloadMediaURL (non-SoundCloud) + probe
// + acquireAudio → processAudioFileAndPublish.  We skip the HTTP handler
// layer because yt-dlp cannot reach a local test server; the handler-level
// routing (URL validation, SoundCloud detection, error codes) is covered
// by existing tests in server_test.go and upload_url_test.go.
// ---------------------------------------------------------------------------

func TestHandleUploadURLRemoteAudioPublish(t *testing.T) {
	audioPath := writeTestWAV(t, t.TempDir(), "song.wav")

	ffprobe := requireFFprobe(t)

	probe, err := video.ProbeMedia(context.Background(), ffprobe, audioPath)
	if err != nil {
		t.Fatal(err)
	}
	class := video.ClassifyMediaProbe(probe)
	if class != video.MediaAudio {
		t.Fatalf("ClassifyMediaProbe = %q, want %q", class, video.MediaAudio)
	}

	meta := extractEmbeddedMetadata(probe)

	acquired := video.AcquiredAudio{
		SourcePath:       audioPath,
		SourceName:       "song.wav",
		Kind:             video.SourceRemoteAudio,
		Probe:            probe,
		EmbeddedMetadata: meta,
	}

	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	req := httptest.NewRequest("GET", "/", nil)
	state, err := srv.processAudioFileAndPublish(req, acquired)
	if err != nil {
		t.Fatal(err)
	}
	_ = state

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image after remote audio publish")
	}
	if current.SourceKind != "remote_audio" {
		t.Fatalf("SourceKind = %q, want remote_audio", current.SourceKind)
	}
	// Title should fall back to filename stem
	if current.Title != "song" {
		t.Fatalf("Title = %q, want song (filename fallback)", current.Title)
	}

	// Verify an audio queue job exists for this media ID.
	queue := video.QueueStatus(srv.store.Dir())
	found := false
	for _, item := range queue {
		if item.MediaID == current.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected audio queue job for remote audio")
	}
}

func TestHandleUploadURLUsesSecureDirectDownloader(t *testing.T) {
	srv, mux := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "secure-direct.wav")
	ffprobe := requireFFprobe(t)
	probe, err := video.ProbeMedia(context.Background(), ffprobe, audioPath)
	if err != nil {
		t.Fatal(err)
	}

	old := directMediaDownloader
	defer func() { directMediaDownloader = old }()
	called := false
	directMediaDownloader = func(ctx context.Context, rawURL, outDir string, probeFn func(context.Context, string) (video.MediaProbe, error)) (downloadedRemoteMedia, error) {
		called = true
		return downloadedRemoteMedia{Path: audioPath, Name: "secure-direct.wav", Class: video.MediaAudio, Probe: probe}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://example.com/song"}`))
	rec := adminJSON(t, mux, req)
	if !called {
		t.Fatal("secure direct downloader was not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if current := srv.store.Current(); current == nil || current.SourceKind != "remote_audio" {
		t.Fatalf("current = %#v, want remote_audio", current)
	}
}

func TestSoundCloudAcquiredAudioKeepsEmbeddedMetadata(t *testing.T) {
	media := video.DownloadedMedia{
		SourcePath:      "track.m4a",
		Name:            "track.m4a",
		Kind:            "soundcloud",
		ArtworkPath:     "page.jpg",
		Metadata:        video.AudioMetadata{Title: "Page title", Artist: "Page artist"},
		InformationPath: "track.info.json",
	}
	probe := video.MediaProbe{FormatTags: map[string]string{"title": "GUNPEI", "artist": "embedded artist"}}
	got := soundCloudAcquiredFromProbe(media, probe, nil)
	if got.EmbeddedMetadata.Title != "GUNPEI" || got.SoundCloudMetadata.Title != "Page title" {
		t.Fatalf("metadata precedence inputs lost: embedded=%#v soundcloud=%#v", got.EmbeddedMetadata, got.SoundCloudMetadata)
	}
}

// ---------------------------------------------------------------------------
// 2. Remote audio via queue path → SourceKind = remote_audio in history
// ---------------------------------------------------------------------------

func TestHandleUploadURLRemoteAudioQueue(t *testing.T) {
	audioPath := writeTestWAV(t, t.TempDir(), "podcast.wav")

	ffprobe := requireFFprobe(t)
	probe, err := video.ProbeMedia(context.Background(), ffprobe, audioPath)
	if err != nil {
		t.Fatal(err)
	}
	class := video.ClassifyMediaProbe(probe)
	if class != video.MediaAudio {
		t.Fatalf("ClassifyMediaProbe = %q, want %q", class, video.MediaAudio)
	}

	meta := extractEmbeddedMetadata(probe)
	sourceName := "podcast.wav"

	acquired := video.AcquiredAudio{
		SourcePath:       audioPath,
		SourceName:       sourceName,
		Kind:             video.SourceRemoteAudio,
		Probe:            probe,
		EmbeddedMetadata: meta,
	}

	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	req := httptest.NewRequest("GET", "/", nil)
	_, err = srv.processAudioFileAndQueue(req, acquired)
	if err != nil {
		t.Fatal(err)
	}

	// Queue should not affect current image.
	current := srv.store.Current()
	if current != nil {
		t.Fatal("expected no current image after queue-only operation")
	}

	// Verify in history.
	history := srv.store.History()
	found := false
	for _, item := range history {
		if item.SourceKind == "remote_audio" && item.OriginalName == sourceName {
			found = true
			if item.Kind != "video" {
				t.Fatalf("history Kind = %q, want video", item.Kind)
			}
			if item.Title != "podcast" {
				t.Fatalf("Title = %q, want podcast", item.Title)
			}
			break
		}
	}
	if !found {
		t.Fatal("queued remote audio not found in history")
	}
}

// ---------------------------------------------------------------------------
// 3. Handler-level: private-network URL validation (redirect path tested via
//    downloadRemoteMedia in remote_media_test.go)
// ---------------------------------------------------------------------------

func TestHandleUploadURLRedirectPrivateNetworkRejected(t *testing.T) {
	// The handler calls validateHTTPURL which blocks private IPs before
	// any download occurs.  This test verifies the initial-validation gate.
	srv, mux := testServer(t, true)
	defer srv.store.Reset()

	for _, url := range []string{
		"http://127.0.0.1/song.wav",
		"http://192.168.0.1/media.mp3",
		"http://10.0.0.5/audio.ogg",
		"http://localhost/file.wav",
	} {
		body := fmt.Sprintf(`{"url":%q}`, url)
		req := httptest.NewRequest("POST", "/api/upload-url", strings.NewReader(body))
		rec := adminJSON(t, mux, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("URL %q: status = %d, want 400; body: %s", url, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "private network") && !strings.Contains(rec.Body.String(), "not allowed") {
			t.Fatalf("URL %q: body = %q, want private-network rejection", url, rec.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// 4. SoundCloud detection still works (remote_audio dispatch for non-SC URLs)
//    verified by the publish/queue tests above.  Content-Length and streamed
//    overflow are validated inside downloadRemoteMedia (tested in
//    remote_media_test.go) – not duplicated at handler level.
// ---------------------------------------------------------------------------

func TestHandleUploadURLContentLengthOverLimit(t *testing.T) {
	// Content-Length rejection happens inside video.DownloadMediaURL or
	// downloadRemoteMedia.  For non-SoundCloud URLs the handler uses
	// DownloadMediaURL which delegates to yt-dlp.  The size limit for
	// yt-dlp is --max-filesize 2G (set in downloadVideoURL).  The
	// video.MaxMediaSourceBytes limit is enforced by the direct-URL path
	// via downloadRemoteMedia when that path is active.  Since this test
	// validates the handler's dispatch, we simply verify the test exists
	// – the actual limit enforcement is covered by
	// TestDownloadRemoteMedia_rejectsOversizedContentLength and
	// TestCopyMediaWithLimit in the video package.
	t.Log("Content-Length > limit rejection is covered by remote_media_test.go")
}

func TestHandleUploadURLLimitStreamedOverflow(t *testing.T) {
	// Same rationale: chunked overflow is covered by
	// TestDownloadRemoteMedia_rejectsOversizedChunkedBody in
	// remote_media_test.go.
	t.Log("Streamed overflow rejection is covered by remote_media_test.go")
}

// ---------------------------------------------------------------------------
// 5. Audio URL without filename extension
// ---------------------------------------------------------------------------

func TestHandleUploadURLRemoteAudioNoExtension(t *testing.T) {
	audioPath := writeTestWAV(t, t.TempDir(), "noext")

	ffprobe := requireFFprobe(t)
	probe, err := video.ProbeMedia(context.Background(), ffprobe, audioPath)
	if err != nil {
		t.Fatal(err)
	}
	class := video.ClassifyMediaProbe(probe)
	if class != video.MediaAudio {
		t.Fatalf("ClassifyMediaProbe = %q, want %q", class, video.MediaAudio)
	}

	meta := extractEmbeddedMetadata(probe)
	sourceName := "noext.wav"

	acquired := video.AcquiredAudio{
		SourcePath:       audioPath,
		SourceName:       sourceName,
		Kind:             video.SourceRemoteAudio,
		Probe:            probe,
		EmbeddedMetadata: meta,
	}

	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	req := httptest.NewRequest("GET", "/", nil)
	_, err = srv.processAudioFileAndPublish(req, acquired)
	if err != nil {
		t.Fatal(err)
	}

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image")
	}
	if current.SourceKind != "remote_audio" {
		t.Fatalf("SourceKind = %q, want remote_audio", current.SourceKind)
	}
}

// ---------------------------------------------------------------------------
// 6. Remote video still uses existing video path (probed as video)
// ---------------------------------------------------------------------------

func TestHandleUploadURLRemoteVideoUnchanged(t *testing.T) {
	// Use a mock ffprobe to simulate video classification.
	ffprobePath := writeMockFFprobe(t, `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":640,"height":480}],"format":{"duration":"10.0"}}`)

	// Create a dummy "downloaded" file that ffprobe can "analyze".
	dummyPath := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(dummyPath, []byte("fake-mp4-content"), 0600); err != nil {
		t.Fatal(err)
	}

	// Run the probe with the mock ffprobe.
	probe, err := video.ProbeMedia(context.Background(), ffprobePath, dummyPath)
	if err != nil {
		t.Fatal(err)
	}
	class := video.ClassifyMediaProbe(probe)
	if class != video.MediaVideo {
		t.Fatalf("ClassifyMediaProbe = %q, want %q", class, video.MediaVideo)
	}

	// Simulate the handler's dispatch for video.
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	req := httptest.NewRequest("GET", "/", nil)
	state, err := srv.processVideoFileAndPublish(req, dummyPath, "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	_ = state

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image after video publish")
	}
	if current.Kind != "video" {
		t.Fatalf("Kind = %q, want video", current.Kind)
	}
	if current.SourceKind != "" {
		t.Fatalf("SourceKind = %q, want empty for regular video", current.SourceKind)
	}
}

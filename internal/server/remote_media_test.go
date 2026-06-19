package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

// ---------------------------------------------------------------------------
// probe helpers
// ---------------------------------------------------------------------------

func probeAudioOnly(_ context.Context, path string) (video.MediaProbe, error) {
	return video.MediaProbe{
		Streams: []video.MediaStream{
			{Index: 0, CodecType: "audio", CodecName: "aac"},
		},
	}, nil
}

func probeVideo(_ context.Context, path string) (video.MediaProbe, error) {
	return video.MediaProbe{
		Streams: []video.MediaStream{
			{Index: 0, CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
			{Index: 1, CodecType: "audio", CodecName: "aac"},
		},
	}, nil
}

// ---------------------------------------------------------------------------
// 1. Content-Length rejection before body read
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_rejectsOversizedContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", video.MaxMediaSourceBytes+1))
		w.Header().Set("Content-Disposition", `attachment; filename="test.mp4"`)
		_, _ = w.Write([]byte("should not be read"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	_, err := downloadRemoteMedia(context.Background(), srv.URL+"/file.mp4", outDir, probeAudioOnly)
	if err == nil {
		t.Fatal("expected error for oversized Content-Length")
	}
	if !errors.Is(err, video.ErrMediaTooLarge) {
		t.Fatalf("err = %v, want ErrMediaTooLarge", err)
	}
}

// ---------------------------------------------------------------------------
// 2. Chunked transfer at limit+1 bytes
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_rejectsOversizedChunkedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="big.mp4"`)
		w.WriteHeader(http.StatusOK)
		// Send limit+1 bytes without Content-Length (chunked).
		data := make([]byte, video.MaxMediaSourceBytes+1)
		for i := range data {
			data[i] = byte(i % 256)
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	outDir := t.TempDir()
	_, err := downloadRemoteMedia(context.Background(), srv.URL+"/big.mp4", outDir, probeAudioOnly)
	if err == nil {
		t.Fatal("expected error for oversized chunked body")
	}
	if !errors.Is(err, video.ErrMediaTooLarge) {
		t.Fatalf("err = %v, want ErrMediaTooLarge", err)
	}
}

// ---------------------------------------------------------------------------
// 3. Redirect to blocked IP
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_rejectsRedirectToBlockedIP(t *testing.T) {
	redirectCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectCount++
		if redirectCount == 1 {
			http.Redirect(w, r, "http://127.0.0.1:8080/secret.mp4", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	_, err := downloadRemoteMedia(context.Background(), srv.URL+"/start", outDir, probeAudioOnly)
	if err == nil {
		t.Fatal("expected error for redirect to blocked IP")
	}
	if !strings.Contains(err.Error(), "private network") && !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v, want private-network rejection", err)
	}
}

// ---------------------------------------------------------------------------
// 4. Content-Disposition filename extraction
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_extractsFilenameFromContentDisposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="presentation.mp4"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("small content"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	result, err := downloadRemoteMedia(context.Background(), srv.URL+"/ignored.mp4", outDir, probeAudioOnly)
	if err != nil {
		t.Fatalf("downloadRemoteMedia failed: %v", err)
	}
	if result.Name != "presentation.mp4" {
		t.Fatalf("Name = %q, want %q", result.Name, "presentation.mp4")
	}
}

// ---------------------------------------------------------------------------
// 5. URL basename fallback
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_fallsBackToURLBasename(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("small content"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	result, err := downloadRemoteMedia(context.Background(), srv.URL+"/downloads/coolvideo.mp4", outDir, probeAudioOnly)
	if err != nil {
		t.Fatalf("downloadRemoteMedia failed: %v", err)
	}
	if result.Name != "coolvideo.mp4" {
		t.Fatalf("Name = %q, want %q", result.Name, "coolvideo.mp4")
	}
}

// ---------------------------------------------------------------------------
// 6. Audio classification via ffprobe
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_classifiesAudioMedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="song.m4a"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-audio-content"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	result, err := downloadRemoteMedia(context.Background(), srv.URL+"/song.m4a", outDir, probeAudioOnly)
	if err != nil {
		t.Fatalf("downloadRemoteMedia failed: %v", err)
	}
	if result.Class != video.MediaAudio {
		t.Fatalf("Class = %q, want %q", result.Class, video.MediaAudio)
	}
}

// ---------------------------------------------------------------------------
// 7. Video preservation
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_preservesVideoClass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="clip.mp4"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-video-content"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	result, err := downloadRemoteMedia(context.Background(), srv.URL+"/clip.mp4", outDir, probeVideo)
	if err != nil {
		t.Fatalf("downloadRemoteMedia failed: %v", err)
	}
	if result.Class != video.MediaVideo {
		t.Fatalf("Class = %q, want %q", result.Class, video.MediaVideo)
	}
}

// ---------------------------------------------------------------------------
// Additional: downloaded file actually exists on disk
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_writesFileToDisk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello-world"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	result, err := downloadRemoteMedia(context.Background(), srv.URL+"/test.bin", outDir, probeAudioOnly)
	if err != nil {
		t.Fatalf("downloadRemoteMedia failed: %v", err)
	}
	if result.Path == "" {
		t.Fatal("Path is empty")
	}
	if _, statErr := os.Stat(result.Path); os.IsNotExist(statErr) {
		t.Fatalf("downloaded file %q does not exist", result.Path)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello-world" {
		t.Fatalf("file content = %q, want %q", string(data), "hello-world")
	}
	if !strings.HasPrefix(result.Path, outDir) {
		t.Fatalf("Path %q not inside outDir %q", result.Path, outDir)
	}
}

// ---------------------------------------------------------------------------
// URL basename from redirect target
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_usesURLBasenameWhenNoContentDisposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	result, err := downloadRemoteMedia(context.Background(), srv.URL+"/path/to/video.mp4", outDir, probeAudioOnly)
	if err != nil {
		t.Fatalf("downloadRemoteMedia failed: %v", err)
	}
	if result.Name != "video.mp4" {
		t.Fatalf("Name = %q, want %q", result.Name, "video.mp4")
	}
}

// ---------------------------------------------------------------------------
// Non-200 status code rejection
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_rejectsNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	_, err := downloadRemoteMedia(context.Background(), srv.URL+"/missing", outDir, probeAudioOnly)
	if err == nil {
		t.Fatal("expected error for 404 status")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("err = %v, want download failure message", err)
	}
}

// ---------------------------------------------------------------------------
// Probe error surfaces to caller
// ---------------------------------------------------------------------------

func TestDownloadRemoteMedia_surfacesProbeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="broken.mp4"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	probeErr := errors.New("ffprobe segmentation fault")
	_, err := downloadRemoteMedia(context.Background(), srv.URL+"/broken.mp4", outDir,
		func(_ context.Context, _ string) (video.MediaProbe, error) {
			return video.MediaProbe{}, probeErr
		})
	if err == nil {
		t.Fatal("expected probe error to surface")
	}
	if !strings.Contains(err.Error(), "ffprobe") {
		t.Fatalf("err = %v, want ffprobe error", err)
	}
}

func TestDownloadRemoteMedia_emptyFilenameFromContentDisposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename=""`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))
	defer srv.Close()

	outDir := t.TempDir()
	result, err := downloadRemoteMedia(context.Background(), srv.URL+"/video.mp4", outDir, probeAudioOnly)
	if err != nil {
		t.Fatalf("downloadRemoteMedia failed: %v", err)
	}
	if result.Name == "" {
		t.Fatal("Name should not be empty even with empty Content-Disposition filename")
	}
}

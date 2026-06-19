package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"imagepadserver/internal/video"
)

func TestProcessAndPublishLocalAudioUsesSharedPipeline(t *testing.T) {
	srv, mux := testServer(t, true)
	defer srv.store.Reset()

	wavDir := t.TempDir()
	audioPath := writeTestWAV(t, wavDir, "test-track.wav")
	wavBytes, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "test-track.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(wavBytes); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:8080"

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image after publish")
	}
	if current.SourceKind != "local_audio" {
		t.Fatalf("SourceKind = %q, want local_audio", current.SourceKind)
	}
	if current.Title != "test-track" {
		t.Fatalf("Title = %q, want test-track", current.Title)
	}

	// Verify an audio queue job exists for this media ID
	queue := video.QueueStatus(srv.store.Dir())
	found := false
	for _, item := range queue {
		if item.MediaID == current.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected audio queue job for published audio")
	}
}

func TestProcessAndQueueLocalAudioUsesSharedPipeline(t *testing.T) {
	srv, mux := testServer(t, true)
	defer srv.store.Reset()

	wavDir := t.TempDir()
	audioPath := writeTestWAV(t, wavDir, "queued-audio.wav")
	wavBytes, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "queued-audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(wavBytes); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	req := httptest.NewRequest("POST", "/api/upload-queue", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:8080"

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}

	// Queue should not affect current image
	current := srv.store.Current()
	if current != nil {
		t.Fatal("expected no current image after queue-only operation")
	}

	// Verify in history
	history := srv.store.History()
	found := false
	for _, item := range history {
		if item.SourceKind == "local_audio" && item.OriginalName == "queued-audio.wav" {
			found = true
			if item.Kind != "video" {
				t.Fatalf("history Kind = %q, want video", item.Kind)
			}
			break
		}
	}
	if !found {
		t.Fatal("queued local audio not found in history")
	}
}

func TestLocalAudioNeverUsesSoundCloudMetadata(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "test.wav")

	acquired := video.AcquiredAudio{
		SourcePath:       audioPath,
		SourceName:       "my-song.wav",
		Kind:             video.SourceLocalAudio,
		EmbeddedMetadata: video.AudioMetadata{}, // empty embedded tags
	}

	req := httptest.NewRequest("GET", "/", nil)
	state, err := srv.processAudioFileAndPublish(req, acquired)
	if err != nil {
		t.Fatal(err)
	}
	_ = state

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image")
	}
	if current.SourceKind != "local_audio" {
		t.Fatalf("SourceKind = %q, want local_audio", current.SourceKind)
	}
	// Title should fall back to filename (without extension)
	if current.Title != "my-song" {
		t.Fatalf("Title = %q, want my-song (filename fallback)", current.Title)
	}
	// SoundCloud-specific fields must be empty for local audio
	if current.Artist != "" {
		t.Fatalf("Artist = %q, want empty for local audio", current.Artist)
	}
	if current.Album != "" {
		t.Fatalf("Album = %q, want empty for local audio", current.Album)
	}
}

func TestShouldProbeUploadedMediaWithoutAudioExtension(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		want        bool
	}{
		{name: "track.ape", contentType: "application/octet-stream", want: true},
		{name: "track", contentType: "application/octet-stream", want: true},
		{name: "cover.png", contentType: "image/png", want: false},
		{name: "camera.nef", contentType: "application/octet-stream", want: false},
	} {
		if got := shouldProbeUploadedMedia(tc.name, tc.contentType); got != tc.want {
			t.Errorf("shouldProbeUploadedMedia(%q, %q) = %v, want %v", tc.name, tc.contentType, got, tc.want)
		}
	}
}

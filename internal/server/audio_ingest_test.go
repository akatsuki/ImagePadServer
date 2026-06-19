package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/video"
)

func writeTestWAV(t *testing.T, dir, name string) string {
	t.Helper()
	var pcm []byte
	for i := 0; i < 4800; i++ {
		s := int16(i * 13)
		pcm = append(pcm, byte(s), byte(s>>8), byte(s), byte(s>>8))
	}
	dataLen := len(pcm)
	fileLen := 36 + dataLen
	header := []byte{
		0x52, 0x49, 0x46, 0x46,
		byte(fileLen), byte(fileLen >> 8), byte(fileLen >> 16), byte(fileLen >> 24),
		0x57, 0x41, 0x56, 0x45,
		0x66, 0x6D, 0x74, 0x20,
		0x10, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x02, 0x00,
		0x80, 0xBB, 0x00, 0x00,
		0x00, 0xEE, 0x02, 0x00,
		0x04, 0x00, 0x10, 0x00,
		0x64, 0x61, 0x74, 0x61,
		byte(dataLen), byte(dataLen >> 8), byte(dataLen >> 16), byte(dataLen >> 24),
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(header, pcm...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProcessAudioFileAndPublish_SoundCloud(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "source.wav")

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "Test Track.wav",
		Kind:       video.SourceSoundCloud,
		SoundCloudMetadata: video.AudioMetadata{
			Title:  "SoundCloud Title",
			Artist: "SoundCloud Artist",
			Album:  "SoundCloud Album",
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	state, err := srv.processAudioFileAndPublish(req, acquired)
	if err != nil {
		t.Fatal(err)
	}

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image after publish")
	}
	if current.SourceKind != "soundcloud" {
		t.Fatalf("SourceKind = %q, want soundcloud", current.SourceKind)
	}
	if current.Title != "SoundCloud Title" {
		t.Fatalf("Title = %q, want SoundCloud Title", current.Title)
	}
	if current.Artist != "SoundCloud Artist" {
		t.Fatalf("Artist = %q, want SoundCloud Artist", current.Artist)
	}
	if current.Album != "SoundCloud Album" {
		t.Fatalf("Album = %q, want SoundCloud Album", current.Album)
	}
	if current.Kind != "video" {
		t.Fatalf("Kind = %q, want video", current.Kind)
	}
	if _, ok := state["videoPlayer"]; !ok {
		t.Fatal("expected videoPlayer in state")
	}
}

func TestProcessAudioFileAndPublish_LocalAudio(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "track.wav")

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "local.wav",
		Kind:       video.SourceLocalAudio,
		EmbeddedMetadata: video.AudioMetadata{
			Title:  "Local Title",
			Artist: "Local Artist",
			Album:  "Local Album",
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	state, err := srv.processAudioFileAndPublish(req, acquired)
	if err != nil {
		t.Fatal(err)
	}

	_ = state
	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image after publish")
	}
	if current.SourceKind != "local_audio" {
		t.Fatalf("SourceKind = %q, want local_audio", current.SourceKind)
	}
	if current.Title != "Local Title" {
		t.Fatalf("Title = %q, want Local Title", current.Title)
	}
	if current.Artist != "Local Artist" {
		t.Fatalf("Artist = %q, want Local Artist", current.Artist)
	}
	if current.Album != "Local Album" {
		t.Fatalf("Album = %q, want Local Album", current.Album)
	}
}

func TestProcessAudioFileAndPublish_RemoteAudio(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "remote_source.wav")

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "remote_audio.wav",
		Kind:       video.SourceRemoteAudio,
		EmbeddedMetadata: video.AudioMetadata{
			Title:  "Remote Title",
			Artist: "Remote Artist",
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	_, err := srv.processAudioFileAndPublish(req, acquired)
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
	if current.Title != "Remote Title" {
		t.Fatalf("Title = %q, want Remote Title", current.Title)
	}
}

func TestProcessAudioFileAndQueue_SoundCloud(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "queue_source.wav")

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "Queued Track.wav",
		Kind:       video.SourceSoundCloud,
		SoundCloudMetadata: video.AudioMetadata{
			Title: "Queued Title",
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	state, err := srv.processAudioFileAndQueue(req, acquired)
	if err != nil {
		t.Fatal(err)
	}
	_ = state

	// Queue-only should not affect current image
	current := srv.store.Current()
	if current != nil {
		t.Fatal("expected no current image after queue-only operation")
	}

	history := srv.store.History()
	found := false
	for _, item := range history {
		if item.Title == "Queued Title" {
			found = true
			if item.SourceKind != "soundcloud" {
				t.Fatalf("history SourceKind = %q, want soundcloud", item.SourceKind)
			}
			if item.Kind != "video" {
				t.Fatalf("history Kind = %q, want video", item.Kind)
			}
			break
		}
	}
	if !found {
		t.Fatal("queued audio not found in history")
	}
}

func TestProcessAudioFileAndQueue_LocalAudio(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "queue_local.wav")

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "queued_local.wav",
		Kind:       video.SourceLocalAudio,
	}

	req := httptest.NewRequest("GET", "/", nil)
	_, err := srv.processAudioFileAndQueue(req, acquired)
	if err != nil {
		t.Fatal(err)
	}

	history := srv.store.History()
	found := false
	for _, item := range history {
		if item.SourceKind == "local_audio" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("local audio not found in history")
	}
}

func TestProcessAudioFileAndPublish_SoundCloudGUNPEIFallback(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "gunpei.wav")

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "gunpei.wav",
		Kind:       video.SourceSoundCloud,
		// Embedded metadata is empty; SoundCloud metadata should supply values.
		SoundCloudMetadata: video.AudioMetadata{
			Title:  "GUNPEI",
			Artist: "藤子名人",
			Album:  "濃度",
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	_, err := srv.processAudioFileAndPublish(req, acquired)
	if err != nil {
		t.Fatal(err)
	}

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image after publish")
	}
	if current.SourceKind != "soundcloud" {
		t.Fatalf("SourceKind = %q, want soundcloud", current.SourceKind)
	}
	if current.Title != "GUNPEI" {
		t.Fatalf("Title = %q, want GUNPEI", current.Title)
	}
	if current.Artist != "藤子名人" {
		t.Fatalf("Artist = %q, want 藤子名人", current.Artist)
	}
	if current.Album != "濃度" {
		t.Fatalf("Album = %q, want 濃度", current.Album)
	}
}

func TestProcessAudioFileAndPublish_TitleFallsBackToFilename(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "my-awesome-track.wav")

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "my-awesome-track.wav",
		Kind:       video.SourceLocalAudio,
	}

	req := httptest.NewRequest("GET", "/", nil)
	_, err := srv.processAudioFileAndPublish(req, acquired)
	if err != nil {
		t.Fatal(err)
	}

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image")
	}
	if current.Title != "my-awesome-track" {
		t.Fatalf("Title = %q, want my-awesome-track (stripped extension)", current.Title)
	}
}

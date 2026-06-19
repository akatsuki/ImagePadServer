package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/video"
)

func TestProcessAudioFileAndPublish_SoundCloud(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := filepath.Join(t.TempDir(), "source.mp3")
	if err := os.WriteFile(audioPath, []byte("fake audio"), 0600); err != nil {
		t.Fatal(err)
	}

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "Test Track.mp3",
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

	audioPath := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(audioPath, []byte("fake flac"), 0600); err != nil {
		t.Fatal(err)
	}

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "local.flac",
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

	audioPath := filepath.Join(t.TempDir(), "remote_source.m4a")
	if err := os.WriteFile(audioPath, []byte("fake m4a"), 0600); err != nil {
		t.Fatal(err)
	}

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "remote_audio.m4a",
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

	audioPath := filepath.Join(t.TempDir(), "queue_source.mp3")
	if err := os.WriteFile(audioPath, []byte("fake queue audio"), 0600); err != nil {
		t.Fatal(err)
	}

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "Queued Track.mp3",
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

	audioPath := filepath.Join(t.TempDir(), "queue_local.wav")
	if err := os.WriteFile(audioPath, []byte("fake wav"), 0600); err != nil {
		t.Fatal(err)
	}

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

func TestProcessAudioFileAndPublish_TitleFallsBackToFilename(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := filepath.Join(t.TempDir(), "my-awesome-track.mp3")
	if err := os.WriteFile(audioPath, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}

	acquired := video.AcquiredAudio{
		SourcePath: audioPath,
		SourceName: "my-awesome-track.mp3",
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

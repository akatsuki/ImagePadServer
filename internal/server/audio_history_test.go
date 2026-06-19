package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/library"
	"imagepadserver/internal/video"
)

func TestAudioHistorySelectCurrent(t *testing.T) {
	srv, mux := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "history_current.wav")

	item, err := srv.store.AddHistory(audioPath, library.CurrentImage{
		Kind:       "video",
		SourceKind: string(video.SourceLocalAudio),
		Title:      "History Current",
		Artist:     "Test Artist",
		Album:      "Test Album",
	})
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"id": item.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/history/select", bytes.NewReader(body))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image after history select")
	}
	if current.Title != "History Current" {
		t.Fatalf("Title = %q", current.Title)
	}

	// Verify a queue job exists for this item
	queue := video.QueueStatus(srv.store.Dir())
	found := false
	for _, q := range queue {
		if q.MediaID == item.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected audio conversion to be enqueued")
	}
}

func TestAudioHistoryQueueUnconverted(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "queue_unconverted.wav")

	item, err := srv.store.AddHistory(audioPath, library.CurrentImage{
		Kind:       "video",
		SourceKind: string(video.SourceLocalAudio),
		Title:      "Queue Unconverted",
		Artist:     "Test Artist",
		Album:      "Test Album",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify item is not converted
	if item.Converted {
		t.Fatal("test item should not be converted")
	}

	// enqueueHistoryItem should call audioRenderInputForStored internally
	if err := srv.enqueueHistoryItem(item.ID); err != nil {
		t.Fatalf("enqueueHistoryItem: %v", err)
	}
}

func TestAudioHistoryFailedReAnalysis(t *testing.T) {
	srv, mux := testServer(t, true)
	defer srv.store.Reset()

	// Create an invalid audio file (just bytes, not actually a WAV)
	badDir := t.TempDir()
	badPath := filepath.Join(badDir, "not_audio.bin")
	if err := os.WriteFile(badPath, []byte("this is not audio data"), 0600); err != nil {
		t.Fatal(err)
	}

	item, err := srv.store.AddHistory(badPath, library.CurrentImage{
		Kind:       "video",
		SourceKind: string(video.SourceLocalAudio),
		Title:      "Bad Audio",
	})
	if err != nil {
		t.Fatal(err)
	}

	// handleHistorySelect should fail with audio analysis error
	body, _ := json.Marshal(map[string]string{"id": item.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/history/select", bytes.NewReader(body))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for failed analysis, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAudioHistoryAlreadyConverted(t *testing.T) {
	srv, _ := testServer(t, true)
	defer srv.store.Reset()

	audioPath := writeTestWAV(t, t.TempDir(), "already_converted.wav")

	item, err := srv.store.AddHistory(audioPath, library.CurrentImage{
		Kind:       "video",
		SourceKind: string(video.SourceLocalAudio),
		Title:      "Already Converted",
		Converted:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// With Converted==true, enqueueHistoryItem should skip analysis
	// and just restore current from history
	if err := srv.enqueueHistoryItem(item.ID); err != nil {
		t.Fatalf("enqueueHistoryItem for converted item: %v", err)
	}

	// Verify no audio job was enqueued - the queue should not have a pending job for this item
	queue := video.QueueStatus(srv.store.Dir())
	for _, q := range queue {
		if q.MediaID == item.ID {
			t.Fatal("converted item should not enqueue an audio conversion job")
		}
	}

	// Verify current was restored
	current := srv.store.Current()
	if current == nil {
		t.Fatal("expected current image after restoring converted item")
	}
}

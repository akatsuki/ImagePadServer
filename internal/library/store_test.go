package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResetDirClearsWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leftover.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ResetDir(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty workspace, got %d entries", len(entries))
	}
}

func TestStoreResetClearsCurrent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentInfo(CurrentImage{
		FileName:    "current.jpg",
		PublicName:  "current.jpg",
		ContentType: "image/jpeg",
	}); err != nil {
		t.Fatal(err)
	}
	if store.Current() == nil {
		t.Fatal("expected current image before reset")
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if store.Current() != nil {
		t.Fatal("expected current image to be cleared after reset")
	}
}

func TestSaveCurrentLockedWritesTempBeforeReplace(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentInfo(CurrentImage{
		FileName:    "current.jpg",
		PublicName:  "current.jpg",
		ContentType: "image/jpeg",
		SizeBytes:   11,
	}); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var beforeImage CurrentImage
	if err := json.Unmarshal(before, &beforeImage); err != nil {
		t.Fatal(err)
	}
	if beforeImage.SizeBytes != 11 {
		t.Fatalf("initial SizeBytes = %d, want 11", beforeImage.SizeBytes)
	}
	if err := os.Mkdir(filepath.Join(dir, "state.json.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateCurrentSize(42); err == nil {
		t.Fatal("expected temp write failure")
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var afterImage CurrentImage
	if err := json.Unmarshal(after, &afterImage); err != nil {
		t.Fatal(err)
	}
	if afterImage.SizeBytes != 11 {
		t.Fatalf("state.json was partially replaced; SizeBytes = %d, want 11", afterImage.SizeBytes)
	}
}

func TestSetCurrentFromHistoryRestoresConvertedFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "obs-recording.mp4")
	if err := os.WriteFile(source, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(source, CurrentImage{
		ID:           "obs1",
		Kind:         "video",
		FileName:     "obs-recording.mp4",
		PublicName:   "obs-obs1.mp4",
		ContentType:  "video/mp4",
		OriginalName: "OBS",
	})
	if err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(dir, "current-"+item.ID+".m3u8")
	segment := filepath.Join(dir, "current-"+item.ID+"-0.ts")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n#EXT-X-ENDLIST\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(segment, []byte("ts"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkConverted(item.ID, []string{playlist, segment}); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(playlist)
	_ = os.Remove(segment)

	if err := store.SetCurrentFromHistory(item.ID); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	if current == nil || !current.Converted {
		t.Fatalf("expected converted current item, got %#v", current)
	}
	if _, err := os.Stat(playlist); err != nil {
		t.Fatalf("expected playlist restored: %v", err)
	}
	if _, err := os.Stat(segment); err != nil {
		t.Fatalf("expected segment restored: %v", err)
	}
}

func TestUpdateCurrentSizePersistsConvertedSize(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentInfo(CurrentImage{
		FileName:    "current.mp4",
		PublicName:  "current.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateCurrentSize(500); err != nil {
		t.Fatal(err)
	}
	if got := store.Current().SizeBytes; got != 500 {
		t.Fatalf("SizeBytes = %d, want 500", got)
	}
}

func TestUpdateHistorySizeUpdatesInMemoryItem(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dummy := filepath.Join(store.Dir(), "dummy.mp4")
	if err := os.WriteFile(dummy, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(dummy, CurrentImage{
		PublicName:  "clip.mp4",
		ContentType: "video/mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateHistorySize(item.ID, 2500); err != nil {
		t.Fatal(err)
	}
	for _, h := range store.History() {
		if h.ID == item.ID && h.SizeBytes != 2500 {
			t.Fatalf("history SizeBytes = %d, want 2500", h.SizeBytes)
		}
	}
}

func TestUpdateHistorySizePersistsFavoriteSize(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dummy := filepath.Join(store.Dir(), "dummy.mp4")
	if err := os.WriteFile(dummy, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(dummy, CurrentImage{
		PublicName:  "clip.mp4",
		ContentType: "video/mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite(item.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateHistorySize(item.ID, 800); err != nil {
		t.Fatal(err)
	}

	// Re-create store to verify favorites.json persistence.
	store2, err := NewStore(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range store2.History() {
		if h.ID == item.ID && h.SizeBytes != 800 {
			t.Fatalf("favorite history SizeBytes = %d, want 800", h.SizeBytes)
		}
	}
}

func TestCurrentImageAudioMetadata(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	info := CurrentImage{
		FileName:    "test.mp3",
		PublicName:  "test.mp3",
		ContentType: "audio/mpeg",
		SourceKind:  "soundcloud",
		Title:       "Test Title",
		Artist:      "Test Artist",
		Album:       "Test Album",
	}

	if err := store.SetCurrentInfo(info); err != nil {
		t.Fatal(err)
	}

	current := store.Current()
	if current.SourceKind != "soundcloud" {
		t.Fatalf("SourceKind = %q, want soundcloud", current.SourceKind)
	}
	if current.Title != "Test Title" {
		t.Fatalf("Title = %q, want Test Title", current.Title)
	}
	if current.Artist != "Test Artist" {
		t.Fatalf("Artist = %q, want Test Artist", current.Artist)
	}
	if current.Album != "Test Album" {
		t.Fatalf("Album = %q, want Test Album", current.Album)
	}
}

func TestHistoryPreservesAudioMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(dir, "source.mp3")
	if err := os.WriteFile(source, []byte("audio data"), 0600); err != nil {
		t.Fatal(err)
	}

	item, err := store.AddHistory(source, CurrentImage{
		FileName:    "source.mp3",
		PublicName:  "source.mp3",
		ContentType: "audio/mpeg",
		SourceKind:  "soundcloud",
		Title:       "History Title",
		Artist:      "History Artist",
		Album:       "History Album",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "History Title" {
		t.Fatalf("Title = %q, want History Title", item.Title)
	}
	if item.Artist != "History Artist" {
		t.Fatalf("Artist = %q, want History Artist", item.Artist)
	}
	if item.Album != "History Album" {
		t.Fatalf("Album = %q, want History Album", item.Album)
	}

	history := store.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 history item, got %d", len(history))
	}
	if history[0].Title != "History Title" {
		t.Fatalf("history Title = %q, want History Title", history[0].Title)
	}
}

func TestSetCurrentReturnsHistoryCopyError(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.jpg")
	if err := os.WriteFile(source, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	err = store.SetCurrent(source, CurrentImage{
		PublicName:  "current.jpg",
		ContentType: "image/jpeg",
		Thumbnail:   "missing-thumb.jpg",
	})
	if err == nil {
		t.Fatal("expected missing thumbnail copy to fail")
	}
}

func TestUnfavoriteDoesNotRemoveHistoryWhenWorkingFileWasPruned(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.jpg")
	if err := os.WriteFile(source, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(source, CurrentImage{
		PublicName:  "source.jpg",
		ContentType: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite(item.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, h := range store.History() {
		if h.ID == item.ID {
			_ = os.Remove(filepath.Join(dir, h.HistoryFileName))
			break
		}
	}
	if err := store.SetFavorite(item.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, h := range store.History() {
		if h.ID == item.ID {
			if h.Favorite || h.Persistent {
				t.Fatalf("favorite flags not cleared: %#v", h)
			}
			return
		}
	}
	t.Fatal("history item was removed by unfavorite")
}

func TestPruneHistoryRemovesThumbnail(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var prunedThumbnail string
	for i := 0; i < 41; i++ {
		source := filepath.Join(dir, "source.jpg")
		if err := os.WriteFile(source, []byte("image"), 0600); err != nil {
			t.Fatal(err)
		}
		thumb := filepath.Join(dir, "thumb.jpg")
		if err := os.WriteFile(thumb, []byte("thumb"), 0600); err != nil {
			t.Fatal(err)
		}
		item, err := store.AddHistory(source, CurrentImage{
			PublicName:  "source.jpg",
			ContentType: "image/jpeg",
			Thumbnail:   "thumb.jpg",
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			for _, h := range store.History() {
				if h.ID == item.ID {
					prunedThumbnail = h.Thumbnail
					break
				}
			}
		}
	}
	for _, entry := range store.History() {
		if entry.Thumbnail != "" {
			if _, err := os.Stat(filepath.Join(dir, entry.Thumbnail)); err != nil {
				t.Fatalf("kept thumbnail missing: %v", err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, prunedThumbnail)); !os.IsNotExist(err) {
		t.Fatalf("expected pruned history thumbnail to be removed, err=%v", err)
	}
}

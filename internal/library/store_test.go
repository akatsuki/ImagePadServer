package library

import (
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

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

package playlist

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "playlists.json"))
}

func sampleTracks(t *testing.T, dir string) []Track {
	t.Helper()
	media := filepath.Join(dir, "a.ts")
	if err := os.WriteFile(media, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	return []Track{
		{ID: "t1", Title: "Alpha", Artist: "Art", MediaPath: media, Status: TrackReady, DurationSeconds: 120},
		{ID: "t2", Title: "Beta", MediaPath: filepath.Join(dir, "missing.ts"), Status: TrackReady},
	}
}

func TestSaveLoadList(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	if err := s.Save("お気に入り", sampleTracks(t, dir)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Save("second", nil); err != nil {
		t.Fatalf("Save second: %v", err)
	}
	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 || names[0] != "お気に入り" || names[1] != "second" {
		t.Fatalf("List = %v", names)
	}
	tracks, err := s.Load("お気に入り")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("Load returned %d tracks", len(tracks))
	}
	if tracks[0].Status != TrackReady || tracks[0].Title != "Alpha" {
		t.Fatalf("track with existing media must stay ready: %+v", tracks[0])
	}
	if tracks[1].Status != TrackFailed || tracks[1].Error == "" {
		t.Fatalf("track with missing media must load as failed: %+v", tracks[1])
	}
}

func TestSaveOverwritesSameName(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	if err := s.Save("mix", sampleTracks(t, dir)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("mix", nil); err != nil {
		t.Fatal(err)
	}
	names, _ := s.List()
	if len(names) != 1 {
		t.Fatalf("overwrite must not duplicate names: %v", names)
	}
	tracks, err := s.Load("mix")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 0 {
		t.Fatalf("overwritten playlist must be empty, got %d", len(tracks))
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save("gone", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load("gone"); err == nil {
		t.Fatal("Load after Delete must fail")
	}
	if err := s.Delete("never-existed"); err == nil {
		t.Fatal("Delete of unknown name must fail")
	}
}

func TestLoadUnknownAndEmptyFile(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Load("nope"); err == nil {
		t.Fatal("Load on empty store must fail")
	}
	names, err := s.List()
	if err != nil {
		t.Fatalf("List on empty store: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("List on empty store = %v", names)
	}
}

func TestSaveRejectsEmptyName(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save("", nil); err == nil {
		t.Fatal("empty name must be rejected")
	}
	if err := s.Save("   ", nil); err == nil {
		t.Fatal("blank name must be rejected")
	}
}

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

func TestSaveCopiesMediaIntoPlaylistFolder(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	tracks := sampleTracks(t, dir)
	if err := s.Save("コピー確認", tracks[:1]); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 元ファイルを消しても保存済みプレイリストは ready のまま再生できる。
	if err := os.Remove(tracks[0].MediaPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load("コピー確認")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded[0].Status != TrackReady {
		t.Fatalf("copied media must keep the track ready: %+v", loaded[0])
	}
	if loaded[0].MediaPath == tracks[0].MediaPath {
		t.Fatal("saved playlist must reference the copy, not the queue file")
	}
	if _, err := os.Stat(loaded[0].MediaPath); err != nil {
		t.Fatalf("copied media missing: %v", err)
	}
	// 削除でコピーも消える。
	if err := s.Delete("コピー確認"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(loaded[0].MediaPath); !os.IsNotExist(err) {
		t.Fatal("Delete must remove the playlist media folder")
	}
}

func TestSaveOverwritesLoadedPlaylistWithoutLosingCopiedMedia(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	tracks := sampleTracks(t, dir)
	if err := s.Save("loaded-copy", tracks[:1]); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := s.Load("loaded-copy")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded[0].MediaPath == tracks[0].MediaPath {
		t.Fatal("loaded playlist must point at the saved copy")
	}

	if err := s.Save("loaded-copy", loaded); err != nil {
		t.Fatalf("Save loaded copy: %v", err)
	}
	reloaded, err := s.Load("loaded-copy")
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].Status != TrackReady {
		t.Fatalf("overwriting loaded copy must keep track ready: %+v", reloaded)
	}
	if _, err := os.Stat(reloaded[0].MediaPath); err != nil {
		t.Fatalf("overwritten media copy missing: %v", err)
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

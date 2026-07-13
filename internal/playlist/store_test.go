package playlist

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/video"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	recipe := testActiveRecipe()
	return NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{
		ActiveRecipe: func() video.RadioRenderRecipe { return recipe },
		ObserveAsset: func(context.Context, string) (video.RadioAssetSpec, error) {
			return observedForContract(recipe.NormalizedEncodingContract()), nil
		},
	})
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

func TestStoreMaterializeCreatesRuntimeOwnedCopiesWithFreshIDs(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	media := filepath.Join(dir, "song.mp4")
	artwork := filepath.Join(dir, "cover.webp")
	source := filepath.Join(dir, "song.m4a")
	for path, contents := range map[string]string{media: "media", artwork: "artwork", source: "source"} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save("mix", []Track{{
		ID: "saved-id", Title: "Song", Status: TrackReady,
		MediaPath: media, ThumbnailPath: artwork, SourcePath: source,
	}}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Load("mix")
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(t.TempDir(), "playlist-runtime")
	loaded, err := s.Materialize("mix", runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID == saved[0].ID {
		t.Fatalf("materialized track must have a fresh ID: saved=%+v loaded=%+v", saved, loaded)
	}
	for _, path := range []string{loaded[0].MediaPath, loaded[0].ThumbnailPath, loaded[0].SourcePath} {
		if filepath.Dir(path) == filepath.Dir(saved[0].MediaPath) {
			t.Fatalf("materialized path must not point into saved playlist: %q", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("materialized file missing at %q: %v", path, err)
		}
	}
	if err := s.Delete("mix"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(loaded[0].MediaPath); err != nil {
		t.Fatalf("deleting saved playlist must not break materialized queue: %v", err)
	}
}

func TestStoreMaterializeEmptyPlaylist(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save("empty", nil); err != nil {
		t.Fatal(err)
	}
	tracks, err := s.Materialize("empty", filepath.Join(t.TempDir(), "playlist-runtime"))
	if err != nil {
		t.Fatalf("Materialize empty playlist: %v", err)
	}
	if len(tracks) != 0 {
		t.Fatalf("materialized empty playlist = %+v", tracks)
	}
}

func TestSaveCopiesArtworkAndSourceAlongsideMedia(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	media := filepath.Join(dir, "song.mp4")
	artwork := filepath.Join(dir, "cover.webp")
	source := filepath.Join(dir, "song.m4a")
	for _, path := range []string{media, artwork, source} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save("complete", []Track{{
		ID: "track", Status: TrackReady, MediaPath: media, ThumbnailPath: artwork, SourcePath: source,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{media, artwork, source} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := s.Load("complete")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Status != TrackReady {
		t.Fatalf("saved media must remain ready: %+v", loaded)
	}
	for _, path := range []string{loaded[0].MediaPath, loaded[0].ThumbnailPath, loaded[0].SourcePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("saved asset missing at %q: %v", path, err)
		}
	}
}

func TestSaveCopiesAvailableAssetsWhenMediaCopyFails(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	artwork := filepath.Join(dir, "cover.webp")
	source := filepath.Join(dir, "source.m4a")
	for _, path := range []string{artwork, source} {
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save("partial", []Track{{
		ID: "track", Status: TrackReady, MediaPath: filepath.Join(dir, "missing.mp4"), ThumbnailPath: artwork, SourcePath: source,
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := s.Load("partial")
	if err != nil {
		t.Fatal(err)
	}
	if loaded[0].MediaPath != "" || loaded[0].Status != TrackPreparing || !loaded[0].NeedsRegeneration {
		t.Fatalf("failed media with retained source must require regeneration: %+v", loaded[0])
	}
	for _, path := range []string{loaded[0].ThumbnailPath, loaded[0].SourcePath} {
		rel, err := filepath.Rel(filepath.Dir(s.path), path)
		if err != nil || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
			t.Fatalf("saved asset must not retain an external path: %q", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("available saved asset missing: %v", err)
		}
	}
}

func TestStoreMaterializeFallsBackToCopyWhenHardLinkFails(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	media := filepath.Join(dir, "song.mp4")
	if err := os.WriteFile(media, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("copy", []Track{{ID: "saved", Status: TrackReady, MediaPath: media}}); err != nil {
		t.Fatal(err)
	}
	oldLink := linkMediaFile
	linkMediaFile = func(string, string) error { return errors.New("link unavailable") }
	t.Cleanup(func() { linkMediaFile = oldLink })
	tracks, err := s.Materialize("copy", filepath.Join(t.TempDir(), "playlist-runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(tracks[0].MediaPath); err != nil || string(data) != "media" {
		t.Fatalf("copy fallback = %q, err=%v", data, err)
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

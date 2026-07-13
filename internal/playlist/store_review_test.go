package playlist

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func seedLegacyPlaylist(t *testing.T, s *Store, name string) (string, string) {
	t.Helper()
	dir := filepath.Join(s.mediaDir, "legacy-"+name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	media, source := filepath.Join(dir, "media.mp4"), filepath.Join(dir, "source.m4a")
	if err := os.WriteFile(media, []byte("legacy-media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("legacy-source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.write(storeFile{Playlists: []storedPlaylist{{Name: name, Tracks: []Track{{ID: "legacy-track", Title: "Legacy", Status: TrackReady, MediaPath: media, SourcePath: source}}}}}); err != nil {
		t.Fatal(err)
	}
	return media, source
}

func TestSaveMigratesUniqueLegacyPlaylistAtomicallyToCanonicalID(t *testing.T) {
	s := newTestStore(t)
	_, source := seedLegacyPlaylist(t, s, "legacy")
	blocked, err := s.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Status != TrackPreparing || !blocked[0].NeedsRegeneration {
		t.Fatalf("legacy load=%+v", blocked)
	}
	replacement := readyTrackFile(t, false)
	replacement.ID = "legacy-track"
	replacement.SourcePath = source
	if err := s.Save("legacy", []Track{replacement}); err != nil {
		t.Fatal(err)
	}
	index, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Playlists) != 1 || !validStorageID(index.Playlists[0].ID) || index.Playlists[0].ActiveVersion == "" {
		t.Fatalf("migrated index=%+v", index)
	}
	manifest, err := s.readManifest(index.Playlists[0])
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RegenerationState != "ready" || manifest.Tracks[0].RegenerationState != "ready" {
		t.Fatalf("manifest=%+v", manifest)
	}
	byID, err := s.LoadID(index.Playlists[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	byName, err := s.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(byID) != 1 || byID[0].Status != TrackReady || len(byName) != 1 || byName[0].Status != TrackReady {
		t.Fatalf("id=%+v name=%+v", byID, byName)
	}
}

func TestFailedLegacyMigrationPreservesLegacyIndexAndAssets(t *testing.T) {
	recipe := testActiveRecipe()
	fail := false
	s := NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{ActiveRecipe: func() video.RadioRenderRecipe { return recipe }, ObserveAsset: func(context.Context, string) (video.RadioAssetSpec, error) {
		return observedForContract(recipe.StreamEncodingContract()), nil
	}, Rename: func(old, new string) error {
		if fail && strings.HasSuffix(old, ".tmp") {
			return errors.New("index fail")
		}
		return os.Rename(old, new)
	}})
	media, source := seedLegacyPlaylist(t, s, "legacy")
	fail = true
	replacement := readyTrackFile(t, false)
	replacement.ID = "legacy-track"
	replacement.SourcePath = source
	if err := s.Save("legacy", []Track{replacement}); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	index, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Playlists) != 1 || index.Playlists[0].ID != "" || index.Playlists[0].ActiveVersion != "" {
		t.Fatalf("legacy pointer changed=%+v", index)
	}
	for _, path := range []string{media, source} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("legacy asset removed: %v", err)
		}
	}
}

func TestDuplicateValidPlaylistIDsRejectEveryIdentityOperationWithoutDeletion(t *testing.T) {
	s := newTestStore(t)
	id := strings.Repeat("a", 24)
	root, err := s.playlistRoot(id, true)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside-sentinel")
	outside := filepath.Join(filepath.Dir(s.mediaDir), "outside-sentinel")
	for _, path := range []string{inside, outside} {
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.write(storeFile{Playlists: []storedPlaylist{{ID: id, Name: "one"}, {ID: id, Name: "two"}}}); err != nil {
		t.Fatal(err)
	}
	checks := []func() error{
		func() error { _, e := s.LoadID(id); return e }, func() error { return s.SaveID(id, "one", nil) }, func() error { return s.DeleteID(id) }, func() error { return s.Rename(id, "renamed") }, func() error { return s.RegenerateTrackID(id, "track", Track{}) },
	}
	for i, check := range checks {
		if err := check(); err == nil || !strings.Contains(err.Error(), "duplicate playlist ID") {
			t.Fatalf("operation %d error=%v", i, err)
		}
	}
	for _, path := range []string{inside, outside} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "keep" {
			t.Fatalf("sentinel %q changed: %q %v", path, data, err)
		}
	}
}

func TestDuplicateTrackIDsRejectedBeforeAssetDerivation(t *testing.T) {
	s := newTestStore(t)
	first := readyTrackFile(t, true)
	second := readyTrackFile(t, true)
	second.ID = first.ID
	if _, err := s.Create("duplicate-tracks", []Track{first, second}); err == nil || !strings.Contains(err.Error(), "duplicate track ID") {
		t.Fatalf("error=%v", err)
	}
	entries, err := s.ListEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("index changed=%+v", entries)
	}
}

func TestStoreRejectsInjectedObservedEncodingWithoutGOPProof(t *testing.T) {
	recipe := testActiveRecipe()
	s := NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{ActiveRecipe: func() video.RadioRenderRecipe { return recipe }, ObserveAsset: func(context.Context, string) (video.RadioAssetSpec, error) {
		spec := observedForContract(recipe.StreamEncodingContract())
		spec.Video.GOPFrames = 0
		return spec, nil
	}})
	if err := s.Save("zero-gop", []Track{readyTrackFile(t, true)}); err == nil || !strings.Contains(err.Error(), "GOP") {
		t.Fatalf("error=%v", err)
	}
}

func TestStorageIDFallbackIsCanonicalDistinctAndCollisionChecked(t *testing.T) {
	oldRead, oldNow := storageIDRandomRead, storageIDFallbackNow
	oldCounter := storageIDFallbackCounter.Load()
	storageIDRandomRead = func([]byte) (int, error) { return 0, errors.New("rng failed") }
	storageIDFallbackNow = func() time.Time { return time.Unix(123, 456) }
	t.Cleanup(func() {
		storageIDRandomRead = oldRead
		storageIDFallbackNow = oldNow
		storageIDFallbackCounter.Store(oldCounter)
	})
	storageIDFallbackCounter.Store(0)
	collision := newStorageID()
	storageIDFallbackCounter.Store(0)
	s := newTestStore(t)
	if err := s.write(storeFile{Playlists: []storedPlaylist{{ID: collision, Name: "seeded"}}}); err != nil {
		t.Fatal(err)
	}
	first, err := s.Create("one", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create("two", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !validStorageID(first) || !validStorageID(second) || first == second || first == collision || second == collision {
		t.Fatalf("fallback IDs=%q %q", first, second)
	}
	index, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Playlists) != 3 {
		t.Fatalf("index=%+v", index)
	}
}

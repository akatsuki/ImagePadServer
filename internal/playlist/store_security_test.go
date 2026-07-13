package playlist

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

func newIndexFailureStore(t *testing.T) (*Store, *bool) {
	t.Helper()
	recipe := testActiveRecipe()
	fail := false
	s := NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{
		ActiveRecipe: func() video.RadioRenderRecipe { return recipe },
		ObserveAsset: func(context.Context, string) (video.RadioAssetSpec, error) {
			return observedForContract(recipe.NormalizedEncodingContract()), nil
		},
		Rename: func(old, new string) error {
			if fail && strings.HasSuffix(old, ".tmp") {
				return os.ErrPermission
			}
			return os.Rename(old, new)
		},
	})
	return s, &fail
}

func TestStoreRejectsHostileIndexIDsWithoutTouchingOutside(t *testing.T) {
	for _, hostile := range []string{"../outside", `..\outside`, "ABCDEF0123456789ABCDEF01", "short", strings.Repeat("a", 23), strings.Repeat("a", 25)} {
		t.Run(strings.ReplaceAll(hostile, "\\", "_"), func(t *testing.T) {
			s := newTestStore(t)
			outside := filepath.Join(filepath.Dir(s.mediaDir), "outside")
			if err := os.MkdirAll(outside, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.write(storeFile{Playlists: []storedPlaylist{{ID: hostile, Name: "hostile", ActiveVersion: "bad"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadID(hostile); err == nil {
				t.Fatal("LoadID accepted hostile ID")
			}
			if err := s.SaveID(hostile, "hostile", nil); err == nil {
				t.Fatal("SaveID accepted hostile ID")
			}
			if err := s.DeleteID(hostile); err == nil {
				t.Fatal("DeleteID accepted hostile ID")
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
				t.Fatalf("outside sentinel changed: %q, %v", data, err)
			}
		})
	}
}

func TestStoreRejectsCanonicalIDSymlinkEscape(t *testing.T) {
	s := newTestStore(t)
	id := strings.Repeat("a", 24)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.mediaDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.mediaDir, id)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := s.write(storeFile{Playlists: []storedPlaylist{{ID: id, Name: "linked", ActiveVersion: "20260711T000000.000000000Z-" + strings.Repeat("b", 24)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadID(id); err == nil {
		t.Fatal("LoadID followed symlink escape")
	}
	if err := s.DeleteID(id); err == nil {
		t.Fatal("DeleteID followed symlink escape")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("outside sentinel removed: %v", err)
	}
}

func TestPlaylistIDIsStableAcrossRenameAndUpdate(t *testing.T) {
	s := newTestStore(t)
	id, err := s.Create("first", []Track{readyTrackFile(t, true)})
	if err != nil {
		t.Fatal(err)
	}
	if !validStorageID(id) {
		t.Fatalf("generated ID is not canonical: %q", id)
	}
	if err := s.Rename(id, "renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MaterializeID(id, filepath.Join(t.TempDir(), "runtime")); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != id || items[0].Name != "renamed" {
		t.Fatalf("entries = %+v", items)
	}
	if _, err := s.LoadID(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("first"); err == nil {
		t.Fatal("old display name remained an identity")
	}
}

func TestDuplicateDisplayNamesRejectedAndLegacyAmbiguityErrors(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("same", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("same", nil); err == nil {
		t.Fatal("duplicate display name accepted")
	}
	index, _ := s.load()
	duplicate := index.Playlists[0]
	duplicate.ID = strings.Repeat("d", 24)
	index.Playlists = append(index.Playlists, duplicate)
	if err := s.write(index); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("same"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("legacy resolver error = %v", err)
	}
}

func TestManifestDigestDetectsEveryTrustedFieldEdit(t *testing.T) {
	mutations := map[string]func(*PlaylistManifest){
		"path":     func(m *PlaylistManifest) { m.Tracks[0].Track.MediaPath = "other.mp4" },
		"hash":     func(m *PlaylistManifest) { m.Tracks[0].MediaSHA256 = "changed" },
		"observed": func(m *PlaylistManifest) { m.Tracks[0].ObservedEncoding.Video.Width++ },
		"contract": func(m *PlaylistManifest) { m.Tracks[0].EncodingContract.Video.GOPFrames++ },
		"state":    func(m *PlaylistManifest) { m.Tracks[0].RegenerationState = "incompatible" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			id, err := s.Create("mix", []Track{readyTrackFile(t, true)})
			if err != nil {
				t.Fatal(err)
			}
			index, _ := s.load()
			entry, _ := findStoredPlaylistByID(index, id)
			path, _ := s.manifestPath(entry)
			data, _ := os.ReadFile(path)
			var manifest PlaylistManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			mutate(&manifest)
			data, _ = json.Marshal(manifest)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadID(id); err == nil || !strings.Contains(err.Error(), "digest") {
				t.Fatalf("LoadID error = %v", err)
			}
		})
	}
}

func TestIndexFailureRemovesOnlyNewOrphanVersion(t *testing.T) {
	s, fail := newIndexFailureStore(t)
	id, err := s.Create("mix", []Track{readyTrackFile(t, true)})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.load()
	entry, _ := findStoredPlaylistByID(before, id)
	root, err := s.playlistRoot(id, false)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(root, entry.ActiveVersion)
	*fail = true
	if err := s.SaveID(id, "mix", []Track{readyTrackFile(t, true)}); err == nil {
		t.Fatal("SaveID unexpectedly succeeded")
	}
	versions, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Name() != entry.ActiveVersion {
		t.Fatalf("orphan versions remain: %+v", versions)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("referenced version removed: %v", err)
	}
}

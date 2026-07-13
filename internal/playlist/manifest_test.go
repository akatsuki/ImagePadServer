package playlist

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

func testActiveRecipe() video.RadioRenderRecipe {
	return video.NewRadioRenderRecipe(video.QualityPreset{
		Height: 720, VideoBitrate: "2400k", MaxRate: "2800k", BufferSize: "5600k",
		AudioBitrate: "160k", CRF: 23, RadioLatency: "rtsp-ultra",
	}, video.CPUVideoEncoder(video.EncoderLowLatency), "loudnorm=I=-14:LRA=11:TP=-1.5", 180)
}

func observedForContract(c video.RadioEncodingContract) video.RadioAssetSpec {
	return video.RadioAssetSpec{
		Video: video.RadioObservedVideo{Codec: c.Video.Codec, Width: c.Video.Width, Height: c.Video.Height, FrameRate: c.Video.FrameRate, PixelFormat: c.Video.PixelFormat, BFrames: c.Video.BFrames, GOPFrames: c.Video.GOPFrames, Bitrate: c.Video.RateControl.Bitrate},
		Audio: video.RadioObservedAudio{Codec: c.Audio.Codec, Bitrate: c.Audio.Bitrate, SampleRate: c.Audio.SampleRate, Channels: c.Audio.Channels},
	}
}

func readyTrackFile(t *testing.T, source bool) Track {
	t.Helper()
	dir := t.TempDir()
	media := filepath.Join(dir, "track.mp4")
	if err := os.WriteFile(media, []byte("media-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	track := Track{ID: "track-1", Title: "Track", Status: TrackReady, MediaPath: media}
	if source {
		track.SourcePath = filepath.Join(dir, "track.m4a")
		if err := os.WriteFile(track.SourcePath, []byte("source-v1"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	contract := testActiveRecipe().NormalizedEncodingContract()
	track.EncodingContract = &contract
	return track
}

func TestExplicitSaveWritesV1ManifestAndLoadDoesNoProbeOrHash(t *testing.T) {
	recipe := testActiveRecipe()
	probeCalls, hashCalls := 0, 0
	s := NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{
		ActiveRecipe: func() video.RadioRenderRecipe { return recipe },
		ObserveAsset: func(context.Context, string) (video.RadioAssetSpec, error) {
			probeCalls++
			return observedForContract(recipe.NormalizedEncodingContract()), nil
		},
		HashFile: func(path string) (string, error) {
			hashCalls++
			return SHA256File(path)
		},
	})
	if err := s.Save("display name", []Track{readyTrackFile(t, true)}); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 1 || hashCalls != 2 {
		t.Fatalf("save calls probe=%d hash=%d, want 1/2", probeCalls, hashCalls)
	}
	index, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Playlists) != 1 || index.Playlists[0].ID == "" || index.Playlists[0].ActiveVersion == "" {
		t.Fatalf("index identity/pointer missing: %+v", index.Playlists)
	}
	manifestPath := filepath.Join(s.mediaDir, index.Playlists[0].ID, index.Playlists[0].ActiveVersion, PlaylistManifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest PlaylistManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.EncodingFingerprint != recipe.EncodingFingerprint() || manifest.CreatedWith.AppVersion == "" || manifest.UpdatedWith.BuildVersion == "" {
		t.Fatalf("manifest diagnostics/contract missing: %+v", manifest)
	}
	if len(manifest.Tracks) != 1 || manifest.Tracks[0].MediaSHA256 == "" || manifest.Tracks[0].SourceSHA256 == "" || manifest.Tracks[0].ObservedEncoding.Video.Codec != "h264" {
		t.Fatalf("track evidence missing: %+v", manifest.Tracks)
	}
	if _, err := s.Load("display name"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuditCanonical(recipe.NormalizedEncodingContract()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Materialize("display name", filepath.Join(t.TempDir(), "runtime")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 1 || hashCalls != 2 {
		t.Fatalf("load/audit/materialize/list performed expensive work: probe=%d hash=%d", probeCalls, hashCalls)
	}
}

func TestAuditCanonicalFlagsContractMutationsAndIgnoresAppVersion(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save("mix", []Track{readyTrackFile(t, true)}); err != nil {
		t.Fatal(err)
	}
	active := testActiveRecipe().NormalizedEncodingContract()
	mutations := map[string]func(*video.RadioEncodingContract){
		"resolution": func(c *video.RadioEncodingContract) { c.Video.Height = 1080 },
		"audio":      func(c *video.RadioEncodingContract) { c.Audio.SampleRate = 44100 },
		"gop":        func(c *video.RadioEncodingContract) { c.Video.GOPFrames = 60 },
		"codec":      func(c *video.RadioEncodingContract) { c.Video.Codec = "hevc" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := active.Clone()
			mutate(&candidate)
			audits, err := s.AuditCanonical(candidate)
			if err != nil || len(audits) != 1 || !audits[0].NeedsRegeneration || !audits[0].Tracks[0].NeedsRegeneration {
				t.Fatalf("audit = %+v, err=%v", audits, err)
			}
		})
	}
	index, _ := s.load()
	manifestPath := filepath.Join(s.mediaDir, index.Playlists[0].ID, index.Playlists[0].ActiveVersion, PlaylistManifestFileName)
	var manifest PlaylistManifest
	data, _ := os.ReadFile(manifestPath)
	_ = json.Unmarshal(data, &manifest)
	manifest.UpdatedWith.AppVersion = "v999.0.0"
	manifest.Digest, _ = manifest.computedDigest()
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	index.Playlists[0].ManifestDigest = manifest.Digest
	if err := s.write(index); err != nil {
		t.Fatal(err)
	}
	audits, err := s.AuditCanonical(active)
	if err != nil || audits[0].NeedsRegeneration {
		t.Fatalf("newer app with same contract blocked: %+v, err=%v", audits, err)
	}
}

func TestAuditCanonicalFlagsRenderRecipeMismatch(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save("mix", []Track{readyTrackFile(t, true)}); err != nil {
		t.Fatal(err)
	}
	index, _ := s.load()
	entry := index.Playlists[0]
	path, _ := s.manifestPath(entry)
	data, _ := os.ReadFile(path)
	var manifest PlaylistManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.RenderRecipeContract.Waveform.Mode = "point"
	manifest.RenderRecipeFingerprint = manifest.RenderRecipeContract.Fingerprint()
	manifest.Tracks[0].RenderRecipeContract = manifest.RenderRecipeContract
	manifest.Tracks[0].RenderRecipeFingerprint = manifest.RenderRecipeFingerprint
	manifest.Digest, _ = manifest.computedDigest()
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	index.Playlists[0].ManifestDigest = manifest.Digest
	if err := s.write(index); err != nil {
		t.Fatal(err)
	}
	audits, err := s.AuditCanonical(testActiveRecipe().StreamEncodingContract())
	if err != nil || !audits[0].NeedsRegeneration || !audits[0].Tracks[0].NeedsRegeneration {
		t.Fatalf("audit=%+v err=%v", audits, err)
	}
}

func TestAuditCanonicalBlocksMissingMalformedFutureAndEscapedManifest(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Store, storedPlaylist, string)
	}{
		{"missing legacy", func(_ *Store, _ storedPlaylist, manifest string) { _ = os.Remove(manifest) }},
		{"malformed", func(_ *Store, _ storedPlaylist, manifest string) {
			_ = os.WriteFile(manifest, []byte(`{"schemaVersion":`), 0600)
		}},
		{"future schema", func(_ *Store, _ storedPlaylist, manifest string) {
			data, _ := os.ReadFile(manifest)
			var m PlaylistManifest
			_ = json.Unmarshal(data, &m)
			m.SchemaVersion = PlaylistManifestSchemaVersion + 1
			data, _ = json.Marshal(m)
			_ = os.WriteFile(manifest, data, 0600)
		}},
		{"path escape", func(s *Store, p storedPlaylist, _ string) {
			f, _ := s.load()
			f.Playlists[0].ActiveVersion = `..\..\outside`
			_ = s.write(f)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			track := readyTrackFile(t, true)
			if err := s.Save("mix", []Track{track}); err != nil {
				t.Fatal(err)
			}
			f, _ := s.load()
			p := f.Playlists[0]
			manifest := filepath.Join(s.mediaDir, p.ID, p.ActiveVersion, PlaylistManifestFileName)
			tc.mutate(s, p, manifest)
			audits, err := s.AuditCanonical(testActiveRecipe().NormalizedEncodingContract())
			wantIncompatible := tc.name == "path escape"
			if err != nil || len(audits) != 1 || audits[0].Incompatible != wantIncompatible || audits[0].NeedsRegeneration == wantIncompatible {
				t.Fatalf("audit = %+v, err=%v", audits, err)
			}
			loaded, err := s.Load("mix")
			if err != nil && !strings.Contains(tc.name, "path escape") {
				t.Fatalf("Load: %v", err)
			}
			if err == nil && len(loaded) > 0 && loaded[0].Status == TrackReady {
				t.Fatalf("blocked manifest materialized ready media: %+v", loaded)
			}
			if tc.name != "path escape" && (len(loaded) != 1 || loaded[0].Status != TrackPreparing || !loaded[0].NeedsRegeneration) {
				t.Fatalf("retained source did not enter regeneration state: %+v", loaded)
			}
		})
	}
}

func TestAuditCanonicalBlocksTrackAssetPathEscape(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save("mix", []Track{readyTrackFile(t, true)}); err != nil {
		t.Fatal(err)
	}
	f, _ := s.load()
	entry := f.Playlists[0]
	manifestPath := filepath.Join(s.mediaDir, entry.ID, entry.ActiveVersion, PlaylistManifestFileName)
	data, _ := os.ReadFile(manifestPath)
	var manifest PlaylistManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Tracks[0].Track.MediaPath = filepath.Join("..", "..", "outside.mp4")
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	audits, err := s.AuditCanonical(testActiveRecipe().NormalizedEncodingContract())
	if err != nil || len(audits) != 1 || !audits[0].NeedsRegeneration || !audits[0].Tracks[0].NeedsRegeneration {
		t.Fatalf("escaped track path audit = %+v, err=%v", audits, err)
	}
}

func TestManifestSchemaFixtures(t *testing.T) {
	matching, err := os.ReadFile(filepath.Join("testdata", "manifest_matching_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest PlaylistManifest
	if err := json.Unmarshal(matching, &manifest); err != nil || manifest.SchemaVersion != PlaylistManifestSchemaVersion {
		t.Fatalf("matching fixture: schema=%d err=%v", manifest.SchemaVersion, err)
	}
	future, err := os.ReadFile(filepath.Join("testdata", "manifest_future.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(future, &manifest); err != nil || manifest.SchemaVersion <= PlaylistManifestSchemaVersion {
		t.Fatalf("future fixture: schema=%d err=%v", manifest.SchemaVersion, err)
	}
	malformed, err := os.ReadFile(filepath.Join("testdata", "manifest_malformed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(malformed, &manifest) == nil {
		t.Fatal("malformed fixture parsed")
	}
}

func TestLegacyPlaylistUsesRetainedContainedSourceForMigrationOnly(t *testing.T) {
	s := newTestStore(t)
	legacyDir := filepath.Join(s.mediaDir, "legacy-name")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(legacyDir, "source-track.m4a")
	media := filepath.Join(legacyDir, "media-track.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(media, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy := storeFile{Playlists: []storedPlaylist{{Name: "legacy", Tracks: []Track{{ID: "track", Status: TrackReady, MediaPath: media, SourcePath: source}}}}}
	if err := s.write(legacy); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Status != TrackPreparing || !loaded[0].NeedsRegeneration || loaded[0].MediaPath != "" || loaded[0].SourcePath != source {
		t.Fatalf("legacy migration state = %+v", loaded)
	}
	audits, err := s.AuditCanonical(testActiveRecipe().NormalizedEncodingContract())
	if err != nil || len(audits) != 1 || !audits[0].NeedsRegeneration || audits[0].Incompatible {
		t.Fatalf("legacy audit = %+v, err=%v", audits, err)
	}
}

func TestSaveProbeOrHashFailureLeavesOldVersionUsable(t *testing.T) {
	for _, failAt := range []string{"probe", "hash"} {
		t.Run(failAt, func(t *testing.T) {
			recipe := testActiveRecipe()
			fail := false
			s := NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{
				ActiveRecipe: func() video.RadioRenderRecipe { return recipe },
				ObserveAsset: func(context.Context, string) (video.RadioAssetSpec, error) {
					if fail && failAt == "probe" {
						return video.RadioAssetSpec{}, errors.New("probe failed")
					}
					return observedForContract(recipe.NormalizedEncodingContract()), nil
				},
				HashFile: func(path string) (string, error) {
					if fail && failAt == "hash" {
						return "", errors.New("hash failed")
					}
					return SHA256File(path)
				},
			})
			old := readyTrackFile(t, true)
			if err := s.Save("mix", []Track{old}); err != nil {
				t.Fatal(err)
			}
			before, _ := s.load()
			fail = true
			if err := s.Save("mix", []Track{readyTrackFile(t, true)}); err == nil {
				t.Fatal("save unexpectedly succeeded")
			}
			after, _ := s.load()
			if after.Playlists[0].ActiveVersion != before.Playlists[0].ActiveVersion {
				t.Fatal("failed save advanced index")
			}
			loaded, err := s.Load("mix")
			if err != nil || len(loaded) != 1 || loaded[0].Status != TrackReady {
				t.Fatalf("old version unusable: %+v, %v", loaded, err)
			}
		})
	}
}

func TestIndexAndVersionCommitFailuresLeaveOldPointerUsable(t *testing.T) {
	recipe := testActiveRecipe()
	for _, failAt := range []string{"version", "index"} {
		t.Run(failAt, func(t *testing.T) {
			fail := false
			s := NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{
				ActiveRecipe: func() video.RadioRenderRecipe { return recipe },
				ObserveAsset: func(context.Context, string) (video.RadioAssetSpec, error) {
					return observedForContract(recipe.NormalizedEncodingContract()), nil
				},
				Rename: func(old, new string) error {
					if fail && ((failAt == "version" && strings.Contains(filepath.Base(old), ".tmp-")) || (failAt == "index" && strings.HasSuffix(old, ".tmp"))) {
						return errors.New("rename failed")
					}
					return os.Rename(old, new)
				},
			})
			if err := s.Save("mix", []Track{readyTrackFile(t, true)}); err != nil {
				t.Fatal(err)
			}
			before, _ := s.load()
			fail = true
			if err := s.Save("mix", []Track{readyTrackFile(t, true)}); err == nil {
				t.Fatal("save unexpectedly succeeded")
			}
			after, _ := s.load()
			if after.Playlists[0].ActiveVersion != before.Playlists[0].ActiveVersion {
				t.Fatal("failed commit advanced pointer")
			}
		})
	}
}

func TestPartialRegenerationOnlyClearsSuccessfulTrack(t *testing.T) {
	s := newTestStore(t)
	first, second := readyTrackFile(t, true), readyTrackFile(t, true)
	second.ID = "track-2"
	old := testActiveRecipe().NormalizedEncodingContract()
	old.Video.GOPFrames = 60
	first.EncodingContract, second.EncodingContract = &old, &old
	if err := s.Save("mix", []Track{first, second}); err != nil {
		t.Fatal(err)
	}
	audits, _ := s.AuditCanonical(testActiveRecipe().NormalizedEncodingContract())
	if len(audits[0].Tracks) != 2 || !audits[0].Tracks[0].NeedsRegeneration || !audits[0].Tracks[1].NeedsRegeneration {
		t.Fatalf("precondition: %+v", audits)
	}
	replacement := readyTrackFile(t, true)
	replacement.ID = first.ID
	if err := s.RegenerateTrack("mix", first.ID, replacement); err != nil {
		t.Fatal(err)
	}
	audits, _ = s.AuditCanonical(testActiveRecipe().NormalizedEncodingContract())
	if audits[0].Tracks[0].NeedsRegeneration || !audits[0].Tracks[1].NeedsRegeneration || !audits[0].NeedsRegeneration {
		t.Fatalf("partial regeneration flags = %+v", audits[0])
	}
}

func TestContractMismatchWithoutRetainedSourceIsIncompatible(t *testing.T) {
	s := newTestStore(t)
	track := readyTrackFile(t, false)
	old := testActiveRecipe().NormalizedEncodingContract()
	old.Video.Codec = "hevc"
	track.EncodingContract = &old
	if err := s.Save("mix", []Track{track}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load("mix")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Status != TrackFailed || !loaded[0].Incompatible || loaded[0].NeedsRegeneration {
		t.Fatalf("no-source mismatch = %+v", loaded)
	}
}

func TestFailedRegenerationPreservesBlockedVersion(t *testing.T) {
	recipe := testActiveRecipe()
	failProbe := false
	s := NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{
		ActiveRecipe: func() video.RadioRenderRecipe { return recipe },
		ObserveAsset: func(context.Context, string) (video.RadioAssetSpec, error) {
			if failProbe {
				return video.RadioAssetSpec{}, errors.New("probe failed")
			}
			return observedForContract(recipe.NormalizedEncodingContract()), nil
		},
	})
	track := readyTrackFile(t, true)
	old := recipe.NormalizedEncodingContract()
	old.Video.GOPFrames = 60
	track.EncodingContract = &old
	if err := s.Save("mix", []Track{track}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.load()
	oldVersionDir := filepath.Join(s.mediaDir, before.Playlists[0].ID, before.Playlists[0].ActiveVersion)
	failProbe = true
	replacement := readyTrackFile(t, true)
	replacement.ID = track.ID
	if err := s.RegenerateTrack("mix", track.ID, replacement); err == nil {
		t.Fatal("regeneration unexpectedly succeeded")
	}
	after, _ := s.load()
	if after.Playlists[0].ActiveVersion != before.Playlists[0].ActiveVersion {
		t.Fatal("failed regeneration advanced index")
	}
	if _, err := os.Stat(oldVersionDir); err != nil {
		t.Fatalf("old blocked version was removed: %v", err)
	}
	audits, err := s.AuditCanonical(recipe.NormalizedEncodingContract())
	if err != nil || len(audits) != 1 || !audits[0].NeedsRegeneration {
		t.Fatalf("failed regeneration cleared flags: %+v, err=%v", audits, err)
	}
}

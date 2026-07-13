package playlist

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestSaveBoundsProbeWithTimeout(t *testing.T) {
	recipe := testActiveRecipe()
	s := NewStoreWithOptions(filepath.Join(t.TempDir(), "playlists.json"), StoreOptions{
		ActiveRecipe: func() video.RadioRenderRecipe { return recipe }, ProbeTimeout: 20 * time.Millisecond,
		ObserveAsset: func(ctx context.Context, _ string) (video.RadioAssetSpec, error) {
			<-ctx.Done()
			return video.RadioAssetSpec{}, ctx.Err()
		},
	})
	start := time.Now()
	err := s.Save("mix", []Track{readyTrackFile(t, true)})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("timeout err=%v elapsed=%v", err, time.Since(start))
	}
}

func TestAuditFlagsDerivedFromBlockedTracks(t *testing.T) {
	cases := []struct {
		name                                  string
		sources                               []bool
		wantNeeds, wantIncompatible           bool
		wantNeedsCount, wantIncompatibleCount int
	}{
		{"source only", []bool{true}, true, false, 1, 0},
		{"no source", []bool{false}, false, true, 0, 1},
		{"mixed", []bool{true, false}, true, true, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			tracks := make([]Track, len(tc.sources))
			old := testActiveRecipe().StreamEncodingContract()
			old.Video.GOPFrames = 60
			for i, source := range tc.sources {
				tracks[i] = readyTrackFile(t, source)
				tracks[i].ID = string(rune('a' + i))
				tracks[i].EncodingContract = &old
			}
			if err := s.Save("mix", tracks); err != nil {
				t.Fatal(err)
			}
			audits, err := s.AuditCanonical(testActiveRecipe().StreamEncodingContract())
			if err != nil {
				t.Fatal(err)
			}
			a := audits[0]
			if a.NeedsRegeneration != tc.wantNeeds || a.Incompatible != tc.wantIncompatible || a.NeedsRegenerationCount != tc.wantNeedsCount || a.IncompatibleCount != tc.wantIncompatibleCount {
				t.Fatalf("audit=%+v", a)
			}
			for _, track := range a.Tracks {
				if track.NeedsRegeneration && track.Incompatible {
					t.Fatalf("contradictory track=%+v", track)
				}
			}
		})
	}
}

func TestManifestPersistsRegenerationReasonsAndAggregateCounts(t *testing.T) {
	s := newTestStore(t)
	track := readyTrackFile(t, true)
	old := testActiveRecipe().StreamEncodingContract()
	old.Video.GOPFrames = 60
	track.EncodingContract = &old
	if err := s.Save("mix", []Track{track}); err != nil {
		t.Fatal(err)
	}
	index, _ := s.load()
	manifest, err := s.readManifest(index.Playlists[0])
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tracks[0].RegenerationReason == "" || manifest.Diagnostics.NeedsRegenerationCount != 1 || len(manifest.Diagnostics.Reasons) == 0 {
		t.Fatalf("manifest diagnostics=%+v track=%+v", manifest.Diagnostics, manifest.Tracks[0])
	}
}

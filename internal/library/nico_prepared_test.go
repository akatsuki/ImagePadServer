package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitPreparedNicoMediaPublishesOnlyAfterAllArtifactsAreReady(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	oldSource := filepath.Join(t.TempDir(), "old.mp4")
	if err := os.WriteFile(oldSource, []byte("old-current"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(oldSource, CurrentImage{Kind: "video", PublicName: "old.mp4"}); err != nil {
		t.Fatal(err)
	}
	old := store.Current()
	if old == nil {
		t.Fatal("old current missing")
	}

	staging := t.TempDir()
	source := filepath.Join(staging, "rendered.mp4")
	thumbnail := filepath.Join(staging, "thumb.jpg")
	snapshot := filepath.Join(staging, "snapshot.json")
	hlsDir := filepath.Join(staging, "hls")
	if err := os.MkdirAll(hlsDir, 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		source:    []byte("new-rendered"),
		thumbnail: []byte("new-thumb"),
		snapshot:  []byte(`{"schemaVersion":1}`),
		filepath.Join(hlsDir, "segment-00000.ts"): []byte("new-segment"),
		filepath.Join(hlsDir, "playlist.m3u8"):    []byte("#EXTM3U\n#EXTINF:4.000,\nsegment-00000.ts\n#EXT-X-ENDLIST\n"),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.CommitPreparedNicoMedia(PreparedNicoMedia{
		Info:       CurrentImage{ID: "nico-1", Kind: "video", FileName: "rendered.mp4", PublicName: "rendered.mp4", ContentType: "video/mp4", OriginalName: "sm9.mp4"},
		SourcePath: source, ThumbnailPath: thumbnail, SnapshotPath: snapshot, HLSDir: hlsDir, RunID: "run-1", SelectCurrent: true,
		ExpectedRevision: store.PublishedRevision(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "nico-1" || store.Current() == nil || store.Current().ID != "nico-1" {
		t.Fatalf("got=%#v current=%#v", got, store.Current())
	}
	if converted, item, ok := store.ConvertedPath("nico-1"); !ok || item.ID != "nico-1" || converted == "" {
		t.Fatalf("converted path = %q item=%#v ok=%v", converted, item, ok)
	}
	for _, path := range []string{
		filepath.Join(store.Dir(), "rendered.mp4"),
		filepath.Join(store.Dir(), "history-nico-1.mp4"),
		filepath.Join(store.Dir(), "niconico-snapshot-nico-1.json"),
		filepath.Join(store.Dir(), "current-nico-1.m3u8"),
		filepath.Join(store.Dir(), "current-nico-1-run-1-00000.ts"),
		filepath.Join(filepath.Dir(store.Dir()), "converted", "nico-1", "current-nico-1.m3u8"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing committed artifact %s: %v", path, err)
		}
	}
	playlist, err := os.ReadFile(filepath.Join(store.Dir(), "current-nico-1.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(playlist), "current-nico-1-run-1-00000.ts") {
		t.Fatalf("playlist = %s", playlist)
	}
	if oldAfter := store.Current(); oldAfter.ID == old.ID {
		t.Fatalf("old current still selected: %#v", oldAfter)
	}
}

func TestCommitPreparedNicoMediaRejectsRevisionConflictWithoutTouchingCurrent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	oldSource := filepath.Join(t.TempDir(), "old.mp4")
	if err := os.WriteFile(oldSource, []byte("old-current"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(oldSource, CurrentImage{Kind: "video", PublicName: "old.mp4"}); err != nil {
		t.Fatal(err)
	}
	old := store.Current()
	staging := t.TempDir()
	source := filepath.Join(staging, "rendered.mp4")
	if err := os.WriteFile(source, []byte("new-rendered"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitPreparedNicoMedia(PreparedNicoMedia{
		Info:       CurrentImage{ID: "nico-conflict", Kind: "video", FileName: "rendered.mp4", PublicName: "rendered.mp4"},
		SourcePath: source, RunID: "run-1", ExpectedRevision: store.PublishedRevision() - 1, SelectCurrent: true,
	})
	if err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("error = %v", err)
	}
	if current := store.Current(); current == nil || current.ID != old.ID {
		t.Fatalf("current changed after conflict: %#v", current)
	}
}

func TestCommitPreparedNicoMediaRollsBackNewArtifactsWhenStateWriteFails(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	oldSource := filepath.Join(t.TempDir(), "old.mp4")
	if err := os.WriteFile(oldSource, []byte("old-current"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(oldSource, CurrentImage{Kind: "video", PublicName: "old.mp4"}); err != nil {
		t.Fatal(err)
	}
	old := store.Current()
	if old == nil {
		t.Fatal("old current missing")
	}

	staging := t.TempDir()
	source := filepath.Join(staging, "rendered.mp4")
	snapshot := filepath.Join(staging, "snapshot.json")
	hlsDir := filepath.Join(staging, "hls")
	if err := os.MkdirAll(hlsDir, 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		source:   []byte("new-rendered"),
		snapshot: []byte(`{"schemaVersion":1}`),
		filepath.Join(hlsDir, "segment-00000.ts"): []byte("new-segment"),
		filepath.Join(hlsDir, "playlist.m3u8"):    []byte("#EXTM3U\n#EXTINF:4.000,\nsegment-00000.ts\n#EXT-X-ENDLIST\n"),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Make the final state target impossible to replace after all media files
	// have been installed. The commit must remove only the new artifacts.
	statePath := filepath.Join(store.Dir(), "state.json")
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}

	_, err = store.CommitPreparedNicoMedia(PreparedNicoMedia{
		Info:       CurrentImage{ID: "nico-state-fail", Kind: "video", FileName: "rendered-state-fail.mp4", PublicName: "current-video.mp4"},
		SourcePath: source, SnapshotPath: snapshot, HLSDir: hlsDir, RunID: "run-state-fail", SelectCurrent: true,
		ExpectedRevision: store.PublishedRevision(),
	})
	if err == nil {
		t.Fatal("commit unexpectedly succeeded")
	}
	if current := store.Current(); current == nil || current.ID != old.ID {
		t.Fatalf("current changed after state write failure: %#v", current)
	}
	for _, path := range []string{
		filepath.Join(store.Dir(), "rendered-state-fail.mp4"),
		filepath.Join(store.Dir(), "history-nico-state-fail.mp4"),
		filepath.Join(store.Dir(), "niconico-snapshot-nico-state-fail.json"),
		filepath.Join(store.Dir(), "current-nico-state-fail.m3u8"),
		filepath.Join(filepath.Dir(store.Dir()), "converted", "nico-state-fail"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("new artifact survived failed commit: %s err=%v", path, statErr)
		}
	}
}

func TestCommitPreparedNicoMediaQueueKeepsCurrentAndStagesHLSOnlyForHistory(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	oldSource := filepath.Join(t.TempDir(), "old.mp4")
	if err := os.WriteFile(oldSource, []byte("old-current"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(oldSource, CurrentImage{Kind: "video", PublicName: "old.mp4"}); err != nil {
		t.Fatal(err)
	}
	old := store.Current()
	if old == nil {
		t.Fatal("old current missing")
	}

	staging := t.TempDir()
	source := filepath.Join(staging, "rendered.mp4")
	snapshot := filepath.Join(staging, "snapshot.json")
	hlsDir := filepath.Join(staging, "hls")
	if err := os.MkdirAll(hlsDir, 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		source:   []byte("queued-rendered"),
		snapshot: []byte(`{"schemaVersion":1}`),
		filepath.Join(hlsDir, "segment-00000.ts"): []byte("queued-segment"),
		filepath.Join(hlsDir, "playlist.m3u8"):    []byte("#EXTM3U\n#EXTINF:4.000,\nsegment-00000.ts\n#EXT-X-ENDLIST\n"),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.CommitPreparedNicoMedia(PreparedNicoMedia{
		Info:       CurrentImage{ID: "nico-queued", Kind: "video", FileName: "queued-rendered.mp4", PublicName: "queued-video.mp4"},
		SourcePath: source, SnapshotPath: snapshot, HLSDir: hlsDir, RunID: "run-queued", SelectCurrent: false,
		ExpectedRevision: store.PublishedRevision(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicName != "queued-video.mp4" {
		t.Fatalf("queued metadata = %#v", got)
	}
	if current := store.Current(); current == nil || current.ID != old.ID {
		t.Fatalf("queue commit changed current: %#v", current)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "current-nico-queued.m3u8")); !os.IsNotExist(err) {
		t.Fatalf("queue commit exposed root HLS: err=%v", err)
	}
	converted, item, ok := store.ConvertedPath("nico-queued")
	if !ok || item.ID != "nico-queued" {
		t.Fatalf("queued converted path = %q item=%#v ok=%v", converted, item, ok)
	}
	if _, err := os.Stat(filepath.Join(converted, "current-nico-queued.m3u8")); err != nil {
		t.Fatalf("queued converted playlist missing: %v", err)
	}
}

func TestCommitPreparedNicoMediaRejectsMissingHLSSegmentBeforeInstall(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	oldSource := filepath.Join(t.TempDir(), "old.mp4")
	if err := os.WriteFile(oldSource, []byte("old-current"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(oldSource, CurrentImage{Kind: "video", PublicName: "old.mp4"}); err != nil {
		t.Fatal(err)
	}
	old := store.Current()
	staging := t.TempDir()
	source := filepath.Join(staging, "rendered.mp4")
	snapshot := filepath.Join(staging, "snapshot.json")
	hlsDir := filepath.Join(staging, "hls")
	if err := os.MkdirAll(hlsDir, 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{
		source:                                 []byte("new-rendered"),
		snapshot:                               []byte(`{"schemaVersion":1}`),
		filepath.Join(hlsDir, "playlist.m3u8"): []byte("#EXTM3U\n#EXTINF:4.000,\nmissing.ts\n#EXT-X-ENDLIST\n"),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.CommitPreparedNicoMedia(PreparedNicoMedia{
		Info:       CurrentImage{ID: "nico-missing-segment", Kind: "video", FileName: "missing-segment.mp4"},
		SourcePath: source, SnapshotPath: snapshot, HLSDir: hlsDir, RunID: "run-missing-segment", SelectCurrent: true,
		ExpectedRevision: store.PublishedRevision(),
	})
	if err == nil || !strings.Contains(err.Error(), "HLS segment") {
		t.Fatalf("error = %v, want missing HLS segment rejection", err)
	}
	if current := store.Current(); current == nil || current.ID != old.ID {
		t.Fatalf("current changed after missing segment: %#v", current)
	}
	if _, statErr := os.Stat(filepath.Join(store.Dir(), "missing-segment.mp4")); !os.IsNotExist(statErr) {
		t.Fatalf("new MP4 survived missing segment rejection: %v", statErr)
	}
}

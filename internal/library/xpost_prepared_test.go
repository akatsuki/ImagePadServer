package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedVideoKeepsXIdentityWithoutAnotherConversion(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	hls := filepath.Join(dir, "hls")
	if err := os.MkdirAll(hls, 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{filepath.Join(dir, "video.mp4"): "encoded", filepath.Join(dir, "job.json"): "{}", filepath.Join(hls, "s0.ts"): "segment", filepath.Join(hls, "playlist.m3u8"): "#EXTM3U\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.CommitPreparedVideo(PreparedVideo{Info: CurrentImage{ID: "xpost-1", Kind: "video", FileName: "xpost.mp4"}, SourcePath: filepath.Join(dir, "video.mp4"), SnapshotPath: filepath.Join(dir, "job.json"), HLSDir: hls, RunID: "run-1", SelectCurrent: true, ExpectedRevision: store.PublishedRevision(), Resolution: "xpost", SnapshotPrefix: "xpost-snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Converted || len(got.Resolutions) != 1 || got.Resolutions[0] != "xpost" {
		t.Fatalf("incorrect conversion identity: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "xpost-snapshot-xpost-1.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := store.ConvertedPath(got.ID); !ok {
		t.Fatal("prepared HLS missing")
	}
}

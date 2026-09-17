package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/niconico"
	"imagepadserver/internal/video"
)

func TestNiconicoTargetGeometryPreservesAspectAndEvenDimensions(t *testing.T) {
	width, height := niconicoTargetGeometry(video.MediaProbe{Streams: []video.MediaStream{{CodecType: "video", Width: 1920, Height: 1080}}}, 720)
	if width != 1280 || height != 720 {
		t.Fatalf("geometry = %dx%d, want 1280x720", width, height)
	}
	width, height = niconicoTargetGeometry(video.MediaProbe{Streams: []video.MediaStream{{CodecType: "video", Width: 1080, Height: 1920}}}, 360)
	if width%2 != 0 || height%2 != 0 || width <= 0 || height != 360 {
		t.Fatalf("vertical geometry = %dx%d", width, height)
	}
}

func TestWriteNicoSnapshotUsesPrivateSidecarName(t *testing.T) {
	dir := t.TempDir()
	snapshot := niconico.Snapshot{SchemaVersion: 1, VideoID: "sm9", CommentStatus: niconico.CommentStatusEmpty}
	if err := writeNicoSnapshot(dir, "media-1", snapshot); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "niconico-snapshot-media-1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got niconico.Snapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.VideoID != "sm9" || !strings.HasSuffix(path, ".json") {
		t.Fatalf("snapshot = %#v path=%q", got, path)
	}
}

func TestProcessPreparedNicoVideoKeepsRenderedFileAfterPublish(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	source := filepath.Join(store.Dir(), "niconico-commented-rendered.mp4")
	thumbnail := filepath.Join(store.Dir(), "niconico-thumb.jpg")
	if err := os.WriteFile(source, []byte("rendered-mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbnail, []byte("thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	previousEnqueue := enqueueNiconicoCommentedVideo
	enqueueNiconicoCommentedVideo = func(string, string, string, string, int) string { return "" }
	t.Cleanup(func() { enqueueNiconicoCommentedVideo = previousEnqueue })

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/upload-url", nil)
	snapshot := niconico.Snapshot{SchemaVersion: 1, VideoID: "sm9", CommentStatus: niconico.CommentStatusEmpty}
	if _, err := srv.processPreparedNicoVideo(req, source, "sm9.mp4", thumbnail, snapshot, false); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	if current == nil || current.FileName != filepath.Base(source) {
		t.Fatalf("current = %#v", current)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), current.FileName)); err != nil {
		t.Fatalf("rendered file was removed during publish: %v", err)
	}
}

package obsrtmp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestSink(t *testing.T) *lhlsSink {
	t.Helper()
	sink, err := newLHLSSink(t.TempDir(), "abc123", 1<<20)
	if err != nil {
		t.Fatalf("newLHLSSink: %v", err)
	}
	t.Cleanup(func() { _ = sink.close() })
	return sink
}

func sinkRequest(method, token, name, body string) *http.Request {
	req := httptest.NewRequest(method, "/"+token+"/"+name, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50505"
	return req
}

func TestLHLSSinkRejectsNonLoopback(t *testing.T) {
	sink := newTestSink(t)
	req := sinkRequest(http.MethodPut, sink.token, "master.m3u8", "data")
	req.RemoteAddr = "203.0.113.7:50505"
	rec := httptest.NewRecorder()
	sink.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback: want 403, got %d", rec.Code)
	}
}

func TestLHLSSinkRejectsWrongToken(t *testing.T) {
	sink := newTestSink(t)
	rec := httptest.NewRecorder()
	sink.ServeHTTP(rec, sinkRequest(http.MethodPut, "wrong-token", "master.m3u8", "data"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong token: want 403, got %d", rec.Code)
	}
}

func TestLHLSSinkRejectsDisallowedMethod(t *testing.T) {
	sink := newTestSink(t)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch} {
		rec := httptest.NewRecorder()
		sink.ServeHTTP(rec, sinkRequest(method, sink.token, "master.m3u8", ""))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("method %s: want 405, got %d", method, rec.Code)
		}
	}
}

func TestLHLSSinkRejectsUnknownFilename(t *testing.T) {
	sink := newTestSink(t)
	for _, name := range []string{"evil.exe", "secret.txt", "stream", "media_.m3u8", "chunk.m4s"} {
		rec := httptest.NewRecorder()
		sink.ServeHTTP(rec, sinkRequest(http.MethodPut, sink.token, name, "data"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("name %q: want 400, got %d", name, rec.Code)
		}
		if fileExists(filepath.Join(sink.dir, name)) {
			t.Fatalf("name %q was written despite rejection", name)
		}
	}
}

func TestLHLSSinkRejectsPathTraversal(t *testing.T) {
	sink := newTestSink(t)
	for _, raw := range []string{"/" + sink.token + "/../escape.m4s", "/" + sink.token + "/sub/chunk-stream0-1.m4s"} {
		req := httptest.NewRequest(http.MethodPut, raw, strings.NewReader("data"))
		req.RemoteAddr = "127.0.0.1:50505"
		rec := httptest.NewRecorder()
		sink.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
			t.Fatalf("traversal %q: want 400/403, got %d", raw, rec.Code)
		}
	}
	// Nothing should have been written outside the session dir.
	if fileExists(filepath.Join(filepath.Dir(sink.dir), "escape.m4s")) {
		t.Fatal("path traversal wrote outside the session directory")
	}
}

func TestLHLSSinkEnforcesBodyLimit(t *testing.T) {
	sink, err := newLHLSSink(t.TempDir(), "abc123", 16)
	if err != nil {
		t.Fatalf("newLHLSSink: %v", err)
	}
	t.Cleanup(func() { _ = sink.close() })
	rec := httptest.NewRecorder()
	sink.ServeHTTP(rec, sinkRequest(http.MethodPut, sink.token, "chunk-stream0-00001.m4s", strings.Repeat("x", 64)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body: want 413, got %d", rec.Code)
	}
}

func TestLHLSSinkStoresSegmentInPlace(t *testing.T) {
	sink := newTestSink(t)
	rec := httptest.NewRecorder()
	sink.ServeHTTP(rec, sinkRequest(http.MethodPut, sink.token, "chunk-stream0-00001.m4s", "segmentbytes"))
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("segment PUT: want 2xx, got %d", rec.Code)
	}
	got, err := os.ReadFile(filepath.Join(sink.dir, "chunk-stream0-00001.m4s"))
	if err != nil {
		t.Fatalf("read segment: %v", err)
	}
	if string(got) != "segmentbytes" {
		t.Fatalf("segment content = %q", got)
	}
}

func TestLHLSSinkPublishesManifestAtomically(t *testing.T) {
	sink := newTestSink(t)
	rec := httptest.NewRecorder()
	sink.ServeHTTP(rec, sinkRequest(http.MethodPut, sink.token, "master.m3u8", "#EXTM3U\n"))
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("manifest PUT: want 2xx, got %d", rec.Code)
	}
	got, err := os.ReadFile(filepath.Join(sink.dir, "master.m3u8"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if string(got) != "#EXTM3U\n" {
		t.Fatalf("manifest content = %q", got)
	}
	// No leftover temp files in the session dir.
	entries, _ := os.ReadDir(sink.dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}

func TestLHLSSinkDeleteRemovesSegment(t *testing.T) {
	sink := newTestSink(t)
	rec := httptest.NewRecorder()
	sink.ServeHTTP(rec, sinkRequest(http.MethodPut, sink.token, "chunk-stream0-00001.m4s", "data"))
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("seed PUT: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	sink.ServeHTTP(rec, sinkRequest(http.MethodDelete, sink.token, "chunk-stream0-00001.m4s", ""))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("delete: want 2xx, got %d", rec.Code)
	}
	if fileExists(filepath.Join(sink.dir, "chunk-stream0-00001.m4s")) {
		t.Fatal("segment still present after DELETE")
	}
}

func TestLHLSSinkReadinessGate(t *testing.T) {
	sink := newTestSink(t)
	if sink.ready() {
		t.Fatal("empty sink should not be ready")
	}
	put := func(name, body string) {
		rec := httptest.NewRecorder()
		sink.ServeHTTP(rec, sinkRequest(http.MethodPut, sink.token, name, body))
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s: %d", name, rec.Code)
		}
	}
	// Media playlist without prefetch + init + segment is not yet ready.
	put("media_0.m3u8", "#EXTM3U\n#EXT-X-VERSION:7\n")
	put("init-stream0.m4s", "initbytes")
	if sink.ready() {
		t.Fatal("missing prefetch + segment should not be ready")
	}
	put("chunk-stream0-00001.m4s", "segmentbytes")
	put("media_0.m3u8", "#EXTM3U\n#EXT-X-PREFETCH:chunk-stream0-00002.m4s\n")
	if !sink.ready() {
		t.Fatal("prefetch + init + segment should be ready")
	}
}

func TestLHLSSinkPublicReadable(t *testing.T) {
	sink := newTestSink(t)
	readable := []string{"master.m3u8", "media_0.m3u8", "init-stream0.m4s", "chunk-stream0-00001.m4s"}
	for _, name := range readable {
		if !sink.publicReadable(name) {
			t.Fatalf("%q should be publicly readable", name)
		}
	}
	hidden := []string{"stream.mpd", "master.m3u8.tmp-123", "../secret", "anything.txt"}
	for _, name := range hidden {
		if sink.publicReadable(name) {
			t.Fatalf("%q must not be publicly readable", name)
		}
	}
}

func TestLHLSSinkCloseRemovesDir(t *testing.T) {
	parent := t.TempDir()
	sink, err := newLHLSSink(parent, "abc123", 1<<20)
	if err != nil {
		t.Fatalf("newLHLSSink: %v", err)
	}
	dir := sink.dir
	if !fileExistsDir(dir) {
		t.Fatal("session dir should exist after creation")
	}
	if err := sink.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if fileExistsDir(dir) {
		t.Fatal("session dir should be removed after close")
	}
}

func fileExistsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

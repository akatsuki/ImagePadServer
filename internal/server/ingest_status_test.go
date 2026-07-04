package server

import (
	"io"
	"strings"
	"testing"
)

func TestIngestPhaseLifecycle(t *testing.T) {
	s := &Server{}

	if got := s.ingestState(); got["active"] != false || got["phase"] != "" {
		t.Fatalf("initial: got %#v, want inactive empty", got)
	}

	s.setIngest(ingestDownloading, "My Track")
	got := s.ingestState()
	if got["active"] != true || got["phase"] != "downloading" || got["title"] != "My Track" {
		t.Fatalf("after set: got %#v", got)
	}

	s.setIngest(ingestAnalyzing, "My Track")
	if s.ingestState()["phase"] != "analyzing" {
		t.Fatalf("after bump: %#v", s.ingestState())
	}

	s.clearIngest()
	if got := s.ingestState(); got["active"] != false || got["phase"] != "" {
		t.Fatalf("after clear: got %#v", got)
	}
}

func TestUploadReceiveProgressTracksHostIngest(t *testing.T) {
	s := &Server{}
	body := io.NopCloser(strings.NewReader(strings.Repeat("x", 100)))

	wrapped, done := s.trackUploadReceiveProgress(body, 100, "remote-video.mp4")
	defer done()

	buf := make([]byte, 40)
	if _, err := io.ReadFull(wrapped, buf); err != nil {
		t.Fatal(err)
	}
	got := s.ingestState()
	if got["active"] != true || got["phase"] != ingestUploading {
		t.Fatalf("ingest state = %#v, want active uploading", got)
	}
	if got["title"] != "remote-video.mp4" {
		t.Fatalf("title = %#v, want remote-video.mp4", got["title"])
	}
	if got["progressPercent"] == 0 {
		t.Fatalf("progressPercent = %#v, want non-zero", got["progressPercent"])
	}
	if !strings.Contains(got["progressText"].(string), "100 B") {
		t.Fatalf("progressText = %#v, want total byte display", got["progressText"])
	}
}

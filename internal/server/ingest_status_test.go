package server

import "testing"

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

package airplay

import (
	"testing"

	"imagepadserver/internal/airplaycontract"
)

func TestVideoViewControllerRejectsStaleAndRefreshesAppliedState(t *testing.T) {
	paths := airplaycontract.VideoViewPaths{
		Control: t.TempDir() + "\\video-view.ini",
		State:   t.TempDir() + "\\video-view.json",
	}
	c := NewVideoViewController(paths, "session-1", 7)
	if _, err := c.Set(VideoViewRequest{
		Mode:                        airplaycontract.VideoViewCover,
		ExpectedSessionID:           "session-1",
		ExpectedPublisherGeneration: 7,
		ExpectedRevision:            0,
	}); err != nil {
		t.Fatalf("initial request: %v", err)
	}
	if _, err := c.Set(VideoViewRequest{
		Mode:                        airplaycontract.VideoViewContain,
		ExpectedSessionID:           "session-1",
		ExpectedPublisherGeneration: 7,
		ExpectedRevision:            1,
	}); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if _, err := c.Set(VideoViewRequest{
		Mode:              airplaycontract.VideoViewCover,
		ExpectedSessionID: "session-1",
		ExpectedRevision:  1,
	}); err != ErrVideoViewStale {
		t.Fatalf("stale request error = %v, want %v", err, ErrVideoViewStale)
	}

	state := c.State()
	state.AppliedRevision = state.ConfiguredRevision
	state.AppliedMode = state.ConfiguredMode
	state.Phase = "active"
	state.OutputPtsNs = 1234
	if err := writeVideoViewState(paths.State, state); err != nil {
		t.Fatalf("write applied state: %v", err)
	}
	refreshed := c.Refresh()
	if refreshed.Phase != "active" || refreshed.AppliedRevision != 2 || refreshed.OutputPtsNs != 1234 {
		t.Fatalf("refresh = %+v", refreshed)
	}
}

func TestVideoViewControllerIgnoresOlderStateSidecar(t *testing.T) {
	paths := airplaycontract.VideoViewPaths{State: t.TempDir() + "\\video-view.json"}
	c := NewVideoViewController(paths, "session-2", 8)
	c.state.ConfiguredRevision = 3
	c.state.AppliedRevision = 2
	c.state.ConfiguredMode = airplaycontract.VideoViewCover
	c.state.AppliedMode = airplaycontract.VideoViewContain
	older := c.state
	older.ConfiguredRevision = 2
	older.AppliedRevision = 1
	older.Phase = "active"
	if err := writeVideoViewState(paths.State, older); err != nil {
		t.Fatalf("write older state: %v", err)
	}
	got := c.Refresh()
	if got.ConfiguredRevision != 3 || got.AppliedRevision != 2 || got.Phase == "active" {
		t.Fatalf("older state was accepted: %+v", got)
	}
}

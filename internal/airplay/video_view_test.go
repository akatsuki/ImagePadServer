package airplay

import (
	"os"
	"path/filepath"
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
		Mode:                        airplaycontract.VideoViewCover,
		ExpectedSessionID:           "session-1",
		ExpectedPublisherGeneration: 7,
		ExpectedRevision:            1,
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

func TestVideoViewControllerRequiresSessionAndGenerationForLiveUpdates(t *testing.T) {
	paths := airplaycontract.VideoViewPaths{
		Control: filepath.Join(t.TempDir(), "video-view.ini"),
		State:   filepath.Join(t.TempDir(), "video-view.json"),
	}
	c := NewVideoViewController(paths, "session-live", 11)

	if _, err := c.Set(VideoViewRequest{
		Mode:             airplaycontract.VideoViewCover,
		ExpectedRevision: 0,
	}); err != ErrVideoViewInvalid {
		t.Fatalf("missing live identity error = %v, want %v", err, ErrVideoViewInvalid)
	}
	if got := c.State(); got.ConfiguredRevision != 0 {
		t.Fatalf("missing live identity changed state: %+v", got)
	}
}

func TestVideoViewControllerDoesNotAdvanceRevisionWhenStatePersistenceFails(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state-dir")
	if err := os.MkdirAll(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	c := NewVideoViewController(airplaycontract.VideoViewPaths{State: statePath}, "session-1", 1)

	if _, err := c.Set(VideoViewRequest{
		Mode:                        airplaycontract.VideoViewCover,
		ExpectedSessionID:           "session-1",
		ExpectedPublisherGeneration: 1,
		ExpectedRevision:            0,
	}); err == nil {
		t.Fatal("state persistence unexpectedly succeeded")
	}
	if got := c.State(); got.ConfiguredRevision != 0 || got.ConfiguredMode != airplaycontract.VideoViewContain {
		t.Fatalf("failed persistence advanced state: %+v", got)
	}
}

func TestManagerPreparesVideoViewGenerationFromCommittedIntent(t *testing.T) {
	oldPaths := airplaycontract.VideoViewPaths{
		Control: filepath.Join(t.TempDir(), "old.ini"),
		State:   filepath.Join(t.TempDir(), "old.json"),
	}
	current := NewVideoViewController(oldPaths, "session-1", 7)
	current.state.ConfiguredRevision = 4
	current.state.ConfiguredMode = airplaycontract.VideoViewCover
	current.state.AppliedRevision = 4
	current.state.AppliedMode = airplaycontract.VideoViewCover
	manager := New(nil)
	manager.videoView = current

	nextPaths := airplaycontract.VideoViewPaths{
		Control: filepath.Join(t.TempDir(), "next.ini"),
		State:   filepath.Join(t.TempDir(), "next.json"),
	}
	next, err := manager.prepareVideoViewGeneration(nextPaths, "session-1", 8)
	if err != nil {
		t.Fatal(err)
	}
	if got := next.State(); got.SessionID != "session-1" || got.PublisherGeneration != 8 || got.ConfiguredRevision != 4 || got.ConfiguredMode != airplaycontract.VideoViewCover || got.AppliedRevision != 0 {
		t.Fatalf("prepared generation state = %+v", got)
	}
	if got := manager.VideoViewStatus(); got.PublisherGeneration != 7 {
		t.Fatalf("uncommitted generation replaced manager state: %+v", got)
	}
	manager.adoptVideoViewGeneration(next)
	if got := manager.VideoViewStatus(); got.PublisherGeneration != 8 {
		t.Fatalf("committed generation was not adopted: %+v", got)
	}
}

func TestVideoViewRefreshPreservesGoPersistenceStateAcrossNativeAck(t *testing.T) {
	paths := airplaycontract.VideoViewPaths{State: filepath.Join(t.TempDir(), "video-view.json")}
	c := NewVideoViewController(paths, "session-1", 3)
	c.state.ConfiguredRevision = 2
	c.state.ConfiguredMode = airplaycontract.VideoViewCover
	c.state.PersistenceState = "failed"
	c.state.PersistenceError = "settings write failed"
	nativeAck := c.state
	nativeAck.AppliedRevision = 2
	nativeAck.AppliedMode = nativeAck.ConfiguredMode
	nativeAck.Phase = "active"
	nativeAck.PersistenceState = ""
	nativeAck.PersistenceError = ""
	if err := writeVideoViewState(paths.State, nativeAck); err != nil {
		t.Fatal(err)
	}
	got := c.Refresh()
	if got.PersistenceState != "failed" || got.PersistenceError != "settings write failed" {
		t.Fatalf("native ACK erased Go persistence state: %+v", got)
	}
}

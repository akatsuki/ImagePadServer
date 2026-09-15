package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

var errFakeInstall = errors.New("install failed")

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestStartVideoToolInstallEnablesOnSuccess(t *testing.T) {
	s, _ := testServer(t, false)
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prevReady, prevEnsure, prevBackoff := videoToolsReady, ensureVideoTools, videoToolInstallBackoff
	t.Cleanup(func() {
		videoToolsReady = prevReady
		ensureVideoTools = prevEnsure
		videoToolInstallBackoff = prevBackoff
	})
	videoToolInstallBackoff = func(int) time.Duration { return 0 }
	videoToolsReady = func() bool { return false }
	ensureVideoTools = func() error { return nil }

	s.startVideoToolInstall()
	waitFor(t, 2*time.Second, func() bool { return s.videoPlayerEnabled() })

	if video.ToolInstallStatus().Failed {
		t.Fatal("tracker should not be failed on success")
	}
}

func TestStartVideoToolInstallKeepsIntentOnFailure(t *testing.T) {
	s, _ := testServer(t, true)
	prevReady, prevEnsure, prevBackoff := videoToolsReady, ensureVideoTools, videoToolInstallBackoff
	t.Cleanup(func() {
		videoToolsReady = prevReady
		ensureVideoTools = prevEnsure
		videoToolInstallBackoff = prevBackoff
	})
	videoToolInstallBackoff = func(int) time.Duration { return 0 }
	videoToolsReady = func() bool { return false }
	ensureVideoTools = func() error { return errFakeInstall }

	s.startVideoToolInstall()
	waitFor(t, 2*time.Second, func() bool { return !s.toolInstallingNow() })

	if !s.videoPlayerEnabled() {
		t.Fatal("video player intent must stay enabled after install failure")
	}
	state := s.videoPlayerState()
	install, ok := state["toolInstall"].(video.ToolInstall)
	if !ok {
		t.Fatalf("toolInstall type = %T, want video.ToolInstall", state["toolInstall"])
	}
	if !install.Failed || install.Message != errFakeInstall.Error() {
		t.Fatalf("toolInstall = %#v, want failed install with %q", install, errFakeInstall)
	}
	if got, _ := state["error"].(string); got != errFakeInstall.Error() {
		t.Fatalf("video player error = %q, want %q", got, errFakeInstall)
	}
}

func TestStartVideoToolInstallSkipsTrailingBackoff(t *testing.T) {
	s, _ := testServer(t, false)
	prevReady, prevEnsure, prevBackoff := videoToolsReady, ensureVideoTools, videoToolInstallBackoff
	t.Cleanup(func() {
		videoToolsReady = prevReady
		ensureVideoTools = prevEnsure
		videoToolInstallBackoff = prevBackoff
	})
	videoToolsReady = func() bool { return false }
	ensureVideoTools = func() error { return errFakeInstall }
	var sleeps int
	videoToolInstallBackoff = func(int) time.Duration {
		sleeps++
		return 0
	}

	s.startVideoToolInstall()
	waitFor(t, 2*time.Second, func() bool { return !s.toolInstallingNow() })

	if sleeps != 3 {
		t.Fatalf("backoff calls = %d, want 3 between 4 attempts", sleeps)
	}
}

func TestVideoPlayerEnableAsyncWhenToolsMissing(t *testing.T) {
	s, _ := testServer(t, false)
	prevReady, prevEnsure, prevBackoff := videoToolsReady, ensureVideoTools, videoToolInstallBackoff
	t.Cleanup(func() {
		videoToolsReady = prevReady
		ensureVideoTools = prevEnsure
		videoToolInstallBackoff = prevBackoff
	})
	videoToolInstallBackoff = func(int) time.Duration { return 0 }
	videoToolsReady = func() bool { return false }
	blocked := make(chan struct{})
	ensureVideoTools = func() error { <-blocked; return nil }

	req := httptest.NewRequest(http.MethodPost, "/api/video-player", strings.NewReader(`{"enabled":true}`))
	rec := httptest.NewRecorder()
	s.handleVideoPlayer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (async accepted)", rec.Code)
	}
	if !s.videoPlayerEnabled() {
		t.Fatal("video player intent must be enabled while install is pending")
	}
	close(blocked)
	waitFor(t, 2*time.Second, func() bool { return !s.toolInstallingNow() })
}

func TestVideoPlayerInstallDoesNotReenableAfterDisable(t *testing.T) {
	s, mux := testServer(t, false)
	prevReady, prevEnsure, prevBackoff := videoToolsReady, ensureVideoTools, videoToolInstallBackoff
	t.Cleanup(func() {
		videoToolsReady = prevReady
		ensureVideoTools = prevEnsure
		videoToolInstallBackoff = prevBackoff
	})
	videoToolInstallBackoff = func(int) time.Duration { return 0 }
	videoToolsReady = func() bool { return false }
	started := make(chan struct{})
	release := make(chan struct{})
	ensureVideoTools = func() error {
		close(started)
		<-release
		return nil
	}

	if rec := adminJSON(t, mux, httptest.NewRequest(http.MethodPost, "/api/video-player", strings.NewReader(`{"enabled":true}`))); rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool installer did not start")
	}
	if rec := adminJSON(t, mux, httptest.NewRequest(http.MethodPost, "/api/video-player", strings.NewReader(`{"enabled":false}`))); rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d: %s", rec.Code, rec.Body.String())
	}
	close(release)
	waitFor(t, 2*time.Second, func() bool { return !s.toolInstallingNow() })

	if s.videoPlayerEnabled() {
		t.Fatal("installer completion must not restore a user-disabled video player")
	}
}

func TestStateIncludesToolInstall(t *testing.T) {
	s, _ := testServer(t, false)
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	st := s.state(req)
	if _, ok := st["toolInstall"]; !ok {
		t.Fatal("state missing toolInstall")
	}
}

func TestVideoPlayerStateSeparatesIntentFromToolReadiness(t *testing.T) {
	s, _ := testServer(t, true)
	previousReady := videoToolsReady
	t.Cleanup(func() { videoToolsReady = previousReady })
	videoToolsReady = func() bool { return false }

	state := s.videoPlayerState()
	if enabled, _ := state["enabled"].(bool); !enabled {
		t.Fatal("video player intent should remain enabled")
	}
	if ready, ok := state["toolsReady"].(bool); !ok || ready {
		t.Fatal("tool readiness must be reported independently")
	}
	if installing, ok := state["installing"].(bool); !ok || installing {
		t.Fatal("tool install should not be reported as active before it starts")
	}
}

func TestStartVideoToolInstallIsIdempotent(t *testing.T) {
	s, _ := testServer(t, false)
	prevReady, prevEnsure, prevBackoff := videoToolsReady, ensureVideoTools, videoToolInstallBackoff
	t.Cleanup(func() {
		videoToolsReady = prevReady
		ensureVideoTools = prevEnsure
		videoToolInstallBackoff = prevBackoff
	})
	videoToolInstallBackoff = func(int) time.Duration { return 0 }
	release := make(chan struct{})
	var calls int
	videoToolsReady = func() bool { return false }
	ensureVideoTools = func() error {
		calls++
		<-release
		return nil
	}

	s.startVideoToolInstall()
	waitFor(t, time.Second, func() bool { return s.toolInstallingNow() })
	s.startVideoToolInstall() // second call must be a no-op while one runs
	close(release)
	waitFor(t, 2*time.Second, func() bool { return !s.toolInstallingNow() })

	if calls != 1 {
		t.Fatalf("ensureVideoTools called %d times, want 1", calls)
	}
}

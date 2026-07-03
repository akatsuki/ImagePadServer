package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestStartVideoToolInstallRevertsOnFailure(t *testing.T) {
	s, _ := testServer(t, false)
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

	if s.videoPlayerEnabled() {
		t.Fatal("video player must stay OFF after install failure")
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
	if s.videoPlayerEnabled() {
		t.Fatal("must not be enabled until install completes")
	}
	close(blocked)
	waitFor(t, 2*time.Second, func() bool { return !s.toolInstallingNow() })
}

func TestStateIncludesToolInstall(t *testing.T) {
	s, _ := testServer(t, false)
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	st := s.state(req)
	if _, ok := st["toolInstall"]; !ok {
		t.Fatal("state missing toolInstall")
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

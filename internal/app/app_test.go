package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStartupCleanupRunsFFmpegThenMediaMTXAndContinuesOnErrors(t *testing.T) {
	oldTracked := cleanupTrackedFFmpeg
	oldPort := cleanupFFmpegOnPort
	oldMediaMTX := cleanupStaleMediaMTX
	t.Cleanup(func() {
		cleanupTrackedFFmpeg = oldTracked
		cleanupFFmpegOnPort = oldPort
		cleanupStaleMediaMTX = oldMediaMTX
	})

	var calls []string
	cleanupTrackedFFmpeg = func() (int, error) {
		calls = append(calls, "ffmpeg-tracked")
		return 0, errors.New("tracked failed")
	}
	cleanupFFmpegOnPort = func(port int) (int, error) {
		calls = append(calls, "ffmpeg-port")
		if port != 1935 {
			t.Fatalf("port = %d, want 1935", port)
		}
		return 1, nil
	}
	cleanupStaleMediaMTX = func() (int, error) {
		calls = append(calls, "mediamtx")
		return 2, errors.New("mediamtx warning")
	}
	var logs []string
	cleanupStaleHelpers(func(format string, args ...any) {
		logs = append(logs, format)
	})

	if !reflect.DeepEqual(calls, []string{"ffmpeg-tracked", "ffmpeg-port", "mediamtx"}) {
		t.Fatalf("cleanup order = %v", calls)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"failed to clean up stale FFmpeg processes", "stopped %d stale FFmpeg process", "failed to clean up stale MediaMTX"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs missing %q: %s", want, joined)
		}
	}
}

func TestWaitForServerHealthyReturnsWhenReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	start := time.Now()
	if !waitForServerHealthy(srv.URL, time.Second) {
		t.Fatal("server did not become healthy")
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("healthy server wait took %s, want under 250ms", elapsed)
	}
}

func TestWaitForServerHealthyTimesOut(t *testing.T) {
	start := time.Now()
	if waitForServerHealthy("http://127.0.0.1:1/healthz", 80*time.Millisecond) {
		t.Fatal("unreachable server reported healthy")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("unreachable server wait took %s, want bounded timeout", elapsed)
	}
}

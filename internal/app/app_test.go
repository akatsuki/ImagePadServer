package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/server"
	"imagepadserver/internal/tunnel"
)

func TestStartupCleanupRunsFFmpegThenMediaMTXAndContinuesOnErrors(t *testing.T) {
	oldTracked := cleanupTrackedFFmpeg
	oldPort := cleanupFFmpegOnPort
	oldMediaMTX := cleanupStaleMediaMTX
	oldUPnP := cleanupStaleUPnP
	oldCloudflare := cleanupStaleCloudflare
	t.Cleanup(func() {
		cleanupTrackedFFmpeg = oldTracked
		cleanupFFmpegOnPort = oldPort
		cleanupStaleMediaMTX = oldMediaMTX
		cleanupStaleUPnP = oldUPnP
		cleanupStaleCloudflare = oldCloudflare
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
	cleanupStaleUPnP = func() (int, error) {
		calls = append(calls, "upnp")
		return 3, errors.New("upnp warning")
	}
	cleanupStaleCloudflare = func() (int, error) {
		calls = append(calls, "cloudflare")
		return 4, errors.New("cloudflare warning")
	}
	var logs []string
	cleanupStaleHelpers(func(format string, args ...any) {
		logs = append(logs, format)
	})

	if !reflect.DeepEqual(calls, []string{"ffmpeg-tracked", "ffmpeg-port", "mediamtx", "cloudflare", "upnp"}) {
		t.Fatalf("cleanup order = %v", calls)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"failed to clean up stale FFmpeg processes", "stopped %d stale FFmpeg process", "failed to clean up stale MediaMTX", "failed to clean up stale Cloudflare Tunnel", "failed to clean up stale UPnP"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs missing %q: %s", want, joined)
		}
	}
}

func TestShutdownCleanupRunsFFmpegAndUPnPAndContinuesOnErrors(t *testing.T) {
	oldTracked := cleanupTrackedFFmpeg
	oldUPnP := cleanupStaleUPnP
	oldCloudflare := cleanupStaleCloudflare
	t.Cleanup(func() {
		cleanupTrackedFFmpeg = oldTracked
		cleanupStaleUPnP = oldUPnP
		cleanupStaleCloudflare = oldCloudflare
	})

	var calls []string
	cleanupTrackedFFmpeg = func() (int, error) {
		calls = append(calls, "ffmpeg-tracked")
		return 1, errors.New("ffmpeg warning")
	}
	cleanupStaleUPnP = func() (int, error) {
		calls = append(calls, "upnp")
		return 2, errors.New("upnp warning")
	}
	cleanupStaleCloudflare = func() (int, error) {
		calls = append(calls, "cloudflare")
		return 3, errors.New("cloudflare warning")
	}

	var logs []string
	cleanupShutdownHelpers(func(format string, args ...any) {
		logs = append(logs, format)
	})

	if !reflect.DeepEqual(calls, []string{"ffmpeg-tracked", "upnp", "cloudflare"}) {
		t.Fatalf("shutdown cleanup order = %v", calls)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"failed to stop FFmpeg processes during shutdown", "failed to clean up UPnP mappings during shutdown", "failed to stop Cloudflare Tunnel processes during shutdown"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs missing %q: %s", want, joined)
		}
	}
}

func TestManageCloudflareTunnelStopsWhileStartIsWaiting(t *testing.T) {
	oldStart := startCloudflareTunnel
	t.Cleanup(func() { startCloudflareTunnel = oldStart })

	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	startCloudflareTunnel = func(ctx context.Context, originURL string) (*tunnel.Tunnel, tunnel.Status) {
		close(started)
		<-ctx.Done()
		return nil, tunnel.Status{Message: "cancelled"}
	}

	var tunnelMu sync.Mutex
	var tunnelHandle *tunnel.Tunnel
	done := make(chan struct{})
	go func() {
		defer close(done)
		manageCloudflareTunnel(ctx, "http://127.0.0.1:8080/", srv, &tunnelMu, &tunnelHandle, make(chan struct{}))
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tunnel start was not called")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manageCloudflareTunnel did not stop after context cancellation")
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

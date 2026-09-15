package obsrtmp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A high-quality HLS startup can legitimately wait for a complete 4s GOP and
// the next segment. The general 5s API timeout must not truncate that request.
func TestMediaMTXProxyHighHLSWaitsBeyondAPITimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5500 * time.Millisecond):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	_, port := splitHostPortForTest(t, upstream.Listener.Addr().String())
	cfg := defaultTestConfig()
	cfg.Ports.HLS, cfg.HLSSegmentDuration = port, "4s"
	rt := newMediaMTXRuntime("unused", cfg)
	rec := httptest.NewRecorder()
	rt.proxyHLS(rec, httptest.NewRequest(http.MethodGet, "/index.m3u8", nil), "index.m3u8")
	if rec.Code != http.StatusOK || rec.Body.String() != "#EXTM3U\n#EXT-X-TARGETDURATION:4\n" {
		t.Fatalf("legitimate HLS wait truncated: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rt.httpClient.Timeout != 5*time.Second {
		t.Fatal("HLS request changed the shared API timeout")
	}
}

func TestMediaMTXProxyWaitIsBoundedAndPreservesSessionRejection(t *testing.T) {
	for _, segment := range []string{"4s", "24h", "-1s", "garbage", ""} {
		t.Run(segment, func(t *testing.T) {
			cfg := defaultTestConfig()
			cfg.HLSSegmentDuration = segment
			rt := newMediaMTXRuntime("unused", cfg)
			rt.httpClient.Transport = directRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) > 15*time.Second || time.Until(deadline) <= 0 {
					t.Error("HLS request has no bounded positive deadline")
				}
				if req.URL.Query().Get("session") != "old-player" {
					t.Error("proxy erased the session token")
				}
				return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"authentication error"}`))}, nil
			})
			rec := httptest.NewRecorder()
			rt.proxyHLS(rec, httptest.NewRequest(http.MethodGet, "/video1_stream.m3u8?session=old-player", nil), "video1_stream.m3u8")
			if rec.Code != http.StatusUnauthorized || rec.Body.String() != `{"error":"authentication error"}` {
				t.Fatal("proxy hid a session/auth failure")
			}
		})
	}
}

func TestMediaMTXProxyHighHLSStillHonorsCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	_, port := splitHostPortForTest(t, upstream.Listener.Addr().String())
	cfg := defaultTestConfig()
	cfg.Ports.HLS, cfg.HLSSegmentDuration = port, "4s"
	rt := newMediaMTXRuntime("unused", cfg)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		rt.proxyHLS(rec, httptest.NewRequest(http.MethodGet, "/index.m3u8", nil).WithContext(ctx), "index.m3u8")
		done <- rec.Code
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("proxy did not reach upstream")
	}
	cancel()
	select {
	case status := <-done:
		if status != http.StatusBadGateway {
			t.Fatalf("cancel status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("HLS ignored caller cancellation")
	}
}

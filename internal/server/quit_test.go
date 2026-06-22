package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQuitInvokesExitCallback(t *testing.T) {
	srv, mux := testServer(t, false)

	called := make(chan struct{}, 1)
	srv.SetExitRequested(func() { called <- struct{}{} })

	req := httptest.NewRequest(http.MethodPost, "/api/quit", nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("exit callback was not invoked")
	}
}

func TestQuitUnavailableWhenCallbackUnset(t *testing.T) {
	_, mux := testServer(t, false)

	req := httptest.NewRequest(http.MethodPost, "/api/quit", nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestQuitRejectsNonPost(t *testing.T) {
	srv, mux := testServer(t, false)
	srv.SetExitRequested(func() {})

	req := httptest.NewRequest(http.MethodGet, "/api/quit", nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", rec.Code, rec.Body.String())
	}
}

func TestQuitRejectsNonAdmin(t *testing.T) {
	srv, mux := testServer(t, false)
	srv.SetExitRequested(func() { t.Fatal("exit must not run for non-admin request") })

	// Non-loopback, non-private remote without a token must be forbidden.
	req := httptest.NewRequest(http.MethodPost, "/api/quit", nil)
	req.RemoteAddr = "203.0.113.10:40000"
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

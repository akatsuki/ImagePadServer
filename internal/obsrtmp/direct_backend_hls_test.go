package obsrtmp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectBackendHLSUsesCommittedRouterNotOldRuntime(t *testing.T) {
	var oldCalls, candidateCalls atomic.Int32
	backend := func(marker, ip string, count *atomic.Int32) *mediaMTXRuntime {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			count.Add(1)
			if strings.HasPrefix(req.URL.Path, "/v3/paths/get/") {
				fmt.Fprint(w, `{"ready":true}`)
				return
			}
			if req.URL.Path == "/v3/rtspsessions/list" {
				fmt.Fprintf(w, `{"items":[{"remoteAddr":"%s:1234","state":"read","path":"obs_session","transport":"TCP"}]}`, ip)
				return
			}
			if req.URL.Path == "/v3/hlssessions/list" {
				fmt.Fprint(w, `{"items":[]}`)
				return
			}
			fmt.Fprint(w, marker)
		}))
		t.Cleanup(server.Close)
		u, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(u.Port())
		return newMediaMTXRuntime("unused", mediaMTXSessionConfig{Path: "obs_session", Ports: mediaMTXPorts{HLS: port, API: port}})
	}
	oldRuntime := backend("old-playlist", "192.0.2.10", &oldCalls)
	newRuntime := backend("candidate-playlist", "192.0.2.20", &candidateCalls)
	active := directBackendRoute{sessionID: "session", sessionEpoch: 4, generation: 1, requestID: "old", privateRTSPPort: 49200, runtime: oldRuntime}
	router := mustNewDirectBackendRouter(t, active)
	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: 49200})
	gate.backendRouter = router
	m := &Manager{directPublishing: true, directHandle: DirectSessionHandle{ID: "session", Generation: 4}, listenerGeneration: 4,
		current: &Session{ID: "session"}, mtx: oldRuntime, rtspGate: gate}
	m.status.Connected = true
	candidate := active
	candidate.generation = 2
	candidate.requestID = "new"
	candidate.runtime = newRuntime
	candidate.privateRTSPPort = 49201
	if _, err := router.prepare(candidate); err != nil {
		t.Fatal(err)
	}
	read := func(want, ip string) {
		rec := httptest.NewRecorder()
		if !m.ProxyLLHLS(rec, httptest.NewRequest(http.MethodGet, "/public/session.m3u8", nil), "session", "index.m3u8") || rec.Body.String() != want {
			t.Fatalf("public HLS route = %q want %q", rec.Body.String(), want)
		}
		if !m.HLSPreviewReady("session", "index.m3u8") {
			t.Fatal("active preview not ready")
		}
		rows := m.ConnectionRows(time.Second)
		if len(rows) != 1 || rows[0].IP != ip {
			t.Fatalf("connection rows use wrong runtime: %+v", rows)
		}
	}
	read("old-playlist", "192.0.2.10")
	if candidateCalls.Load() != 0 {
		t.Fatal("prepare leaked candidate through HLS")
	}
	// Exercise the existing drain/CAS boundary without public TCP connections.
	drain, err := gate.beginDrain(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := drain.wait(0); err != nil {
		t.Fatal(err)
	}
	assertUnavailable := func() {
		rec := httptest.NewRecorder()
		if !m.ProxyLLHLS(rec, httptest.NewRequest(http.MethodGet, "/public/session.m3u8", nil), "session", "index.m3u8") || rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("unavailable direct HLS fell through to stale files: %d", rec.Code)
		}
		if m.HLSPreviewReady("session", "index.m3u8") || len(m.ConnectionRows(time.Second)) != 0 {
			t.Fatal("unavailable backend reports ready")
		}
	}
	assertUnavailable()
	if err := gate.commitDrained(active, candidate); err != nil {
		t.Fatal(err)
	}
	before := oldCalls.Load()
	read("candidate-playlist", "192.0.2.20")
	if oldCalls.Load() != before {
		t.Fatal("old runtime still receives HLS/preview requests")
	}
	if m.mtx != oldRuntime || m.current.ID != "session" || m.rtspGate != gate {
		t.Fatal("test mutated session to simulate the result")
	}
	m.directHandle.Generation++
	assertUnavailable()
	m.directHandle.Generation--
	router.markTerminal()
	assertUnavailable()
}

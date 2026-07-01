package server

import (
	"sync/atomic"
	"testing"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/upnp"
)

type fakeRTSPMapping struct {
	ip         string
	port       int
	closeCalls atomic.Int32
}

func (f *fakeRTSPMapping) ExternalIP() string { return f.ip }
func (f *fakeRTSPMapping) ExternalPort() int  { return f.port }
func (f *fakeRTSPMapping) Close() error       { f.closeCalls.Add(1); return nil }

type rtspMapCall struct {
	protocol     string
	internalPort int
	externalPort int
	description  string
}

func TestMapRTSPCompatibilityPortsMapsTCPAndUDPPairs(t *testing.T) {
	var calls []rtspMapCall
	mapper := func(protocol string, internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result) {
		calls = append(calls, rtspMapCall{protocol: protocol, internalPort: internalPort, externalPort: externalPort, description: description})
		return &fakeRTSPMapping{ip: "8.8.8.8", port: externalPort}, upnp.Result{OK: true, ExternalIP: "8.8.8.8"}
	}

	mapping, result := mapRTSPCompatibilityPorts(mapper, obsrtmp.RTSPEndpoint{
		Port:     8554,
		RTPPort:  5004,
		RTCPPort: 5005,
	})
	if mapping == nil || !result.OK {
		t.Fatalf("mapping/result = %#v/%#v, want success", mapping, result)
	}
	want := []rtspMapCall{
		{protocol: "TCP", internalPort: 8554, externalPort: 8554, description: "ImagePadServer RTSP TCP"},
		{protocol: "UDP", internalPort: 5004, externalPort: 5004, description: "ImagePadServer RTSP RTP"},
		{protocol: "UDP", internalPort: 5005, externalPort: 5005, description: "ImagePadServer RTSP RTCP"},
	}
	if len(calls) != len(want) {
		t.Fatalf("map calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call[%d] = %#v, want %#v", i, calls[i], want[i])
		}
	}
}

func TestHandleRTSPReadyStoresPublicMappingAndClosesPrevious(t *testing.T) {
	srv := &Server{}
	previous := &fakeRTSPMapping{ip: "8.8.4.4", port: 8554}
	srv.rtspMap = previous
	srv.rtspSessionID = "old-session"
	srv.mapRTSPPort = func(string, int, int, string) (rtspMappingHandle, upnp.Result) {
		return &fakeRTSPMapping{ip: "8.8.8.8", port: 8554}, upnp.Result{OK: true, ExternalIP: "8.8.8.8"}
	}
	var gotSession, gotURL, gotMessage string
	srv.setRTSPURL = func(sessionID, publicURL, message string) bool {
		gotSession = sessionID
		gotURL = publicURL
		gotMessage = message
		return true
	}

	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
		SessionID: "new-session",
		Port:      8554,
		Path:      "live/stream",
		LocalURL:  "rtsp://127.0.0.1:8554/live/stream",
	})

	if gotSession != "new-session" || gotURL != "rtsp://8.8.8.8:8554/live/stream" || gotMessage == "" {
		t.Fatalf("setRTSPURL = %q, %q, %q", gotSession, gotURL, gotMessage)
	}
	if srv.rtspMap == nil || srv.rtspSessionID != "new-session" {
		t.Fatalf("stored mapping/session = %#v/%q", srv.rtspMap, srv.rtspSessionID)
	}
	if previous.closeCalls.Load() != 1 {
		t.Fatalf("previous close calls = %d, want 1", previous.closeCalls.Load())
	}
}

func TestCloseRTSPMappingHonorsSessionID(t *testing.T) {
	srv := &Server{}
	mapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 8554}
	srv.rtspMap = mapping
	srv.rtspSessionID = "active"

	srv.closeRTSPMapping("other")
	if srv.rtspMap == nil || mapping.closeCalls.Load() != 0 {
		t.Fatalf("mapping closed for mismatched session")
	}

	srv.closeRTSPMapping("active")
	if srv.rtspMap != nil || srv.rtspSessionID != "" {
		t.Fatalf("mapping ownership not cleared: %#v/%q", srv.rtspMap, srv.rtspSessionID)
	}
	if mapping.closeCalls.Load() != 1 {
		t.Fatalf("close calls = %d, want 1", mapping.closeCalls.Load())
	}
}

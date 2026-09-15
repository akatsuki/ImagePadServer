package obsrtmp

import (
	"errors"
	"strings"
	"testing"
)

func TestRTSPDiagnosticsRedactsSecretsAndBoundsEvents(t *testing.T) {
	d := newRTSPDiagnostics(rtspDiagnosticsConfig{Enabled: true, MaxEvents: 2, RunID: "run-1"})
	d.record(rtspDiagnosticEvent{Request: "DESCRIBE rtsp://user:password@example/live?key=secret RTSP/1.0", Authorization: "Bearer secret", Headers: map[string]string{"User-Agent": "client", "CSeq": "7", "Transport": "RTP/AVP/TCP"}})
	d.record(rtspDiagnosticEvent{Request: "SETUP rtsp://example/live RTSP/1.0"})
	d.record(rtspDiagnosticEvent{Request: "PLAY rtsp://example/live RTSP/1.0"})

	events := d.snapshot()
	if len(events) != 2 {
		t.Fatalf("event count = %d, want bounded count 2", len(events))
	}
	joined := events[0].Request + events[0].Authorization
	if strings.Contains(joined, "password") || strings.Contains(joined, "secret") || strings.Contains(joined, "Bearer") {
		t.Fatalf("diagnostic event leaked secret: %q", joined)
	}
	if events[0].RunID != "run-1" || events[0].ConnectionID == "" {
		t.Fatalf("identity fields = %+v", events[0])
	}
}

func TestRTSPDiagnosticsCopyStatsRecordDirectionBytesElapsedAndReason(t *testing.T) {
	d := newRTSPDiagnostics(rtspDiagnosticsConfig{Enabled: true, MaxEvents: 8, RunID: "run-1"})
	d.copyFinished("conn-1", 3, rtspDiagnosticDirectionClientToBackend, 12, 25, errors.New("write failed"))
	d.copyFinished("conn-1", 3, rtspDiagnosticDirectionBackendToClient, 19, 30, nil)

	events := d.snapshot()
	if len(events) != 2 {
		t.Fatalf("copy event count = %d, want 2", len(events))
	}
	if events[0].CopyBytes != 12 || events[0].CopyDirection != rtspDiagnosticDirectionClientToBackend || events[0].EndReason != "write failed" || events[0].ElapsedMS != 25 {
		t.Fatalf("client-to-backend event = %+v", events[0])
	}
	if events[1].CopyBytes != 19 || events[1].EndReason != "eof" || events[1].PublisherGeneration != 3 {
		t.Fatalf("backend-to-client event = %+v", events[1])
	}
}

func TestRTSPDiagnosticsConfigFingerprintIsStableAndDoesNotExposeConfig(t *testing.T) {
	fingerprint := rtspDiagnosticsConfigFingerprint([]byte("path: /live\nreadUser: user\nreadPass: password\n"))
	if len(fingerprint) != 64 || strings.Contains(fingerprint, "password") {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	if fingerprint != rtspDiagnosticsConfigFingerprint([]byte("path: /live\nreadUser: user\nreadPass: password\n")) {
		t.Fatal("fingerprint is not stable")
	}
}

func TestRTSPDiagnosticsDisabledDoesNotRetainEvents(t *testing.T) {
	d := newRTSPDiagnostics(rtspDiagnosticsConfig{Enabled: false, MaxEvents: 8})
	d.record(rtspDiagnosticEvent{Request: "OPTIONS rtsp://example/live RTSP/1.0"})
	if got := len(d.snapshot()); got != 0 {
		t.Fatalf("disabled diagnostics retained %d events", got)
	}
}

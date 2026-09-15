package airplay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func TestReadSourceClockMediaReadyRequiresValidatedCurrentGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media-ready.json")
	if ready, err := readSourceClockMediaReady(path, "session-1", 2); err != nil || ready {
		t.Fatalf("missing media-ready = %v, %v", ready, err)
	}

	running := uint64(0)
	event := airplaycontract.Event{
		Schema:              2,
		SessionID:           "session-1",
		PublisherGeneration: 2,
		Event:               "video-decoded",
		At:                  time.Unix(100, 0).UTC(),
		VideoDecoded:        true,
		RunningTimeNS:       &running,
	}
	writeSourceClockEventForTest(t, path, event)
	if ready, err := readSourceClockMediaReady(path, "session-1", 2); err != nil || !ready {
		t.Fatalf("valid media-ready = %v, %v", ready, err)
	}
	if ready, err := readSourceClockMediaReady(path, "session-1", 3); err != nil || ready {
		t.Fatalf("wrong generation media-ready = %v, %v", ready, err)
	}
	if ready, err := readSourceClockMediaReady(path, "session-2", 2); err != nil || ready {
		t.Fatalf("wrong session media-ready = %v, %v", ready, err)
	}

	event.Event = "publisher-ready"
	writeSourceClockEventForTest(t, path, event)
	if ready, err := readSourceClockMediaReady(path, "session-1", 2); err != nil || ready {
		t.Fatalf("publisher-ready implied decoded media: %v, %v", ready, err)
	}
}

func TestObserveSourceClockGenerationMediaReadyUpdatesOnlyCurrentGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media-ready.json")
	running := uint64(0)
	writeSourceClockEventForTest(t, path, airplaycontract.Event{
		Schema:              2,
		SessionID:           "session-1",
		PublisherGeneration: 2,
		Event:               "video-decoded",
		At:                  time.Unix(100, 0).UTC(),
		VideoDecoded:        true,
		RunningTimeNS:       &running,
	})
	manager := New(nil)
	manager.running = true
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true, MediaReadyKnown: true}

	if ready, err := manager.observeSourceClockGenerationMediaReady(path, "session-1", 3); err != nil || ready {
		t.Fatalf("stale generation observation = %v, %v", ready, err)
	}
	if status := manager.Status(); status.MediaReady {
		t.Fatalf("stale generation activated media status: %+v", status)
	}
	if ready, err := manager.observeSourceClockGenerationMediaReady(path, "session-1", 2); err != nil || !ready {
		t.Fatalf("current generation observation = %v, %v", ready, err)
	}
	if status := manager.Status(); !status.MediaReady || !status.MediaReadyKnown {
		t.Fatalf("current generation did not activate media status: %+v", status)
	}
}

func TestObserveSourceClockGenerationMediaReadyIgnoresArtifactWhilePublisherIsAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media-ready.json")
	running := uint64(0)
	writeSourceClockEventForTest(t, path, airplaycontract.Event{
		Schema:              2,
		SessionID:           "session-1",
		PublisherGeneration: 2,
		Event:               "video-decoded",
		At:                  time.Unix(100, 0).UTC(),
		VideoDecoded:        true,
		RunningTimeNS:       &running,
	})
	manager := New(nil)
	manager.running = true
	manager.status = Status{Running: true, ReceiverRunning: true, MediaReadyKnown: true}

	if ready, err := manager.observeSourceClockGenerationMediaReadyIfRunning(false, path, "session-1", 2); err != nil || ready {
		t.Fatalf("publisher-absent observation = %v, %v", ready, err)
	}
	if status := manager.Status(); status.MediaReady {
		t.Fatalf("publisher-absent observation revived old media readiness: %+v", status)
	}
}

func TestReadSourceClockMediaReadyRejectsMalformedArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media-ready.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if ready, err := readSourceClockMediaReady(path, "session-1", 1); err == nil || ready {
		t.Fatalf("malformed media-ready = %v, %v", ready, err)
	}
}

func TestManagedReadinessForwardsOnlyMatchingSnapshotPair(t *testing.T) {
	for _, kind := range []string{"valid", "no-audio-no-media", "wrong-session", "stale-generation", "malformed", "not-decoded"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			readyPath, mediaPath := filepath.Join(root, "ready.json"), filepath.Join(root, "media.json")
			now, running := time.Now().UTC(), uint64(0)
			ready := airplaycontract.Event{Schema: 2, SessionID: "session-1", PublisherGeneration: 2, Event: "publisher-ready", At: now, ProtocolVersion: 1, VideoListenPort: 5000, AudioListenPort: 5001, PipelineStartAccepted: true}
			media := airplaycontract.Event{Schema: 2, SessionID: "session-1", PublisherGeneration: 2, Event: "video-decoded", At: now, VideoDecoded: true, RunningTimeNS: &running}
			switch kind {
			case "wrong-session":
				media.SessionID = "other"
			case "stale-generation":
				media.PublisherGeneration = 1
			case "not-decoded":
				media.VideoDecoded = false
			}
			writeSourceClockEventForTest(t, readyPath, ready)
			if kind != "no-audio-no-media" {
				writeSourceClockEventForTest(t, mediaPath, media)
			}
			if kind == "malformed" {
				if err := os.WriteFile(mediaPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			observer := &sourceClockArtifactObserverForTest{}
			got, err := observeSourceClockPublisherReadiness(observer, readyPath, mediaPath, "session-1", 2)
			if (err != nil) != (kind == "malformed") {
				t.Fatalf("err=%v", err)
			}
			if kind == "valid" {
				if !got || len(observer.events) != 2 || observer.events[0].Event != "publisher-ready" || observer.events[1].Event != "video-decoded" {
					t.Fatal("validated real video did not reach downstream owner")
				}
			} else if got || len(observer.events) != 0 {
				t.Fatal("incomplete/stale readiness leaked to downstream owner")
			}
		})
	}
}

func TestSourceClockLifecycleTickUsesMediaReadyThenCurrentReceiverMetrics(t *testing.T) {
	base := time.Unix(400, 0)
	path := filepath.Join(t.TempDir(), "media-ready.json")
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	if disposition, err := gate.consumeLine(metricsLineWith(1, 1, 0, 0), base); err != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("first metrics disposition=%v err=%v", disposition, err)
	}
	tracker := newSourceClockLifecycleTracker(10 * time.Second)
	if expired, err := sourceClockLifecycleTick(tracker, gate, path, "session-1", 2, base); err != nil || expired {
		t.Fatalf("pre-media tick expired=%v err=%v", expired, err)
	}
	if !tracker.Snapshot().FirstDecodedAt.IsZero() {
		t.Fatal("metrics started lifecycle before media-ready")
	}

	running := uint64(0)
	writeSourceClockEventForTest(t, path, airplaycontract.Event{
		Schema: 2, SessionID: "session-1", PublisherGeneration: 2,
		Event: "video-decoded", At: base.UTC(), VideoDecoded: true, RunningTimeNS: &running,
	})
	if expired, err := sourceClockLifecycleTick(tracker, gate, path, "session-1", 2, base.Add(time.Second)); err != nil || expired {
		t.Fatalf("first decoded tick expired=%v err=%v", expired, err)
	}
	if got := tracker.Snapshot().FirstDecodedAt; !got.Equal(base.Add(time.Second)) {
		t.Fatalf("FirstDecodedAt=%s", got)
	}

	if disposition, err := gate.consumeLine(metricsLineWith(2, 2, 0, 0), base.Add(5*time.Second)); err != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("second metrics disposition=%v err=%v", disposition, err)
	}
	if expired, err := sourceClockLifecycleTick(tracker, gate, path, "session-1", 2, base.Add(5*time.Second)); err != nil || expired {
		t.Fatalf("activity tick expired=%v err=%v", expired, err)
	}
	lastVideo := tracker.Snapshot().LastVideoAt
	if !sourceClockAgeWithin(lastVideo, base.Add(5*time.Second), 25*time.Millisecond) {
		t.Fatalf("LastVideoAt=%s, want receivedAt-videoAge", lastVideo)
	}
	if expired, err := sourceClockLifecycleTick(tracker, gate, path, "session-1", 2, lastVideo.Add(10*time.Second)); err != nil || !expired {
		t.Fatalf("deadline tick expired=%v err=%v", expired, err)
	}
}

func sourceClockAgeWithin(got, receivedAt time.Time, age time.Duration) bool {
	return got.Equal(receivedAt.Add(-age))
}

func writeSourceClockEventForTest(t *testing.T, path string, event airplaycontract.Event) {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

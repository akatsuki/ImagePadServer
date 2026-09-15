package airplaycontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testSessionID           = "obs_example"
	testPublisherGeneration = uint64(1)
)

func loadEventFixture(t *testing.T, name string) Event {
	t.Helper()

	payload, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}

	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("unmarshal fixture %q: %v", name, err)
	}
	return event
}

func TestPublisherReadyFixtureMatchesExpectedPublisher(t *testing.T) {
	event := loadEventFixture(t, "publisher-ready.json")

	if !event.ReadyFor(testSessionID, testPublisherGeneration) {
		t.Fatal("canonical publisher-ready fixture was rejected")
	}
	if event.MediaReadyFor(testSessionID, testPublisherGeneration) {
		t.Fatal("publisher-ready event must not imply decoded video")
	}
	if event.SourceNTPNS != nil {
		t.Fatal("publisher-ready fixture fabricated sourceNtpNs")
	}
}

func TestVideoDecodedFixtureMatchesExpectedPublisher(t *testing.T) {
	event := loadEventFixture(t, "video-decoded.json")

	if !event.MediaReadyFor(testSessionID, testPublisherGeneration) {
		t.Fatal("canonical video-decoded fixture was rejected")
	}
	if event.ReadyFor(testSessionID, testPublisherGeneration) {
		t.Fatal("video-decoded event must not imply publisher readiness")
	}
	if event.RunningTimeNS == nil || *event.RunningTimeNS != 1_000_000_000 {
		t.Fatalf("runningTimeNs = %v, want pointer to 1000000000", event.RunningTimeNS)
	}
	if event.SourceNTPNS != nil {
		t.Fatal("video-decoded fixture fabricated sourceNtpNs")
	}
}

func TestRecordingFinalizedFixturePreservesCompletionEvidence(t *testing.T) {
	event := loadEventFixture(t, "recording-finalized.json")

	if event.Schema != 2 || event.SessionID != testSessionID || event.PublisherGeneration != testPublisherGeneration {
		t.Fatalf("recording-finalized identity = schema %d session %q generation %d", event.Schema, event.SessionID, event.PublisherGeneration)
	}
	if event.Event != "recording-finalized" {
		t.Fatalf("event = %q, want recording-finalized", event.Event)
	}
	if event.RecordingPath != "media/airplay/obs_example/publisher-0001.mp4" {
		t.Fatalf("recordingPath = %q", event.RecordingPath)
	}
	if !event.RecordingClosed {
		t.Fatal("recordingClosed = false, want true")
	}
	if event.ReadyFor(testSessionID, testPublisherGeneration) {
		t.Fatal("recording-finalized event must not imply publisher readiness")
	}
	if event.MediaReadyFor(testSessionID, testPublisherGeneration) {
		t.Fatal("recording-finalized event must not imply decoded video")
	}
}

func TestRecordingFinalizedForAcceptsOnlyMatchingClosedRecording(t *testing.T) {
	canonical := loadEventFixture(t, "recording-finalized.json")
	const recordingPath = "media/airplay/obs_example/publisher-0001.mp4"
	if !canonical.RecordingFinalizedFor(testSessionID, testPublisherGeneration, recordingPath) {
		t.Fatal("canonical recording-finalized fixture was rejected")
	}

	tests := []struct {
		name       string
		mutate     func(*Event)
		sessionID  string
		generation uint64
		path       string
	}{
		{name: "schema 1", mutate: func(e *Event) { e.Schema = 1 }, sessionID: testSessionID, generation: 1, path: recordingPath},
		{name: "other session", mutate: func(e *Event) {}, sessionID: "other", generation: 1, path: recordingPath},
		{name: "old generation", mutate: func(e *Event) {}, sessionID: testSessionID, generation: 2, path: recordingPath},
		{name: "wrong event", mutate: func(e *Event) { e.Event = "video-decoded" }, sessionID: testSessionID, generation: 1, path: recordingPath},
		{name: "open recording", mutate: func(e *Event) { e.RecordingClosed = false }, sessionID: testSessionID, generation: 1, path: recordingPath},
		{name: "missing event path", mutate: func(e *Event) { e.RecordingPath = "" }, sessionID: testSessionID, generation: 1, path: recordingPath},
		{name: "both paths empty", mutate: func(e *Event) { e.RecordingPath = "" }, sessionID: testSessionID, generation: 1, path: ""},
		{name: "different registered path", mutate: func(e *Event) {}, sessionID: testSessionID, generation: 1, path: "media/airplay/obs_example/publisher-0002.mp4"},
		{name: "empty expected path", mutate: func(e *Event) {}, sessionID: testSessionID, generation: 1, path: ""},
		{name: "zero event time", mutate: func(e *Event) { e.At = time.Time{} }, sessionID: testSessionID, generation: 1, path: recordingPath},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := canonical
			tt.mutate(&event)
			if event.RecordingFinalizedFor(tt.sessionID, tt.generation, tt.path) {
				t.Fatal("invalid recording-finalized event was accepted")
			}
		})
	}
}

func TestReadyForRejectsInvalidPublisherReadyEvents(t *testing.T) {
	canonical := loadEventFixture(t, "publisher-ready.json")
	zeroTime, err := time.Parse(time.RFC3339, "0001-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("parse zero time: %v", err)
	}

	tests := []struct {
		name       string
		mutate     func(*Event)
		sessionID  string
		generation uint64
	}{
		{name: "schema 1", mutate: func(e *Event) { e.Schema = 1 }, sessionID: testSessionID, generation: 1},
		{name: "old generation", mutate: func(e *Event) {}, sessionID: testSessionID, generation: 2},
		{name: "other session", mutate: func(e *Event) {}, sessionID: "other", generation: 1},
		{name: "missing event", mutate: func(e *Event) { e.Event = "" }, sessionID: testSessionID, generation: 1},
		{name: "wrong event", mutate: func(e *Event) { e.Event = "video-decoded" }, sessionID: testSessionID, generation: 1},
		{name: "zero timestamp", mutate: func(e *Event) { e.At = zeroTime }, sessionID: testSessionID, generation: 1},
		{name: "missing session identity", mutate: func(e *Event) { e.SessionID = "" }, sessionID: testSessionID, generation: 1},
		{name: "missing generation identity", mutate: func(e *Event) { e.PublisherGeneration = 0 }, sessionID: testSessionID, generation: 1},
		{name: "empty expected session", mutate: func(e *Event) {}, sessionID: "", generation: 1},
		{name: "zero expected generation", mutate: func(e *Event) {}, sessionID: testSessionID, generation: 0},
		{name: "wrong protocol", mutate: func(e *Event) { e.ProtocolVersion = 2 }, sessionID: testSessionID, generation: 1},
		{name: "pipeline start not accepted", mutate: func(e *Event) { e.PipelineStartAccepted = false }, sessionID: testSessionID, generation: 1},
		{name: "zero video port", mutate: func(e *Event) { e.VideoListenPort = 0 }, sessionID: testSessionID, generation: 1},
		{name: "reserved video port", mutate: func(e *Event) { e.VideoListenPort = 65535 }, sessionID: testSessionID, generation: 1},
		{name: "zero audio port", mutate: func(e *Event) { e.AudioListenPort = 0 }, sessionID: testSessionID, generation: 1},
		{name: "reserved audio port", mutate: func(e *Event) { e.AudioListenPort = 65535 }, sessionID: testSessionID, generation: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := canonical
			tt.mutate(&event)
			if event.ReadyFor(tt.sessionID, tt.generation) {
				t.Fatal("invalid publisher-ready event was accepted")
			}
		})
	}
}

func TestMediaReadyForRejectsInvalidVideoDecodedEvents(t *testing.T) {
	canonical := loadEventFixture(t, "video-decoded.json")
	zeroTime, err := time.Parse(time.RFC3339, "0001-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("parse zero time: %v", err)
	}

	tests := []struct {
		name       string
		mutate     func(*Event)
		sessionID  string
		generation uint64
	}{
		{name: "schema 1", mutate: func(e *Event) { e.Schema = 1 }, sessionID: testSessionID, generation: 1},
		{name: "old generation", mutate: func(e *Event) {}, sessionID: testSessionID, generation: 2},
		{name: "other session", mutate: func(e *Event) {}, sessionID: "other", generation: 1},
		{name: "missing event", mutate: func(e *Event) { e.Event = "" }, sessionID: testSessionID, generation: 1},
		{name: "wrong event", mutate: func(e *Event) { e.Event = "publisher-ready" }, sessionID: testSessionID, generation: 1},
		{name: "compressed AU marker", mutate: func(e *Event) { e.Event = "video-au-received" }, sessionID: testSessionID, generation: 1},
		{name: "video not decoded", mutate: func(e *Event) { e.VideoDecoded = false }, sessionID: testSessionID, generation: 1},
		{name: "missing running time", mutate: func(e *Event) { e.RunningTimeNS = nil }, sessionID: testSessionID, generation: 1},
		{name: "zero timestamp", mutate: func(e *Event) { e.At = zeroTime }, sessionID: testSessionID, generation: 1},
		{name: "missing session identity", mutate: func(e *Event) { e.SessionID = "" }, sessionID: testSessionID, generation: 1},
		{name: "missing generation identity", mutate: func(e *Event) { e.PublisherGeneration = 0 }, sessionID: testSessionID, generation: 1},
		{name: "empty expected session", mutate: func(e *Event) {}, sessionID: "", generation: 1},
		{name: "zero expected generation", mutate: func(e *Event) {}, sessionID: testSessionID, generation: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := canonical
			tt.mutate(&event)
			if event.MediaReadyFor(tt.sessionID, tt.generation) {
				t.Fatal("invalid video-decoded event was accepted")
			}
		})
	}
}

func TestMediaReadyForAcceptsZeroRunningTime(t *testing.T) {
	event := loadEventFixture(t, "video-decoded.json")
	zero := uint64(0)
	event.RunningTimeNS = &zero

	if !event.MediaReadyFor(testSessionID, testPublisherGeneration) {
		t.Fatal("present runningTimeNs=0 was treated as a missing field")
	}
}

func TestEventJSONRejectsInvalidTimestamp(t *testing.T) {
	var event Event
	err := json.Unmarshal([]byte(`{"schema":2,"at":"not-rfc3339"}`), &event)
	if err == nil {
		t.Fatal("invalid RFC3339 timestamp was accepted")
	}
}

func TestEventJSONRoundTripsSourceSessionAndVideoSequence(t *testing.T) {
	sourceSession := uint64(12)
	sequence := uint64(345)
	want := Event{
		Schema: 2, SessionID: testSessionID, PublisherGeneration: 7,
		Event: "video-input-idr", At: time.Unix(200, 0).UTC(),
		SourceSessionGeneration: sourceSession, SourceVideoSequence: &sequence,
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.SourceSessionGeneration != sourceSession || got.SourceVideoSequence == nil || *got.SourceVideoSequence != sequence {
		t.Fatalf("source identity did not round-trip: %+v", got)
	}
	if !strings.Contains(string(payload), `"sourceSessionGeneration":12`) || !strings.Contains(string(payload), `"sourceVideoSequence":345`) {
		t.Fatalf("source identity missing from JSON: %s", payload)
	}
}

func TestEventJSONOmitsAbsentSourceSessionAndVideoSequence(t *testing.T) {
	event := Event{
		Schema: 2, SessionID: testSessionID, PublisherGeneration: 7,
		Event: "publisher-ready", At: time.Unix(201, 0).UTC(),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "sourceSessionGeneration") || strings.Contains(string(payload), "sourceVideoSequence") {
		t.Fatalf("absent source identity was not omitted: %s", payload)
	}
	var decoded Event
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SourceSessionGeneration != 0 || decoded.SourceVideoSequence != nil {
		t.Fatalf("absent source identity decoded as present: %+v", decoded)
	}
}

func TestRTSPEOFForAcceptsOnlyMatchingPublisherProcessEvent(t *testing.T) {
	canonical := Event{
		Schema: 2, SessionID: testSessionID, PublisherGeneration: testPublisherGeneration,
		Event: "rtsp-eof", At: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
		ProcessID: 4321,
	}
	if !canonical.RTSPEOFFor(testSessionID, testPublisherGeneration) {
		t.Fatal("canonical RTSP EOF event was rejected")
	}
	tests := []struct {
		name       string
		mutate     func(*Event)
		sessionID  string
		generation uint64
	}{
		{name: "wrong event", mutate: func(e *Event) { e.Event = "publisher-ready" }, sessionID: testSessionID, generation: testPublisherGeneration},
		{name: "missing pid", mutate: func(e *Event) { e.ProcessID = 0 }, sessionID: testSessionID, generation: testPublisherGeneration},
		{name: "negative pid", mutate: func(e *Event) { e.ProcessID = -1 }, sessionID: testSessionID, generation: testPublisherGeneration},
		{name: "wrong session", mutate: func(e *Event) {}, sessionID: "other", generation: testPublisherGeneration},
		{name: "wrong generation", mutate: func(e *Event) {}, sessionID: testSessionID, generation: testPublisherGeneration + 1},
		{name: "zero time", mutate: func(e *Event) { e.At = time.Time{} }, sessionID: testSessionID, generation: testPublisherGeneration},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := canonical
			tt.mutate(&event)
			if event.RTSPEOFFor(tt.sessionID, tt.generation) {
				t.Fatal("invalid RTSP EOF event was accepted")
			}
		})
	}
}

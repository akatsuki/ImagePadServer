package airplaycontract

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const nativeEventWriterTestExecutableEnv = "IMAGEPAD_SOURCE_CLOCK_EVENTS_TEST_EXE"

func readNativeEvent(t *testing.T, path string) Event {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read native event %q: %v", path, err)
	}
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("unmarshal native event %q: %v", path, err)
	}
	return event
}

func verifyNativeSourceClockEventWriterBoundary(t *testing.T, executable, directory string) {
	t.Helper()
	command := exec.Command(executable, "--emit-dir", directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native event writer failed: %v\n%s", err, output)
	}

	const sessionID = "fixture-session-001"
	const generation = uint64(7)
	ready := readNativeEvent(t, filepath.Join(directory, "publisher-ready.json"))
	if !ready.ReadyFor(sessionID, generation) {
		t.Fatalf("native publisher-ready event was rejected: %+v", ready)
	}
	if ready.VideoListenPort != 41001 || ready.AudioListenPort != 41002 {
		t.Fatalf("native ports = %d/%d, want 41001/41002",
			ready.VideoListenPort, ready.AudioListenPort)
	}

	mediaReady := readNativeEvent(t, filepath.Join(directory, "video-decoded.json"))
	if !mediaReady.MediaReadyFor(sessionID, generation) {
		t.Fatalf("native video-decoded event was rejected: %+v", mediaReady)
	}
	if mediaReady.RunningTimeNS == nil || *mediaReady.RunningTimeNS != 0 {
		t.Fatalf("native runningTimeNs = %v, want present zero", mediaReady.RunningTimeNS)
	}
	if mediaReady.SourceNTPNS != nil {
		t.Fatalf("native writer fabricated sourceNtpNs: %d", *mediaReady.SourceNTPNS)
	}
	recordingPath := filepath.Join(directory, "publisher-0007.mp4")

	eventLog, err := os.Open(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		t.Fatalf("open native event log: %v", err)
	}
	defer eventLog.Close()

	var events []Event
	scanner := bufio.NewScanner(eventLog)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("unmarshal native event log line %d: %v", len(events)+1, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan native event log: %v", err)
	}
	if len(events) != 8 {
		t.Fatalf("native event log lines = %d, want 8", len(events))
	}
	if !events[0].ReadyFor(sessionID, generation) {
		t.Fatalf("first native event log line is not publisher-ready: %+v", events[0])
	}
	if !events[1].MediaReadyFor(sessionID, generation) {
		t.Fatalf("second native event log line is not video-decoded: %+v", events[1])
	}
	if !events[2].RTSPEOFFor(sessionID, generation) || events[2].ProcessID != 4321 {
		t.Fatalf("third native event log line is not RTSP EOF: %+v", events[2])
	}
	if !events[3].RecordingFinalizedFor(sessionID, generation, recordingPath) {
		t.Fatalf("fourth native event log line is not recording-finalized: %+v", events[3])
	}
	for i, name := range []string{"video-watermark-final", "video-input-idr", "video-decoded", "video-encoded-idr"} {
		event := events[i+4]
		wantSequence := uint64(101)
		if i == 0 {
			wantSequence = 100
		}
		if event.Event != name || event.Schema != 2 || event.At.IsZero() ||
			event.SessionID != sessionID || event.PublisherGeneration != generation ||
			event.SourceSessionGeneration != 9 || event.SourceVideoSequence == nil ||
			*event.SourceVideoSequence != wantSequence {
			t.Fatalf("native proof event %d lost stage or identity: %+v", i, event)
		}
	}
	if !events[6].MediaReadyFor(sessionID, generation) ||
		mediaReady.SourceSessionGeneration != 9 || mediaReady.SourceVideoSequence == nil ||
		*mediaReady.SourceVideoSequence != 101 {
		t.Fatalf("native candidate decoded snapshot lost proof identity: %+v", mediaReady)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(directory, "*.tmp"))
	if err != nil {
		t.Fatalf("find temporary event files: %v", err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("native writer left temporary files: %v", temporaryFiles)
	}
}

func TestNativeSourceClockEventWriterBoundary(t *testing.T) {
	executable := os.Getenv(nativeEventWriterTestExecutableEnv)
	if executable == "" {
		t.Skipf("%s is not set", nativeEventWriterTestExecutableEnv)
	}

	t.Run("ASCII output directory", func(t *testing.T) {
		verifyNativeSourceClockEventWriterBoundary(t, executable, t.TempDir())
	})
	t.Run("UTF-8 Japanese and emoji output directory", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "日本語-📺")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create Unicode output directory: %v", err)
		}
		verifyNativeSourceClockEventWriterBoundary(t, executable, directory)
	})
}

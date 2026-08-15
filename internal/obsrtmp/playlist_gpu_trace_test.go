package obsrtmp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPlaylistGPUMuxBufferEmitsBoundaryTraceEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buffer-trace.jsonl")
	var sink bytes.Buffer
	buffer := newPlaylistMuxPublisherBuffer(&sink, path)
	if _, err := buffer.Write([]byte("mpegts")); err != nil {
		t.Fatal(err)
	}
	if err := buffer.CloseAndWait(); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []playlistGPUTraceEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event playlistGPUTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0].Event != "mux_stdout_chunk" || events[1].Event != "publisher_stdin_write" {
		t.Fatalf("buffer boundary events = %#v", events)
	}
	if events[0].Fields["bytes"] != float64(len("mpegts")) || events[1].Fields["completed_bytes"] != float64(len("mpegts")) {
		t.Fatalf("buffer boundary byte fields = %#v", events)
	}
}

func TestPlaylistGPUTraceRecordsOrderedJSONLEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playlist-gpu-trace.jsonl")
	if err := recordPlaylistGPUTrace(path, "gpu_mux_started", map[string]any{"pid": 42}); err != nil {
		t.Fatal(err)
	}
	if err := recordPlaylistGPUTrace(path, "publisher_stdin_write", map[string]any{"bytes": 188}); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []playlistGPUTraceEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event playlistGPUTraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode trace event: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("trace events = %d, want 2", len(events))
	}
	if events[0].Event != "gpu_mux_started" || events[1].Event != "publisher_stdin_write" {
		t.Fatalf("trace event order = %#v", events)
	}
	if events[0].UnixNano <= 0 || events[1].UnixNano < events[0].UnixNano {
		t.Fatalf("trace timestamps are not nondecreasing: %#v", events)
	}
	if events[0].Fields["pid"] != float64(42) || events[1].Fields["bytes"] != float64(188) {
		t.Fatalf("trace fields = %#v", events)
	}
}

func TestPlaylistGPUTraceDisabledDoesNotCreateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disabled", "playlist-gpu-trace.jsonl")
	if err := recordPlaylistGPUTrace("", "should_not_write", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled trace unexpectedly created %s: %v", path, err)
	}
}

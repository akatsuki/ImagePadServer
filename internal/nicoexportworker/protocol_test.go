package nicoexportworker

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func validTestRequest() Request {
	return Request{
		Version: 1, RunID: "run-1", MediaID: "media-1",
		SourcePath: "source.mp4", SnapshotPath: "snapshot.json", OutputPath: "output.mp4", HLSStagingDir: "hls",
		FFmpeg: "ffmpeg.exe", Width: 1280, Height: 720, DurationMs: 6000, FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k",
	}
}

func TestReadRequestRequiresVersionedSingleJSONLine(t *testing.T) {
	request := validTestRequest()
	request.Encoder = "nvenc"
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadRequest(bytes.NewReader(append(data, '\n')))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.MediaID != "media-1" || got.Encoder != "nvenc" {
		t.Fatalf("request = %#v", got)
	}

	data, err = json.Marshal(Request{Version: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRequest(bytes.NewReader(append(data, '\n'))); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("version error = %v", err)
	}
	validData, err := json.Marshal(validTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRequest(bytes.NewReader(append(append(validData, '\n'), []byte("{}\n")...))); err == nil || !strings.Contains(err.Error(), "one request") {
		t.Fatalf("trailing request error = %v", err)
	}
}

func TestRequestValidateRejectsUnsafeIdentityAndMissingArtifact(t *testing.T) {
	request := validTestRequest()
	request.MediaID = "../outside"
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "media") {
		t.Fatalf("identity error = %v", err)
	}
	request = validTestRequest()
	request.OutputPath = ""
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("output error = %v", err)
	}
}

func TestRequestValidateRejectsUnknownEncoder(t *testing.T) {
	request := validTestRequest()
	request.Encoder = "not-a-real-encoder"
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "encoder") {
		t.Fatalf("encoder validation error = %v", err)
	}
}

func TestRequestValidateAcceptsOutputModesAndRejectsUnknownBeforeStagingChecks(t *testing.T) {
	for _, mode := range []string{"", "separate", "tee"} {
		request := validTestRequest()
		request.OutputMode = mode
		if err := request.Validate(); err != nil {
			t.Fatalf("OutputMode %q validation error = %v", mode, err)
		}
	}

	request := validTestRequest()
	request.OutputMode = "sidecar"
	request.OutputPath = request.SourcePath
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "output mode") {
		t.Fatalf("unknown OutputMode error = %v", err)
	}
}

func TestRequestJSONKeepsLegacyEmptyOutputModeAndRoundTripsTee(t *testing.T) {
	legacy := `{"version":1,"run_id":"run-1","media_id":"media-1","source_path":"source.mp4","snapshot_path":"snapshot.json","output_path":"output.mp4","hls_staging_dir":"hls","ffmpeg":"ffmpeg.exe","width":1280,"height":720,"duration_ms":6000,"fps_num":30,"fps_den":1,"crf":26,"audio_bitrate":"160k"}`
	got, err := ReadRequest(strings.NewReader(legacy + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.OutputMode != "" {
		t.Fatalf("legacy OutputMode = %q, want empty", got.OutputMode)
	}

	request := validTestRequest()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "output_mode") {
		t.Fatalf("empty OutputMode was not omitted: %s", data)
	}
	request.OutputMode = "tee"
	data, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"output_mode":"tee"`) {
		t.Fatalf("tee OutputMode was not encoded: %s", data)
	}
	got, err = ReadRequest(strings.NewReader(string(data) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.OutputMode != "tee" {
		t.Fatalf("round-tripped OutputMode = %q", got.OutputMode)
	}
}

func TestWriteEventRejectsOversizedBoundedOutput(t *testing.T) {
	var out bytes.Buffer
	err := WriteEvent(&out, Event{Version: 1, Type: "result", Error: strings.Repeat("x", maxEventBytes)})
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("oversize error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("oversized event wrote %d bytes", out.Len())
	}

	out.Reset()
	if err := WriteEvent(&out, Event{Version: 1, Type: "progress", Stage: "render", Completed: 1, Total: 2}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(), "\n") || !strings.Contains(out.String(), `"version":1`) {
		t.Fatalf("event = %q", out.String())
	}
}

func TestEventMetadataAbsentKeepsLegacyJSONShape(t *testing.T) {
	event := Event{Version: 1, Type: "result", RunID: "run-1", MediaID: "media-1", OK: true}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":1,"type":"result","run_id":"run-1","media_id":"media-1","ok":true}`
	if string(data) != want {
		t.Fatalf("legacy event JSON = %s, want %s", data, want)
	}
}

func TestWriteEventKeepsNCT2AttemptMetadataWithinEventLimit(t *testing.T) {
	input := `{"version":1,"type":"result","timeline_attempt":{"protocol":"` + strings.Repeat("x", maxEventBytes) + `"}}`
	var event Event
	if err := json.Unmarshal([]byte(input), &event); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := WriteEvent(&out, event); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("oversized timeline diagnostic error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("oversized timeline diagnostic wrote %d bytes", out.Len())
	}
}

package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"imagepadserver/internal/nicoexportworker"
)

func TestNicoWorkerEventCollectorReportsProgress(t *testing.T) {
	var progress []nicoexportworker.Event
	request := nicoexportworker.Request{RunID: "run-1", MediaID: "media-1"}
	ctx := withNicoWorkerProgress(context.Background(), func(event nicoexportworker.Event) {
		progress = append(progress, event)
	})
	collector := newNicoWorkerEventCollector(ctx, request)
	output := `{"version":1,"type":"progress","run_id":"run-1","media_id":"media-1","stage":"render","completed":12,"total":30}
{"version":1,"type":"result","run_id":"run-1","media_id":"media-1","output":"out.mp4","playlist":"playlist.m3u8","ok":true}
`
	if _, err := io.Copy(collector, strings.NewReader(output)); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.event(); err != nil {
		t.Fatal(err)
	}
	if len(progress) != 1 {
		t.Fatalf("progress events = %d, want 1", len(progress))
	}
	if progress[0].Stage != "render" || progress[0].Completed != 12 || progress[0].Total != 30 {
		t.Fatalf("progress = %#v", progress[0])
	}
}

func TestParseNicoWorkerEventsRequiresOneSuccessfulResult(t *testing.T) {
	result, err := parseNicoWorkerEvents(`{"version":1,"type":"progress","stage":"render"}
{"version":1,"type":"result","run_id":"run-1","media_id":"media-1","output":"out.mp4","playlist":"playlist.m3u8","ok":true}
`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "result" || !result.OK || result.Output != "out.mp4" {
		t.Fatalf("result = %#v", result)
	}

	for _, input := range []string{
		`{"version":1,"type":"progress"}`,
		`{"version":1,"type":"result","ok":false,"error":"failed"}`,
		`{"version":2,"type":"result","ok":true}`,
	} {
		if _, err := parseNicoWorkerEvents(input); err == nil {
			t.Fatalf("input unexpectedly accepted: %s", input)
		}
	}
}

func TestParseNicoWorkerEventsRejectsOversizedLine(t *testing.T) {
	_, err := parseNicoWorkerEvents(strings.Repeat("x", 64*1024+1))
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("error = %v", err)
	}
}

func TestNicoWorkerEventContractUsesProtocolVersion(t *testing.T) {
	if nicoexportworker.ProtocolVersion != 1 {
		t.Fatalf("protocol version = %d", nicoexportworker.ProtocolVersion)
	}
}

func TestValidateNicoWorkerResultBindsRunMediaAndOutput(t *testing.T) {
	request := nicoexportworker.Request{RunID: "run-1", MediaID: "media-1", OutputPath: `C:\work\out.mp4`, HLSStagingDir: `C:\work`}
	event := nicoexportworker.Event{Version: 1, Type: "result", RunID: "run-1", MediaID: "media-1", Output: `C:\work\out.mp4`, Playlist: `C:\work\playlist.m3u8`, OK: true}
	if err := validateNicoWorkerResult(request, event); err != nil {
		t.Fatal(err)
	}
	event.MediaID = "other"
	if err := validateNicoWorkerResult(request, event); err == nil || !strings.Contains(err.Error(), "media") {
		t.Fatalf("identity error = %v", err)
	}
}

func TestRunNicoWorkerWithBudgetDoesNotStartAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, report, err := runNicoWorkerWithBudget(ctx, nicoexportworker.Request{
		Version: nicoexportworker.ProtocolVersion,
		RunID:   "run-canceled", MediaID: "media-canceled",
		SourcePath: "source.mp4", SnapshotPath: "snapshot.json", OutputPath: "output.mp4", HLSStagingDir: "hls",
		FFmpeg: "ffmpeg", Width: 320, Height: 180, DurationMs: 1000, FPSNum: 30, FPSDen: 1, CRF: 28,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if report.Verified {
		t.Fatalf("canceled worker returned verified report: %+v", report)
	}
}

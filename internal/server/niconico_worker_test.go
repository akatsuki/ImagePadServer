package server

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/nicoexportworker"
	"imagepadserver/internal/nicorender"
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

func TestNicoWorkerEventCollectorMeasuresRequestToResult(t *testing.T) {
	collector := &nicoWorkerEventCollector{workerStartedAt: time.Now().Add(-20 * time.Millisecond)}
	line := `{"version":1,"type":"result","run_id":"run-1","media_id":"media-1","output":"out.mp4","playlist":"playlist.m3u8","ok":true}` + "\n"
	if _, err := io.Copy(collector, strings.NewReader(line)); err != nil {
		t.Fatal(err)
	}
	result, err := collector.event()
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkerWallSeconds < 0.015 {
		t.Fatalf("worker wall=%f seconds, want time to final result event", result.WorkerWallSeconds)
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

func TestNicoWorkerCPUAllowanceUsesFullMachineCapacity(t *testing.T) {
	options := nicoWorkerCPUOptions()
	if options.Percent != 100 {
		t.Fatalf("production worker CPU allowance = %d%%, want full capacity (100%%)", options.Percent)
	}
}

func TestNicoTimelineWorkerOptionsEnablesConfiguredHelperByDefaultAndRequireValidOptOut(t *testing.T) {
	for _, key := range []string{
		"IMAGEPAD_NICO_RENDERER", "IMAGEPAD_NICO_TIMELINE_ENABLED", "IMAGEPAD_NICO_TIMELINE_COMPOSITOR",
		"IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS", "IMAGEPAD_NICO_TIMELINE_GPU_BACKEND",
	} {
		t.Setenv(key, "")
	}
	defaults, err := nicoTimelineWorkerRequestOptions()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Backend != "" || defaults.TimelineCompositor != "" || defaults.TimelineReadbackSlots != 0 || defaults.TimelineGPUBackend != "" {
		t.Fatalf("unexpected default timeline options: %+v", defaults)
	}
	if defaults.TimelineEnabled != nicorender.EmbeddedTimelineCompositorSupportsNCT2() {
		t.Fatalf("default enablement does not match a valid embedded NCT2 helper: options=%+v", defaults)
	}
	t.Setenv("IMAGEPAD_NICO_RENDERER", "auto")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_COMPOSITOR", `C:\helpers\nico-compositord.exe`)
	t.Setenv("IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS", "2")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_GPU_BACKEND", "vulkan")
	configured, err := nicoTimelineWorkerRequestOptions()
	if err != nil {
		t.Fatal(err)
	}
	if configured.Backend != "auto" || !configured.TimelineEnabled || configured.TimelineCompositor != `C:\helpers\nico-compositord.exe` || configured.TimelineReadbackSlots != 2 || configured.TimelineGPUBackend != "vulkan" {
		t.Fatalf("timeline worker options=%+v", configured)
	}
	t.Setenv("IMAGEPAD_NICO_TIMELINE_ENABLED", "false")
	disabled, err := nicoTimelineWorkerRequestOptions()
	if err != nil {
		t.Fatal(err)
	}
	if disabled.TimelineEnabled {
		t.Fatalf("explicit false did not disable timeline rendering: %+v", disabled)
	}
	t.Setenv("IMAGEPAD_NICO_TIMELINE_ENABLED", "sometimes")
	if _, err := nicoTimelineWorkerRequestOptions(); err == nil || !strings.Contains(err.Error(), "IMAGEPAD_NICO_TIMELINE_ENABLED") {
		t.Fatalf("invalid enable value error=%v", err)
	}
	t.Setenv("IMAGEPAD_NICO_TIMELINE_ENABLED", "false")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS", "4")
	if _, err := nicoTimelineWorkerRequestOptions(); err == nil || !strings.Contains(err.Error(), "readback slots") {
		t.Fatalf("invalid readback slot error=%v", err)
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

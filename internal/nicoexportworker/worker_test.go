package nicoexportworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
	"imagepadserver/internal/video"
)

func TestRunRejectsInvalidRequestAndEmitsBoundedResult(t *testing.T) {
	request := validTestRequest()
	request.MediaID = "../outside"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), request, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "media") {
		t.Fatalf("error = %v", err)
	}
	var event Event
	if decodeErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); decodeErr != nil {
		t.Fatalf("event decode: %v; stdout=%q", decodeErr, stdout.String())
	}
	if event.Version != ProtocolVersion || event.Type != "result" || event.OK || event.Error == "" {
		t.Fatalf("event = %#v", event)
	}
}

func TestRunForwardsDiagnosticThreadOptions(t *testing.T) {
	dir := t.TempDir()
	request := validTestRequest()
	request.SourcePath = filepath.Join(dir, "source.mp4")
	request.SnapshotPath = filepath.Join(dir, "snapshot.json")
	request.OutputPath = filepath.Join(dir, "rendered.mp4")
	request.HLSStagingDir = filepath.Join(dir, "hls")
	request.FilterThreads = 1
	request.DecoderThreads = 1
	request.EncoderThreads = 1
	request.Encoder = "nvenc"
	if err := os.WriteFile(request.SourcePath, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.SnapshotPath, []byte(`{"schemaVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldExport := nicoExportWithRenderer
	t.Cleanup(func() { nicoExportWithRenderer = oldExport })
	var got video.NicoEncodeOptions
	nicoExportWithRenderer = func(_ context.Context, _, _, _, _ string, _ niconico.Snapshot, _ nicorender.RenderOptions, options video.NicoEncodeOptions, _ video.NicoOutputMode) (video.NicoExportOutput, nicorender.RenderReport, error) {
		got = options
		return video.NicoExportOutput{}, nicorender.RenderReport{}, errors.New("stop after options capture")
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), request, &stdout, nil); err == nil || !strings.Contains(err.Error(), "stop after options capture") {
		t.Fatalf("Run error = %v", err)
	}
	if got.FilterThreads != 1 || got.DecoderThreads != 1 || got.EncoderThreads != 1 {
		t.Fatalf("thread options = %+v", got)
	}
	if got.Encoder != "nvenc" {
		t.Fatalf("encoder option = %q", got.Encoder)
	}
}

func TestRunForwardsTimelineOptionsAndReturnsSafeRendererMetadata(t *testing.T) {
	dir := t.TempDir()
	request := validTestRequest()
	request.SourcePath = filepath.Join(dir, "source.mp4")
	request.SnapshotPath = filepath.Join(dir, "snapshot.json")
	request.OutputPath = filepath.Join(dir, "rendered.mp4")
	request.HLSStagingDir = filepath.Join(dir, "hls")
	request.Backend = "timeline"
	request.TimelineEnabled = true
	request.TimelineCompositor = filepath.Join(dir, "nico-compositord.exe")
	request.TimelineReadbackSlots = 2
	request.TimelineGPUBackend = "vulkan"
	if err := os.WriteFile(request.SourcePath, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.SnapshotPath, []byte(`{"schemaVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	oldExport := nicoExportWithRenderer
	oldPrepare := nicoPrepareHLS
	t.Cleanup(func() {
		nicoExportWithRenderer = oldExport
		nicoPrepareHLS = oldPrepare
	})
	var gotRender nicorender.RenderOptions
	timelineStart := 10 * time.Millisecond
	nicoExportWithRenderer = func(_ context.Context, _, _, _, _ string, _ niconico.Snapshot, render nicorender.RenderOptions, _ video.NicoEncodeOptions, _ video.NicoOutputMode) (video.NicoExportOutput, nicorender.RenderReport, error) {
		gotRender = render
		if render.OnTimelineFallback == nil {
			t.Fatal("worker did not register the immediate WGPU fallback notification")
		}
		render.OnTimelineFallback()
		time.Sleep(20 * time.Millisecond)
		return video.NicoExportOutput{Encode: video.NicoEncodeReport{Attempts: []video.NicoEncodeAttempt{{
			Backend: "timeline-wgpu", Error: `device lost C:\private\comment-secret`, HelperSHA256: strings.Repeat("a", 64),
			BundleSHA256: strings.Repeat("b", 64), GPUBackend: "vulkan", GPUAdapter: "AMD Radeon", ReadbackSlots: 2,
			TimelineProtocol: "NCT2", TimelineFirstAssetReady: &timelineStart,
		}}}}, nicorender.RenderReport{RendererLabel: "niconicomments@0.4.1", Backend: "browser"}, nil
	}
	nicoPrepareHLS = func(dir, mediaID, runID string) (video.PreparedNicoHLS, error) {
		return video.PreparedNicoHLS{Playlist: filepath.Join(dir, "playlist.m3u8"), MediaID: mediaID, RunID: runID}, nil
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), request, &stdout, nil); err != nil {
		t.Fatal(err)
	}
	if gotRender.Backend != "timeline" || !gotRender.TimelineEnabled || gotRender.TimelineCompositorPath != request.TimelineCompositor || gotRender.TimelineReadbackSlots != 2 || gotRender.TimelineGPUBackend != "vulkan" {
		t.Fatalf("worker render options=%+v", gotRender)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var result Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
		t.Fatalf("decode result event: %v; stdout=%q", err, stdout.String())
	}
	if result.Type != "result" || !result.OK || result.Renderer != "niconicomments@0.4.1" || result.Backend != "browser" || !result.TimelineFallback || result.FallbackNotice != "WGPUコメント描画に失敗したため、CPU描画へ切り替えました" || result.GPUBackend != "vulkan" || result.GPUAdapter != "AMD Radeon" || result.ReadbackSlots != 2 || result.HelperSHA256 != strings.Repeat("a", 64) || result.BundleSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("timeline result metadata=%+v", result)
	}
	if result.TimelineAttempt == nil || result.TimelineAttempt.Protocol != "NCT2" || result.TimelineAttempt.FirstAssetReady == nil || *result.TimelineAttempt.FirstAssetReady != timelineStart {
		t.Fatalf("terminal event omitted fallback NCT2 timing metadata: %+v", result.TimelineAttempt)
	}
	if result.ConvertWallSeconds < 0.015 {
		t.Fatalf("convert wall=%f seconds, want measured renderer call duration", result.ConvertWallSeconds)
	}
	fallbackIndex, hlsIndex, fallbackEvents := -1, -1, 0
	for index, line := range lines {
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		switch event.Stage {
		case "timeline_fallback":
			fallbackEvents++
			fallbackIndex = index
			if event.FallbackNotice != result.FallbackNotice {
				t.Fatalf("fallback progress notice=%q result notice=%q", event.FallbackNotice, result.FallbackNotice)
			}
		case "hls":
			hlsIndex = index
		}
	}
	if fallbackEvents != 1 || fallbackIndex < 0 || hlsIndex < 0 || fallbackIndex >= hlsIndex {
		t.Fatalf("fallback event count/index=%d/%d must occur once before HLS index %d: %q", fallbackEvents, fallbackIndex, hlsIndex, stdout.String())
	}
	if strings.Contains(stdout.String(), "private") || strings.Contains(stdout.String(), "comment-secret") || strings.Contains(stdout.String(), request.TimelineCompositor) {
		t.Fatalf("worker result leaked failure text or helper path: %q", stdout.String())
	}
}

func TestApplyNicoTimelineResultMetadataIncludesStageTimings(t *testing.T) {
	stage := video.NicoStageTiming{
		Name:    "timeline_capture",
		Elapsed: 1658180200,
		Bytes:   4096,
	}
	spriteMetrics := &nicorender.TimelineCaptureMetrics{
		Mode: "sync", TextureCreations: 7, ReadbackCalls: 7, ReadbackBytes: 65536,
		ReadbackWallMs: 1.25, PackWallMs: 0.5, BrowserCallCount: 2,
		BrowserCallWallMs: 9.75, CaptureWallSeconds: 0.02, Valid: true,
	}
	var event Event
	applyNicoTimelineResultMetadata(&event, video.NicoEncodeReport{
		StageTimings: []video.NicoStageTiming{stage}, SpriteCaptureMetrics: spriteMetrics,
	}, nicorender.RenderReport{})

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	var got []video.NicoStageTiming
	if raw, ok := fields["stage_timings"]; !ok {
		t.Fatalf("worker result omitted stage timings: %s", payload)
	} else if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode stage timings: %v", err)
	}
	if len(got) != 1 || got[0].Name != "timeline_capture" || got[0].Elapsed != 1658180200 || got[0].Bytes != 4096 {
		t.Fatalf("stage timings = %+v, want the measured timeline capture stage", got)
	}
	var gotSpriteMetrics nicorender.TimelineCaptureMetrics
	if raw, ok := fields["sprite_capture_metrics"]; !ok {
		t.Fatalf("worker result omitted sprite capture metrics: %s", payload)
	} else if err := json.Unmarshal(raw, &gotSpriteMetrics); err != nil {
		t.Fatalf("decode sprite capture metrics: %v", err)
	}
	if gotSpriteMetrics != *spriteMetrics {
		t.Fatalf("sprite capture metrics = %+v, want %+v", gotSpriteMetrics, *spriteMetrics)
	}
}

func TestApplyNicoTimelineResultMetadataProjectsNCT2ReportAndAttemptOffsets(t *testing.T) {
	zero, asset, frame, end, helper, ffmpeg := time.Duration(0), 12*time.Millisecond, 35*time.Millisecond, 70*time.Millisecond, 90*time.Millisecond, 100*time.Millisecond
	encode := video.NicoEncodeReport{
		TimelineProtocol: "NCT2", TimelineCaptureDone: &zero, TimelineFirstAssetReady: &asset,
		TimelineFirstFrame: &frame, TimelineStreamEnd: &end, TimelineHelperDone: &helper, TimelineFFmpegDone: &ffmpeg,
		Attempts: []video.NicoEncodeAttempt{{
			Backend: "timeline-wgpu", Error: "device lost", TimelineProtocol: "NCT2",
			TimelineCaptureDone: &zero, TimelineFirstAssetReady: &asset, TimelineFirstFrame: &frame, TimelineStreamEnd: &end,
			TimelineHelperDone: &helper, TimelineFFmpegDone: &ffmpeg,
		}},
	}
	var event Event
	applyNicoTimelineResultMetadata(&event, encode, nicorender.RenderReport{})
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"timeline_protocol": "NCT2", "timeline_capture_done_ns": float64(0),
		"timeline_first_asset_ready_ns": float64(12_000_000), "timeline_first_frame_ns": float64(35_000_000),
		"timeline_stream_end_ns": float64(70_000_000), "timeline_helper_done_ns": float64(90_000_000),
		"timeline_ffmpeg_done_ns": float64(100_000_000),
	} {
		raw, ok := fields[key]
		if !ok {
			t.Errorf("terminal event omitted %s: %s", key, payload)
			continue
		}
		var got any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	var attempt map[string]json.RawMessage
	if raw := fields["timeline_attempt"]; len(raw) == 0 || json.Unmarshal(raw, &attempt) != nil {
		t.Fatalf("terminal event omitted fallback timeline attempt: %s", payload)
	}
	for key, want := range map[string]float64{"capture_done_ns": 0, "first_asset_ready_ns": 12_000_000, "first_frame_ns": 35_000_000, "stream_end_ns": 70_000_000, "helper_done_ns": 90_000_000, "ffmpeg_done_ns": 100_000_000} {
		var got float64
		if raw := attempt[key]; len(raw) == 0 || json.Unmarshal(raw, &got) != nil || got != want {
			t.Errorf("attempt.%s = %v, want %v; event=%s", key, got, want, payload)
		}
	}
	var roundTripped Event
	if err := json.Unmarshal(payload, &roundTripped); err != nil {
		t.Fatal(err)
	}
	reencoded, err := json.Marshal(roundTripped)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripFields map[string]json.RawMessage
	if err := json.Unmarshal(reencoded, &roundTripFields); err != nil {
		t.Fatal(err)
	}
	if len(roundTripFields["timeline_attempt"]) == 0 || string(roundTripFields["timeline_protocol"]) != `"NCT2"` {
		t.Fatalf("NCT2 metadata lost on Event round-trip: %s", reencoded)
	}
}

func TestApplyNicoTimelineResultMetadataOmitsLegacyNCT1AndUnreachedValues(t *testing.T) {
	var event Event
	applyNicoTimelineResultMetadata(&event, video.NicoEncodeReport{
		TimelineProtocol: "NCT1",
		Attempts:         []video.NicoEncodeAttempt{{Backend: "timeline-wgpu", Error: "legacy failure", TimelineProtocol: "NCT1"}},
	}, nicorender.RenderReport{})
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"timeline_protocol", "timeline_capture_done_ns", "timeline_first_asset_ready_ns", "timeline_attempt"} {
		if _, ok := fields[key]; ok {
			t.Errorf("legacy metadata field %s unexpectedly present: %s", key, payload)
		}
	}
}

func TestRunRemovesPartialArtifactsWhenHLSFailsAfterEncode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	snapshotPath := filepath.Join(dir, "snapshot.json")
	output := filepath.Join(dir, "staging", "rendered.mp4")
	hlsDir := filepath.Join(dir, "staging", "hls")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, []byte(`{"schemaVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	request := validTestRequest()
	request.SourcePath = source
	request.SnapshotPath = snapshotPath
	request.OutputPath = output
	request.HLSStagingDir = hlsDir

	oldExport := nicoExportWithRenderer
	t.Cleanup(func() { nicoExportWithRenderer = oldExport })
	nicoExportWithRenderer = func(_ context.Context, _, _, outputPath, hlsDir string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ video.NicoEncodeOptions, _ video.NicoOutputMode) (video.NicoExportOutput, nicorender.RenderReport, error) {
		if err := os.WriteFile(outputPath, []byte("partial-mp4"), 0600); err != nil {
			return video.NicoExportOutput{}, nicorender.RenderReport{}, err
		}
		if err := os.MkdirAll(hlsDir, 0700); err != nil {
			return video.NicoExportOutput{}, nicorender.RenderReport{}, err
		}
		if err := os.WriteFile(filepath.Join(hlsDir, "partial.ts"), []byte("partial"), 0600); err != nil {
			return video.NicoExportOutput{}, nicorender.RenderReport{}, err
		}
		return video.NicoExportOutput{}, nicorender.RenderReport{}, errors.New("injected HLS failure")
	}

	var stdout bytes.Buffer
	err := Run(context.Background(), request, &stdout, nil)
	if err == nil || !strings.Contains(err.Error(), "injected HLS failure") {
		t.Fatalf("Run error = %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("partial MP4 survived failed worker: %v", statErr)
	}
	if _, statErr := os.Stat(hlsDir); !os.IsNotExist(statErr) {
		t.Fatalf("partial HLS survived failed worker: %v", statErr)
	}
	var event Event
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 {
		t.Fatalf("worker emitted no result event: %q", stdout.String())
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &event); err != nil {
		t.Fatalf("result event decode: %v; stdout=%q", err, stdout.String())
	}
	if event.Type != "result" || event.OK || event.Error == "" {
		t.Fatalf("failure event = %#v", event)
	}
}

func TestRunTeeUsesEncodedHLSAndSkipsSecondFFmpegPass(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	snapshotPath := filepath.Join(dir, "snapshot.json")
	staging := filepath.Join(dir, "staging")
	output := filepath.Join(staging, "rendered.mp4")
	hlsDir := filepath.Join(staging, "hls")
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, []byte(`{"schemaVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	request := validTestRequest()
	request.SourcePath = source
	request.SnapshotPath = snapshotPath
	request.OutputPath = output
	request.HLSStagingDir = hlsDir
	request.OutputMode = "tee"

	oldExport := nicoExportWithRenderer
	t.Cleanup(func() { nicoExportWithRenderer = oldExport })
	nicoExportWithRenderer = func(_ context.Context, _, _, outputPath, hlsDir string, _ niconico.Snapshot, _ nicorender.RenderOptions, options video.NicoEncodeOptions, mode video.NicoOutputMode) (video.NicoExportOutput, nicorender.RenderReport, error) {
		if options.OutputMode != video.NicoOutputTee || options.HLSOutputDir != hlsDir {
			t.Fatalf("tee options = %+v", options)
		}
		if mode != video.NicoOutputTee {
			t.Fatalf("tee mode = %q", mode)
		}
		if err := os.WriteFile(outputPath, []byte("encoded-mp4"), 0600); err != nil {
			return video.NicoExportOutput{}, nicorender.RenderReport{}, err
		}
		if err := os.MkdirAll(options.HLSOutputDir, 0700); err != nil {
			return video.NicoExportOutput{}, nicorender.RenderReport{}, err
		}
		if err := os.WriteFile(filepath.Join(options.HLSOutputDir, "segment-00000.ts"), []byte("encoded-ts"), 0600); err != nil {
			return video.NicoExportOutput{}, nicorender.RenderReport{}, err
		}
		playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:1.0,\nsegment-00000.ts\n#EXT-X-ENDLIST\n"
		if err := os.WriteFile(filepath.Join(options.HLSOutputDir, "playlist.m3u8"), []byte(playlist), 0600); err != nil {
			return video.NicoExportOutput{}, nicorender.RenderReport{}, err
		}
		return video.NicoExportOutput{Mode: mode}, nicorender.RenderReport{}, nil
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), request, &stdout, nil); err != nil {
		t.Fatalf("Run error = %v; stdout=%q", err, stdout.String())
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("tee output missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hlsDir, "playlist.m3u8")); err != nil {
		t.Fatalf("tee playlist missing: %v", err)
	}
}

func TestRunRemovesPartialArtifactsWhenContextIsCanceledDuringEncode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	snapshotPath := filepath.Join(dir, "snapshot.json")
	output := filepath.Join(dir, "staging", "rendered.mp4")
	hlsDir := filepath.Join(dir, "staging", "hls")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, []byte(`{"schemaVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	request := validTestRequest()
	request.SourcePath = source
	request.SnapshotPath = snapshotPath
	request.OutputPath = output
	request.HLSStagingDir = hlsDir

	oldExport := nicoExportWithRenderer
	t.Cleanup(func() { nicoExportWithRenderer = oldExport })
	started := make(chan struct{})
	nicoExportWithRenderer = func(ctx context.Context, _, _, outputPath, _ string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ video.NicoEncodeOptions, _ video.NicoOutputMode) (video.NicoExportOutput, nicorender.RenderReport, error) {
		if err := os.WriteFile(outputPath, []byte("partial-mp4"), 0600); err != nil {
			return video.NicoExportOutput{}, nicorender.RenderReport{}, err
		}
		close(started)
		<-ctx.Done()
		return video.NicoExportOutput{}, nicorender.RenderReport{}, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var stdout bytes.Buffer
		done <- Run(ctx, request, &stdout, nil)
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("encode did not start")
	}
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled worker did not return")
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("partial MP4 survived canceled worker: %v", statErr)
	}
	if _, statErr := os.Stat(hlsDir); !os.IsNotExist(statErr) {
		t.Fatalf("partial HLS survived canceled worker: %v", statErr)
	}
}

func TestRunRemovesPartialArtifactsWhenRealAutoFallbackHLSFails(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_REAL_FALLBACK_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICO_REAL_FALLBACK_TEST=1 for real browser fallback cleanup E2E")
	}
	if runtime.GOOS != "windows" {
		t.Skip("real browser fallback cleanup E2E requires Windows")
	}
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		t.Skip("IMAGEPAD_FFMPEG is not set")
	}
	fixtureDir := filepath.Join("..", "..", "build", "nico-cpu20", "t11-fixtures-20260921")
	sourceData, err := os.ReadFile(filepath.Join(fixtureDir, "source-high-density-10s.mp4"))
	if err != nil {
		t.Skipf("real worker source fixture is unavailable: %v", err)
	}
	snapshotData, err := os.ReadFile(filepath.Join(fixtureDir, "snapshot-high-density.json"))
	if err != nil {
		t.Skipf("real worker snapshot fixture is unavailable: %v", err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	snapshotPath := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(source, sourceData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, snapshotData, 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "staging", "rendered.mp4")
	hlsDir := filepath.Join(dir, "staging", "hls")
	request := validTestRequest()
	request.SourcePath = source
	request.SnapshotPath = snapshotPath
	request.OutputPath = output
	request.HLSStagingDir = hlsDir
	request.FFmpeg = ffmpeg
	request.Backend = "auto"
	request.Compositor = filepath.Join(dir, "missing-nico-compositor.exe")
	request.Width = 320
	request.Height = 180
	request.DurationMs = 1000

	oldExport := nicoExportWithRenderer
	t.Cleanup(func() { nicoExportWithRenderer = oldExport })
	// Keep the real encoder and real auto->browser fallback. Inject only the
	// post-encode HLS failure so the worker's cleanup is tested at the same
	// boundary as a failed production publication.
	nicoExportWithRenderer = func(ctx context.Context, ffmpeg, sourcePath, outputPath, hlsDir string, snapshot niconico.Snapshot, renderOptions nicorender.RenderOptions, encodeOptions video.NicoEncodeOptions, mode video.NicoOutputMode) (video.NicoExportOutput, nicorender.RenderReport, error) {
		er, rr, err := video.EncodeNicoCommentedWithRenderer(ctx, ffmpeg, sourcePath, outputPath, snapshot, renderOptions, encodeOptions)
		if err != nil {
			return video.NicoExportOutput{}, rr, err
		}
		if err := os.MkdirAll(hlsDir, 0700); err != nil {
			return video.NicoExportOutput{}, rr, err
		}
		if err := os.WriteFile(filepath.Join(hlsDir, "partial.ts"), []byte("partial"), 0600); err != nil {
			return video.NicoExportOutput{}, rr, err
		}
		return video.NicoExportOutput{Encode: er, Mode: mode}, rr, errors.New("injected fallback HLS failure")
	}

	var stdout bytes.Buffer
	err = Run(context.Background(), request, &stdout, nil)
	if err == nil || !strings.Contains(err.Error(), "injected fallback HLS failure") {
		t.Fatalf("Run error = %v; stdout=%q", err, stdout.String())
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("partial MP4 survived real fallback HLS failure: %v", statErr)
	}
	if _, statErr := os.Stat(hlsDir); !os.IsNotExist(statErr) {
		t.Fatalf("partial HLS survived real fallback HLS failure: %v", statErr)
	}
}

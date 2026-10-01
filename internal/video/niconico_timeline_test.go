package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

func TestTimelineSelection(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		enabled bool
		want    string
	}{
		{name: "default auto stays legacy", backend: "auto", want: "legacy"},
		{name: "opt-in auto tries timeline", backend: "auto", enabled: true, want: "try"},
		{name: "explicit timeline tries with CPU fallback", backend: "timeline", want: "try"},
		{name: "browser remains legacy", backend: "browser", enabled: true, want: "legacy"},
		{name: "native remains legacy", backend: "native", enabled: true, want: "legacy"},
		{name: "case is normalized", backend: " TiMeLiNe ", want: "try"},
		{name: "empty backend defaults to auto", enabled: true, want: "try"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := timelineSelection(tc.backend, tc.enabled); got != tc.want {
				t.Fatalf("timelineSelection(%q, %t) = %q, want %q", tc.backend, tc.enabled, got, tc.want)
			}
		})
	}
}

func TestNicoTimelineEncoderInventoryRecognizesOnlyVideoEncoderRows(t *testing.T) {
	output := []byte("" +
		"Encoders:\n" +
		" V..... = Video\n" +
		" A..... = Audio\n" +
		" V....D libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10\n" +
		" V....D h264_nvenc NVIDIA NVENC H.264 encoder\n" +
		" A....D libx264 audio-looking-name\n")
	for _, encoder := range []string{"libx264", "h264_nvenc"} {
		if !nicoTimelineEncoderListed(output, encoder) {
			t.Errorf("encoder %q was not found", encoder)
		}
	}
	for _, encoder := range []string{"libx265", "missing"} {
		if nicoTimelineEncoderListed(output, encoder) {
			t.Errorf("unexpected encoder %q found", encoder)
		}
	}
}

func TestNicoTimelineStreamsAndPromotesOnlyValidatedOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("previous-output"), 0600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "nico-compositord.exe")
	helperBytes := []byte("helper-binary-fixture")
	if err := os.WriteFile(helper, helperBytes, 0600); err != nil {
		t.Fatal(err)
	}
	oldCapture := captureNicoTimelineStreamForPipeline
	oldPrepare := prepareNicoTimelineCompositorForPipeline
	oldPipe := runNicoTimelinePipeForPipeline
	oldProbe := probeNicoTimelineEncoderForPipeline
	t.Cleanup(func() {
		captureNicoTimelineStreamForPipeline = oldCapture
		prepareNicoTimelineCompositorForPipeline = oldPrepare
		runNicoTimelinePipeForPipeline = oldPipe
		probeNicoTimelineEncoderForPipeline = oldProbe
	})
	probeNicoTimelineEncoderForPipeline = func(context.Context, string, NicoEncodeOptions) error { return nil }
	captureNicoTimelineStreamForPipeline = func(ctx context.Context, _ niconico.Snapshot, options nicorender.RenderOptions, out io.Writer) (nicorender.TimelineCaptureReport, error) {
		if err := ctx.Err(); err != nil {
			return nicorender.TimelineCaptureReport{}, err
		}
		if err := writeTestNicoTimelineStream(out, options); err != nil {
			return nicorender.TimelineCaptureReport{}, err
		}
		return nicorender.TimelineCaptureReport{RenderReport: nicorender.RenderReport{FrameCount: 3}, BundleSHA256: strings.Repeat("b", 64)}, nil
	}
	prepareNicoTimelineCompositorForPipeline = func(_ context.Context, path string, options nicorender.TimelineRuntimeOptions) (string, func(), error) {
		if path != helper || options.Backend != "auto" || options.ReadbackSlots != 2 {
			t.Errorf("prepare arguments path=%q options=%+v", path, options)
		}
		return helper, func() {}, nil
	}
	runNicoTimelinePipeForPipeline = func(ctx context.Context, nativePath string, nativeArgs []string, encoderPath string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), encoderDir string) (nicoNativePipeResult, error) {
		if nativePath != helper || encoderPath != "ffmpeg" || encoderDir != "" || expected != 3 {
			t.Errorf("pipe paths/count native=%q encoder=%q dir=%q frames=%d", nativePath, encoderPath, encoderDir, expected)
		}
		if got := timelineArgumentValue(nativeArgs, "--backend"); got != "auto" {
			t.Errorf("helper backend arg=%q", got)
		}
		if got := timelineArgumentValue(nativeArgs, "--readback-slots"); got != "2" {
			t.Errorf("helper ring arg=%q", got)
		}
		if !containsNicoArg(nativeArgs, "--stdin-stream") || containsNicoArg(nativeArgs, "--stdin") {
			t.Errorf("helper must use NCT2 streaming input: %v", nativeArgs)
		}
		reportPath := timelineArgumentValue(nativeArgs, "--report")
		if reportPath == "" {
			t.Fatal("helper report path was not supplied")
		}
		var wire bytes.Buffer
		produced, err := produce(ctx, &wire)
		if err != nil {
			return nicoNativePipeResult{}, err
		}
		if wire.Len() < nicorender.TimelineStreamEnvelopeBytes+4 || !bytes.Equal(wire.Bytes()[nicorender.TimelineStreamEnvelopeBytes:nicorender.TimelineStreamEnvelopeBytes+4], []byte("NCT2")) || produced.FrameCount != expected {
			t.Errorf("producer did not write a complete NCT2 header record: prefix=%q report=%+v", wire.Bytes()[:min(4, wire.Len())], produced)
		}
		if timelineOutputPath(encoderArgs) == "" {
			t.Errorf("unexpected ffmpeg output args: %v", encoderArgs)
		}
		if !strings.Contains(strings.Join(encoderArgs, " "), "alpha=premultiplied") {
			t.Errorf("timeline RGBA overlay must declare its premultiplied alpha mode: %v", encoderArgs)
		}
		if !containsNicoArg(encoderArgs, "libx264") || !containsNicoArg(encoderArgs, "-x264-params") || !containsNicoArg(encoderArgs, "sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0") {
			t.Errorf("x264 settings were not kept on the shared encoder path: %v", encoderArgs)
		}
		if err := os.WriteFile(encoderArgs[len(encoderArgs)-1], []byte("completed-mp4"), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		if got, err := os.ReadFile(output); err != nil || string(got) != "previous-output" {
			t.Errorf("final output was replaced before report validation: %q err=%v", got, err)
		}
		if err := os.WriteFile(reportPath, testNicoTimelineRuntimeReportWithAssets("auto", 3, 2, 1), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		progress(expected, expected)
		return nicoNativePipeResult{
			report: nicorender.RenderReport{FrameCount: expected},
			milestones: nicoNativeMilestones{
				CaptureDone: durationOffset(0), FirstAssetReady: durationOffset(2), FirstFrame: durationOffset(4),
				StreamEnd: durationOffset(6), HelperDone: durationOffset(8), FFmpegDone: durationOffset(10),
			},
		}, nil
	}

	encoded, rendered, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		Backend: "timeline", TimelineCompositorPath: helper, TimelineReadbackSlots: 2, TimelineGPUBackend: "auto",
		Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, Encoder: "x264", Preset: "fast", EncoderThreads: 3})
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Backend != "timeline-wgpu" || rendered.FrameCount != 3 || encoded.FrameCount != 3 {
		t.Fatalf("reports encode=%+v render=%+v", encoded, rendered)
	}
	wantHelperHash := fmt.Sprintf("%x", sha256.Sum256(helperBytes))
	if encoded.TimelineHelperSHA256 != wantHelperHash || encoded.TimelineBundleSHA256 != strings.Repeat("b", 64) || encoded.TimelineGPUBackend != testTimelinePlatformBackend() || encoded.TimelineReadbackSlots != 2 {
		t.Fatalf("timeline runtime metadata=%+v", encoded)
	}
	assertNicoTimelineMilestonesJSON(t, encoded, map[string]any{
		"timeline_protocol": "NCT2", "timeline_capture_done_ns": float64(0), "timeline_first_asset_ready_ns": float64(2),
		"timeline_first_frame_ns": float64(4), "timeline_stream_end_ns": float64(6), "timeline_helper_done_ns": float64(8), "timeline_ffmpeg_done_ns": float64(10),
	})
	if got, err := os.ReadFile(output); err != nil || string(got) != "completed-mp4" {
		t.Fatalf("promoted output=%q err=%v", got, err)
	}
}

func TestNicoTimelineRejectsAssetReadyMarkerCountMismatch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pageCount  int
		firstAsset bool
		lateError  bool
		mismatch   bool
	}{
		{name: "zero pages without marker accepted", pageCount: 0},
		{name: "zero pages but marker present", pageCount: 0, firstAsset: true, mismatch: true},
		{name: "positive pages but marker absent", pageCount: 1, mismatch: true},
		{name: "late pipe failure retains validated partial metadata", pageCount: 1, firstAsset: true, lateError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.mp4")
			helper := filepath.Join(dir, "nico-compositord.exe")
			if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper, []byte("helper"), 0600); err != nil {
				t.Fatal(err)
			}
			oldCapture, oldPrepare := captureNicoTimelineStreamForPipeline, prepareNicoTimelineCompositorForPipeline
			oldPipe, oldProbe := runNicoTimelinePipeForPipeline, probeNicoTimelineEncoderForPipeline
			t.Cleanup(func() {
				captureNicoTimelineStreamForPipeline, prepareNicoTimelineCompositorForPipeline = oldCapture, oldPrepare
				runNicoTimelinePipeForPipeline, probeNicoTimelineEncoderForPipeline = oldPipe, oldProbe
			})
			probeNicoTimelineEncoderForPipeline = func(context.Context, string, NicoEncodeOptions) error { return nil }
			captureNicoTimelineStreamForPipeline = testNicoTimelineStreamCapture(nicorender.TimelineCaptureReport{RenderReport: nicorender.RenderReport{FrameCount: 3}})
			prepareNicoTimelineCompositorForPipeline = func(context.Context, string, nicorender.TimelineRuntimeOptions) (string, func(), error) {
				return helper, func() {}, nil
			}
			runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, args []string, _ string, encArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
				var stream bytes.Buffer
				if _, err := produce(ctx, &stream); err != nil {
					return nicoNativePipeResult{}, err
				}
				if err := os.WriteFile(timelineOutputPath(encArgs), []byte("staged"), 0600); err != nil {
					return nicoNativePipeResult{}, err
				}
				if err := os.WriteFile(timelineArgumentValue(args, "--report"), testNicoTimelineRuntimeReportWithAssets("auto", uint32(expected), 2, tc.pageCount), 0600); err != nil {
					return nicoNativePipeResult{}, err
				}
				progress(expected, expected)
				milestones := nicoNativeMilestones{CaptureDone: durationOffset(0), StreamEnd: durationOffset(6)}
				if tc.firstAsset {
					milestones.FirstAssetReady = durationOffset(1)
				}
				result := nicoNativePipeResult{milestones: milestones}
				if tc.lateError {
					return result, &nicoNativePipeError{PrimaryStage: "compositor", err: errors.New("late compositor failure")}
				}
				return result, nil
			}
			encoded, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, filepath.Join(dir, "out.mp4"), niconico.Snapshot{}, nicorender.RenderOptions{
				Backend: "timeline", TimelineCompositorPath: helper, TimelineReadbackSlots: 2, TimelineGPUBackend: "auto",
				Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
			}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, Encoder: "x264", Preset: "fast", EncoderThreads: 3})
			if tc.lateError {
				if err == nil || !strings.Contains(err.Error(), "late compositor failure") {
					t.Fatalf("expected late pipe error, got %v", err)
				}
				assertNicoTimelineMilestonesJSON(t, encoded, map[string]any{"timeline_protocol": "NCT2", "timeline_capture_done_ns": float64(0), "timeline_first_asset_ready_ns": float64(1), "timeline_stream_end_ns": float64(6)})
				return
			}
			if tc.mismatch && (err == nil || !strings.Contains(err.Error(), "asset page count")) {
				t.Fatalf("expected asset marker/count mismatch, got %v", err)
			}
			if !tc.mismatch && err != nil {
				t.Fatalf("matching asset marker/count rejected: %v", err)
			}
		})
	}
}

func TestNicoEncodeReportOmitsNCT2MetadataWithoutNCT2Run(t *testing.T) {
	assertNicoTimelineMilestonesJSON(t, NicoEncodeReport{OutputPath: "legacy.mp4"}, nil)
}

type timelinePipelineFailure struct {
	stage string
	err   error
}

func (e *timelinePipelineFailure) Error() string                    { return e.err.Error() }
func (e *timelinePipelineFailure) Unwrap() error                    { return e.err }
func (e *timelinePipelineFailure) nicoTimelineFailureStage() string { return e.stage }

func TestNicoTimelineEncoderFailureDoesNotTriggerRendererFallback(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTimeline := encodeNicoTimelineForPipeline
	oldPrepare := prepareNicoNativeCompositor
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		encodeNicoTimelineForPipeline = oldTimeline
		prepareNicoNativeCompositor = oldPrepare
		encodeNicoBrowserForPipeline = oldBrowser
	})
	encodeNicoTimelineForPipeline = func(context.Context, string, string, string, niconico.Snapshot, nicorender.RenderOptions, NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		return NicoEncodeReport{}, nicorender.RenderReport{}, &timelinePipelineFailure{stage: "encoder", err: errors.New("simulated FFmpeg encoder failure")}
	}
	prepareCalls, browserCalls, notifications := 0, 0, 0
	prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
		prepareCalls++
		return "", nil, errors.New("should not retry native renderer after encoder failure")
	}
	encodeNicoBrowserForPipeline = func(context.Context, string, string, string, niconico.Snapshot, nicorender.RenderOptions, NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		browserCalls++
		return NicoEncodeReport{}, nicorender.RenderReport{}, errors.New("should not retry browser renderer after encoder failure")
	}

	_, _, err := EncodeNicoCommentedWithRenderer(context.Background(), "ffmpeg", source, filepath.Join(dir, "out.mp4"), niconico.Snapshot{},
		nicorender.RenderOptions{Backend: "timeline", Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, OnTimelineFallback: func() { notifications++ }},
		NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if err == nil || !strings.Contains(err.Error(), "simulated FFmpeg encoder failure") {
		t.Fatalf("error=%v, want original FFmpeg failure", err)
	}
	if prepareCalls != 0 || browserCalls != 0 || notifications != 0 {
		t.Fatalf("renderer fallback after encoder failure: prepare=%d browser=%d notices=%d", prepareCalls, browserCalls, notifications)
	}
}

func TestNicoTimelineBadRuntimeReportPreservesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	helper := filepath.Join(dir, "nico-compositord.exe")
	for path, data := range map[string]string{source: "source", output: "previous-output", helper: "helper"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installFakeNicoTimelineRuntime(t, helper, output, 2)
	_, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		TimelineCompositorPath: helper, TimelineReadbackSlots: 1, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if err == nil || !strings.Contains(err.Error(), "runtime report") {
		t.Fatalf("expected rejected frame-count report, got %v", err)
	}
	if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "previous-output" {
		t.Fatalf("existing output changed after rejected report: %q err=%v", got, readErr)
	}
}

func TestNicoTimelineRuntimeReportReadsWriterQueueMetrics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-report.json")
	base := strings.TrimSuffix(string(testNicoTimelineRuntimeReport("auto", 1, 2)), "}")
	payload := base + `,"requestedAssetLayout":"separate","assetLayout":"separate","assetPageCount":0,"assetSourceBytes":0,"assetAllocatedBytes":0,"outputQueueMetrics":{"capacity":2,"finalOccupancy":0,"highWater":2}}`
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}

	report, err := readNicoTimelineReport(path)
	if err != nil {
		t.Fatalf("read runtime report with writer queue metrics: %v", err)
	}
	if report.OutputQueueMetrics == nil {
		t.Fatal("writer queue metrics were not decoded")
	}
	if got, want := *report.OutputQueueMetrics, (nicoTimelineWriterQueueMetrics{Capacity: 2, FinalOccupancy: 0, HighWater: 2}); got != want {
		t.Fatalf("writer queue metrics=%+v, want %+v", got, want)
	}
	validate := func() error {
		return validateNicoTimelineRuntimeReport(report, "auto", 2, "separate", 1, runtime.GOOS)
	}
	if err := validate(); err != nil {
		t.Fatalf("valid runtime report rejected: %v", err)
	}
	noFrameBacklog := nicoTimelineWriterQueueMetrics{Capacity: 2, FinalOccupancy: 0, HighWater: 0}
	report.OutputQueueMetrics = &noFrameBacklog
	if err := validate(); err != nil {
		t.Fatalf("valid zero-backlog runtime report rejected: %v", err)
	}
	for _, metrics := range []nicoTimelineWriterQueueMetrics{
		{Capacity: 0, FinalOccupancy: 0, HighWater: 1},
		{Capacity: 2, FinalOccupancy: 1, HighWater: 2},
		{Capacity: 2, FinalOccupancy: 0, HighWater: -1},
		{Capacity: 2, FinalOccupancy: 0, HighWater: 3},
	} {
		report.OutputQueueMetrics = &metrics
		if err := validate(); err == nil {
			t.Errorf("invalid writer queue metrics accepted: %+v", metrics)
		}
	}
}

func TestNicoTimelineRuntimeReportNCT2TelemetryAndLegacyCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, requested, actual, fallback string
	}{
		{name: "atlas falls back to streamed distinct textures", requested: "atlas", actual: "streamed-distinct-textures", fallback: "NCT2 streams each asset as a distinct texture; atlas aggregation is unavailable"},
		{name: "separate streams distinct textures", requested: "separate", actual: "streamed-distinct-textures"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime-report.json")
			payload := testNicoTimelineReportWithTelemetry("NCT2", tc.requested, tc.actual, tc.fallback, timelineTestNCT2TelemetryJSON("untracked streamed upload aggregation"))
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			report, err := readNicoTimelineReport(path)
			if err != nil {
				t.Fatalf("read NCT2 telemetry report: %v", err)
			}
			if err := validateNicoTimelineRuntimeReport(report, "auto", 2, tc.requested, 1, runtime.GOOS); err != nil {
				t.Fatalf("validate NCT2 telemetry report: %v", err)
			}
			if report.AssetTelemetry == nil || report.AssetTelemetry.TelemetryScope != "nct2-streamed-assets" || report.AssetTelemetry.Pages != nil || report.AssetTelemetry.TextureUploadBytes != nil {
				t.Fatalf("NCT2 null telemetry was not preserved: %+v", report.AssetTelemetry)
			}
			encoded := NicoEncodeReport{}
			applyNicoTimelineRuntimeReportMetadata(&encoded, report)
			jsonBytes, err := json.Marshal(encoded)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip struct {
				AssetLayoutRequested string                    `json:"timeline_asset_layout_requested"`
				AssetLayout          string                    `json:"timeline_asset_layout"`
				FallbackReason       string                    `json:"timeline_asset_layout_fallback_reason"`
				Telemetry            *NicoAssetTelemetryReport `json:"timeline_asset_telemetry"`
			}
			if err := json.Unmarshal(jsonBytes, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if roundTrip.Telemetry == nil || roundTrip.Telemetry.Pages != nil || roundTrip.Telemetry.TextureUploadBytes != nil {
				t.Fatalf("NicoEncodeReport JSON lost NCT2 null telemetry: %s", jsonBytes)
			}
			if roundTrip.AssetLayoutRequested != tc.requested || roundTrip.AssetLayout != tc.actual || roundTrip.FallbackReason != tc.fallback {
				t.Fatalf("NicoEncodeReport JSON lost layout/fallback metadata: %+v", roundTrip)
			}
		})
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy-report.json")
	if err := os.WriteFile(legacyPath, testNicoTimelineRuntimeReport("auto", 1, 2), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := readNicoTimelineReport(legacyPath)
	if err != nil {
		t.Fatalf("read legacy report without assetTelemetry: %v", err)
	}
	if err := validateNicoTimelineRuntimeReport(legacy, "auto", 2, "separate", 1, runtime.GOOS); err != nil {
		t.Fatalf("legacy Separate report without assetTelemetry rejected: %v", err)
	}
	if legacy.AssetTelemetry != nil || legacy.AssetTelemetryPresent {
		t.Fatalf("legacy report omission was not preserved: %+v", legacy)
	}
	legacyAtlas := nicoTimelineRuntimeReport{
		Protocol: "NCT2", RequestedAssetLayout: "atlas", AssetLayout: "atlas",
	}
	if err := validateNicoTimelineRuntimeAssetLayout(legacyAtlas, "atlas"); err == nil {
		t.Fatal("legacy NCT2 Atlas report without telemetry qualified as Atlas")
	}
}

func TestNicoTimelineRuntimeReportNCT1StaticTelemetryAndStrictBound(t *testing.T) {
	staticPath := filepath.Join(t.TempDir(), "nct1-report.json")
	if err := os.WriteFile(staticPath, testNicoTimelineReportWithTelemetry("NCT1", "atlas", "atlas", "", timelineTestNCT1TelemetryJSON()), 0600); err != nil {
		t.Fatal(err)
	}
	static, err := readNicoTimelineReport(staticPath)
	if err != nil {
		t.Fatalf("read NCT1 static telemetry: %v", err)
	}
	if err := validateNicoTimelineAssetTelemetry(static); err != nil {
		t.Fatalf("validate NCT1 static telemetry: %v", err)
	}
	if err := validateNicoTimelineRuntimeAssetLayout(static, "atlas"); err != nil {
		t.Fatalf("NCT1 Atlas with effective atlas and no fallback rejected: %v", err)
	}

	largePath := filepath.Join(t.TempDir(), "large-report.json")
	largePayload := testNicoTimelineReportWithTelemetry("NCT2", "separate", "streamed-distinct-textures", "", timelineTestNCT2TelemetryJSON(strings.Repeat("x", 70<<10)))
	if len(largePayload) <= 64<<10 || len(largePayload) > nicoTimelineReportLimit {
		t.Fatalf("large fixture size=%d, want >64 KiB and <=2 MiB", len(largePayload))
	}
	if err := os.WriteFile(largePath, largePayload, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNicoTimelineReport(largePath); err != nil {
		t.Fatalf("valid report above old 64 KiB cap rejected: %v", err)
	}

	tooLargePath := filepath.Join(t.TempDir(), "oversized-report.json")
	tooLarge := append(append([]byte(nil), largePayload...), bytes.Repeat([]byte(" "), nicoTimelineReportLimit)...)
	if err := os.WriteFile(tooLargePath, tooLarge, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNicoTimelineReport(tooLargePath); err == nil || !strings.Contains(err.Error(), "runtime report size") {
		t.Fatalf("report above 2 MiB cap accepted or wrong error: %v", err)
	}
}

func TestNicoTimelineRuntimeReportRejectsMalformedTelemetryAndNCT1StreamedLayout(t *testing.T) {
	base := testNicoTimelineReportWithTelemetry("NCT2", "separate", "streamed-distinct-textures", "", timelineTestNCT2TelemetryJSON("unknown incrementally streamed data"))
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "unknown top-level runtime field", data: bytes.Replace(base, []byte(`"assetTelemetry":`), []byte(`"futureRuntimeField":true,"assetTelemetry":`), 1)},
		{name: "unknown telemetry field", data: bytes.Replace(base, []byte(`"unavailableReasons"`), []byte(`"futureMetric":null,"unavailableReasons"`), 1)},
		{name: "malformed telemetry number", data: bytes.Replace(base, []byte(`"textureUploadBytes":null`), []byte(`"textureUploadBytes":"unknown"`), 1)},
		{name: "malformed schema", data: bytes.Replace(base, []byte(`"schema":1`), []byte(`"schema":2`), 1)},
		{name: "missing unknown reason", data: bytes.Replace(base, []byte(`"unknownReasons":["unknown incrementally streamed data"]`), []byte(`"unknownReasons":[]`), 1)},
		{name: "known metric must remain unknown", data: bytes.Replace(base, []byte(`"textureUploadBytes":null`), []byte(`"textureUploadBytes":0`), 1)},
		{name: "explicit null telemetry is not legacy omission", data: testNicoTimelineReportWithTelemetry("NCT2", "separate", "separate", "", "null")},
		{name: "NCT2 atlas lacks fallback reason", data: testNicoTimelineReportWithTelemetry("NCT2", "atlas", "streamed-distinct-textures", "", timelineTestNCT2TelemetryJSON("unknown streamed data"))},
		{name: "NCT1 cannot use streamed layout", data: testNicoTimelineReportWithTelemetry("NCT1", "separate", "streamed-distinct-textures", "not valid for NCT1", timelineTestNCT2TelemetryJSON("unknown streamed data"))},
		{name: "NCT1 atlas cannot report fallback", data: testNicoTimelineReportWithTelemetry("NCT1", "atlas", "atlas", "forced fallback", timelineTestNCT1TelemetryJSON())},
		{name: "NCT1 atlas cannot fall back to separate", data: testNicoTimelineReportWithTelemetry("NCT1", "atlas", "separate", "atlas allocation unavailable", strings.Replace(timelineTestNCT1TelemetryJSON(), `"effectiveLayout":"atlas"`, `"effectiveLayout":"separate"`, 1))},
		{name: "NCT1 allocated texels must match page dimensions", data: testNicoTimelineReportWithTelemetry("NCT1", "atlas", "atlas", "", strings.Replace(timelineTestNCT1TelemetryJSON(), `"allocatedTexels":4`, `"allocatedTexels":3`, 1))},
		{name: "NCT1 used texels must match decoded source", data: testNicoTimelineReportWithTelemetry("NCT1", "atlas", "atlas", "", strings.Replace(timelineTestNCT1TelemetryJSON(), `"usedTexels":4`, `"usedTexels":3`, 1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime-report.json")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			report, err := readNicoTimelineReport(path)
			if err != nil {
				return // strict decoding rejected malformed payload as expected
			}
			if tc.name == "malformed schema" {
				err = validateNicoTimelineRuntimeReport(report, "auto", 2, "separate", 1, runtime.GOOS)
			} else {
				err = validateNicoTimelineRuntimeAssetLayout(report, "separate")
			}
			if tc.name == "NCT2 atlas lacks fallback reason" {
				err = validateNicoTimelineRuntimeAssetLayout(report, "atlas")
			} else if strings.HasPrefix(tc.name, "NCT1 atlas") || strings.HasPrefix(tc.name, "NCT1 allocated") || strings.HasPrefix(tc.name, "NCT1 used") {
				err = validateNicoTimelineRuntimeAssetLayout(report, "atlas")
			}
			if err == nil {
				t.Fatal("malformed telemetry or protocol/layout combination accepted")
			}
		})
	}
}

func TestNicoTimelineRuntimeReportRejectsDuplicateJSONKeys(t *testing.T) {
	topLevelDuplicate := bytes.Replace(
		testNicoTimelineRuntimeReport("auto", 1, 2),
		[]byte(`"protocol":"NCT2"`),
		[]byte(`"protocol":"NCT2","protocol":"NCT2"`),
		1,
	)
	nestedTelemetryDuplicate := testNicoTimelineReportWithTelemetry(
		"NCT1", "atlas", "atlas", "", strings.Replace(
			timelineTestNCT1TelemetryJSON(),
			`"effectiveLayout":"atlas"`,
			`"effectiveLayout":"atlas","effectiveLayout":"atlas"`,
			1,
		),
	)

	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{name: "top-level field", payload: topLevelDuplicate},
		{name: "nested telemetry field", payload: nestedTelemetryDuplicate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime-report.json")
			if err := os.WriteFile(path, tc.payload, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := readNicoTimelineReport(path)
			if err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
				t.Fatalf("duplicate JSON key accepted or rejected without duplicate-key diagnostic: %v", err)
			}
		})
	}
}

func testNicoTimelineReportWithTelemetry(protocol, requested, actual, fallback, telemetry string) []byte {
	base := strings.TrimSuffix(string(testNicoTimelineRuntimeReportForProtocol(protocol, "auto", 1, 2)), "}")
	fallbackJSON := ""
	assetBytes := 0
	if protocol == "NCT1" {
		assetBytes = 16
	}
	if fallback != "" {
		encoded, _ := json.Marshal(fallback)
		fallbackJSON = `,"assetLayoutFallbackReason":` + string(encoded)
	}
	return []byte(fmt.Sprintf(`%s,"requestedAssetLayout":%q,"assetLayout":%q%s,"assetPageCount":1,"assetSourceBytes":%d,"assetAllocatedBytes":%d,"assetTelemetry":%s}`, base, requested, actual, fallbackJSON, assetBytes, assetBytes, telemetry))
}

func timelineTestNCT2TelemetryJSON(reason string) string {
	encodedReason, _ := json.Marshal(reason)
	return fmt.Sprintf(`{"telemetryScope":"nct2-streamed-assets","effectiveLayout":"streamed-distinct-textures","pages":null,"textureUploadCallCount":null,"textureUploadBytes":null,"drawOrderPageBindRunCount":null,"bundleBuildCpuWallTimeNs":null,"residentMemory":{"decodedScenePixelBytes":null,"logicalTextureAllocationBytes":null,"rendererOwnedDrawBufferPayloadBytes":null,"renderBundleInternalBytes":null,"readbackRingBufferBytes":null,"wgpuTransientStagingBytes":null,"physicalVramBytes":null,"unknownReasons":[%s]},"unavailableReasons":[%s]}`, encodedReason, encodedReason)
}

func timelineTestNCT1TelemetryJSON() string {
	return `{"telemetryScope":"nct1-static-assets","effectiveLayout":"atlas","pages":[{"pageIndex":0,"width":2,"height":2,"usedTexels":4,"allocatedTexels":4}],"textureUploadCallCount":1,"textureUploadBytes":16,"drawOrderPageBindRunCount":1,"bundleBuildCpuWallTimeNs":2,"residentMemory":{"decodedScenePixelBytes":16,"logicalTextureAllocationBytes":16,"rendererOwnedDrawBufferPayloadBytes":64,"renderBundleInternalBytes":null,"readbackRingBufferBytes":4096,"wgpuTransientStagingBytes":null,"physicalVramBytes":null,"unknownReasons":["WGPU render-bundle internal storage is not exposed by the API","WGPU internal upload staging allocation is not exposed by the API","Physical VRAM residency is driver-managed and not exposed by WGPU"]},"unavailableReasons":[]}`
}

func TestNicoTimelineRejectsNCT1RuntimeReportForNCT2AndPreservesOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	helper := filepath.Join(dir, "nico-compositord.exe")
	for path, data := range map[string]string{source: "source", output: "previous-output", helper: "helper"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installFakeNicoTimelineRuntime(t, helper, output, 3)
	runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, nativeArgs []string, _ string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
		if _, err := produce(ctx, io.Discard); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(timelineOutputPath(encoderArgs), []byte("complete-looking-output"), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(timelineArgumentValue(nativeArgs, "--report"), testNicoTimelineRuntimeReportForProtocol("NCT1", "auto", int(expected), 1), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		progress(expected, expected)
		return nicoNativePipeResult{}, nil
	}
	_, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		TimelineCompositorPath: helper, TimelineReadbackSlots: 1, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if err == nil || !strings.Contains(err.Error(), "runtime report identity mismatch") {
		t.Fatalf("error=%v, want NCT1/NCT2 report mismatch", err)
	}
	assertNicoTimelineMilestonesJSON(t, NicoEncodeReport{}, nil)
	if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "previous-output" {
		t.Fatalf("NCT1 runtime report replaced existing output: %q err=%v", got, readErr)
	}
}

func TestNicoTimelineCaptureFailureStopsStreamAndPreservesOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	helper := filepath.Join(dir, "nico-compositord.exe")
	for path, data := range map[string]string{source: "source", output: "previous-output", helper: "helper"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installFakeNicoTimelineRuntime(t, helper, output, 3)
	oldCapture := captureNicoTimelineStreamForPipeline
	oldPipe := runNicoTimelinePipeForPipeline
	t.Cleanup(func() {
		captureNicoTimelineStreamForPipeline = oldCapture
		runNicoTimelinePipeForPipeline = oldPipe
	})
	captureNicoTimelineStreamForPipeline = func(context.Context, niconico.Snapshot, nicorender.RenderOptions, io.Writer) (nicorender.TimelineCaptureReport, error) {
		return nicorender.TimelineCaptureReport{}, errors.New("invalid snapshot fixture")
	}
	pipeCalls := 0
	runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, _ []string, _ string, _ []string, _ int64, _ func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
		pipeCalls++
		_, produceErr := produce(ctx, io.Discard)
		if produceErr == nil || !strings.Contains(produceErr.Error(), "invalid snapshot fixture") {
			return nicoNativePipeResult{}, errors.New("test producer did not fail as expected")
		}
		return nicoNativePipeResult{}, &nicoNativePipeError{PrimaryStage: "compositor", err: errors.New("pipe failed after capture error")}
	}
	_, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		TimelineCompositorPath: helper, TimelineReadbackSlots: 1, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if err == nil || !strings.Contains(err.Error(), "pipe failed after capture error") || strings.Contains(err.Error(), "NCT2 capture did not run") || pipeCalls != 1 {
		t.Fatalf("error=%v pipe calls=%d", err, pipeCalls)
	}
	if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "previous-output" {
		t.Fatalf("existing output changed after capture failure: %q err=%v", got, readErr)
	}
}

func TestNicoTimelinePipeFailureKeepsPartialEncodePrivate(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	helper := filepath.Join(dir, "nico-compositord.exe")
	for path, data := range map[string]string{source: "source", output: "previous-output", helper: "helper"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installFakeNicoTimelineRuntime(t, helper, output, 3)
	oldPipe := runNicoTimelinePipeForPipeline
	t.Cleanup(func() { runNicoTimelinePipeForPipeline = oldPipe })
	runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, _ []string, _ string, encoderArgs []string, _ int64, _ func(int64, int64), _ func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
		if err := os.WriteFile(timelineOutputPath(encoderArgs), []byte("partial-ffmpeg-output"), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := ctx.Err(); err != nil {
			return nicoNativePipeResult{}, err
		}
		return nicoNativePipeResult{}, errors.New("ffmpeg exited before NICO_DONE")
	}
	_, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		TimelineCompositorPath: helper, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if err == nil || !strings.Contains(err.Error(), "ffmpeg exited before NICO_DONE") {
		t.Fatalf("error=%v", err)
	}
	if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "previous-output" {
		t.Fatalf("partial MP4 escaped staging: %q err=%v", got, readErr)
	}
}

func TestNicoTimelineRejectsMissingEndAndTrailingDataBeforePublication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
		want   string
	}{
		{
			name: "missing End",
			mutate: func(stream []byte) []byte {
				return stream[:len(stream)-nicorender.TimelineStreamEnvelopeBytes-32]
			},
			want: "helper rejected NCT2 stream without End",
		},
		{
			name: "End followed by bytes instead of clean EOF",
			mutate: func(stream []byte) []byte {
				return append(stream, 0)
			},
			want: "helper timed out waiting for clean EOF after End",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.mp4")
			output := filepath.Join(dir, "out.mp4")
			helper := filepath.Join(dir, "nico-compositord.exe")
			for path, data := range map[string]string{source: "source", output: "previous-output", helper: "helper"} {
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			installFakeNicoTimelineRuntime(t, helper, output, 3)
			captureNicoTimelineStreamForPipeline = func(ctx context.Context, _ niconico.Snapshot, options nicorender.RenderOptions, out io.Writer) (nicorender.TimelineCaptureReport, error) {
				if err := ctx.Err(); err != nil {
					return nicorender.TimelineCaptureReport{}, err
				}
				stream, err := testNicoTimelineStreamBytes(options)
				if err != nil {
					return nicorender.TimelineCaptureReport{}, err
				}
				stream = tc.mutate(stream)
				if _, err := out.Write(stream); err != nil {
					return nicorender.TimelineCaptureReport{}, err
				}
				return nicorender.TimelineCaptureReport{RenderReport: nicorender.RenderReport{FrameCount: 3}, BundleSHA256: strings.Repeat("b", 64)}, nil
			}
			runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, _ []string, _ string, encoderArgs []string, _ int64, _ func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
				var wire bytes.Buffer
				if _, err := produce(ctx, &wire); err != nil {
					return nicoNativePipeResult{}, err
				}
				if err := os.WriteFile(timelineOutputPath(encoderArgs), []byte("partial-ffmpeg-output"), 0600); err != nil {
					return nicoNativePipeResult{}, err
				}
				if testNicoTimelineStreamHasCleanEnd(wire.Bytes()) {
					return nicoNativePipeResult{}, errors.New("fake helper accepted malformed NCT2 stream")
				}
				return nicoNativePipeResult{}, errors.New(tc.want)
			}
			_, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
				TimelineCompositorPath: helper, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
			}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want helper failure %q", err, tc.want)
			}
			if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "previous-output" {
				t.Fatalf("malformed NCT2 stream replaced existing output: %q err=%v", got, readErr)
			}
		})
	}
}

func TestNicoTimelineOutputInspectionFailurePreservesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	helper := filepath.Join(dir, "nico-compositord.exe")
	for path, data := range map[string]string{source: "source", output: "previous-output", helper: "helper"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installFakeNicoTimelineRuntime(t, helper, output, 3)
	runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, nativeArgs []string, _ string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
		if _, err := produce(ctx, io.Discard); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(timelineOutputPath(encoderArgs), nil, 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(timelineArgumentValue(nativeArgs, "--report"), testNicoTimelineRuntimeReport("auto", int(expected), 1), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		progress(expected, expected)
		return nicoNativePipeResult{}, nil
	}
	_, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		TimelineCompositorPath: helper, TimelineReadbackSlots: 1, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if err == nil || !strings.Contains(err.Error(), "staged MP4 is empty") {
		t.Fatalf("error=%v, want staged output inspection failure", err)
	}
	if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "previous-output" {
		t.Fatalf("uninspected output replaced existing file: %q err=%v", got, readErr)
	}
}

func TestNicoTimelineTeePromotesMP4AndHLSAfterValidation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	hls := filepath.Join(dir, "hls")
	helper := filepath.Join(dir, "nico-compositord.exe")
	for path, data := range map[string]string{source: "source", output: "previous-mp4", helper: "helper"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeNicoTimelineHLS(t, hls, "previous-segment")
	oldCapture := captureNicoTimelineStreamForPipeline
	oldPrepare := prepareNicoTimelineCompositorForPipeline
	oldPipe := runNicoTimelinePipeForPipeline
	oldProbe := probeNicoTimelineEncoderForPipeline
	t.Cleanup(func() {
		captureNicoTimelineStreamForPipeline = oldCapture
		prepareNicoTimelineCompositorForPipeline = oldPrepare
		runNicoTimelinePipeForPipeline = oldPipe
		probeNicoTimelineEncoderForPipeline = oldProbe
	})
	probeNicoTimelineEncoderForPipeline = func(context.Context, string, NicoEncodeOptions) error { return nil }
	captureNicoTimelineStreamForPipeline = testNicoTimelineStreamCapture(nicorender.TimelineCaptureReport{RenderReport: nicorender.RenderReport{FrameCount: 3}, BundleSHA256: strings.Repeat("b", 64)})
	prepareNicoTimelineCompositorForPipeline = func(context.Context, string, nicorender.TimelineRuntimeOptions) (string, func(), error) {
		return helper, func() {}, nil
	}
	runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, nativeArgs []string, _ string, _ []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), encoderDir string) (nicoNativePipeResult, error) {
		if encoderDir == "" {
			return nicoNativePipeResult{}, errors.New("tee encoder working directory was empty")
		}
		if _, err := produce(ctx, io.Discard); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(filepath.Join(encoderDir, "out.mp4"), []byte("completed-mp4"), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(filepath.Join(encoderDir, "segment-00000.ts"), []byte("completed-segment"), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(filepath.Join(encoderDir, "playlist.m3u8"), []byte("#EXTM3U\nsegment-00000.ts\n#EXT-X-ENDLIST\n"), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(timelineArgumentValue(nativeArgs, "--report"), testNicoTimelineRuntimeReport("auto", int(expected), 3), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		progress(expected, expected)
		return nicoNativePipeResult{}, nil
	}
	_, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		TimelineCompositorPath: helper, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, OutputMode: NicoOutputTee, HLSOutputDir: hls})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(output); err != nil || string(got) != "completed-mp4" {
		t.Fatalf("promoted MP4=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(hls, "segment-00000.ts")); err != nil || string(got) != "completed-segment" {
		t.Fatalf("promoted HLS segment=%q err=%v", got, err)
	}
	if _, err := validateNicoPlaylist(filepath.Join(hls, "playlist.m3u8"), hls); err != nil {
		t.Fatalf("promoted HLS playlist invalid: %v", err)
	}
}

func TestPromoteNicoTimelineArtifactsRollsBackBothOutputs(t *testing.T) {
	dir := t.TempDir()
	stagedMP4 := filepath.Join(dir, "stage.mp4")
	stagedHLS := filepath.Join(dir, "stage-hls")
	outputMP4 := filepath.Join(dir, "out.mp4")
	outputHLS := filepath.Join(dir, "hls")
	for path, data := range map[string]string{stagedMP4: "new-mp4", outputMP4: "old-mp4"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeNicoTimelineHLS(t, stagedHLS, "new-segment")
	writeNicoTimelineHLS(t, outputHLS, "old-segment")
	oldRename := renameNicoTimelineArtifact
	t.Cleanup(func() { renameNicoTimelineArtifact = oldRename })
	failPromotion := true
	renameNicoTimelineArtifact = func(oldPath, newPath string) error {
		if oldPath == stagedHLS && failPromotion {
			failPromotion = false
			return errors.New("simulated final HLS rename failure")
		}
		return os.Rename(oldPath, newPath)
	}
	if err := promoteNicoTimelineArtifacts(stagedMP4, stagedHLS, outputMP4, outputHLS); err == nil || !strings.Contains(err.Error(), "simulated final HLS rename failure") {
		t.Fatalf("promotion error=%v", err)
	}
	if got, err := os.ReadFile(outputMP4); err != nil || string(got) != "old-mp4" {
		t.Fatalf("old MP4 was not restored: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(outputHLS, "segment.ts")); err != nil || string(got) != "old-segment" {
		t.Fatalf("old HLS was not restored: %q err=%v", got, err)
	}
}

func testNicoTimelineScene(options nicorender.RenderOptions) nicorender.CommentTimeline {
	clock := niconico.MustFrameClock(options.FPSNum, options.FPSDen)
	var bundleHash [32]byte
	for i := range bundleHash {
		bundleHash[i] = 0xbb
	}
	return nicorender.CommentTimeline{Header: nicorender.TimelineHeader{
		Width: uint32(options.Width), Height: uint32(options.Height), FrameCount: uint32(clock.FrameCountForDurationMs(options.DurationMs)),
		FPSNum: uint32(options.FPSNum), FPSDen: uint32(options.FPSDen), BundleSHA256: bundleHash,
	}}
}

func timelineArgumentValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func timelineOutputPath(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

func containsNicoArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func testTimelinePlatformBackend() string {
	switch runtime.GOOS {
	case "windows":
		return "dx12"
	case "darwin":
		return "metal"
	default:
		return "vulkan"
	}
}

func testNicoTimelineRuntimeReport(requestedBackend string, completed, slots int) []byte {
	backend := testTimelinePlatformBackend()
	payload := fmt.Sprintf(`{"schema":1,"protocol":"NCT2","renderer":"wgpu","version":"0.1.0","requestedBackend":%q,"backend":%q,"adapterName":"test-gpu","adapterType":"DiscreteGpu","readbackSlots":%d,"maxTextureDimension2D":16384,"completedFrames":%d}`, requestedBackend, backend, slots, completed)
	return []byte(payload)
}

func testNicoTimelineRuntimeReportForProtocol(protocol, requestedBackend string, completed, slots int) []byte {
	return bytes.Replace(testNicoTimelineRuntimeReport(requestedBackend, completed, slots), []byte(`"protocol":"NCT2"`), []byte(fmt.Sprintf(`"protocol":%q`, protocol)), 1)
}

func testNicoTimelineRuntimeReportWithAssets(requestedBackend string, completed uint32, slots, pages int) []byte {
	base := strings.TrimSuffix(string(testNicoTimelineRuntimeReport(requestedBackend, int(completed), slots)), "}")
	return []byte(fmt.Sprintf(`%s,"requestedAssetLayout":"separate","assetLayout":"separate","assetPageCount":%d,"assetSourceBytes":%d,"assetAllocatedBytes":%d}`, base, pages, pages, pages))
}

func durationOffset(value time.Duration) *time.Duration { return &value }

func assertNicoTimelineMilestonesJSON(t *testing.T, report NicoEncodeReport, want map[string]any) {
	t.Helper()
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	keys := []string{"timeline_protocol", "timeline_capture_done_ns", "timeline_first_asset_ready_ns", "timeline_first_frame_ns", "timeline_stream_end_ns", "timeline_helper_done_ns", "timeline_ffmpeg_done_ns"}
	for _, key := range keys {
		value, ok := got[key]
		wantValue, wantOK := want[key]
		if ok != wantOK || ok && value != wantValue {
			t.Errorf("%s = %v (present=%v), want %v (present=%v); report=%s", key, value, ok, wantValue, wantOK, payload)
		}
	}
}

func setNicoTimelineMilestonesReflect(t *testing.T, report *NicoEncodeReport, protocol string, captureDone, assetReady, firstFrame, streamEnd, helperDone, ffmpegDone *time.Duration) {
	t.Helper()
	value := reflect.ValueOf(report).Elem()
	protocolField := value.FieldByName("TimelineProtocol")
	if !protocolField.IsValid() || !protocolField.CanSet() || protocolField.Kind() != reflect.String {
		t.Fatalf("NicoEncodeReport lacks settable TimelineProtocol field")
	}
	protocolField.SetString(protocol)
	for name, offset := range map[string]*time.Duration{
		"TimelineCaptureDone": captureDone, "TimelineFirstAssetReady": assetReady, "TimelineFirstFrame": firstFrame,
		"TimelineStreamEnd": streamEnd, "TimelineHelperDone": helperDone, "TimelineFFmpegDone": ffmpegDone,
	} {
		field := value.FieldByName(name)
		if !field.IsValid() || !field.CanSet() || field.Type() != reflect.TypeOf((*time.Duration)(nil)) {
			t.Fatalf("NicoEncodeReport lacks settable %s field", name)
		}
		if offset != nil {
			field.Set(reflect.ValueOf(offset))
		}
	}
}

func assertNicoTimelineAttemptMilestonesJSON(t *testing.T, report NicoEncodeReport, want map[string]any) {
	t.Helper()
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Attempts []map[string]any `json:"renderer_attempts"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Attempts) != 1 {
		t.Fatalf("attempt JSON=%s", payload)
	}
	keys := []string{"timeline_protocol", "timeline_capture_done_ns", "timeline_first_asset_ready_ns", "timeline_first_frame_ns", "timeline_stream_end_ns", "timeline_helper_done_ns", "timeline_ffmpeg_done_ns"}
	for _, key := range keys {
		got, ok := decoded.Attempts[0][key]
		wantValue, wantOK := want[key]
		if ok != wantOK || ok && got != wantValue {
			t.Errorf("attempt.%s = %v (present=%v), want %v (present=%v); report=%s", key, got, ok, wantValue, wantOK, payload)
		}
	}
}

func testNicoTimelineStreamCapture(report nicorender.TimelineCaptureReport) func(context.Context, niconico.Snapshot, nicorender.RenderOptions, io.Writer) (nicorender.TimelineCaptureReport, error) {
	return func(ctx context.Context, _ niconico.Snapshot, options nicorender.RenderOptions, out io.Writer) (nicorender.TimelineCaptureReport, error) {
		if err := ctx.Err(); err != nil {
			return nicorender.TimelineCaptureReport{}, err
		}
		if err := writeTestNicoTimelineStream(out, options); err != nil {
			return nicorender.TimelineCaptureReport{}, err
		}
		return report, nil
	}
}

func writeTestNicoTimelineStream(out io.Writer, options nicorender.RenderOptions) error {
	stream, err := testNicoTimelineStreamBytes(options)
	if err != nil {
		return err
	}
	_, err = out.Write(stream)
	return err
}

func testNicoTimelineStreamBytes(options nicorender.RenderOptions) ([]byte, error) {
	clock := niconico.MustFrameClock(options.FPSNum, options.FPSDen)
	header := nicorender.TimelineHeader{
		Width: uint32(options.Width), Height: uint32(options.Height), FrameCount: uint32(clock.FrameCountForDurationMs(options.DurationMs)),
		FPSNum: uint32(options.FPSNum), FPSDen: uint32(options.FPSDen), BundleSHA256: sha256.Sum256([]byte("test-nct2-bundle")),
	}
	var output bytes.Buffer
	if err := nicorender.WriteCommentTimelineStream(&output, nicorender.CommentTimelineStream{Header: header}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func testNicoTimelineStreamHasCleanEnd(stream []byte) bool {
	var sequence uint64
	for offset := 0; offset < len(stream); {
		if len(stream)-offset < nicorender.TimelineStreamEnvelopeBytes {
			return false
		}
		header := stream[offset : offset+nicorender.TimelineStreamEnvelopeBytes]
		kind := binary.LittleEndian.Uint32(header)
		payloadBytes := binary.LittleEndian.Uint32(header[4:])
		if binary.LittleEndian.Uint64(header[8:]) != sequence {
			return false
		}
		sequence++
		next := offset + nicorender.TimelineStreamEnvelopeBytes + int(payloadBytes)
		if next > len(stream) {
			return false
		}
		if offset == 0 && (kind != nicorender.TimelineStreamRecordHeader || payloadBytes != nicorender.TimelineStreamHeaderBytes || !bytes.Equal(stream[offset+nicorender.TimelineStreamEnvelopeBytes:offset+nicorender.TimelineStreamEnvelopeBytes+4], []byte("NCT2"))) {
			return false
		}
		if kind == nicorender.TimelineStreamRecordEnd {
			return payloadBytes == 64 && next == len(stream)
		}
		offset = next
	}
	return false
}

func writeNicoTimelineHLS(t *testing.T, dir, segment string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "segment.ts"), []byte(segment), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "playlist.m3u8"), []byte("#EXTM3U\nsegment.ts\n#EXT-X-ENDLIST\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func installFakeNicoTimelineRuntime(t *testing.T, helper, finalOutput string, completedFrames int) {
	t.Helper()
	oldCapture := captureNicoTimelineStreamForPipeline
	oldPrepare := prepareNicoTimelineCompositorForPipeline
	oldPipe := runNicoTimelinePipeForPipeline
	oldProbe := probeNicoTimelineEncoderForPipeline
	t.Cleanup(func() {
		captureNicoTimelineStreamForPipeline = oldCapture
		prepareNicoTimelineCompositorForPipeline = oldPrepare
		runNicoTimelinePipeForPipeline = oldPipe
		probeNicoTimelineEncoderForPipeline = oldProbe
	})
	probeNicoTimelineEncoderForPipeline = func(context.Context, string, NicoEncodeOptions) error { return nil }
	captureNicoTimelineStreamForPipeline = testNicoTimelineStreamCapture(nicorender.TimelineCaptureReport{RenderReport: nicorender.RenderReport{FrameCount: 3}, BundleSHA256: strings.Repeat("b", 64)})
	prepareNicoTimelineCompositorForPipeline = func(context.Context, string, nicorender.TimelineRuntimeOptions) (string, func(), error) {
		return helper, func() {}, nil
	}
	runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, nativeArgs []string, _ string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
		if _, err := produce(ctx, io.Discard); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(timelineOutputPath(encoderArgs), []byte("partial"), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		if err := os.WriteFile(timelineArgumentValue(nativeArgs, "--report"), testNicoTimelineRuntimeReport("auto", completedFrames, 1), 0600); err != nil {
			return nicoNativePipeResult{}, err
		}
		progress(expected, expected)
		return nicoNativePipeResult{}, nil
	}
}

func TestNicoAutoDoesNotUseTimelineByDefault(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTimeline := encodeNicoTimelineForPipeline
	oldPrepare := prepareNicoNativeCompositor
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		encodeNicoTimelineForPipeline = oldTimeline
		prepareNicoNativeCompositor = oldPrepare
		encodeNicoBrowserForPipeline = oldBrowser
	})
	timelineCalls := 0
	encodeNicoTimelineForPipeline = func(context.Context, string, string, string, niconico.Snapshot, nicorender.RenderOptions, NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		timelineCalls++
		return NicoEncodeReport{}, nicorender.RenderReport{}, errors.New("timeline should not be called")
	}
	prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
		return "", nil, errors.New("native unavailable")
	}
	encodeNicoBrowserForPipeline = func(_ context.Context, _, _, output string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		if err := os.WriteFile(output, []byte("browser"), 0600); err != nil {
			return NicoEncodeReport{}, nicorender.RenderReport{}, err
		}
		return NicoEncodeReport{OutputPath: output}, nicorender.RenderReport{}, nil
	}

	_, renderReport, err := EncodeNicoCommentedWithRenderer(context.Background(), "ffmpeg", source, output, niconico.Snapshot{},
		nicorender.RenderOptions{Backend: "auto", Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1},
		NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, Encoder: "x264", Preset: "fast", EncoderThreads: 3})
	if err != nil {
		t.Fatal(err)
	}
	if timelineCalls != 0 || renderReport.Backend != "browser" {
		t.Fatalf("timeline calls=%d report=%+v", timelineCalls, renderReport)
	}
	if got, err := os.ReadFile(output); err != nil || string(got) != "browser" {
		t.Fatalf("legacy output=%q err=%v", got, err)
	}
}

func TestNicoAutoTimelineFailureFallsBackOnceAndRetainsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTimeline := encodeNicoTimelineForPipeline
	oldPrepare := prepareNicoNativeCompositor
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		encodeNicoTimelineForPipeline = oldTimeline
		prepareNicoNativeCompositor = oldPrepare
		encodeNicoBrowserForPipeline = oldBrowser
	})
	timelineCalls, browserCalls := 0, 0
	encodeNicoTimelineForPipeline = func(_ context.Context, _, _, _ string, _ niconico.Snapshot, _ nicorender.RenderOptions, enc NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		timelineCalls++
		if enc.Encoder != "x264" || enc.Preset != "fast" || enc.EncoderThreads != 3 {
			t.Fatalf("timeline attempt changed encoder options: %+v", enc)
		}
		report := NicoEncodeReport{
			TimelineHelperSHA256: "helper-hash", TimelineBundleSHA256: "bundle-hash", TimelineGPUBackend: "vulkan", TimelineGPUAdapter: "GPU", TimelineReadbackSlots: 2,
			StageTimings: []NicoStageTiming{{Name: "timeline_capture"}},
		}
		setNicoTimelineMilestonesReflect(t, &report, "NCT2", durationOffset(0), durationOffset(2), nil, durationOffset(6), nil, nil)
		return report, nicorender.RenderReport{}, errors.New("simulated device lost")
	}
	prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
		return "", nil, errors.New("native unavailable")
	}
	encodeNicoBrowserForPipeline = func(_ context.Context, _, _, output string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		browserCalls++
		if err := os.WriteFile(output, []byte("browser-complete"), 0600); err != nil {
			return NicoEncodeReport{}, nicorender.RenderReport{}, err
		}
		return NicoEncodeReport{OutputPath: output}, nicorender.RenderReport{}, nil
	}

	encoded, renderReport, err := EncodeNicoCommentedWithRenderer(context.Background(), "ffmpeg", source, output, niconico.Snapshot{},
		nicorender.RenderOptions{Backend: "auto", TimelineEnabled: true, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1},
		NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, Encoder: "x264", Preset: "fast", EncoderThreads: 3})
	if err != nil {
		t.Fatal(err)
	}
	if timelineCalls != 1 || browserCalls != 1 {
		t.Fatalf("timeline calls=%d browser calls=%d", timelineCalls, browserCalls)
	}
	if renderReport.Backend != "browser" || !strings.Contains(renderReport.FallbackReason, "simulated device lost") {
		t.Fatalf("fallback report=%+v", renderReport)
	}
	if len(encoded.Attempts) != 1 || encoded.Attempts[0].HelperSHA256 != "helper-hash" || encoded.Attempts[0].BundleSHA256 != "bundle-hash" || encoded.Attempts[0].GPUBackend != "vulkan" || len(encoded.Attempts[0].StageTimings) != 1 {
		t.Fatalf("timeline attempt diagnostics=%+v", encoded.Attempts)
	}
	assertNicoTimelineAttemptMilestonesJSON(t, encoded, map[string]any{
		"timeline_protocol": "NCT2", "timeline_capture_done_ns": float64(0), "timeline_first_asset_ready_ns": float64(2), "timeline_stream_end_ns": float64(6),
	})
	if got, err := os.ReadFile(output); err != nil || string(got) != "browser-complete" {
		t.Fatalf("output=%q err=%v", got, err)
	}
}

func TestNicoLateNCT2FailureCPUFallbackRetainsValidatedMetadata(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "source.mp4"), filepath.Join(dir, "out.mp4")
	helper := filepath.Join(dir, "nico-compositord.exe")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("helper"), 0600); err != nil {
		t.Fatal(err)
	}
	installFakeNicoTimelineRuntime(t, helper, output, 3)
	oldTimeline, oldPrepare, oldBrowser := encodeNicoTimelineForPipeline, prepareNicoNativeCompositor, encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		encodeNicoTimelineForPipeline, prepareNicoNativeCompositor, encodeNicoBrowserForPipeline = oldTimeline, oldPrepare, oldBrowser
	})
	encodeNicoTimelineForPipeline = encodeNicoTimeline
	prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
		return "", nil, errors.New("native CPU compositor unavailable")
	}
	encodeNicoBrowserForPipeline = func(_ context.Context, _, _, path string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		if err := os.WriteFile(path, []byte("cpu-complete"), 0600); err != nil {
			return NicoEncodeReport{}, nicorender.RenderReport{}, err
		}
		return NicoEncodeReport{OutputPath: path}, nicorender.RenderReport{}, nil
	}
	basePipe := runNicoTimelinePipeForPipeline
	runNicoTimelinePipeForPipeline = func(ctx context.Context, native string, nativeArgs []string, encoder string, encoderArgs []string, frames int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), workDir string) (nicoNativePipeResult, error) {
		result, err := basePipe(ctx, native, nativeArgs, encoder, encoderArgs, frames, progress, produce, workDir)
		if err != nil {
			return result, err
		}
		if err := os.WriteFile(timelineArgumentValue(nativeArgs, "--report"), testNicoTimelineRuntimeReportWithAssets("auto", uint32(frames), 1, 1), 0600); err != nil {
			return result, err
		}
		result.milestones = nicoNativeMilestones{
			CaptureDone: durationOffset(0), FirstAssetReady: durationOffset(2), FirstFrame: durationOffset(4),
			StreamEnd: durationOffset(6), HelperDone: durationOffset(8),
		}
		return result, &nicoNativePipeError{PrimaryStage: "compositor", err: errors.New("late NCT2 compositor failure")}
	}
	encoded, rendered, err := EncodeNicoCommentedWithRenderer(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		Backend: "timeline", TimelineEnabled: true, TimelineCompositorPath: helper, TimelineReadbackSlots: 1, TimelineGPUBackend: "auto",
		Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, Encoder: "x264", Preset: "fast", EncoderThreads: 3})
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Backend != "browser" || len(encoded.Attempts) != 1 {
		t.Fatalf("fallback result=%+v render=%+v", encoded, rendered)
	}
	assertNicoTimelineAttemptMilestonesJSON(t, encoded, map[string]any{
		"timeline_protocol": "NCT2", "timeline_capture_done_ns": float64(0), "timeline_first_asset_ready_ns": float64(2),
		"timeline_first_frame_ns": float64(4), "timeline_stream_end_ns": float64(6), "timeline_helper_done_ns": float64(8),
	})
	if got, err := os.ReadFile(output); err != nil || string(got) != "cpu-complete" {
		t.Fatalf("CPU output=%q err=%v", got, err)
	}
}

func TestNicoRequiredTimelineFailureFallsBackToCPU(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTimeline := encodeNicoTimelineForPipeline
	oldPrepare := prepareNicoNativeCompositor
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		encodeNicoTimelineForPipeline = oldTimeline
		prepareNicoNativeCompositor = oldPrepare
		encodeNicoBrowserForPipeline = oldBrowser
	})
	timelineCalls, legacyCalls := 0, 0
	fallbackNotified, fallbackBeforeCPU := false, false
	encodeNicoTimelineForPipeline = func(context.Context, string, string, string, niconico.Snapshot, nicorender.RenderOptions, NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		timelineCalls++
		return NicoEncodeReport{}, nicorender.RenderReport{}, fmt.Errorf("unsupported fixture: %w", nicorender.ErrTimelineUnsupported)
	}
	prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
		legacyCalls++
		return "", nil, errors.New("native CPU compositor unavailable")
	}
	encodeNicoBrowserForPipeline = func(_ context.Context, _, _, fallbackOutput string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		legacyCalls++
		if !fallbackNotified {
			t.Fatal("CPU renderer started before the WGPU fallback notification")
		}
		fallbackBeforeCPU = true
		if err := os.WriteFile(fallbackOutput, []byte("cpu-browser-complete"), 0600); err != nil {
			return NicoEncodeReport{}, nicorender.RenderReport{}, err
		}
		return NicoEncodeReport{OutputPath: fallbackOutput}, nicorender.RenderReport{}, nil
	}

	encoded, rendered, err := EncodeNicoCommentedWithRenderer(context.Background(), "ffmpeg", source, output, niconico.Snapshot{},
		nicorender.RenderOptions{Backend: "timeline", Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, OnTimelineFallback: func() {
			if fallbackNotified {
				t.Fatal("WGPU fallback was announced more than once")
			}
			fallbackNotified = true
		}},
		NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if err != nil {
		t.Fatal(err)
	}
	if timelineCalls != 1 || legacyCalls != 2 {
		t.Fatalf("timeline calls=%d legacy calls=%d", timelineCalls, legacyCalls)
	}
	if !fallbackNotified || !fallbackBeforeCPU {
		t.Fatalf("fallback notified=%t beforeCPU=%t", fallbackNotified, fallbackBeforeCPU)
	}
	if rendered.Backend != "browser" || !strings.Contains(rendered.FallbackReason, "unsupported fixture") {
		t.Fatalf("CPU fallback report=%+v", rendered)
	}
	if len(encoded.Attempts) != 1 || !strings.Contains(encoded.Attempts[0].Error, "unsupported fixture") {
		t.Fatalf("timeline diagnostics=%+v", encoded.Attempts)
	}
	if got, err := os.ReadFile(output); err != nil || string(got) != "cpu-browser-complete" {
		t.Fatalf("CPU fallback output=%q err=%v", got, err)
	}
}

func TestNicoAutoUnsupportedTimelineFallsBackWithOriginalComments(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := niconico.Snapshot{
		VideoID: "sm9",
		Threads: []niconico.Thread{{Fork: "main", Comments: []niconico.Comment{{
			ID: "original-comment", Body: "元コメントを保持",
		}}}},
	}
	oldTimeline := encodeNicoTimelineForPipeline
	oldPrepare := prepareNicoNativeCompositor
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		encodeNicoTimelineForPipeline = oldTimeline
		prepareNicoNativeCompositor = oldPrepare
		encodeNicoBrowserForPipeline = oldBrowser
	})
	encodeNicoTimelineForPipeline = func(context.Context, string, string, string, niconico.Snapshot, nicorender.RenderOptions, NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		return NicoEncodeReport{}, nicorender.RenderReport{}, fmt.Errorf("capture unsupported: %w", nicorender.ErrTimelineUnsupported)
	}
	prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
		return "", nil, errors.New("native compositor unavailable")
	}
	browserCalls := 0
	encodeNicoBrowserForPipeline = func(_ context.Context, _, _, fallbackOutput string, got niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		browserCalls++
		if got.VideoID != snapshot.VideoID || len(got.Threads) != 1 || len(got.Threads[0].Comments) != 1 || got.Threads[0].Comments[0].ID != "original-comment" || got.Threads[0].Comments[0].Body != "元コメントを保持" {
			t.Fatalf("legacy fallback did not receive original comments: %+v", got)
		}
		if err := os.WriteFile(fallbackOutput, []byte("legacy-commented-output"), 0600); err != nil {
			return NicoEncodeReport{}, nicorender.RenderReport{}, err
		}
		return NicoEncodeReport{OutputPath: fallbackOutput}, nicorender.RenderReport{}, nil
	}
	encoded, rendered, err := EncodeNicoCommentedWithRenderer(context.Background(), "ffmpeg", source, output, snapshot,
		nicorender.RenderOptions{Backend: "auto", TimelineEnabled: true, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1},
		NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, Encoder: "x264"})
	if err != nil {
		t.Fatal(err)
	}
	if browserCalls != 1 || rendered.Backend != "browser" || !strings.Contains(rendered.FallbackReason, "capture unsupported") {
		t.Fatalf("browser calls=%d render report=%+v", browserCalls, rendered)
	}
	if len(encoded.Attempts) != 1 || !strings.Contains(encoded.Attempts[0].Error, "unsupported") {
		t.Fatalf("timeline attempt diagnostics=%+v", encoded.Attempts)
	}
	if got, err := os.ReadFile(output); err != nil || string(got) != "legacy-commented-output" {
		t.Fatalf("legacy output=%q err=%v", got, err)
	}
}

func TestNicoAutoCancellationDoesNotFallback(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTimeline := encodeNicoTimelineForPipeline
	oldPrepare := prepareNicoNativeCompositor
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		encodeNicoTimelineForPipeline = oldTimeline
		prepareNicoNativeCompositor = oldPrepare
		encodeNicoBrowserForPipeline = oldBrowser
	})
	ctx, cancel := context.WithCancel(context.Background())
	timelineCalls, legacyCalls := 0, 0
	fallbackNotified := false
	encodeNicoTimelineForPipeline = func(ctx context.Context, _, _, _ string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		timelineCalls++
		cancel()
		return NicoEncodeReport{}, nicorender.RenderReport{}, ctx.Err()
	}
	prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
		legacyCalls++
		return "", nil, errors.New("must not prepare legacy compositor")
	}
	encodeNicoBrowserForPipeline = func(context.Context, string, string, string, niconico.Snapshot, nicorender.RenderOptions, NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		legacyCalls++
		return NicoEncodeReport{}, nicorender.RenderReport{}, nil
	}
	_, _, err := EncodeNicoCommentedWithRenderer(ctx, "ffmpeg", source, filepath.Join(dir, "out.mp4"), niconico.Snapshot{},
		nicorender.RenderOptions{Backend: "auto", TimelineEnabled: true, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, OnTimelineFallback: func() { fallbackNotified = true }},
		NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if !errors.Is(err, context.Canceled) || timelineCalls != 1 || legacyCalls != 0 || fallbackNotified {
		t.Fatalf("error=%v timeline calls=%d legacy calls=%d fallback notified=%t", err, timelineCalls, legacyCalls, fallbackNotified)
	}
}

func TestNicoTimelineDelayedFailureFallsBackOnceAndProtectsPublishedOutput(t *testing.T) {
	tests := []struct {
		name         string
		cancel       bool
		cpuFails     bool
		wantFallback bool
		wantErr      bool
	}{
		{name: "late helper failure publishes completed CPU output", wantFallback: true},
		{name: "late helper and CPU failures preserve prior output", cpuFails: true, wantFallback: true, wantErr: true},
		{name: "cancel during delayed helper attempt does not fallback", cancel: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.mp4")
			output := filepath.Join(dir, "out.mp4")
			helper := filepath.Join(dir, "nico-compositord.exe")
			for path, data := range map[string]string{source: "source", output: "previous-completed-output", helper: "helper"} {
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}

			oldCapture := captureNicoTimelineStreamForPipeline
			oldPrepareTimeline := prepareNicoTimelineCompositorForPipeline
			oldProbe := probeNicoTimelineEncoderForPipeline
			oldPipe := runNicoTimelinePipeForPipeline
			oldPrepareNative := prepareNicoNativeCompositor
			oldBrowser := encodeNicoBrowserForPipeline
			t.Cleanup(func() {
				captureNicoTimelineStreamForPipeline = oldCapture
				prepareNicoTimelineCompositorForPipeline = oldPrepareTimeline
				probeNicoTimelineEncoderForPipeline = oldProbe
				runNicoTimelinePipeForPipeline = oldPipe
				prepareNicoNativeCompositor = oldPrepareNative
				encodeNicoBrowserForPipeline = oldBrowser
			})

			captureCalls := 0
			captureNicoTimelineStreamForPipeline = func(ctx context.Context, _ niconico.Snapshot, options nicorender.RenderOptions, out io.Writer) (nicorender.TimelineCaptureReport, error) {
				captureCalls++
				if err := writeTestNicoTimelineStream(out, options); err != nil {
					return nicorender.TimelineCaptureReport{}, err
				}
				if err := ctx.Err(); err != nil {
					return nicorender.TimelineCaptureReport{}, err
				}
				return nicorender.TimelineCaptureReport{RenderReport: nicorender.RenderReport{FrameCount: 3}, BundleSHA256: strings.Repeat("c", 64)}, nil
			}
			prepareNicoTimelineCompositorForPipeline = func(context.Context, string, nicorender.TimelineRuntimeOptions) (string, func(), error) {
				return helper, func() {}, nil
			}
			probeNicoTimelineEncoderForPipeline = func(context.Context, string, NicoEncodeOptions) error { return nil }

			attemptProgress := make(chan struct{}, 1)
			finishAttempt := make(chan struct{})
			runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, args []string, _ string, encoderArgs []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
				if len(args) == 0 || args[0] != "--stdin-stream" {
					return nicoNativePipeResult{}, fmt.Errorf("timeline helper args=%v", args)
				}
				var streamed bytes.Buffer
				if _, err := produce(ctx, &streamed); err != nil {
					return nicoNativePipeResult{}, err
				}
				if !testNicoTimelineStreamHasCleanEnd(streamed.Bytes()) {
					return nicoNativePipeResult{}, errors.New("timeline producer did not stream a complete NCT2 scene")
				}
				stagedOutput := timelineOutputPath(encoderArgs)
				if err := os.WriteFile(stagedOutput, []byte("partial-timeline-output"), 0600); err != nil {
					return nicoNativePipeResult{}, err
				}
				progress(expected/2, expected)
				attemptProgress <- struct{}{}
				select {
				case <-ctx.Done():
					return nicoNativePipeResult{}, ctx.Err()
				case <-finishAttempt:
					return nicoNativePipeResult{}, errors.New("late helper failure after NCT2 progress")
				}
			}

			prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
				return "", nil, errors.New("native CPU compositor unavailable")
			}
			snapshot := niconico.Snapshot{
				VideoID: "sm9",
				Threads: []niconico.Thread{{Fork: "main", Comments: []niconico.Comment{{ID: "original", Body: "keep this snapshot"}}}},
			}
			fallbackNotices, browserCalls := 0, 0
			encodeNicoBrowserForPipeline = func(ctx context.Context, _, _, stagedPath string, got niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
				browserCalls++
				if !reflect.DeepEqual(got, snapshot) {
					t.Errorf("CPU fallback snapshot changed: got=%+v want=%+v", got, snapshot)
				}
				if fallbackNotices != 1 {
					t.Errorf("CPU fallback started after %d notifications, want exactly one", fallbackNotices)
				}
				if stagedPath == output {
					t.Errorf("CPU fallback wrote directly to published output %q", output)
				}
				if err := os.WriteFile(stagedPath, []byte("partial-cpu-output"), 0600); err != nil {
					return NicoEncodeReport{}, nicorender.RenderReport{}, err
				}
				if got, err := os.ReadFile(output); err != nil || string(got) != "previous-completed-output" {
					return NicoEncodeReport{}, nicorender.RenderReport{}, fmt.Errorf("partial CPU output was published: got=%q err=%v", got, err)
				}
				if tc.cpuFails {
					return NicoEncodeReport{}, nicorender.RenderReport{}, errors.New("CPU fallback failed late")
				}
				if err := os.WriteFile(stagedPath, []byte("completed-cpu-output"), 0600); err != nil {
					return NicoEncodeReport{}, nicorender.RenderReport{}, err
				}
				return NicoEncodeReport{OutputPath: stagedPath}, nicorender.RenderReport{}, nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := nicorender.RenderOptions{
				Backend: "timeline", Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
				OnTimelineFallback: func() { fallbackNotices++ },
			}
			encodeOptions := NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, Encoder: "x264", Preset: "fast", EncoderThreads: 3}
			done := make(chan struct {
				encoded  NicoEncodeReport
				rendered nicorender.RenderReport
				err      error
			}, 1)
			go func() {
				encoded, rendered, err := EncodeNicoCommentedWithRenderer(ctx, "ffmpeg", source, output, snapshot, options, encodeOptions)
				done <- struct {
					encoded  NicoEncodeReport
					rendered nicorender.RenderReport
					err      error
				}{encoded: encoded, rendered: rendered, err: err}
			}()

			select {
			case <-attemptProgress:
			case <-time.After(5 * time.Second):
				t.Fatal("timeline attempt did not reach delayed post-progress failure point")
			}
			if got, err := os.ReadFile(output); err != nil || string(got) != "previous-completed-output" {
				t.Fatalf("published output changed before timeline attempt completed: got=%q err=%v", got, err)
			}
			if tc.cancel {
				cancel()
			} else {
				close(finishAttempt)
			}

			var result struct {
				encoded  NicoEncodeReport
				rendered nicorender.RenderReport
				err      error
			}
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timeline fallback owner did not return")
			}
			if captureCalls != 1 {
				t.Fatalf("NCT2 capture calls=%d, want exactly one", captureCalls)
			}
			if tc.cancel {
				if !errors.Is(result.err, context.Canceled) || fallbackNotices != 0 || browserCalls != 0 {
					t.Fatalf("cancel result err=%v notices=%d CPU calls=%d", result.err, fallbackNotices, browserCalls)
				}
			} else {
				if fallbackNotices != 1 || browserCalls != 1 {
					t.Fatalf("delayed failure notices=%d CPU calls=%d, want one each", fallbackNotices, browserCalls)
				}
				if tc.wantErr != (result.err != nil) {
					t.Fatalf("result error=%v, wantErr=%t", result.err, tc.wantErr)
				}
			}
			if tc.wantFallback && !tc.cancel && !tc.cpuFails {
				if result.rendered.Backend != "browser" || !strings.Contains(result.rendered.FallbackReason, "late helper failure") {
					t.Fatalf("fallback report=%+v", result.rendered)
				}
				if got, err := os.ReadFile(output); err != nil || string(got) != "completed-cpu-output" {
					t.Fatalf("published CPU output=%q err=%v", got, err)
				}
			} else if got, err := os.ReadFile(output); err != nil || string(got) != "previous-completed-output" {
				t.Fatalf("failed/cancelled attempt changed published output: got=%q err=%v", got, err)
			}
		})
	}
}

func TestNicoTimelineNoProgressWatchdogCancelsAndPreservesOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	helper := filepath.Join(dir, "nico-compositord.exe")
	for path, data := range map[string]string{source: "source", output: "previous-output", helper: "helper"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	installFakeNicoTimelineRuntime(t, helper, output, 3)
	oldPipe := runNicoTimelinePipeForPipeline
	oldTimeout, oldInterval := nicoTimelineStallTimeout, nicoTimelineWatchInterval
	t.Cleanup(func() {
		runNicoTimelinePipeForPipeline = oldPipe
		nicoTimelineStallTimeout, nicoTimelineWatchInterval = oldTimeout, oldInterval
	})
	nicoTimelineStallTimeout = 20 * time.Millisecond
	nicoTimelineWatchInterval = 2 * time.Millisecond
	runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, _ []string, _ string, _ []string, _ int64, _ func(int64, int64), _ func(context.Context, io.Writer) (nicorender.RenderReport, error), _ string) (nicoNativePipeResult, error) {
		<-ctx.Done()
		return nicoNativePipeResult{}, ctx.Err()
	}
	_, _, err := encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		TimelineCompositorPath: helper, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25})
	if !errors.Is(err, ErrTimelineStalled) {
		t.Fatalf("error=%v, want ErrTimelineStalled", err)
	}
	if got, readErr := os.ReadFile(output); readErr != nil || string(got) != "previous-output" {
		t.Fatalf("existing output changed after watchdog: %q err=%v", got, readErr)
	}
}

func TestNicoTimelineRealProcessWatchdogReapsChildrenAndPreservesArtifacts(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	helper := filepath.Join(dir, "nico-compositord.exe")
	hls := filepath.Join(dir, "hls")
	for path, data := range map[string]string{source: "source", output: "previous-mp4", helper: "helper"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeNicoTimelineHLS(t, hls, "previous-segment")
	if nicoTimelineStallTimeout != 30*time.Second || nicoTimelineWatchInterval != 250*time.Millisecond {
		t.Fatalf("watchdog settings are not production values: timeout=%s interval=%s", nicoTimelineStallTimeout, nicoTimelineWatchInterval)
	}
	installFakeNicoTimelineRuntime(t, helper, output, 3)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childArgs := func(role string) []string {
		return []string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-" + role}
	}
	runNicoTimelinePipeForPipeline = func(ctx context.Context, _ string, _ []string, _ string, _ []string, expected int64, progress func(int64, int64), produce func(context.Context, io.Writer) (nicorender.RenderReport, error), workDir string) (nicoNativePipeResult, error) {
		return runNicoNativePipeTimedInDir(ctx, exe, childArgs("blocked"), exe, childArgs("encoder"), expected, progress, produce, workDir)
	}
	beforeMP4 := hashNicoTestArtifact(t, output)
	beforeHLS := hashNicoTestArtifact(t, hls)
	started := time.Now()
	_, _, err = encodeNicoTimeline(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		TimelineCompositorPath: helper, Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 25, OutputMode: NicoOutputTee, HLSOutputDir: hls})
	elapsed := time.Since(started)
	if !errors.Is(err, ErrTimelineStalled) {
		t.Fatalf("error=%v, want ErrTimelineStalled after hung helper", err)
	}
	if elapsed < nicoTimelineStallTimeout-nicoTimelineWatchInterval || elapsed > nicoTimelineStallTimeout+nicoTimelineWatchInterval+5*time.Second {
		t.Fatalf("real watchdog elapsed %s; want about %s plus one poll and <=5s reap", elapsed, nicoTimelineStallTimeout)
	}
	assertNicoTestArtifactHashes(t, output, beforeMP4)
	assertNicoTestArtifactHashes(t, hls, beforeHLS)
	assertNoNicoTimelineStaging(t, dir)
}

func hashNicoTestArtifact(t *testing.T, path string) map[string][32]byte {
	t.Helper()
	hashes := make(map[string][32]byte)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hashes[filepath.Base(path)] = sha256.Sum256(data)
		return hashes
	}
	err = filepath.WalkDir(path, func(item string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(item)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, item)
		if err != nil {
			return err
		}
		hashes[rel] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hashes
}

func assertNicoTestArtifactHashes(t *testing.T, path string, want map[string][32]byte) {
	t.Helper()
	got := hashNicoTestArtifact(t, path)
	if len(got) != len(want) {
		t.Fatalf("artifact file count changed for %s: got=%v want=%v", path, got, want)
	}
	for name, wantHash := range want {
		if gotHash, ok := got[name]; !ok || gotHash != wantHash {
			t.Fatalf("artifact hash changed for %s/%s: got=%x want=%x", path, name, gotHash, wantHash)
		}
	}
}

func assertNoNicoTimelineStaging(t *testing.T, dir string) {
	t.Helper()
	for _, pattern := range []string{
		".niconico-fallback-*.mp4",
		".niconico-timeline-job-*",
		".niconico-timeline-hls-*",
		".niconico-tee-*",
		".niconico-previous-*",
	} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) > 0 {
			t.Fatalf("timeline staging remains after failure: pattern=%s paths=%v", pattern, matches)
		}
	}
}

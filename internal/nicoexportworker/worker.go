package nicoexportworker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
	"imagepadserver/internal/video"
)

var nicoExportWithRenderer = video.ExportNicoCommented
var nicoPrepareHLS = video.PrepareNicoHLSForID

const timelineCPUFallbackNotice = "WGPUコメント描画に失敗したため、CPU描画へ切り替えました"

// Run executes exactly one finite Nico render request. It owns only the paths
// named by the request; the parent process owns the CPU Job and publication.
func Run(ctx context.Context, request Request, stdout, stderr io.Writer) (runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if stderr == nil {
		stderr = io.Discard
	}
	var outputMu sync.Mutex
	emit := func(event Event) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		return WriteEvent(stdout, event)
	}
	return runJob(ctx, request, nil, stderr, emit)
}

// runJob is the shared one-request core. A session may inject its owner-scoped
// browser pool and envelope-aware event sink; legacy Run keeps its old sink.
func runJob(ctx context.Context, request Request, browserPool *nicorender.BrowserPool, stderr io.Writer, emit func(Event) error) (runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fail := func(err error) error {
		if err == nil {
			err = errors.New("nico export worker failed")
		}
		if emitErr := emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, Error: err.Error()}); emitErr != nil {
			return errors.Join(err, emitErr)
		}
		return err
	}
	if err := request.Validate(); err != nil {
		return fail(err)
	}
	outputMode, err := video.NormalizeNicoOutputMode(video.NicoOutputMode(request.OutputMode))
	if err != nil {
		return fail(err)
	}
	if _, err := os.Stat(request.SourcePath); err != nil {
		return fail(fmt.Errorf("nico export worker: source: %w", err))
	}
	data, err := os.ReadFile(request.SnapshotPath)
	if err != nil {
		return fail(fmt.Errorf("nico export worker: snapshot: %w", err))
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fail(fmt.Errorf("nico export worker: decode snapshot: %w", err))
	}
	if err := os.MkdirAll(filepath.Dir(request.OutputPath), 0700); err != nil {
		return fail(fmt.Errorf("nico export worker: output directory: %w", err))
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(request.OutputPath)
			_ = os.RemoveAll(request.HLSStagingDir)
		}
	}()
	if err := emit(Event{Version: ProtocolVersion, Type: "progress", RunID: request.RunID, MediaID: request.MediaID, Stage: "render", Total: 1}); err != nil {
		return err
	}
	var emitErr error
	var emitErrMu sync.Mutex
	recordEmitErr := func(err error) {
		if err == nil {
			return
		}
		emitErrMu.Lock()
		if emitErr == nil {
			emitErr = err
			cancel()
		}
		emitErrMu.Unlock()
	}
	renderOptions := nicorender.RenderOptions{
		Width: request.Width, Height: request.Height, DurationMs: request.DurationMs,
		FPSNum: request.FPSNum, FPSDen: request.FPSDen, BrowserPath: request.BrowserPath,
		RendererLabel: "niconicomments@0.4.1", Backend: request.Backend, CompositorPath: request.Compositor,
		TimelineEnabled: request.TimelineEnabled, TimelineCompositorPath: request.TimelineCompositor,
		TimelineReadbackSlots: request.TimelineReadbackSlots, TimelineGPUBackend: request.TimelineGPUBackend,
		TimelineRandomSeed: request.TimelineRandomSeed,
		OnTimelineFallback: func() {
			recordEmitErr(emit(Event{
				Version: ProtocolVersion, Type: "progress", RunID: request.RunID, MediaID: request.MediaID,
				Stage: "timeline_fallback", FallbackNotice: timelineCPUFallbackNotice,
			}))
		},
		Transport: "binary", ReuseUnchanged: true, BatchFrames: 30, SparseFrames: true,
		Progress: func(completed, total int64) {
			recordEmitErr(emit(Event{Version: ProtocolVersion, Type: "progress", RunID: request.RunID, MediaID: request.MediaID, Stage: "render", Completed: completed, Total: total}))
		},
	}
	encodeOptions := video.NicoEncodeOptions{
		Width: request.Width, Height: request.Height, DurationMs: request.DurationMs,
		FPSNum: request.FPSNum, FPSDen: request.FPSDen, CRF: request.CRF, Encoder: request.Encoder, AudioBitrate: request.AudioBitrate,
		FilterThreads: request.FilterThreads, DecoderThreads: request.DecoderThreads, EncoderThreads: request.EncoderThreads,
		OutputMode: outputMode, HLSOutputDir: request.HLSStagingDir,
		StageObserver: func(stage video.NicoStageTiming) {
			recordEmitErr(emit(Event{Version: ProtocolVersion, Type: "progress", RunID: request.RunID, MediaID: request.MediaID, Stage: stage.Name}))
		},
	}
	if browserPool != nil {
		renderOptions = renderOptions.WithBrowserPool(browserPool)
	}
	convertStartedAt := time.Now()
	exported, renderReport, err := nicoExportWithRenderer(runCtx, request.FFmpeg, request.SourcePath, request.OutputPath, request.HLSStagingDir, snapshot, renderOptions, encodeOptions, outputMode)
	convertWallSeconds := time.Since(convertStartedAt).Seconds()
	if err != nil {
		return fail(err)
	}
	if err := currentEmitError(&emitErrMu, &emitErr); err != nil {
		return fail(err)
	}
	if err := emit(Event{Version: ProtocolVersion, Type: "progress", RunID: request.RunID, MediaID: request.MediaID, Stage: "hls"}); err != nil {
		return err
	}
	prepared, err := nicoPrepareHLS(request.HLSStagingDir, request.MediaID, request.RunID)
	if err != nil {
		return fail(err)
	}
	if err := emit(Event{Version: ProtocolVersion, Type: "progress", RunID: request.RunID, MediaID: request.MediaID, Stage: "validate", Completed: 1, Total: 1}); err != nil {
		return err
	}
	resultEvent := Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, Output: request.OutputPath, Playlist: prepared.Playlist, OK: true, ConvertWallSeconds: convertWallSeconds}
	applyNicoTimelineResultMetadata(&resultEvent, exported.Encode, renderReport)
	if err := emit(resultEvent); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "nico export worker complete run_id=%s media_id=%s\n", request.RunID, request.MediaID)
	failed = false
	return nil
}

func applyNicoTimelineResultMetadata(event *Event, encode video.NicoEncodeReport, render nicorender.RenderReport) {
	if event == nil {
		return
	}
	event.StageTimings = append([]video.NicoStageTiming(nil), encode.StageTimings...)
	event.Renderer = boundedWorkerLabel(render.RendererLabel, 128)
	event.Backend = boundedWorkerLabel(render.Backend, 64)
	event.GPUBackend = boundedWorkerLabel(encode.TimelineGPUBackend, 32)
	event.GPUAdapter = boundedWorkerLabel(encode.TimelineGPUAdapter, 128)
	event.HelperSHA256 = validWorkerSHA256(encode.TimelineHelperSHA256)
	event.BundleSHA256 = validWorkerSHA256(encode.TimelineBundleSHA256)
	event.ReadbackSlots = encode.TimelineReadbackSlots
	event.SpriteCaptureMetrics = encode.SpriteCaptureMetrics
	if encode.TimelineProtocol == "NCT2" {
		event.TimelineProtocol = encode.TimelineProtocol
		event.TimelineCaptureDone = encode.TimelineCaptureDone
		event.TimelineFirstAssetReady = encode.TimelineFirstAssetReady
		event.TimelineFirstFrame = encode.TimelineFirstFrame
		event.TimelineStreamEnd = encode.TimelineStreamEnd
		event.TimelineHelperDone = encode.TimelineHelperDone
		event.TimelineFFmpegDone = encode.TimelineFFmpegDone
	}
	for _, attempt := range encode.Attempts {
		if attempt.Backend != "timeline-wgpu" || attempt.Error == "" {
			continue
		}
		event.TimelineFallback = true
		event.FallbackNotice = timelineCPUFallbackNotice
		if event.GPUBackend == "" {
			event.GPUBackend = boundedWorkerLabel(attempt.GPUBackend, 32)
		}
		if event.GPUAdapter == "" {
			event.GPUAdapter = boundedWorkerLabel(attempt.GPUAdapter, 128)
		}
		if event.HelperSHA256 == "" {
			event.HelperSHA256 = validWorkerSHA256(attempt.HelperSHA256)
		}
		if event.BundleSHA256 == "" {
			event.BundleSHA256 = validWorkerSHA256(attempt.BundleSHA256)
		}
		if event.ReadbackSlots == 0 {
			event.ReadbackSlots = attempt.ReadbackSlots
		}
		if attempt.TimelineProtocol == "NCT2" {
			event.TimelineAttempt = &TimelineAttemptMetadata{
				Protocol: attempt.TimelineProtocol, CaptureDone: attempt.TimelineCaptureDone,
				FirstAssetReady: attempt.TimelineFirstAssetReady, FirstFrame: attempt.TimelineFirstFrame,
				StreamEnd: attempt.TimelineStreamEnd, HelperDone: attempt.TimelineHelperDone,
				FFmpegDone: attempt.TimelineFFmpegDone,
			}
		}
		break
	}
}

func boundedWorkerLabel(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return ""
	}
	if filepath.IsAbs(value) || strings.Contains(value, "\\") || strings.Contains(value, ":") {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func validWorkerSHA256(value string) string {
	if len(value) != 64 {
		return ""
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return ""
	}
	return strings.ToLower(value)
}

func currentEmitError(mu *sync.Mutex, value *error) error {
	mu.Lock()
	defer mu.Unlock()
	return *value
}

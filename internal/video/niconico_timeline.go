package video

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

const nicoTimelineReportLimit = 2 << 20

const nicoTimelineEncoderListLimit = 2 << 20

var (
	nicoTimelineStallTimeout  = 30 * time.Second
	nicoTimelineWatchInterval = 250 * time.Millisecond
)

var ErrTimelineStalled = errors.New("niconico timeline: renderer made no progress")

var captureNicoTimelineForPipeline = nicorender.CaptureCommentTimeline
var captureNicoTimelineStreamForPipeline = nicorender.CaptureCommentTimelineStream
var prepareNicoTimelineCompositorForPipeline = nicorender.PrepareTimelineCompositor
var runNicoTimelinePipeForPipeline = runNicoNativePipeTimedInDir
var probeNicoTimelineEncoderForPipeline = probeNicoTimelineEncoder
var renameNicoTimelineArtifact = os.Rename

// timelineSelection keeps the experimental renderer opt-in. Once selected,
// the caller may fall back to the legacy CPU renderer if the WGPU attempt fails.
func timelineSelection(backend string, enabled bool) string {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "timeline":
		return "try"
	case "", "auto":
		if enabled {
			return "try"
		}
	}
	return "legacy"
}

type nicoTimelineRuntimeReport struct {
	Schema                int                             `json:"schema"`
	Protocol              string                          `json:"protocol"`
	Renderer              string                          `json:"renderer"`
	Version               string                          `json:"version"`
	RequestedBackend      string                          `json:"requestedBackend"`
	Backend               string                          `json:"backend"`
	AdapterName           string                          `json:"adapterName"`
	AdapterType           string                          `json:"adapterType"`
	ReadbackSlots         int                             `json:"readbackSlots"`
	RequestedAssetLayout  string                          `json:"requestedAssetLayout"`
	AssetLayout           string                          `json:"assetLayout"`
	AssetLayoutFallback   string                          `json:"assetLayoutFallbackReason"`
	AssetPageCount        int                             `json:"assetPageCount"`
	AssetSourceBytes      uint64                          `json:"assetSourceBytes"`
	AssetAllocatedBytes   uint64                          `json:"assetAllocatedBytes"`
	AssetTelemetry        *NicoAssetTelemetryReport       `json:"assetTelemetry"`
	AssetTelemetryPresent bool                            `json:"-"`
	MaxTextureDimension2D uint32                          `json:"maxTextureDimension2D"`
	CompletedFrames       uint32                          `json:"completedFrames"`
	OutputQueueMetrics    *nicoTimelineWriterQueueMetrics `json:"outputQueueMetrics"`
	Error                 string                          `json:"error"`
}

type NicoAssetTelemetryPage struct {
	PageIndex       uint64 `json:"pageIndex"`
	Width           uint32 `json:"width"`
	Height          uint32 `json:"height"`
	UsedTexels      uint64 `json:"usedTexels"`
	AllocatedTexels uint64 `json:"allocatedTexels"`
}

type NicoAssetTelemetryResidentMemory struct {
	DecodedScenePixelBytes              *uint64  `json:"decodedScenePixelBytes"`
	LogicalTextureAllocationBytes       *uint64  `json:"logicalTextureAllocationBytes"`
	RendererOwnedDrawBufferPayloadBytes *uint64  `json:"rendererOwnedDrawBufferPayloadBytes"`
	RenderBundleInternalBytes           *uint64  `json:"renderBundleInternalBytes"`
	ReadbackRingBufferBytes             *uint64  `json:"readbackRingBufferBytes"`
	WGPUTransientStagingBytes           *uint64  `json:"wgpuTransientStagingBytes"`
	PhysicalVRAMBytes                   *uint64  `json:"physicalVramBytes"`
	UnknownReasons                      []string `json:"unknownReasons"`
}

type NicoAssetTelemetryReport struct {
	TelemetryScope            string                            `json:"telemetryScope"`
	EffectiveLayout           string                            `json:"effectiveLayout"`
	Pages                     *[]NicoAssetTelemetryPage         `json:"pages"`
	TextureUploadCallCount    *uint64                           `json:"textureUploadCallCount"`
	TextureUploadBytes        *uint64                           `json:"textureUploadBytes"`
	DrawOrderPageBindRunCount *uint64                           `json:"drawOrderPageBindRunCount"`
	BundleBuildCPUWallTimeNs  *uint64                           `json:"bundleBuildCpuWallTimeNs"`
	ResidentMemory            *NicoAssetTelemetryResidentMemory `json:"residentMemory"`
	UnavailableReasons        []string                          `json:"unavailableReasons"`
}

type nicoTimelineWriterQueueMetrics struct {
	Capacity       int `json:"capacity"`
	FinalOccupancy int `json:"finalOccupancy"`
	HighWater      int `json:"highWater"`
}

type nicoTimelineActivity struct {
	mu   sync.Mutex
	last time.Time
}

func (a *nicoTimelineActivity) touch() {
	a.mu.Lock()
	a.last = time.Now()
	a.mu.Unlock()
}

func (a *nicoTimelineActivity) idleFor() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Since(a.last)
}

type nicoTimelineTrackingWriter struct {
	w io.Writer
	a *nicoTimelineActivity
}

func (w nicoTimelineTrackingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 {
		w.a.touch()
	}
	return n, err
}

type nicoTimelinePrepareResult struct {
	path    string
	cleanup func()
	err     error
	timing  NicoStageTiming
}

type nicoTimelineEncoderProbeResult struct {
	err    error
	timing NicoStageTiming
}

// encodeNicoTimeline captures reusable comment textures and streams the NCT2
// scene through the WGPU helper directly into the existing FFmpeg argument
// path. The caller decides whether a failure is terminal or falls back once.
func encodeNicoTimeline(ctx context.Context, ffmpeg, sourcePath, outputPath string, snapshot niconico.Snapshot, render nicorender.RenderOptions, enc NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := NicoEncodeReport{OutputPath: outputPath, Width: enc.Width, Height: enc.Height, FPSNum: enc.FPSNum, FPSDen: enc.FPSDen}
	renderReport := nicorender.RenderReport{Width: render.Width, Height: render.Height, FPSNum: render.FPSNum, FPSDen: render.FPSDen, RendererLabel: "niconico-timeline-wgpu"}
	timer := newNicoStageTimer(enc.StageObserver)
	if err := ctx.Err(); err != nil {
		return report, renderReport, err
	}
	clock, err := enc.validate()
	if err != nil {
		return report, renderReport, err
	}
	if strings.TrimSpace(ffmpeg) == "" {
		return report, renderReport, wrapNicoTimelineStage("encoder", errors.New("niconico: ffmpeg path is required"))
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return report, renderReport, wrapNicoTimelineStage("input", fmt.Errorf("niconico: source: %w", err))
	}
	if render.Width != enc.Width || render.Height != enc.Height || render.DurationMs != enc.DurationMs || render.FPSNum != enc.FPSNum || render.FPSDen != enc.FPSDen {
		return report, renderReport, wrapNicoTimelineStage("configuration", errors.New("niconico: renderer and encoder geometry/timeline differ"))
	}
	frameCount := clock.FrameCountForDurationMs(enc.DurationMs)
	if frameCount <= 0 {
		return report, renderReport, wrapNicoTimelineStage("configuration", errors.New("niconico: duration produced no frames"))
	}
	mode, err := NormalizeNicoOutputMode(enc.OutputMode)
	if err != nil {
		return report, renderReport, wrapNicoTimelineStage("configuration", err)
	}
	enc.OutputMode = mode
	runtimeOptions, err := nicorender.ValidateTimelineRuntimeOptions(nicorender.TimelineRuntimeOptions{
		Backend: render.TimelineGPUBackend, ReadbackSlots: render.TimelineReadbackSlots, AssetLayout: render.TimelineAssetLayout,
	})
	if err != nil {
		return report, renderReport, wrapNicoTimelineStage("configuration", err)
	}

	childCtx, cancelPreparation := context.WithCancel(ctx)
	prepareDone := make(chan nicoTimelinePrepareResult, 1)
	encoderProbeDone := make(chan nicoTimelineEncoderProbeResult, 1)
	go func() {
		started := time.Now()
		path, cleanup, prepareErr := prepareNicoTimelineCompositorForPipeline(childCtx, render.TimelineCompositorPath, runtimeOptions)
		finished := time.Now()
		prepareDone <- nicoTimelinePrepareResult{path: path, cleanup: cleanup, err: prepareErr, timing: NicoStageTiming{
			Name: "timeline_runtime_prepare", StartedAt: started, FinishedAt: finished, Elapsed: finished.Sub(started),
		}}
	}()
	go func() {
		started := time.Now()
		probeErr := probeNicoTimelineEncoderForPipeline(childCtx, ffmpeg, enc)
		finished := time.Now()
		encoderProbeDone <- nicoTimelineEncoderProbeResult{err: probeErr, timing: NicoStageTiming{
			Name: "timeline_encoder_probe", StartedAt: started, FinishedAt: finished, Elapsed: finished.Sub(started),
		}}
	}()

	var prepared nicoTimelinePrepareResult
	var encoderProbe nicoTimelineEncoderProbeResult
	gotPrepare, gotEncoderProbe := false, false
	ctxDone := ctx.Done()
	for !gotPrepare || !gotEncoderProbe {
		select {
		case <-ctxDone:
			cancelPreparation()
			ctxDone = nil
		case prepared = <-prepareDone:
			gotPrepare = true
			timer.record(prepared.timing)
			if prepared.err != nil {
				cancelPreparation()
			}
		case encoderProbe = <-encoderProbeDone:
			gotEncoderProbe = true
			timer.record(encoderProbe.timing)
			if encoderProbe.err != nil {
				cancelPreparation()
			}
		}
	}
	cancelPreparation()
	if prepared.cleanup != nil {
		defer prepared.cleanup()
	}
	if err := ctx.Err(); err != nil {
		report.StageTimings = timer.snapshot()
		return report, renderReport, err
	}
	if prepared.err != nil {
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("renderer", fmt.Errorf("niconico timeline: runtime prepare: %w", prepared.err))
	}
	if encoderProbe.err != nil {
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("encoder", fmt.Errorf("niconico timeline: encoder capability: %w", encoderProbe.err))
	}
	renderReport.FrameCount = frameCount
	renderReport.Backend = "timeline-wgpu"
	report.FrameCount = frameCount

	startValidate := time.Now()
	reportPathDir, err := os.MkdirTemp(filepath.Dir(outputPath), ".niconico-timeline-job-*")
	if err != nil {
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("output", fmt.Errorf("niconico timeline: create attempt directory: %w", err))
	}
	defer os.RemoveAll(reportPathDir)
	reportPath := filepath.Join(reportPathDir, "runtime-report.json")
	if report.TimelineHelperSHA256, err = hashNicoFile(prepared.path); err != nil {
		timer.mark("timeline_helper_hash", startValidate)
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("renderer", fmt.Errorf("niconico timeline: hash helper: %w", err))
	}
	timer.mark("timeline_helper_hash", startValidate)

	var stagedOutput, stagedHLS string
	var cleanupOutput func()
	teeWorkDir := ""
	if mode == NicoOutputSeparate {
		stagedOutput, cleanupOutput, err = createNicoFallbackOutput(outputPath)
	} else {
		stagedOutput, cleanupOutput, err = createNicoFallbackOutput(outputPath)
		if err == nil {
			stagedHLS, err = createNicoAbsentDirectoryPath(enc.HLSOutputDir, ".niconico-timeline-hls-*")
		}
		if err == nil {
			teeWorkDir, err = createNicoTeeWorkspace(outputPath)
		}
	}
	if err != nil {
		if cleanupOutput != nil {
			cleanupOutput()
		}
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("output", fmt.Errorf("niconico timeline: create output staging: %w", err))
	}
	defer cleanupOutput()
	if stagedHLS != "" {
		defer os.RemoveAll(stagedHLS)
	}
	if teeWorkDir != "" {
		defer os.RemoveAll(teeWorkDir)
	}

	argsStarted := time.Now()
	ffmpegArgs := nicoTimelineEncodeArgs(sourcePath, stagedOutput, enc)
	timer.mark("build_ffmpeg_args", argsStarted)
	activity := &nicoTimelineActivity{last: time.Now()}
	progress := func(completed, total int64) {
		activity.touch()
		if render.Progress != nil {
			render.Progress(completed, total)
		}
	}
	var captureReport nicorender.TimelineCaptureReport
	var captureTiming NicoStageTiming
	var captureErr error
	captureCalled := false
	produce := func(produceCtx context.Context, w io.Writer) (nicorender.RenderReport, error) {
		started := time.Now()
		captureReport, captureErr = captureNicoTimelineStreamForPipeline(produceCtx, snapshot, render, nicoTimelineTrackingWriter{w: w, a: activity})
		finished := time.Now()
		captureCalled = true
		captureTiming = NicoStageTiming{Name: "timeline_capture", StartedAt: started, FinishedAt: finished, Elapsed: finished.Sub(started)}
		if captureErr != nil {
			return nicorender.RenderReport{}, captureErr
		}
		return nicorender.RenderReport{
			FrameCount: frameCount, Width: enc.Width, Height: enc.Height, FPSNum: enc.FPSNum, FPSDen: enc.FPSDen,
			RendererLabel: "niconico-timeline-wgpu", Backend: "timeline-wgpu",
		}, nil
	}

	pipeStarted := time.Now()
	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	watchDone := make(chan struct{})
	stalled := make(chan struct{}, 1)
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(nicoTimelineWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				if activity.idleFor() >= nicoTimelineStallTimeout {
					select {
					case stalled <- struct{}{}:
					default:
					}
					cancelWatch()
					return
				}
			}
		}
	}()
	helperArgs := []string{"--stdin-stream", "--backend", normalizedTimelineBackend(render.TimelineGPUBackend), "--readback-slots", fmt.Sprint(normalizedTimelineSlots(render.TimelineReadbackSlots))}
	if runtimeOptions.AssetLayout != "separate" {
		helperArgs = append(helperArgs, "--asset-layout", runtimeOptions.AssetLayout)
	}
	helperArgs = append(helperArgs, "--report", reportPath)
	pipeResult, pipeErr := runNicoTimelinePipeForPipeline(watchCtx, prepared.path,
		helperArgs,
		ffmpeg, ffmpegArgs, frameCount, progress, produce, teeWorkDir)
	cancelWatch()
	<-watchDone
	if captureTiming.Name != "" {
		timer.record(captureTiming)
	}
	timer.mark("timeline_helper_pipe", pipeStarted)
	for _, stage := range pipeResult.stageTimings {
		timer.record(stage)
	}
	for len(stalled) > 0 {
		<-stalled
		pipeErr = errors.Join(ErrTimelineStalled, pipeErr)
		break
	}
	if ctx.Err() != nil {
		report.StageTimings = timer.snapshot()
		return report, renderReport, ctx.Err()
	}
	if pipeErr != nil && (!captureCalled || captureErr != nil) {
		// Only inspect a runtime report when the producer completed cleanly;
		// earlier pipe failures keep their original error precedence.
		report.StageTimings = timer.snapshot()
		return report, renderReport, pipeErr
	}
	if !captureCalled {
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("renderer", errors.New("niconico timeline: NCT2 capture did not run"))
	}
	if captureErr != nil {
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("renderer", fmt.Errorf("niconico timeline: capture: %w", captureErr))
	}
	report.TimelineBundleSHA256 = captureReport.BundleSHA256
	report.SpriteCaptureMetrics = captureReport.SpriteCaptureMetrics

	validationStarted := time.Now()
	runtimeReport, err := readNicoTimelineReport(reportPath)
	timer.mark("timeline_report_validate", validationStarted)
	if err != nil {
		report.StageTimings = timer.snapshot()
		if pipeErr != nil {
			return report, renderReport, pipeErr
		}
		return report, renderReport, wrapNicoTimelineStage("renderer", err)
	}
	identityErr := validateNicoTimelineReportIdentity(runtimeReport)
	if identityErr == nil {
		report.TimelineProtocol = runtimeReport.Protocol
		report.TimelineCaptureDone = pipeResult.milestones.CaptureDone
		report.TimelineFirstAssetReady = pipeResult.milestones.FirstAssetReady
		report.TimelineFirstFrame = pipeResult.milestones.FirstFrame
		report.TimelineStreamEnd = pipeResult.milestones.StreamEnd
		report.TimelineHelperDone = pipeResult.milestones.HelperDone
		report.TimelineFFmpegDone = pipeResult.milestones.FFmpegDone
	}
	if identityErr == nil && (runtimeReport.AssetPageCount == 0) != (pipeResult.milestones.FirstAssetReady == nil) {
		mismatch := wrapNicoTimelineStage("renderer", fmt.Errorf(
			"niconico timeline: runtime asset page count %d disagrees with NICO_FIRST_ASSET_READY marker presence=%t",
			runtimeReport.AssetPageCount, pipeResult.milestones.FirstAssetReady != nil,
		))
		report.StageTimings = timer.snapshot()
		return report, renderReport, errors.Join(pipeErr, mismatch)
	}
	err = validateNicoTimelineRuntimeReport(runtimeReport, render.TimelineGPUBackend, normalizedTimelineSlots(render.TimelineReadbackSlots), runtimeOptions.AssetLayout, uint32(frameCount), runtime.GOOS)
	if err != nil {
		report.StageTimings = timer.snapshot()
		validationErr := wrapNicoTimelineStage("renderer", err)
		if pipeErr != nil {
			return report, renderReport, errors.Join(pipeErr, validationErr)
		}
		return report, renderReport, validationErr
	}
	applyNicoTimelineRuntimeReportMetadata(&report, runtimeReport)
	report.TimelineAssetTelemetry = runtimeReport.AssetTelemetry
	renderReport.RendererLabel = fmt.Sprintf("niconico-timeline-wgpu/%s", runtimeReport.Version)
	if pipeErr != nil {
		report.StageTimings = timer.snapshot()
		return report, renderReport, pipeErr
	}

	outputValidationStarted := time.Now()
	if mode == NicoOutputTee {
		if err := promoteNicoTeeArtifacts(teeWorkDir, stagedOutput, stagedHLS); err != nil {
			timer.mark("timeline_output_validate", outputValidationStarted)
			report.StageTimings = timer.snapshot()
			return report, renderReport, wrapNicoTimelineStage("output", err)
		}
	}
	if info, err := os.Stat(stagedOutput); err != nil {
		timer.mark("timeline_output_validate", outputValidationStarted)
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("output", fmt.Errorf("niconico timeline: staged MP4: %w", err))
	} else if info.Size() == 0 {
		timer.mark("timeline_output_validate", outputValidationStarted)
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("output", errors.New("niconico timeline: staged MP4 is empty"))
	}
	timer.mark("timeline_output_validate", outputValidationStarted)

	promoteStarted := time.Now()
	if mode == NicoOutputTee {
		err = promoteNicoTimelineArtifacts(stagedOutput, stagedHLS, outputPath, enc.HLSOutputDir)
	} else {
		err = promoteNicoFallbackOutput(stagedOutput, outputPath)
	}
	timer.mark("timeline_output_promote", promoteStarted)
	if err != nil {
		report.StageTimings = timer.snapshot()
		return report, renderReport, wrapNicoTimelineStage("output", fmt.Errorf("niconico timeline: promote outputs: %w", err))
	}
	report.OutputPath = outputPath
	report.StageTimings = timer.snapshot()
	return report, renderReport, nil
}

func probeNicoTimelineEncoder(ctx context.Context, ffmpeg string, options NicoEncodeOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	encoder := strings.ToLower(strings.TrimSpace(options.Encoder))
	if encoder == "" {
		encoder = "x264"
	}
	wanted := map[string]string{"x264": "libx264", "nvenc": "h264_nvenc"}[encoder]
	if wanted == "" {
		return fmt.Errorf("unsupported encoder %q", options.Encoder)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, ffmpeg, "-hide_banner", "-encoders")
	hideWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	var output boundedNicoEncoderOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if probeCtx.Err() != nil {
			return errors.New("FFmpeg encoder inventory timed out")
		}
		return errors.New("FFmpeg encoder inventory failed")
	}
	if output.exceeded {
		return errors.New("FFmpeg encoder inventory exceeded 2 MiB")
	}
	if !nicoTimelineEncoderListed(output.Bytes(), wanted) {
		return fmt.Errorf("FFmpeg does not provide required encoder %s", wanted)
	}
	return nil
}

type boundedNicoEncoderOutput struct {
	mu sync.Mutex
	bytes.Buffer
	exceeded bool
}

func (b *boundedNicoEncoderOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := nicoTimelineEncoderListLimit - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(remaining, n)])
	}
	if n > remaining {
		b.exceeded = true
	}
	return n, nil
}

func nicoTimelineEncoderListed(output []byte, encoder string) bool {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == encoder && len(fields[0]) == 6 && strings.Contains(fields[0], "V") {
			return true
		}
	}
	return false
}

func normalizedTimelineBackend(backend string) string {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		return "auto"
	}
	return backend
}

func normalizedTimelineSlots(slots int) int {
	if slots == 0 {
		return 3
	}
	return slots
}

func readAndValidateNicoTimelineReport(path, requestedBackend string, requestedSlots int, requestedAssetLayout string, expectedFrames uint32, goos string) (nicoTimelineRuntimeReport, error) {
	report, err := readNicoTimelineReport(path)
	if err != nil {
		return report, err
	}
	if err := validateNicoTimelineRuntimeReport(report, requestedBackend, requestedSlots, requestedAssetLayout, expectedFrames, goos); err != nil {
		return report, err
	}
	return report, nil
}

func readNicoTimelineReport(path string) (nicoTimelineRuntimeReport, error) {
	var report nicoTimelineRuntimeReport
	f, err := os.Open(path)
	if err != nil {
		return report, fmt.Errorf("niconico timeline: open runtime report: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(f, nicoTimelineReportLimit+1))
	closeErr := f.Close()
	if readErr != nil {
		return report, fmt.Errorf("niconico timeline: read runtime report: %w", readErr)
	}
	if closeErr != nil {
		return report, fmt.Errorf("niconico timeline: close runtime report: %w", closeErr)
	}
	if len(payload) == 0 || len(payload) > nicoTimelineReportLimit {
		return report, fmt.Errorf("niconico timeline: runtime report size %d is outside 1..%d bytes", len(payload), nicoTimelineReportLimit)
	}
	if err := nicorender.ValidateStrictJSONDocument(payload); err != nil {
		return report, fmt.Errorf("niconico timeline: validate runtime report JSON: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, fmt.Errorf("niconico timeline: decode runtime report: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return report, errors.New("niconico timeline: runtime report has trailing JSON data")
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &rawFields); err != nil {
		return report, fmt.Errorf("niconico timeline: inspect runtime report fields: %w", err)
	}
	if rawTelemetry, exists := rawFields["assetTelemetry"]; exists {
		report.AssetTelemetryPresent = true
		if string(rawTelemetry) != "null" {
			if err := validateNicoAssetTelemetryShape(rawTelemetry); err != nil {
				return report, fmt.Errorf("niconico timeline: invalid assetTelemetry shape: %w", err)
			}
		}
	}
	return report, nil
}

func validateNicoTimelineRuntimeReport(report nicoTimelineRuntimeReport, requestedBackend string, requestedSlots int, requestedAssetLayout string, expectedFrames uint32, goos string) error {
	if err := validateNicoTimelineReportIdentity(report); err != nil {
		return err
	}
	if report.Error != "" {
		return fmt.Errorf("niconico timeline: helper reported failure: %s", report.Error)
	}
	if report.AdapterName == "" || (report.AdapterType != "DiscreteGpu" && report.AdapterType != "IntegratedGpu") {
		return fmt.Errorf("niconico timeline: unsupported adapter %q (%q)", report.AdapterName, report.AdapterType)
	}
	if report.ReadbackSlots != requestedSlots || report.CompletedFrames != expectedFrames {
		return fmt.Errorf("niconico timeline: runtime report has %d frames/%d slots; want %d/%d", report.CompletedFrames, report.ReadbackSlots, expectedFrames, requestedSlots)
	}
	if metrics := report.OutputQueueMetrics; metrics != nil {
		if metrics.Capacity <= 0 || metrics.FinalOccupancy != 0 || metrics.HighWater < 0 || metrics.HighWater > metrics.Capacity {
			return fmt.Errorf("niconico timeline: invalid writer output queue metrics: capacity=%d finalOccupancy=%d highWater=%d", metrics.Capacity, metrics.FinalOccupancy, metrics.HighWater)
		}
	}
	if report.MaxTextureDimension2D == 0 || report.Backend != report.RequestedBackend && report.RequestedBackend != "auto" {
		return errors.New("niconico timeline: runtime backend report mismatch")
	}
	if report.RequestedBackend != normalizedTimelineBackend(requestedBackend) {
		return fmt.Errorf("niconico timeline: helper requested backend %q, want %q", report.RequestedBackend, normalizedTimelineBackend(requestedBackend))
	}
	if !nicorender.TimelineBackendAllowed(report.Backend, goos) {
		return fmt.Errorf("niconico timeline: backend %q is not allowed on %s", report.Backend, goos)
	}
	if err := validateNicoTimelineRuntimeAssetLayout(report, requestedAssetLayout); err != nil {
		return fmt.Errorf("niconico timeline: invalid asset layout report: %w", err)
	}
	return nil
}

func validateNicoTimelineRuntimeAssetLayout(report nicoTimelineRuntimeReport, requestedAssetLayout string) error {
	requestedAssetLayout = strings.TrimSpace(requestedAssetLayout)
	if requestedAssetLayout == "" {
		requestedAssetLayout = "separate"
	}
	hasTelemetry := report.AssetTelemetryPresent || report.AssetTelemetry != nil
	if report.Protocol == "NCT1" {
		if report.AssetLayout == "streamed-distinct-textures" {
			return errors.New("NCT1 cannot report streamed-distinct-textures layout")
		}
		if err := nicorender.ValidateTimelineAssetLayoutReport(requestedAssetLayout, report.RequestedAssetLayout, report.AssetLayout, report.AssetLayoutFallback, report.AssetPageCount, report.AssetSourceBytes, report.AssetAllocatedBytes); err != nil {
			return err
		}
		if requestedAssetLayout == "atlas" && (report.AssetLayout != "atlas" || report.AssetLayoutFallback != "") {
			return errors.New("NCT1 Atlas request did not produce Atlas without fallback")
		}
		if !hasTelemetry && requestedAssetLayout == "separate" {
			return nil // legacy NCT1 Separate report omitted additive telemetry
		}
		return validateNicoTimelineAssetTelemetry(report)
	}
	if report.Protocol != "NCT2" {
		return fmt.Errorf("unsupported runtime report protocol %q", report.Protocol)
	}
	if requestedAssetLayout != "separate" && requestedAssetLayout != "atlas" {
		return fmt.Errorf("unsupported requested asset layout %q", requestedAssetLayout)
	}
	if !hasTelemetry {
		if requestedAssetLayout == "separate" && report.AssetLayout != "streamed-distinct-textures" {
			return nicorender.ValidateTimelineAssetLayoutReport(requestedAssetLayout, report.RequestedAssetLayout, report.AssetLayout, report.AssetLayoutFallback, report.AssetPageCount, report.AssetSourceBytes, report.AssetAllocatedBytes)
		}
		return errors.New("NCT2 streamed layout report is missing assetTelemetry")
	}
	if report.AssetTelemetry == nil {
		return errors.New("assetTelemetry must be an object, not null")
	}
	if report.RequestedAssetLayout != requestedAssetLayout {
		return fmt.Errorf("helper requested asset layout %q, want %q", report.RequestedAssetLayout, requestedAssetLayout)
	}
	if report.AssetLayout != "streamed-distinct-textures" {
		return fmt.Errorf("NCT2 asset layout=%q, want streamed-distinct-textures", report.AssetLayout)
	}
	if requestedAssetLayout == "atlas" && strings.TrimSpace(report.AssetLayoutFallback) == "" {
		return errors.New("NCT2 atlas request lacks explicit streamed-layout fallback reason")
	}
	if requestedAssetLayout == "separate" && strings.TrimSpace(report.AssetLayoutFallback) != "" {
		return errors.New("NCT2 Separate request reported an unexpected fallback reason")
	}
	if report.AssetPageCount < 0 || report.AssetSourceBytes != 0 || report.AssetAllocatedBytes != 0 {
		return fmt.Errorf("invalid NCT2 legacy asset fields: pages=%d sourceBytes=%d allocatedBytes=%d", report.AssetPageCount, report.AssetSourceBytes, report.AssetAllocatedBytes)
	}
	return validateNicoTimelineAssetTelemetry(report)
}

func validateNicoTimelineAssetTelemetry(report nicoTimelineRuntimeReport) error {
	telemetry := report.AssetTelemetry
	if telemetry == nil {
		return errors.New("assetTelemetry is missing")
	}
	if telemetry.ResidentMemory == nil {
		return errors.New("assetTelemetry residentMemory is missing or null")
	}
	if telemetry.UnavailableReasons == nil {
		return errors.New("assetTelemetry unavailableReasons must be an array")
	}
	resident := telemetry.ResidentMemory
	if report.Protocol == "NCT2" {
		if telemetry.TelemetryScope != "nct2-streamed-assets" || telemetry.EffectiveLayout != "streamed-distinct-textures" {
			return fmt.Errorf("invalid NCT2 assetTelemetry scope/layout %q/%q", telemetry.TelemetryScope, telemetry.EffectiveLayout)
		}
		if telemetry.Pages != nil || telemetry.TextureUploadCallCount != nil || telemetry.TextureUploadBytes != nil || telemetry.DrawOrderPageBindRunCount != nil || telemetry.BundleBuildCPUWallTimeNs != nil {
			return errors.New("NCT2 streamed telemetry values must remain unknown/null")
		}
		if resident.DecodedScenePixelBytes != nil || resident.LogicalTextureAllocationBytes != nil || resident.RendererOwnedDrawBufferPayloadBytes != nil || resident.RenderBundleInternalBytes != nil || resident.ReadbackRingBufferBytes != nil || resident.WGPUTransientStagingBytes != nil || resident.PhysicalVRAMBytes != nil {
			return errors.New("NCT2 streamed resident-memory values must remain unknown/null")
		}
		if resident.UnknownReasons == nil || len(resident.UnknownReasons) == 0 || len(telemetry.UnavailableReasons) == 0 {
			return errors.New("NCT2 unknown telemetry requires resident and top-level unavailable reasons")
		}
		return nil
	}
	if report.Protocol != "NCT1" {
		return fmt.Errorf("unsupported assetTelemetry protocol %q", report.Protocol)
	}
	if telemetry.TelemetryScope != "nct1-static-assets" || telemetry.EffectiveLayout != report.AssetLayout {
		return fmt.Errorf("invalid NCT1 assetTelemetry scope/layout %q/%q", telemetry.TelemetryScope, telemetry.EffectiveLayout)
	}
	if telemetry.Pages == nil || telemetry.UnavailableReasons == nil || len(telemetry.UnavailableReasons) != 0 {
		return errors.New("NCT1 static telemetry requires pages and an empty unavailableReasons array")
	}
	if len(*telemetry.Pages) != report.AssetPageCount {
		return fmt.Errorf("NCT1 telemetry pages=%d, assetPageCount=%d", len(*telemetry.Pages), report.AssetPageCount)
	}
	if telemetry.TextureUploadCallCount == nil || telemetry.TextureUploadBytes == nil || telemetry.DrawOrderPageBindRunCount == nil || telemetry.BundleBuildCPUWallTimeNs == nil {
		return errors.New("NCT1 static upload, ordered-run, and host CPU timing metrics must be present")
	}
	if resident.DecodedScenePixelBytes == nil || *resident.DecodedScenePixelBytes != report.AssetSourceBytes || resident.LogicalTextureAllocationBytes == nil || *resident.LogicalTextureAllocationBytes != report.AssetAllocatedBytes || resident.RendererOwnedDrawBufferPayloadBytes == nil || resident.ReadbackRingBufferBytes == nil {
		return errors.New("NCT1 known resident-memory metrics are missing or inconsistent with legacy report fields")
	}
	if *telemetry.TextureUploadBytes != report.AssetSourceBytes {
		return fmt.Errorf("NCT1 textureUploadBytes=%d, assetSourceBytes=%d", *telemetry.TextureUploadBytes, report.AssetSourceBytes)
	}
	for metric, value := range map[string]*uint64{
		"renderBundleInternalBytes": resident.RenderBundleInternalBytes,
		"wgpuTransientStagingBytes": resident.WGPUTransientStagingBytes,
		"physicalVramBytes":         resident.PhysicalVRAMBytes,
	} {
		if value != nil {
			return fmt.Errorf("opaque WGPU telemetry %s must be null", metric)
		}
	}
	for _, reason := range []string{"render-bundle", "staging", "physical vram"} {
		if !nicoTimelineReasonContains(resident.UnknownReasons, reason) {
			return fmt.Errorf("residentMemory.unknownReasons lacks reason for %q", reason)
		}
	}
	if report.AssetSourceBytes%4 != 0 || report.AssetAllocatedBytes%4 != 0 {
		return errors.New("NCT1 source and allocated texture bytes must be whole RGBA texels")
	}
	var allocatedTexels uint64
	var usedTexels uint64
	for i, page := range *telemetry.Pages {
		if page.PageIndex != uint64(i) || page.Width == 0 || page.Height == 0 || page.UsedTexels > page.AllocatedTexels {
			return fmt.Errorf("invalid NCT1 page occupancy at index %d", i)
		}
		if uint64(page.Width) > ^uint64(0)/uint64(page.Height) {
			return fmt.Errorf("NCT1 page %d dimensions overflow texel count", i)
		}
		if page.AllocatedTexels != uint64(page.Width)*uint64(page.Height) {
			return fmt.Errorf("NCT1 page %d allocatedTexels=%d, dimensions require %d", i, page.AllocatedTexels, uint64(page.Width)*uint64(page.Height))
		}
		if page.AllocatedTexels > ^uint64(0)/4 || allocatedTexels > ^uint64(0)-page.AllocatedTexels {
			return errors.New("NCT1 allocated texel count overflow")
		}
		if usedTexels > ^uint64(0)-page.UsedTexels {
			return errors.New("NCT1 used texel count overflow")
		}
		allocatedTexels += page.AllocatedTexels
		usedTexels += page.UsedTexels
	}
	if allocatedTexels > ^uint64(0)/4 || allocatedTexels*4 != report.AssetAllocatedBytes {
		return fmt.Errorf("NCT1 pages allocate %d bytes, assetAllocatedBytes=%d", allocatedTexels*4, report.AssetAllocatedBytes)
	}
	if usedTexels != report.AssetSourceBytes/4 {
		return fmt.Errorf("NCT1 pages use %d texels, decoded source has %d", usedTexels, report.AssetSourceBytes/4)
	}
	return nil
}

func nicoTimelineReasonContains(reasons []string, fragment string) bool {
	fragment = strings.ToLower(fragment)
	for _, reason := range reasons {
		if strings.Contains(strings.ToLower(reason), fragment) {
			return true
		}
	}
	return false
}

func validateNicoAssetTelemetryShape(raw json.RawMessage) error {
	var telemetryFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &telemetryFields); err != nil || telemetryFields == nil {
		return errors.New("assetTelemetry must be a JSON object")
	}
	for _, field := range []string{"telemetryScope", "effectiveLayout", "pages", "textureUploadCallCount", "textureUploadBytes", "drawOrderPageBindRunCount", "bundleBuildCpuWallTimeNs", "residentMemory", "unavailableReasons"} {
		if _, ok := telemetryFields[field]; !ok {
			return fmt.Errorf("assetTelemetry is missing %q", field)
		}
	}
	var residentFields map[string]json.RawMessage
	if err := json.Unmarshal(telemetryFields["residentMemory"], &residentFields); err != nil || residentFields == nil {
		return errors.New("assetTelemetry residentMemory must be a JSON object")
	}
	for _, field := range []string{"decodedScenePixelBytes", "logicalTextureAllocationBytes", "rendererOwnedDrawBufferPayloadBytes", "renderBundleInternalBytes", "readbackRingBufferBytes", "wgpuTransientStagingBytes", "physicalVramBytes", "unknownReasons"} {
		if _, ok := residentFields[field]; !ok {
			return fmt.Errorf("residentMemory is missing %q", field)
		}
	}
	if string(telemetryFields["pages"]) != "null" {
		var pages []json.RawMessage
		if err := json.Unmarshal(telemetryFields["pages"], &pages); err != nil || pages == nil {
			return errors.New("assetTelemetry pages must be an array or null")
		}
		for index, rawPage := range pages {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(rawPage, &fields); err != nil || fields == nil {
				return fmt.Errorf("assetTelemetry page %d must be an object", index)
			}
			for _, field := range []string{"pageIndex", "width", "height", "usedTexels", "allocatedTexels"} {
				if _, ok := fields[field]; !ok {
					return fmt.Errorf("assetTelemetry page %d is missing %q", index, field)
				}
			}
		}
	}
	return nil
}

func applyNicoTimelineRuntimeReportMetadata(destination *NicoEncodeReport, report nicoTimelineRuntimeReport) {
	destination.TimelineProtocol = report.Protocol
	destination.TimelineGPUBackend = report.Backend
	destination.TimelineGPUAdapter = report.AdapterName
	destination.TimelineReadbackSlots = report.ReadbackSlots
	destination.TimelineAssetLayoutRequested = report.RequestedAssetLayout
	destination.TimelineAssetLayout = report.AssetLayout
	destination.TimelineAssetLayoutFallback = report.AssetLayoutFallback
	destination.TimelineAssetPageCount = report.AssetPageCount
	destination.TimelineAssetSourceBytes = report.AssetSourceBytes
	destination.TimelineAssetAllocatedBytes = report.AssetAllocatedBytes
	destination.TimelineAssetTelemetry = report.AssetTelemetry
}

func validateNicoTimelineReportIdentity(report nicoTimelineRuntimeReport) error {
	if report.Schema != 1 || report.Protocol != "NCT2" || report.Renderer != "wgpu" || strings.TrimSpace(report.Version) == "" {
		return errors.New("niconico timeline: runtime report identity mismatch")
	}
	return nil
}

func hashNicoFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func createNicoAbsentDirectoryPath(finalPath, pattern string) (string, error) {
	parent := filepath.Dir(finalPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	if err := os.Remove(temp); err != nil {
		return "", err
	}
	return temp, nil
}

func reserveNicoBackupPath(finalPath, pattern string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(finalPath), pattern)
	if err != nil {
		return "", err
	}
	backup := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(backup)
		return "", err
	}
	if err := os.Remove(backup); err != nil {
		return "", err
	}
	return backup, nil
}

// promoteNicoTimelineArtifacts replaces an MP4 and HLS directory as one
// rollback-capable transaction. All staged paths must be complete first.
func promoteNicoTimelineArtifacts(stagedMP4, stagedHLS, outputMP4, outputHLS string) error {
	if strings.TrimSpace(stagedMP4) == "" || strings.TrimSpace(stagedHLS) == "" || strings.TrimSpace(outputMP4) == "" || strings.TrimSpace(outputHLS) == "" {
		return errors.New("niconico timeline: MP4 and HLS paths are required")
	}
	if info, err := os.Stat(stagedMP4); err != nil {
		return fmt.Errorf("niconico timeline: inspect staged MP4: %w", err)
	} else if info.Size() == 0 {
		return errors.New("niconico timeline: staged MP4 is empty")
	}
	if _, err := validateNicoPlaylist(filepath.Join(stagedHLS, "playlist.m3u8"), stagedHLS); err != nil {
		return err
	}
	backupMP4, backupHLS := "", ""
	moveOld := func(path, pattern string) (string, error) {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return "", nil
		} else if err != nil {
			return "", err
		}
		backup, err := reserveNicoBackupPath(path, pattern)
		if err != nil {
			return "", err
		}
		if err := renameNicoTimelineArtifact(path, backup); err != nil {
			return "", err
		}
		return backup, nil
	}
	backupMP4, err := moveOld(outputMP4, ".niconico-previous-*.mp4")
	if err != nil {
		return fmt.Errorf("niconico timeline: stage previous MP4: %w", err)
	}
	backupHLS, err = moveOld(outputHLS, ".niconico-previous-hls-*")
	if err != nil {
		if backupMP4 != "" {
			_ = renameNicoTimelineArtifact(backupMP4, outputMP4)
		}
		return fmt.Errorf("niconico timeline: stage previous HLS: %w", err)
	}
	newMP4, newHLS := false, false
	rollback := func() error {
		var rollbackErrors []error
		if newHLS {
			if err := os.RemoveAll(outputHLS); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new HLS: %w", err))
			}
		}
		if newMP4 {
			if err := os.Remove(outputMP4); err != nil && !os.IsNotExist(err) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new MP4: %w", err))
			}
		}
		if backupMP4 != "" {
			if err := renameNicoTimelineArtifact(backupMP4, outputMP4); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous MP4: %w", err))
			}
		}
		if backupHLS != "" {
			if err := renameNicoTimelineArtifact(backupHLS, outputHLS); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous HLS: %w", err))
			}
		}
		return errors.Join(rollbackErrors...)
	}
	if err := renameNicoTimelineArtifact(stagedMP4, outputMP4); err != nil {
		return errors.Join(fmt.Errorf("niconico timeline: promote MP4: %w", err), rollback())
	}
	newMP4 = true
	if err := renameNicoTimelineArtifact(stagedHLS, outputHLS); err != nil {
		return errors.Join(fmt.Errorf("niconico timeline: promote HLS: %w", err), rollback())
	}
	newHLS = true
	if backupMP4 != "" {
		if err := os.Remove(backupMP4); err != nil {
			reportNicoFallbackCleanup("niconico: previous MP4 cleanup failed path=%q: %v", backupMP4, err)
		}
	}
	if backupHLS != "" {
		if err := os.RemoveAll(backupHLS); err != nil {
			reportNicoFallbackCleanup("niconico: previous HLS cleanup failed path=%q: %v", backupHLS, err)
		}
	}
	return nil
}

package video

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

var prepareNicoNativeCompositor = nicorender.PrepareNativeCompositorWithMode
var encodeNicoNativeForPipeline = encodeNicoNative
var encodeNicoBrowserForPipeline = encodeNicoBrowser
var encodeNicoTimelineForPipeline = encodeNicoTimeline
var removeNicoFallbackBackup = os.Remove
var reportNicoFallbackCleanup = log.Printf

type nicoTimelineFailure interface {
	nicoTimelineFailureStage() string
}

type nicoTimelineStageError struct {
	stage string
	err   error
}

func (e *nicoTimelineStageError) Error() string {
	return fmt.Sprintf("niconico timeline %s: %v", e.stage, e.err)
}

func (e *nicoTimelineStageError) Unwrap() error { return e.err }

func (e *nicoTimelineStageError) nicoTimelineFailureStage() string { return e.stage }

func wrapNicoTimelineStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &nicoTimelineStageError{stage: stage, err: err}
}

func nicoTimelineFailureStageOf(err error) string {
	var staged nicoTimelineFailure
	if errors.As(err, &staged) {
		return staged.nicoTimelineFailureStage()
	}
	return ""
}

// EncodeNicoCommentedWithRenderer selects the embedded WARP compositor when
// available, otherwise the browser RGBA pipeline. Both use the same encoder.
func EncodeNicoCommentedWithRenderer(ctx context.Context, ffmpeg, sourcePath, outputPath string, snapshot niconico.Snapshot, renderOptions nicorender.RenderOptions, encodeOptions NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return NicoEncodeReport{}, nicorender.RenderReport{}, err
	}
	if _, err := encodeOptions.validate(); err != nil {
		return NicoEncodeReport{}, nicorender.RenderReport{}, err
	}
	if renderOptions.Width != encodeOptions.Width || renderOptions.Height != encodeOptions.Height || renderOptions.DurationMs != encodeOptions.DurationMs || renderOptions.FPSNum != encodeOptions.FPSNum || renderOptions.FPSDen != encodeOptions.FPSDen {
		return NicoEncodeReport{}, nicorender.RenderReport{}, fmt.Errorf("niconico: renderer and encoder geometry/timeline differ")
	}
	backend := strings.TrimSpace(renderOptions.Backend)
	if backend == "" {
		backend = strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_RENDERER"))
	}
	if backend == "" {
		backend = "auto"
	}
	backend = strings.ToLower(backend)
	if backend != "auto" && backend != "browser" && backend != "native" && backend != "timeline" {
		return NicoEncodeReport{}, nicorender.RenderReport{}, fmt.Errorf("niconico: unknown backend %q", backend)
	}
	fallback := ""
	var timelineAttempt *NicoEncodeAttempt
	selection := timelineSelection(backend, renderOptions.TimelineEnabled)
	if selection != "legacy" {
		timelineEncode, timelineRender, timelineErr := encodeNicoTimelineForPipeline(ctx, ffmpeg, sourcePath, outputPath, snapshot, renderOptions, encodeOptions)
		if timelineErr == nil {
			return timelineEncode, timelineRender, nil
		}
		if ctx.Err() != nil {
			return timelineEncode, timelineRender, ctx.Err()
		}
		if stage := nicoTimelineFailureStageOf(timelineErr); stage != "" && stage != "renderer" {
			// An encoder or output failure is not evidence of a WGPU failure;
			// retrying comment rendering would waste time and report a false fallback.
			return timelineEncode, timelineRender, timelineErr
		}
		timelineAttempt = &NicoEncodeAttempt{
			Backend: "timeline-wgpu", Error: timelineErr.Error(),
			HelperSHA256:            timelineEncode.TimelineHelperSHA256,
			BundleSHA256:            timelineEncode.TimelineBundleSHA256,
			GPUBackend:              timelineEncode.TimelineGPUBackend,
			GPUAdapter:              timelineEncode.TimelineGPUAdapter,
			ReadbackSlots:           timelineEncode.TimelineReadbackSlots,
			StageTimings:            append([]NicoStageTiming(nil), timelineEncode.StageTimings...),
			TimelineProtocol:        timelineEncode.TimelineProtocol,
			TimelineCaptureDone:     timelineEncode.TimelineCaptureDone,
			TimelineFirstAssetReady: timelineEncode.TimelineFirstAssetReady,
			TimelineFirstFrame:      timelineEncode.TimelineFirstFrame,
			TimelineStreamEnd:       timelineEncode.TimelineStreamEnd,
			TimelineHelperDone:      timelineEncode.TimelineHelperDone,
			TimelineFFmpegDone:      timelineEncode.TimelineFFmpegDone,
		}
		fallback = fmt.Sprintf("timeline renderer failed: %v", timelineErr)
		log.Printf("niconico: timeline runtime failed; trying legacy renderer (%s)", timelineErr)
		if renderOptions.OnTimelineFallback != nil {
			renderOptions.OnTimelineFallback()
			if err := ctx.Err(); err != nil {
				return timelineEncode, timelineRender, err
			}
		}
	}
	outputExisted := false
	if _, statErr := os.Stat(outputPath); statErr == nil {
		outputExisted = true
	} else if !os.IsNotExist(statErr) {
		return NicoEncodeReport{}, nicorender.RenderReport{}, statErr
	}
	if backend != "browser" {
		configured := renderOptions.CompositorPath
		if configured == "" {
			configured = os.Getenv("IMAGEPAD_NICO_COMPOSITOR")
		}
		path, cleanup, err := prepareNicoNativeCompositor(ctx, configured, renderOptions.CompositorDevice)
		if err == nil {
			defer cleanup()
			log.Printf("niconico: renderer backend=native-warp")
			er, rr, nativeErr := encodeNicoNativeForPipeline(ctx, path, ffmpeg, sourcePath, outputPath, snapshot, renderOptions, encodeOptions)
			if nativeErr == nil {
				er = attachNicoTimelineAttempt(er, timelineAttempt)
				if timelineAttempt != nil {
					rr.FallbackReason = fallback
				}
				return er, rr, nil
			}
			if ctx.Err() != nil {
				return NicoEncodeReport{}, nicorender.RenderReport{}, ctx.Err()
			}
			if backend == "native" {
				return NicoEncodeReport{}, nicorender.RenderReport{}, nativeErr
			}
			if !outputExisted {
				if removeErr := os.Remove(outputPath); removeErr != nil && !os.IsNotExist(removeErr) {
					return NicoEncodeReport{}, nicorender.RenderReport{}, fmt.Errorf("niconico: native fallback cleanup: %w", removeErr)
				}
			}
			fallback = appendNicoFallbackReason(fallback, nativeErr.Error())
			log.Printf("niconico: native runtime failed; regenerating with browser (%s)", fallback)
		}
		if err != nil && ctx.Err() != nil {
			return NicoEncodeReport{}, nicorender.RenderReport{}, ctx.Err()
		}
		if err != nil && backend == "native" {
			return NicoEncodeReport{}, nicorender.RenderReport{}, err
		}
		if err != nil {
			fallback = appendNicoFallbackReason(fallback, err.Error())
			log.Printf("niconico: renderer backend=browser (%s)", fallback)
		}
	}
	fallbackOutputPath, cleanupFallback, err := createNicoFallbackOutput(outputPath)
	if err != nil {
		return NicoEncodeReport{}, nicorender.RenderReport{}, err
	}
	defer cleanupFallback()
	er, rr, err := encodeNicoBrowserForPipeline(ctx, ffmpeg, sourcePath, fallbackOutputPath, snapshot, renderOptions, encodeOptions)
	rr.Backend = "browser"
	rr.FallbackReason = fallback
	if err != nil {
		return NicoEncodeReport{}, rr, err
	}
	if err := promoteNicoFallbackOutput(fallbackOutputPath, outputPath); err != nil {
		return NicoEncodeReport{}, rr, err
	}
	er.OutputPath = outputPath
	er = attachNicoTimelineAttempt(er, timelineAttempt)
	return er, rr, nil
}

func attachNicoTimelineAttempt(report NicoEncodeReport, attempt *NicoEncodeAttempt) NicoEncodeReport {
	if attempt != nil {
		report.Attempts = append(report.Attempts, *attempt)
	}
	return report
}

func appendNicoFallbackReason(current, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	switch {
	case current == "":
		return next
	case next == "":
		return current
	default:
		return current + "; " + next
	}
}

func createNicoFallbackOutput(outputPath string) (string, func(), error) {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", func() {}, err
	}
	f, err := os.CreateTemp(dir, ".niconico-fallback-*.mp4")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", func() {}, err
	}
	if err := os.Remove(path); err != nil {
		return "", func() {}, err
	}
	return path, func() { _ = os.Remove(path) }, nil
}

func promoteNicoFallbackOutput(sourcePath, outputPath string) error {
	if filepath.Clean(sourcePath) == filepath.Clean(outputPath) {
		return nil
	}
	backupPath := ""
	if _, err := os.Stat(outputPath); err == nil {
		backup, createErr := os.CreateTemp(filepath.Dir(outputPath), ".niconico-previous-*.mp4")
		if createErr != nil {
			return createErr
		}
		backupPath = backup.Name()
		if closeErr := backup.Close(); closeErr != nil {
			_ = os.Remove(backupPath)
			return closeErr
		}
		if removeErr := removeNicoFallbackBackup(backupPath); removeErr != nil {
			return removeErr
		}
		if err := os.Rename(outputPath, backupPath); err != nil {
			return err
		}
	}
	if err := os.Rename(sourcePath, outputPath); err != nil {
		if backupPath != "" {
			_ = os.Rename(backupPath, outputPath)
		}
		return err
	}
	if backupPath != "" {
		if err := removeNicoFallbackBackup(backupPath); err != nil {
			reportNicoFallbackCleanup("niconico: fallback backup cleanup failed path=%q: %v", backupPath, err)
		}
	}
	return nil
}

func encodeNicoBrowser(ctx context.Context, ffmpeg, sourcePath, outputPath string, snapshot niconico.Snapshot, renderOptions nicorender.RenderOptions, encodeOptions NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pipe := nicorender.NewFramePipe(2)
	renderDone := make(chan struct{})
	var renderReport nicorender.RenderReport
	var renderErr error
	go func() {
		defer close(renderDone)
		renderReport, renderErr = nicorender.Render(runCtx, snapshot, renderOptions, pipe)
		pipe.Close(renderErr)
	}()
	encodeReport, encodeErr := EncodeNicoCommented(runCtx, ffmpeg, sourcePath, outputPath, encodeOptions, pipe)
	if encodeErr != nil {
		cancel()
	}
	<-renderDone
	if encodeErr != nil {
		return NicoEncodeReport{}, renderReport, encodeErr
	}
	if renderErr != nil {
		return NicoEncodeReport{}, renderReport, fmt.Errorf("niconico: renderer: %w", renderErr)
	}
	return encodeReport, renderReport, nil
}

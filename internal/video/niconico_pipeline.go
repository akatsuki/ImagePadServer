package video

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

var prepareNicoNativeCompositor = nicorender.PrepareNativeCompositor
var encodeNicoNativeForPipeline = encodeNicoNative
var encodeNicoBrowserForPipeline = encodeNicoBrowser
var removeNicoFallbackBackup = os.Remove
var reportNicoFallbackCleanup = log.Printf

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
	if backend != "auto" && backend != "browser" && backend != "native" {
		return NicoEncodeReport{}, nicorender.RenderReport{}, fmt.Errorf("niconico: unknown backend %q", backend)
	}
	fallback := ""
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
		path, cleanup, err := prepareNicoNativeCompositor(ctx, configured)
		if err == nil {
			defer cleanup()
			log.Printf("niconico: renderer backend=native-warp")
			er, rr, nativeErr := encodeNicoNativeForPipeline(ctx, path, ffmpeg, sourcePath, outputPath, snapshot, renderOptions, encodeOptions)
			if nativeErr == nil {
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
			fallback = nativeErr.Error()
			log.Printf("niconico: native runtime failed; regenerating with browser (%s)", fallback)
		}
		if err != nil && ctx.Err() != nil {
			return NicoEncodeReport{}, nicorender.RenderReport{}, ctx.Err()
		}
		if err != nil && backend == "native" {
			return NicoEncodeReport{}, nicorender.RenderReport{}, err
		}
		if err != nil {
			fallback = err.Error()
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
	return er, rr, nil
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

package video

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

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
	if backend != "browser" {
		configured := renderOptions.CompositorPath
		if configured == "" {
			configured = os.Getenv("IMAGEPAD_NICO_COMPOSITOR")
		}
		path, cleanup, err := nicorender.PrepareNativeCompositor(ctx, configured)
		if err == nil {
			defer cleanup()
			log.Printf("niconico: renderer backend=native-warp")
			return encodeNicoNative(ctx, path, ffmpeg, sourcePath, outputPath, snapshot, renderOptions, encodeOptions)
		}
		if ctx.Err() != nil {
			return NicoEncodeReport{}, nicorender.RenderReport{}, ctx.Err()
		}
		if backend == "native" {
			return NicoEncodeReport{}, nicorender.RenderReport{}, err
		}
		fallback = err.Error()
		log.Printf("niconico: renderer backend=browser (%s)", fallback)
	}
	er, rr, err := encodeNicoBrowser(ctx, ffmpeg, sourcePath, outputPath, snapshot, renderOptions, encodeOptions)
	rr.Backend = "browser"
	rr.FallbackReason = fallback
	return er, rr, err
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

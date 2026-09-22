package nicoexportworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
	"imagepadserver/internal/video"
)

var nicoExportWithRenderer = video.ExportNicoCommented
var nicoPrepareHLS = video.PrepareNicoHLSForID

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
	_, _, err = nicoExportWithRenderer(runCtx, request.FFmpeg, request.SourcePath, request.OutputPath, request.HLSStagingDir, snapshot, renderOptions, encodeOptions, outputMode)
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
	if err := emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, Output: request.OutputPath, Playlist: prepared.Playlist, OK: true}); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "nico export worker complete run_id=%s media_id=%s\n", request.RunID, request.MediaID)
	failed = false
	return nil
}

func currentEmitError(mu *sync.Mutex, value *error) error {
	mu.Lock()
	defer mu.Unlock()
	return *value
}

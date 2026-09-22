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

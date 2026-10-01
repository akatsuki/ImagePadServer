package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

// TestNicoTimelineIntegrationEncodersMP4HLS runs the production NCT2 -> WGPU
// -> FFmpeg path against a small deterministic scene. It is opt-in because it
// starts a real GPU helper and encoders. Artifacts are retained under
// NICO_TIMELINE_ARTIFACTS when that directory is supplied.
func TestNicoTimelineIntegrationEncodersMP4HLS(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_INTEGRATION") != "1" {
		t.Skip("set NICO_TIMELINE_INTEGRATION=1 to run real WGPU/FFmpeg output qualification")
	}
	ffmpeg := requiredNicoTimelineIntegrationEnv(t, "NICO_TIMELINE_FFMPEG")
	helper := requiredNicoTimelineIntegrationEnv(t, "IMAGEPAD_NICO_TIMELINE_COMPOSITOR")
	if info, err := os.Stat(helper); err != nil || info.IsDir() {
		t.Fatalf("timeline compositor is unavailable: %s (%v)", helper, err)
	}
	ffprobe := strings.TrimSpace(os.Getenv("NICO_TIMELINE_FFPROBE"))
	if ffprobe == "" {
		name := "ffprobe"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		ffprobe = filepath.Join(filepath.Dir(ffmpeg), name)
	}
	if _, err := os.Stat(ffprobe); err != nil {
		t.Fatalf("ffprobe is unavailable: %s (%v)", ffprobe, err)
	}
	artifactRoot := strings.TrimSpace(os.Getenv("NICO_TIMELINE_ARTIFACTS"))
	if artifactRoot == "" {
		artifactRoot = t.TempDir()
	}
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp(artifactRoot, "timeline-encode-*")
	if err != nil {
		t.Fatal(err)
	}
	backend := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_TIMELINE_GPU_BACKEND"))
	if backend == "" {
		backend = "vulkan"
	}

	const width, height = 320, 180
	const fpsNum, fpsDen = int64(30), int64(1)
	const durationMs = int64(300)
	clock := niconico.MustFrameClock(fpsNum, fpsDen)
	frameCount := clock.FrameCountForDurationMs(durationMs)
	renderOptions := nicorender.RenderOptions{
		Backend: "timeline", TimelineCompositorPath: helper, TimelineGPUBackend: backend, TimelineReadbackSlots: 2,
		Width: width, Height: height, DurationMs: durationMs, FPSNum: fpsNum, FPSDen: fpsDen,
	}
	scene, captureReport := makeNicoTimelineIntegrationScene(renderOptions)
	emptyScene := scene
	emptyScene.Assets = nil
	emptyScene.Draws = nil
	verifyNicoTimelineIntegrationTransparentSuccess(t, helper, backend, runDir, emptyScene)
	verifyNicoTimelineIntegrationEmptyNCT2(t, helper, backend, runDir, emptyScene)
	oldCapture := captureNicoTimelineStreamForPipeline
	captureNicoTimelineStreamForPipeline = func(ctx context.Context, snapshot niconico.Snapshot, options nicorender.RenderOptions, out io.Writer) (nicorender.TimelineCaptureReport, error) {
		if err := ctx.Err(); err != nil {
			return nicorender.TimelineCaptureReport{}, err
		}
		if snapshot.VideoID != "sm9" {
			return nicorender.TimelineCaptureReport{}, fmt.Errorf("unexpected integration snapshot video ID %q", snapshot.VideoID)
		}
		if options.Width != width || options.Height != height || options.FPSNum != fpsNum || options.FPSDen != fpsDen || options.DurationMs != durationMs {
			return nicorender.TimelineCaptureReport{}, fmt.Errorf("unexpected integration render options: %+v", options)
		}
		stream := nicorender.CommentTimelineStream{
			Header: scene.Header,
			Declarations: []nicorender.TimelineStreamDeclaration{{
				Ordinal: 0, OwnerOrder: 0, CommentIndex: 0, StartVPos: 0, EndVPos: 100,
			}},
			Assets:   scene.Assets,
			Elements: []nicorender.TimelineStreamElement{{Ordinal: 0, Draws: scene.Draws}},
		}
		if err := nicorender.WriteCommentTimelineStream(out, stream); err != nil {
			return nicorender.TimelineCaptureReport{}, fmt.Errorf("write integration NCT2 stream: %w", err)
		}
		return captureReport, nil
	}
	t.Cleanup(func() { captureNicoTimelineStreamForPipeline = oldCapture })

	for _, audioMode := range []string{"short-audio", "no-audio"} {
		audioSource := filepath.Join(runDir, "source-"+audioMode+".mp4")
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=30:d=1"}
		if audioMode == "short-audio" {
			args = append(args, "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=0.10", "-map", "0:v:0", "-map", "1:a:0")
		} else {
			args = append(args, "-map", "0:v:0")
		}
		args = append(args, "-t", "0.5", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
		if audioMode == "short-audio" {
			args = append(args, "-c:a", "aac")
		}
		args = append(args, audioSource)
		if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("create %s source: %v: %s", audioMode, err, output)
		}

		for _, encoder := range []string{"x264", "nvenc"} {
			t.Run(audioMode+"/"+encoder, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				outputPath := filepath.Join(runDir, audioMode+"-"+encoder+".mp4")
				hlsDir := filepath.Join(runDir, audioMode+"-"+encoder+"-hls")
				preset := "veryfast"
				if encoder == "nvenc" {
					preset = "p4"
				}
				snapshot := niconico.Snapshot{SchemaVersion: 1, VideoID: "sm9", CommentStatus: "ready", CommentCount: 1}
				encodeReport, renderReport, err := encodeNicoTimeline(ctx, ffmpeg, audioSource, outputPath, snapshot, renderOptions,
					NicoEncodeOptions{
						Width: width, Height: height, DurationMs: durationMs, FPSNum: fpsNum, FPSDen: fpsDen,
						CRF: 26, Preset: preset, Encoder: encoder, OutputMode: NicoOutputTee, HLSOutputDir: hlsDir,
					})
				if err != nil {
					t.Fatalf("encode timeline with %s: %v (artifacts=%s)", encoder, err, runDir)
				}
				if encodeReport.FrameCount != frameCount || renderReport.FrameCount != frameCount || renderReport.Backend != "timeline-wgpu" {
					t.Fatalf("unexpected reports: encode=%+v render=%+v", encodeReport, renderReport)
				}
				verifyNicoTimelineIntegrationMedia(t, ffmpeg, ffprobe, outputPath, audioMode == "short-audio", frameCount)
				playlist := filepath.Join(hlsDir, "playlist.m3u8")
				segments, err := filepath.Glob(filepath.Join(hlsDir, "*.ts"))
				if err != nil || len(segments) == 0 {
					t.Fatalf("HLS segments=%v err=%v", segments, err)
				}
				verifyNicoTimelineIntegrationMedia(t, ffmpeg, ffprobe, playlist, audioMode == "short-audio", frameCount)
				if slices := verifyNicoTimelineIntegrationSingleSlice(t, ffmpeg, outputPath, frameCount); slices != int(frameCount) {
					t.Fatalf("MP4 VCL slices=%d, want %d", slices, frameCount)
				}
				for _, segment := range segments {
					if slices := verifyNicoTimelineIntegrationSingleSlice(t, ffmpeg, segment, -1); slices == 0 {
						t.Fatalf("HLS segment %s has no VCL slices", segment)
					}
				}
				verifyNicoTimelineIntegrationCommentPixels(t, ffmpeg, outputPath)
				verifyNicoTimelineIntegrationFaststart(t, outputPath)
				t.Logf("encoder=%s audio=%s frames=%d adapter=%s backend=%s mp4=%s hls=%s", encoder, audioMode, frameCount, renderReport.RendererLabel, encodeReport.TimelineGPUBackend, outputPath, playlist)
			})
		}
	}
}

// TestNicoTimelineIntegrationQ40RangeFailureFallsBackToBrowser exercises a
// finite base value that fits in f32 but is outside the helper's signed Q40.40
// range. It is opt-in because it runs the production timeline helper, GPU
// compositor, browser renderer, and FFmpeg pipeline.
func TestNicoTimelineIntegrationQ40RangeFailureFallsBackToBrowser(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_INTEGRATION") != "1" {
		t.Skip("set NICO_TIMELINE_INTEGRATION=1 to run real WGPU/FFmpeg fallback qualification")
	}
	ffmpeg := requiredNicoTimelineIntegrationEnv(t, "NICO_TIMELINE_FFMPEG")
	helper := requiredNicoTimelineIntegrationEnv(t, "IMAGEPAD_NICO_TIMELINE_COMPOSITOR")
	if info, err := os.Stat(helper); err != nil || info.IsDir() {
		t.Fatalf("timeline compositor is unavailable: %s (%v)", helper, err)
	}
	ffprobe := strings.TrimSpace(os.Getenv("NICO_TIMELINE_FFPROBE"))
	if ffprobe == "" {
		name := "ffprobe"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		ffprobe = filepath.Join(filepath.Dir(ffmpeg), name)
	}
	if _, err := os.Stat(ffprobe); err != nil {
		t.Fatalf("ffprobe is unavailable: %s (%v)", ffprobe, err)
	}
	runDir := t.TempDir()
	backend := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_TIMELINE_GPU_BACKEND"))
	if backend == "" {
		backend = "vulkan"
	}
	const width, height = 320, 180
	const fpsNum, fpsDen = int64(30), int64(1)
	const durationMs = int64(300)
	clock := niconico.MustFrameClock(fpsNum, fpsDen)
	frameCount := clock.FrameCountForDurationMs(durationMs)

	sourcePath := filepath.Join(runDir, "source.mp4")
	sourceArgs := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=30:d=1", "-t", "0.3", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", sourcePath}
	if output, err := exec.Command(ffmpeg, sourceArgs...).CombinedOutput(); err != nil {
		t.Fatalf("create fallback source: %v: %s", err, output)
	}

	options := nicorender.RenderOptions{
		Backend: "timeline", TimelineCompositorPath: helper, TimelineGPUBackend: backend, TimelineReadbackSlots: 2,
		Width: width, Height: height, DurationMs: durationMs, FPSNum: fpsNum, FPSDen: fpsDen,
	}
	scene, captureReport := makeNicoTimelineIntegrationScene(options)
	const outOfRangeBase = 1 << 23
	scene.Draws[0].AnchorX = outOfRangeBase
	scene.Draws[0].Rect[0] = outOfRangeBase
	scene.Draws[0].SpeedX = 0
	// Verify the exact regression value is finite and accepted by the NCT2 writer.
	if math.IsInf(float64(scene.Draws[0].AnchorX), 0) || math.IsNaN(float64(scene.Draws[0].AnchorX)) {
		t.Fatal("Q40.40 range fixture must be finite")
	}
	var wire bytes.Buffer
	stream := nicorender.CommentTimelineStream{
		Header: scene.Header,
		Declarations: []nicorender.TimelineStreamDeclaration{{
			Ordinal: 0, OwnerOrder: 0, CommentIndex: 0, StartVPos: 0, EndVPos: 100,
		}},
		Assets:   scene.Assets,
		Elements: []nicorender.TimelineStreamElement{{Ordinal: 0, Draws: scene.Draws}},
	}
	if err := nicorender.WriteCommentTimelineStream(&wire, stream); err != nil {
		t.Fatalf("encode finite out-of-range NCT2 fixture: %v", err)
	}

	oldCapture := captureNicoTimelineStreamForPipeline
	oldPrepareNative := prepareNicoNativeCompositor
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		captureNicoTimelineStreamForPipeline = oldCapture
		prepareNicoNativeCompositor = oldPrepareNative
		encodeNicoBrowserForPipeline = oldBrowser
	})
	var captureCalls int
	captureNicoTimelineStreamForPipeline = func(ctx context.Context, snapshot niconico.Snapshot, gotOptions nicorender.RenderOptions, out io.Writer) (nicorender.TimelineCaptureReport, error) {
		captureCalls++
		if snapshot.VideoID != "sm9" {
			return nicorender.TimelineCaptureReport{}, fmt.Errorf("unexpected integration snapshot video ID %q", snapshot.VideoID)
		}
		if gotOptions.Backend != "timeline" || gotOptions.Width != width || gotOptions.Height != height || gotOptions.DurationMs != durationMs || gotOptions.FPSNum != fpsNum || gotOptions.FPSDen != fpsDen {
			return nicorender.TimelineCaptureReport{}, fmt.Errorf("unexpected integration render options: %+v", gotOptions)
		}
		if _, err := io.Copy(out, bytes.NewReader(wire.Bytes())); err != nil {
			return nicorender.TimelineCaptureReport{}, err
		}
		return captureReport, nil
	}
	// The production selector tries native WARP before browser after a timeline
	// error. Force that optional route unavailable so this test qualifies the
	// actual browser fallback without launching a separately configured service.
	prepareNicoNativeCompositor = func(context.Context, string, string) (string, func(), error) {
		return "", nil, fmt.Errorf("native compositor omitted from timeline fallback integration")
	}
	var browserCalls, successfulBrowserCalls int
	encodeNicoBrowserForPipeline = func(ctx context.Context, ffmpeg, source, destination string, snapshot niconico.Snapshot, render nicorender.RenderOptions, encode NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		browserCalls++
		report, renderReport, err := oldBrowser(ctx, ffmpeg, source, destination, snapshot, render, encode)
		if err == nil {
			successfulBrowserCalls++
		}
		return report, renderReport, err
	}

	var fallbackNotices int
	options.OnTimelineFallback = func() { fallbackNotices++ }
	outputPath := filepath.Join(runDir, "fallback.mp4")
	snapshot := niconico.Snapshot{
		SchemaVersion: 1, VideoID: "sm9", CommentStatus: "ready", CommentCount: 1,
		Threads: []niconico.Thread{{Fork: "main", Comments: []niconico.Comment{{ID: "fallback-comment", Body: "fallback", VposMs: 0}}}},
	}
	encodeOptions := NicoEncodeOptions{
		Width: width, Height: height, DurationMs: durationMs, FPSNum: fpsNum, FPSDen: fpsDen,
		CRF: 26, Preset: "ultrafast", Encoder: "x264", OutputMode: NicoOutputSeparate,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	encodeReport, renderReport, err := EncodeNicoCommentedWithRenderer(ctx, ffmpeg, sourcePath, outputPath, snapshot, options, encodeOptions)
	if err != nil {
		t.Fatalf("production timeline-to-browser fallback: %v", err)
	}
	if captureCalls != 1 {
		t.Fatalf("NCT2 capture calls=%d, want 1", captureCalls)
	}
	if fallbackNotices != 1 {
		t.Fatalf("OnTimelineFallback notifications=%d, want exactly 1", fallbackNotices)
	}
	if browserCalls != 1 || successfulBrowserCalls != 1 {
		t.Fatalf("browser fallback calls=%d successful=%d, want exactly one successful attempt", browserCalls, successfulBrowserCalls)
	}
	if renderReport.Backend != "browser" || strings.TrimSpace(renderReport.FallbackReason) == "" {
		t.Fatalf("render report does not show browser fallback: %+v", renderReport)
	}
	if len(encodeReport.Attempts) != 1 || !strings.Contains(strings.ToLower(encodeReport.Attempts[0].Error), "q40") {
		t.Fatalf("timeline attempt should contain the helper Q40.40 range error: %+v", encodeReport.Attempts)
	}
	if encodeReport.FrameCount != frameCount || encodeReport.OutputPath != outputPath {
		t.Fatalf("fallback encode report does not describe complete published output: %+v", encodeReport)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("fallback output was not published: %v", err)
	}
	verifyNicoTimelineIntegrationMedia(t, ffmpeg, ffprobe, outputPath, false, frameCount)
	if raw, err := exec.Command(ffmpeg, "-v", "error", "-i", outputPath, "-map", "0:v:0", "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-").Output(); err != nil {
		t.Fatalf("decode published browser fallback frame: %v", err)
	} else if len(raw) != width*height*3 || raw[(100*width+100)*3+2] < 100 || raw[(100*width+100)*3] > 120 {
		t.Fatalf("published fallback frame is incomplete or does not contain the blue source: bytes=%d pixel=%v", len(raw), raw[(100*width+100)*3:(100*width+100)*3+3])
	}
	if slices := verifyNicoTimelineIntegrationSingleSlice(t, ffmpeg, outputPath, frameCount); slices != int(frameCount) {
		t.Fatalf("fallback H.264 VCL slices=%d, want %d", slices, frameCount)
	}
	t.Logf("Q40.40 timeline failure fell back to browser; published %d-frame output at %s", frameCount, outputPath)
}

func verifyNicoTimelineIntegrationEmptyNCT2(t *testing.T, helper, backend, artifactDir string, scene nicorender.CommentTimeline) {
	t.Helper()
	var wire bytes.Buffer
	if err := nicorender.WriteCommentTimelineStream(&wire, nicorender.CommentTimelineStream{Header: scene.Header}); err != nil {
		t.Fatalf("encode zero-asset NCT2 stream: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reportPath := filepath.Join(artifactDir, "empty-nct2-runtime.json")
	cmd := exec.CommandContext(ctx, helper, "--stdin-stream", "--backend", backend, "--readback-slots", "2", "--report", reportPath)
	cmd.Stdin = bytes.NewReader(wire.Bytes())
	var rgba, stderr bytes.Buffer
	cmd.Stdout = &rgba
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("render empty NCT2 stream: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var report struct {
		Protocol       string `json:"protocol"`
		AssetPageCount int    `json:"assetPageCount"`
	}
	encodedReport, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read empty NCT2 runtime report: %v", err)
	}
	if err := json.Unmarshal(encodedReport, &report); err != nil {
		t.Fatalf("decode empty NCT2 runtime report: %v", err)
	}
	if report.Protocol != "NCT2" || report.AssetPageCount != 0 {
		t.Fatalf("empty NCT2 report protocol=%q assetPages=%d", report.Protocol, report.AssetPageCount)
	}
	if got := strings.Count(stderr.String(), "NICO_FIRST_ASSET_READY\n"); got != 0 {
		t.Fatalf("empty NCT2 emitted first-asset marker %d times", got)
	}
	if got := strings.Count(stderr.String(), "NICO_STREAM_END\n"); got != 1 {
		t.Fatalf("empty NCT2 stream-end marker count=%d, want 1", got)
	}
	if got, want := len(rgba.Bytes()), int(scene.Header.FrameCount)*int(scene.Header.Width)*int(scene.Header.Height)*4; got != want {
		t.Fatalf("empty NCT2 RGBA bytes=%d, want %d", got, want)
	}
	for i, value := range rgba.Bytes() {
		if value != 0 {
			t.Fatalf("empty NCT2 transparent frame has nonzero byte at %d: %d", i, value)
		}
	}
}

func makeNicoTimelineIntegrationScene(options nicorender.RenderOptions) (nicorender.CommentTimeline, nicorender.TimelineCaptureReport) {
	clock := niconico.MustFrameClock(options.FPSNum, options.FPSDen)
	bundleHash := sha256.Sum256([]byte("Nico timeline integration fixture bundle"))
	assetRGBA := make([]byte, 64*32*4)
	for i := 0; i < len(assetRGBA); i += 4 {
		assetRGBA[i], assetRGBA[i+1], assetRGBA[i+2], assetRGBA[i+3] = 255, 0, 0, 255
	}
	assetHash := sha256.Sum256(assetRGBA)
	projection := [16]float32{2.0 / 320, 0, 0, 0, 0, -2.0 / 180, 0, 0, 0, 0, 1, 0, -1, 1, 0, 1}
	scene := nicorender.CommentTimeline{
		Header: nicorender.TimelineHeader{
			Width: uint32(options.Width), Height: uint32(options.Height),
			FrameCount: uint32(clock.FrameCountForDurationMs(options.DurationMs)),
			FPSNum:     uint32(options.FPSNum), FPSDen: uint32(options.FPSDen), BundleSHA256: bundleHash,
		},
		Assets: []nicorender.TimelineAsset{{ID: 1, Width: 64, Height: 32, SHA256: assetHash, RGBA: assetRGBA}},
		Draws: []nicorender.TimelineDraw{{
			AssetID: 1, StartVPos: 0, EndVPos: 100, Rect: [4]float32{8, 8, 64, 32},
			Projection: projection, Alpha: 1, AnchorX: 8,
		}},
	}
	return scene, nicorender.TimelineCaptureReport{
		RenderReport:     nicorender.RenderReport{FrameCount: int64(scene.Header.FrameCount), Width: options.Width, Height: options.Height, FPSNum: options.FPSNum, FPSDen: options.FPSDen},
		EligibleComments: 1, TimelineDraws: 1, TimelineAssets: 1, BundleSHA256: hex.EncodeToString(bundleHash[:]),
	}
}

func requiredNicoTimelineIntegrationEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("NICO_TIMELINE_INTEGRATION=1 requires %s", key)
	}
	return value
}

func verifyNicoTimelineIntegrationTransparentSuccess(t *testing.T, helper, backend, artifactDir string, scene nicorender.CommentTimeline) {
	t.Helper()
	var wire bytes.Buffer
	if err := nicorender.WriteCommentTimeline(&wire, scene); err != nil {
		t.Fatalf("encode zero-visible NCT1 scene: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report := filepath.Join(artifactDir, "no-visible-runtime.json")
	cmd := exec.CommandContext(ctx, helper, "--stdin", "--backend", backend, "--readback-slots", "2", "--report", report)
	cmd.Stdin = &wire
	var rgba, stderr bytes.Buffer
	cmd.Stdout = &rgba
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("render zero-visible NCT1 scene: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	wantBytes := scene.Header.Width * scene.Header.Height * 4 * scene.Header.FrameCount
	if uint64(rgba.Len()) != uint64(wantBytes) {
		t.Fatalf("transparent render bytes=%d, want %d", rgba.Len(), wantBytes)
	}
	for i, value := range rgba.Bytes() {
		if value != 0 {
			t.Fatalf("zero-visible GPU output is not transparent at byte %d: %d", i, value)
		}
	}
}

func verifyNicoTimelineIntegrationMedia(t *testing.T, ffmpeg, ffprobe, input string, wantAudio bool, frameCount int64) {
	t.Helper()
	probe := func(args ...string) []byte {
		cmd := exec.Command(ffprobe, args...)
		payload, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ffprobe %s: %v: %s", input, err, payload)
		}
		return payload
	}
	type streamInfo struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		PixelFormat string `json:"pix_fmt"`
		FrameRate   string `json:"avg_frame_rate"`
		ReadFrames  string `json:"nb_read_frames"`
		SampleRate  string `json:"sample_rate"`
	}
	var report struct {
		Streams []streamInfo `json:"streams"`
	}
	if err := json.Unmarshal(probe("-v", "error", "-count_frames", "-show_streams", "-of", "json", input), &report); err != nil {
		t.Fatalf("decode ffprobe report for %s: %v", input, err)
	}
	var video, audio *streamInfo
	for i := range report.Streams {
		stream := &report.Streams[i]
		if stream.CodecType == "video" {
			video = stream
		}
		if stream.CodecType == "audio" {
			audio = stream
		}
	}
	if video == nil || video.CodecName != "h264" || video.Width != 320 || video.Height != 180 || video.PixelFormat != "yuv420p" || video.FrameRate != "30/1" || video.ReadFrames != strconv.FormatInt(frameCount, 10) {
		t.Fatalf("video stream for %s = %+v", input, video)
	}
	if wantAudio {
		if audio == nil || audio.CodecName != "aac" {
			t.Fatalf("audio stream for %s = %+v, want AAC", input, audio)
		}
		sampleRate, sampleRateErr := strconv.Atoi(audio.SampleRate)
		audioFramesJSON := probe("-v", "error", "-select_streams", "a:0", "-show_frames", "-show_entries", "frame=nb_samples", "-of", "json", input)
		var audioFrames struct {
			Frames []struct {
				Samples int `json:"nb_samples"`
			} `json:"frames"`
		}
		if err := json.Unmarshal(audioFramesJSON, &audioFrames); err != nil || sampleRateErr != nil || sampleRate <= 0 || len(audioFrames.Frames) == 0 {
			t.Fatalf("audio frame report for %s: sample_rate=%q frames=%d err=%v", input, audio.SampleRate, len(audioFrames.Frames), err)
		}
		samples := 0
		for _, frame := range audioFrames.Frames {
			samples += frame.Samples
		}
		if float64(samples)/float64(sampleRate) >= float64(frameCount)/30.0 {
			t.Fatalf("short audio duration for %s: %d samples at %d Hz covers %.3fs, video covers %.3fs", input, samples, sampleRate, float64(samples)/float64(sampleRate), float64(frameCount)/30.0)
		}
	} else if audio != nil {
		t.Fatalf("unexpected audio stream for %s: %+v", input, audio)
	}
	framesJSON := probe("-v", "error", "-select_streams", "v:0", "-show_frames", "-show_entries", "frame=best_effort_timestamp_time,key_frame", "-of", "json", input)
	var frames struct {
		Frames []struct {
			PTS      string `json:"best_effort_timestamp_time"`
			KeyFrame int    `json:"key_frame"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(framesJSON, &frames); err != nil || int64(len(frames.Frames)) != frameCount {
		t.Fatalf("frame list for %s has %d entries; err=%v", input, len(frames.Frames), err)
	}
	previous := math.Inf(-1)
	for i, frame := range frames.Frames {
		pts, err := strconv.ParseFloat(frame.PTS, 64)
		if err != nil || pts <= previous {
			t.Fatalf("nonmonotonic PTS at frame %d for %s: %q (%v)", i, input, frame.PTS, err)
		}
		if i > 0 && math.Abs((pts-previous)-1.0/30.0) > 0.002 {
			t.Fatalf("PTS delta at frame %d for %s = %.6f, want 1/30", i, input, pts-previous)
		}
		previous = pts
	}
	if frames.Frames[0].KeyFrame != 1 {
		t.Fatalf("first frame for %s is not a keyframe", input)
	}
	if output, err := exec.Command(ffmpeg, "-v", "error", "-xerror", "-i", input, "-f", "null", os.DevNull).CombinedOutput(); err != nil {
		t.Fatalf("decode %s: %v: %s", input, err, output)
	}
}

func verifyNicoTimelineIntegrationSingleSlice(t *testing.T, ffmpeg, input string, expectedFrames int64) int {
	t.Helper()
	data, err := exec.Command(ffmpeg, "-v", "error", "-i", input, "-map", "0:v:0", "-c:v", "copy", "-bsf:v", "h264_mp4toannexb", "-f", "h264", "-").Output()
	if err != nil {
		t.Fatalf("extract H.264 from %s: %v", input, err)
	}
	startCode := func(at int) (int, int) {
		if at+3 <= len(data) && data[at] == 0 && data[at+1] == 0 && data[at+2] == 1 {
			return at, 3
		}
		if at+4 <= len(data) && data[at] == 0 && data[at+1] == 0 && data[at+2] == 0 && data[at+3] == 1 {
			return at, 4
		}
		return -1, 0
	}
	var counts []int
	currentSlices := -1
	totalVCL := 0
	hasAUD := false
	for offset := 0; offset < len(data); {
		_, prefix := startCode(offset)
		if prefix == 0 {
			offset++
			continue
		}
		nalAt := offset + prefix
		if nalAt >= len(data) {
			break
		}
		kind := data[nalAt] & 0x1f
		if kind == 9 {
			hasAUD = true
			if currentSlices >= 0 {
				if currentSlices > 0 {
					counts = append(counts, currentSlices)
				}
			}
			currentSlices = 0
		} else if kind == 1 || kind == 5 {
			totalVCL++
			if currentSlices >= 0 {
				currentSlices++
			}
		}
		offset = nalAt + 1
	}
	if currentSlices >= 0 {
		if currentSlices > 0 {
			counts = append(counts, currentSlices)
		}
	}
	if totalVCL == 0 {
		t.Fatal("H.264 stream has no access-unit delimiters")
	}
	if hasAUD {
		for i, count := range counts {
			if count != 1 {
				t.Fatalf("H.264 access unit %d has %d VCL NAL units: %v", i, count, counts)
			}
		}
		if expectedFrames >= 0 && int64(len(counts)) != expectedFrames {
			t.Fatalf("H.264 nonempty access units=%d, want %d (empty delimiter count=%d)", len(counts), expectedFrames, totalVCL-len(counts))
		}
	}
	if expectedFrames >= 0 && int64(totalVCL) != expectedFrames {
		t.Fatalf("H.264 VCL slices=%d, want decoded frame count %d", totalVCL, expectedFrames)
	}
	return totalVCL
}

func verifyNicoTimelineIntegrationCommentPixels(t *testing.T, ffmpeg, input string) {
	t.Helper()
	raw, err := exec.Command(ffmpeg, "-v", "error", "-i", input, "-map", "0:v:0", "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Fatalf("decode comment frame: %v", err)
	}
	if len(raw) != 320*180*3 {
		t.Fatalf("decoded frame bytes=%d, want %d", len(raw), 320*180*3)
	}
	inside := (20*320 + 20) * 3
	outside := (100*320 + 100) * 3
	if raw[inside] < 160 || raw[inside+1] > 100 || raw[inside+2] > 100 {
		t.Fatalf("comment pixel is not red: %v", raw[inside:inside+3])
	}
	if raw[outside+2] < 100 || raw[outside] > 120 {
		t.Fatalf("source pixel outside comment is not blue: %v", raw[outside:outside+3])
	}
}

func verifyNicoTimelineIntegrationFaststart(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	var moov, mdat int64 = -1, -1
	for offset := int64(0); offset+8 <= info.Size(); {
		var header [8]byte
		if _, err := file.ReadAt(header[:], offset); err != nil {
			t.Fatal(err)
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		boxType := string(header[4:8])
		headerSize := int64(8)
		if size == 1 {
			var extended [8]byte
			if _, err := file.ReadAt(extended[:], offset+8); err != nil {
				t.Fatal(err)
			}
			size = int64(binary.BigEndian.Uint64(extended[:]))
			headerSize = 16
		} else if size == 0 {
			size = info.Size() - offset
		}
		if size < headerSize || offset+size > info.Size() {
			t.Fatalf("invalid MP4 box %q at offset %d with size %d", boxType, offset, size)
		}
		if boxType == "moov" {
			moov = offset
		}
		if boxType == "mdat" {
			mdat = offset
		}
		offset += size
	}
	if moov < 0 || mdat < 0 || moov > mdat {
		t.Fatalf("MP4 faststart order invalid: moov=%d mdat=%d", moov, mdat)
	}
}

package video

import (
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
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

type timelinePerformanceManifest struct {
	SchemaVersion        int                                `json:"schema_version"`
	RunID                string                             `json:"run_id"`
	PairID               string                             `json:"pair_id"`
	Variant              string                             `json:"variant"`
	Encoder              string                             `json:"encoder"`
	CPUPercent           string                             `json:"cpu_percent"`
	StartedAt            time.Time                          `json:"started_at"`
	FinishedAt           time.Time                          `json:"finished_at"`
	ConvertWallSeconds   float64                            `json:"convert_wall_seconds"`
	VerifyWallSeconds    float64                            `json:"verify_wall_seconds"`
	Width                int                                `json:"width"`
	Height               int                                `json:"height"`
	DurationMs           int64                              `json:"duration_ms"`
	FPSNum               int64                              `json:"fps_num"`
	FPSDen               int64                              `json:"fps_den"`
	FrameCount           int64                              `json:"frame_count"`
	SourcePath           string                             `json:"source_path"`
	SourceSHA256         string                             `json:"source_sha256"`
	SnapshotPath         string                             `json:"snapshot_path"`
	SnapshotSHA256       string                             `json:"snapshot_sha256"`
	FFmpegPath           string                             `json:"ffmpeg_path"`
	FFmpegSHA256         string                             `json:"ffmpeg_sha256"`
	FFmpegVersion        string                             `json:"ffmpeg_version"`
	FFmpegArgs           []string                           `json:"ffmpeg_args"`
	BrowserPath          string                             `json:"browser_path,omitempty"`
	BrowserSHA256        string                             `json:"browser_sha256,omitempty"`
	Backend              string                             `json:"backend"`
	FallbackReason       string                             `json:"fallback_reason,omitempty"`
	GPUBackend           string                             `json:"gpu_backend,omitempty"`
	GPUAdapter           string                             `json:"gpu_adapter,omitempty"`
	GPUInfo              string                             `json:"gpu_info,omitempty"`
	ReadbackSlots        int                                `json:"readback_slots,omitempty"`
	AssetLayoutRequested string                             `json:"asset_layout_requested,omitempty"`
	AssetLayout          string                             `json:"asset_layout,omitempty"`
	AssetLayoutFallback  string                             `json:"asset_layout_fallback_reason,omitempty"`
	AssetPageCount       int                                `json:"asset_page_count,omitempty"`
	AssetSourceBytes     uint64                             `json:"asset_source_bytes,omitempty"`
	AssetAllocatedBytes  uint64                             `json:"asset_allocated_bytes,omitempty"`
	HelperPath           string                             `json:"helper_path,omitempty"`
	HelperSHA256         string                             `json:"helper_sha256,omitempty"`
	BundleSHA256         string                             `json:"bundle_sha256,omitempty"`
	StageTimings         []NicoStageTiming                  `json:"stage_timings,omitempty"`
	SpriteCaptureMetrics *nicorender.TimelineCaptureMetrics `json:"sprite_capture_metrics,omitempty"`
	OutputPath           string                             `json:"output_path"`
	OutputBytes          int64                              `json:"output_bytes"`
	OutputSHA256         string                             `json:"output_sha256"`
	DecodedFrames        int64                              `json:"decoded_frames"`
	OutputVideoCodec     string                             `json:"output_video_codec"`
	OutputPixelFormat    string                             `json:"output_pixel_format"`
	OutputFrameRate      string                             `json:"output_frame_rate"`
	OutputHasAudio       bool                               `json:"output_has_audio"`
	OutputDuration       string                             `json:"output_duration"`
	RunnerRecordPath     string                             `json:"runner_record_path,omitempty"`
	OperatingSystem      string                             `json:"operating_system"`
	Architecture         string                             `json:"architecture"`
	LogicalCPUCount      int                                `json:"logical_cpu_count"`
	ProcessorIdentifier  string                             `json:"processor_identifier,omitempty"`
}

type timelinePerformanceProbe struct {
	Streams []struct {
		CodecName   string `json:"codec_name"`
		CodecType   string `json:"codec_type"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		PixelFormat string `json:"pix_fmt"`
		AverageRate string `json:"avg_frame_rate"`
		ReadFrames  string `json:"nb_read_frames"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

type timelineTransparentFrameSource struct {
	remaining int64
	pixels    []byte
}

func (s *timelineTransparentFrameSource) Next(ctx context.Context) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if s.remaining == 0 {
		return nil, false, nil
	}
	s.remaining--
	return s.pixels, true, nil
}

// TestNicoTimelinePerformance performs exactly one opt-in conversion. The
// benchmark script starts a fresh test process for every paired run so test
// repetition and Go's benchmark scheduler never choose the sample count.
func TestNicoTimelinePerformance(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_TIMELINE_PERF") != "1" {
		t.Skip("opt-in; run scripts/experiments/nico-timeline/benchmark.ps1")
	}
	variant := strings.TrimSpace(os.Getenv("NICO_TIMELINE_VARIANT"))
	if variant == "" {
		t.Fatal("NICO_TIMELINE_VARIANT is required")
	}
	runDir := strings.TrimSpace(os.Getenv("NICO_TIMELINE_RUN_DIR"))
	if runDir == "" {
		t.Fatal("NICO_TIMELINE_RUN_DIR is required")
	}
	sourcePath := requiredTimelinePerfPath(t, "IMAGEPAD_NICO_PERF_SOURCE")
	snapshotPath := requiredTimelinePerfPath(t, "IMAGEPAD_NICO_PERF_SNAPSHOT")
	ffmpeg := requiredTimelinePerfPath(t, "IMAGEPAD_NICO_PERF_FFMPEG")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(runDir, "output.mp4")
	manifestPath := filepath.Join(runDir, "run.json")
	if _, err := os.Stat(outputPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("benchmark output already exists or cannot be inspected: %s (%v)", outputPath, err)
	}
	if _, err := os.Stat(manifestPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("benchmark manifest already exists or cannot be inspected: %s (%v)", manifestPath, err)
	}
	if err := os.MkdirAll(filepath.Join(runDir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_DATA_DIR", filepath.Join(runDir, "state"))

	snapshotBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotBytes, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	snapshot, err = niconico.NormalizeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("invalid snapshot: %v", err)
	}

	width := timelinePerfInt(t, "NICO_TIMELINE_WIDTH", 1920)
	height := timelinePerfInt(t, "NICO_TIMELINE_HEIGHT", 1080)
	fpsNum := int64(timelinePerfInt(t, "NICO_TIMELINE_FPS_NUM", 30))
	fpsDen := int64(timelinePerfInt(t, "NICO_TIMELINE_FPS_DEN", 1))
	durationMs := int64(timelinePerfInt(t, "NICO_TIMELINE_DURATION_MS", 6000))
	crf := timelinePerfInt(t, "NICO_TIMELINE_CRF", 26)
	encoder := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_ENCODER")))
	if encoder == "" {
		encoder = "x264"
	}
	if encoder != "x264" && encoder != "nvenc" {
		t.Fatalf("unsupported benchmark encoder %q", encoder)
	}
	preset := strings.TrimSpace(os.Getenv("NICO_TIMELINE_PRESET"))
	if preset == "" {
		preset = "veryfast"
	}
	clock, err := niconico.NewFrameClock(fpsNum, fpsDen)
	if err != nil {
		t.Fatal(err)
	}
	frameCount := clock.FrameCountForDurationMs(durationMs)
	if frameCount <= 0 {
		t.Fatalf("invalid frame count %d", frameCount)
	}

	render := nicorender.RenderOptions{
		Width: width, Height: height, DurationMs: durationMs, FPSNum: fpsNum, FPSDen: fpsDen,
		Transport: "binary", BrowserPath: os.Getenv("NICO_TIMELINE_BROWSER"),
		CompositorPath:         os.Getenv("IMAGEPAD_NICO_PERF_LEGACY_HELPER"),
		CompositorDevice:       os.Getenv("IMAGEPAD_NICO_PERF_NATIVE_MODE"),
		TimelineCompositorPath: os.Getenv("IMAGEPAD_NICO_TIMELINE_COMPOSITOR"),
		TimelineGPUBackend:     os.Getenv("IMAGEPAD_NICO_TIMELINE_GPU_BACKEND"),
		TimelineAssetLayout:    os.Getenv("NICO_TIMELINE_ASSET_LAYOUT"),
	}
	if render.TimelineGPUBackend == "" {
		render.TimelineGPUBackend = "auto"
	}
	encode := NicoEncodeOptions{
		Width: width, Height: height, DurationMs: durationMs,
		FPSNum: fpsNum, FPSDen: fpsDen, CRF: crf, Preset: preset,
		Encoder: encoder, AudioBitrate: "160k",
	}
	var encodeReport NicoEncodeReport
	var renderReport nicorender.RenderReport
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	startedAt := time.Now()
	convertStarted := time.Now()
	switch variant {
	case "no-comments":
		transparent := make([]byte, width*height*4)
		encodeReport, err = EncodeNicoCommented(ctx, ffmpeg, sourcePath, outputPath, encode,
			&timelineTransparentFrameSource{remaining: frameCount, pixels: transparent})
		renderReport.Backend = "no-comments"
	case "browser", "native", "timeline-slots1", "timeline-slots2", "timeline-slots3", "timeline-a", "timeline-b", "timeline-sync", "timeline-pbo", "timeline-separate", "timeline-atlas":
		switch variant {
		case "browser":
			render.Backend = "browser"
		case "native":
			render.Backend = "native"
		case "timeline-slots1", "timeline-slots2", "timeline-slots3", "timeline-a", "timeline-b", "timeline-sync", "timeline-pbo", "timeline-separate", "timeline-atlas":
			render.Backend = "timeline"
			if variant == "timeline-a" || variant == "timeline-b" || variant == "timeline-sync" || variant == "timeline-pbo" || variant == "timeline-separate" || variant == "timeline-atlas" {
				render.TimelineReadbackSlots = 3
			} else {
				render.TimelineReadbackSlots = int(variant[len("timeline-slots")] - '0')
			}
			if variant == "timeline-separate" {
				render.TimelineAssetLayout = "separate"
			}
			if variant == "timeline-atlas" {
				render.TimelineAssetLayout = "atlas"
			}
			if variant == "timeline-separate" || variant == "timeline-atlas" {
				render.TimelineReadbackSlots, err = parseTimelinePerfReadbackSlots(os.Getenv("NICO_TIMELINE_READBACK_SLOTS"))
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		encodeReport, renderReport, err = EncodeNicoCommentedWithRenderer(ctx, ffmpeg, sourcePath, outputPath, snapshot, render, encode)
	default:
		t.Fatalf("unknown NICO_TIMELINE_VARIANT %q", variant)
	}
	convertFinished := time.Now()
	if err != nil {
		t.Fatalf("benchmark variant %s failed: %v", variant, err)
	}
	if encodeReport.FrameCount != frameCount {
		t.Fatalf("encoder produced %d frames, want %d", encodeReport.FrameCount, frameCount)
	}
	if variant != "no-comments" && renderReport.Backend == "" {
		t.Fatalf("renderer variant %s returned no backend identity", variant)
	}
	if renderReport.FallbackReason != "" || len(encodeReport.Attempts) != 0 {
		t.Fatalf("fallback is not a valid performance sample: backend=%s reason=%q attempts=%+v", renderReport.Backend, renderReport.FallbackReason, encodeReport.Attempts)
	}
	if strings.HasPrefix(variant, "timeline-") {
		if renderReport.Backend != "timeline-wgpu" || encodeReport.TimelineReadbackSlots != render.TimelineReadbackSlots || encodeReport.TimelineGPUBackend == "" || encodeReport.TimelineGPUAdapter == "" {
			t.Fatalf("timeline sample did not use requested GPU path: backend=%s report=%+v", renderReport.Backend, encodeReport)
		}
	}
	if variant == "timeline-separate" || variant == "timeline-atlas" {
		wantLayout := strings.TrimPrefix(variant, "timeline-")
		if encodeReport.TimelineAssetLayoutRequested != wantLayout || encodeReport.TimelineAssetLayout != wantLayout || encodeReport.TimelineAssetLayoutFallback != "" {
			t.Fatalf("atlas layout benchmark did not use requested %q mode: %+v", wantLayout, encodeReport)
		}
	}
	if variant == "timeline-sync" || variant == "timeline-pbo" {
		wantMode := strings.TrimPrefix(variant, "timeline-")
		metrics := encodeReport.SpriteCaptureMetrics
		if metrics == nil || !metrics.Valid || metrics.RequestedMode != wantMode || metrics.Mode != wantMode {
			t.Fatalf("readback variant did not use its requested mode %q: %+v", wantMode, metrics)
		}
		if wantMode == "pbo" && (metrics.PBOTextures == 0 || metrics.PBOFallbacks != 0) {
			t.Fatalf("PBO benchmark had no PBO work or used sync fallbacks: %+v", metrics)
		}
	}
	convertWall := convertFinished.Sub(convertStarted)

	verifyStarted := time.Now()
	ffprobe := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_PERF_FFPROBE"))
	if ffprobe == "" {
		ffprobe, err = exec.LookPath("ffprobe")
		if err != nil {
			t.Fatalf("ffprobe is required to validate the timed output: %v", err)
		}
	}
	probe, err := verifyTimelinePerformanceOutput(ctx, ffmpeg, ffprobe, outputPath, width, height, fpsNum, fpsDen, frameCount)
	if err != nil {
		t.Fatal(err)
	}
	verifyWall := time.Since(verifyStarted)

	startedHash, err := hashTimelinePerfFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	snapshotHash, err := hashTimelinePerfFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	ffmpegHash, err := hashTimelinePerfFile(ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	outputHash, err := hashTimelinePerfFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	browserPath := strings.TrimSpace(render.BrowserPath)
	browserHash := ""
	if browserPath != "" {
		browserHash, err = hashTimelinePerfFile(browserPath)
		if err != nil {
			t.Fatalf("hash browser executable: %v", err)
		}
	}
	helperPath := render.TimelineCompositorPath
	if variant == "native" {
		helperPath = render.CompositorPath
	}
	helperHash := encodeReport.TimelineHelperSHA256
	if helperPath != "" {
		if explicitHelperHash, hashErr := hashTimelinePerfFile(helperPath); hashErr == nil {
			helperHash = explicitHelperHash
		} else {
			t.Fatalf("hash helper executable: %v", hashErr)
		}
	}
	ffmpegVersion := timelinePerfVersion(ctx, ffmpeg)
	stageTimings := append([]NicoStageTiming(nil), encodeReport.StageTimings...)
	manifest := timelinePerformanceManifest{
		SchemaVersion: 1, RunID: strings.TrimSpace(os.Getenv("NICO_TIMELINE_RUN_ID")),
		PairID: strings.TrimSpace(os.Getenv("NICO_TIMELINE_PAIR_ID")), Variant: variant, Encoder: encoder,
		CPUPercent: strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_PERF_CPU_PERCENT")),
		StartedAt:  startedAt.UTC(), FinishedAt: convertFinished.UTC(),
		ConvertWallSeconds: convertWall.Seconds(), VerifyWallSeconds: verifyWall.Seconds(),
		Width: width, Height: height, DurationMs: durationMs, FPSNum: fpsNum, FPSDen: fpsDen, FrameCount: frameCount,
		SourcePath: sourcePath, SourceSHA256: startedHash, SnapshotPath: snapshotPath, SnapshotSHA256: snapshotHash,
		FFmpegPath: ffmpeg, FFmpegSHA256: ffmpegHash, FFmpegVersion: ffmpegVersion,
		FFmpegArgs: nicoEncodeArgs(sourcePath, outputPath, encode), Backend: renderReport.Backend,
		BrowserPath: browserPath, BrowserSHA256: browserHash,
		FallbackReason: renderReport.FallbackReason, GPUBackend: encodeReport.TimelineGPUBackend,
		GPUAdapter: encodeReport.TimelineGPUAdapter, GPUInfo: timelinePerfGPUInfo(ctx),
		ReadbackSlots:        encodeReport.TimelineReadbackSlots,
		AssetLayoutRequested: encodeReport.TimelineAssetLayoutRequested,
		AssetLayout:          encodeReport.TimelineAssetLayout,
		AssetLayoutFallback:  encodeReport.TimelineAssetLayoutFallback,
		AssetPageCount:       encodeReport.TimelineAssetPageCount,
		AssetSourceBytes:     encodeReport.TimelineAssetSourceBytes,
		AssetAllocatedBytes:  encodeReport.TimelineAssetAllocatedBytes,
		HelperPath:           helperPath,
		HelperSHA256:         helperHash, BundleSHA256: encodeReport.TimelineBundleSHA256,
		StageTimings: stageTimings, SpriteCaptureMetrics: encodeReport.SpriteCaptureMetrics,
		OutputPath: outputPath, OutputBytes: outputInfo.Size(), OutputSHA256: outputHash,
		DecodedFrames: probe.DecodedFrames, OutputVideoCodec: probe.VideoCodec, OutputPixelFormat: probe.PixelFormat,
		OutputFrameRate: probe.FrameRate, OutputHasAudio: probe.HasAudio, OutputDuration: probe.Duration,
		RunnerRecordPath: strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_PERF_RUNNER_RECORD")),
		OperatingSystem:  runtime.GOOS, Architecture: runtime.GOARCH, LogicalCPUCount: runtime.NumCPU(),
		ProcessorIdentifier: os.Getenv("PROCESSOR_IDENTIFIER"),
	}
	if manifest.RunID == "" {
		manifest.RunID = fmt.Sprintf("%s-%d", variant, startedAt.UnixNano())
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("variant=%s encoder=%s backend=%s slots=%d convert_ms=%.1f verify_ms=%.1f frames=%d adapter=%q output=%s manifest=%s", variant, encoder, renderReport.Backend, encodeReport.TimelineReadbackSlots, convertWall.Seconds()*1000, verifyWall.Seconds()*1000, probe.DecodedFrames, encodeReport.TimelineGPUAdapter, outputPath, manifestPath)
}

func requiredTimelinePerfPath(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		t.Fatalf("resolve %s: %v", name, err)
	}
	if info, err := os.Stat(absolute); err != nil || info.IsDir() {
		t.Fatalf("%s is not a regular file: %s (%v)", name, absolute, err)
	}
	return absolute
}

func timelinePerfInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		t.Fatalf("invalid %s=%q", name, value)
	}
	return parsed
}

func parseTimelinePerfReadbackSlots(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 3, nil
	}
	slots, err := strconv.Atoi(raw)
	if err != nil || slots < 1 || slots > 3 {
		return 0, fmt.Errorf("NICO_TIMELINE_READBACK_SLOTS=%q must be 1, 2, or 3", raw)
	}
	return slots, nil
}

func TestNicoTimelinePerformanceReadbackSlots(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  int
		bad   bool
	}{
		{name: "default", want: 3},
		{name: "x264 optimized ring", value: "2", want: 2},
		{name: "small ring", value: "1", want: 1},
		{name: "too many slots", value: "4", bad: true},
		{name: "malformed", value: "three", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTimelinePerfReadbackSlots(tc.value)
			if tc.bad {
				if err == nil {
					t.Fatal("accepted invalid readback slot count")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("readback slots=%d, want %d", got, tc.want)
			}
		})
	}
}

func verifyTimelinePerformanceOutput(ctx context.Context, ffmpeg, ffprobe, path string, width, height int, fpsNum, fpsDen, frameCount int64) (struct {
	DecodedFrames int64
	VideoCodec    string
	PixelFormat   string
	FrameRate     string
	HasAudio      bool
	Duration      string
}, error) {
	var result struct {
		DecodedFrames int64
		VideoCodec    string
		PixelFormat   string
		FrameRate     string
		HasAudio      bool
		Duration      string
	}
	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error", "-count_frames", "-show_entries",
		"stream=codec_type,codec_name,width,height,pix_fmt,avg_frame_rate,nb_read_frames:format=duration",
		"-of", "json", path)
	data, err := cmd.Output()
	if err != nil {
		return result, fmt.Errorf("ffprobe timed output: %w", err)
	}
	var probe timelinePerformanceProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return result, fmt.Errorf("parse ffprobe output: %w", err)
	}
	for _, stream := range probe.Streams {
		if stream.CodecType == "audio" {
			result.HasAudio = true
			continue
		}
		if stream.CodecType != "video" {
			continue
		}
		if result.VideoCodec != "" {
			return result, fmt.Errorf("timed output has multiple video streams")
		}
		result.VideoCodec, result.PixelFormat, result.FrameRate = stream.CodecName, stream.PixelFormat, stream.AverageRate
		if stream.Width != width || stream.Height != height || stream.PixelFormat != "yuv420p" {
			return result, fmt.Errorf("timed output geometry/pixel format is %dx%d %s, want %dx%d yuv420p", stream.Width, stream.Height, stream.PixelFormat, width, height)
		}
		if stream.ReadFrames == "" {
			return result, fmt.Errorf("ffprobe returned no decoded frame count")
		}
		decodedFrames, err := strconv.ParseInt(stream.ReadFrames, 10, 64)
		if err != nil || decodedFrames != frameCount {
			return result, fmt.Errorf("timed output decoded frames=%q, want %d", stream.ReadFrames, frameCount)
		}
		result.DecodedFrames = decodedFrames
	}
	if result.VideoCodec == "" || result.DecodedFrames != frameCount {
		return result, fmt.Errorf("timed output has no complete video stream")
	}
	wantRate := fmt.Sprintf("%d/%d", fpsNum, fpsDen)
	if result.FrameRate != wantRate {
		return result, fmt.Errorf("timed output average frame rate=%s, want %s", result.FrameRate, wantRate)
	}
	result.Duration = probe.Format.Duration
	decode := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", path, "-map", "0:v:0", "-f", "null", "-")
	if output, err := decode.CombinedOutput(); err != nil {
		return result, fmt.Errorf("decode timed output: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return result, nil
}

func hashTimelinePerfFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func timelinePerfVersion(ctx context.Context, ffmpeg string) string {
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, ffmpeg, "-version").Output()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	line, _, _ := strings.Cut(string(output), "\n")
	return strings.TrimSpace(line)
}

func timelinePerfGPUInfo(ctx context.Context) string {
	exe, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, exe, "--query-gpu=name,driver_version,memory.total", "--format=csv,noheader")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/nicoexportworker"
	"imagepadserver/internal/nicorender"
	"imagepadserver/internal/video"
)

type nicoWorkerPerformanceRecord struct {
	SchemaVersion          int                                `json:"schema_version"`
	RunID                  string                             `json:"run_id"`
	StartedAt              string                             `json:"started_at"`
	WorkerWallSeconds      float64                            `json:"worker_wall_seconds"`
	ConvertWallSeconds     float64                            `json:"convert_wall_seconds"`
	StageTimings           []video.NicoStageTiming            `json:"stage_timings,omitempty"`
	SpriteCaptureMetrics   *nicorender.TimelineCaptureMetrics `json:"sprite_capture_metrics,omitempty"`
	SourceSHA256           string                             `json:"source_sha256"`
	SnapshotSHA256         string                             `json:"snapshot_sha256"`
	WorkerSHA256           string                             `json:"worker_sha256"`
	FFmpegSHA256           string                             `json:"ffmpeg_sha256"`
	ConfiguredHelperSHA256 string                             `json:"configured_helper_sha256"`
	HelperSHA256           string                             `json:"helper_sha256,omitempty"`
	OutputSHA256           string                             `json:"output_sha256"`
	OutputBytes            int64                              `json:"output_bytes"`
	Encoder                string                             `json:"encoder"`
	OutputMode             string                             `json:"output_mode"`
	Backend                string                             `json:"backend"`
	GPUBackend             string                             `json:"gpu_backend,omitempty"`
	GPUAdapter             string                             `json:"gpu_adapter,omitempty"`
	ReadbackSlots          int                                `json:"readback_slots,omitempty"`
	TimelineFallback       bool                               `json:"timeline_fallback"`
	FallbackNotice         string                             `json:"fallback_notice,omitempty"`
	Width                  int                                `json:"width"`
	Height                 int                                `json:"height"`
	DurationMs             int64                              `json:"duration_ms"`
	FPSNum                 int64                              `json:"fps_num"`
	FPSDen                 int64                              `json:"fps_den"`
	JobCPURate             uint32                             `json:"job_cpu_rate"`
	TestCPUPercent         int                                `json:"test_cpu_percent"`
	CPUSeconds             float64                            `json:"cpu_seconds"`
	OS                     string                             `json:"os"`
	Architecture           string                             `json:"architecture"`
	LogicalCPUs            int                                `json:"logical_cpus"`
}

// TestNicoTimelineWorkerPerformance is an opt-in one-request-per-test-run E2E
// measurement. Run with -count=5 to collect five independent worker records.
func TestNicoTimelineWorkerPerformance(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_WORKER_PERF") != "1" {
		t.Skip("set NICO_TIMELINE_WORKER_PERF=1 to run the real worker timing test")
	}
	workerExe := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_EXE")
	source := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_SOURCE")
	snapshot := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_SNAPSHOT")
	ffmpeg := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_FFMPEG")
	helper := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_HELPER")
	outputRoot := strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_OUTPUT_ROOT"))
	if outputRoot == "" {
		t.Fatal("NICO_TIMELINE_WORKER_OUTPUT_ROOT is required")
	}
	outputRoot, err := filepath.Abs(outputRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outputRoot, 0700); err != nil {
		t.Fatal(err)
	}

	width, err := nicoWorkerPerfInt("NICO_TIMELINE_WORKER_WIDTH", 1920)
	if err != nil {
		t.Fatal(err)
	}
	height, err := nicoWorkerPerfInt("NICO_TIMELINE_WORKER_HEIGHT", 1080)
	if err != nil {
		t.Fatal(err)
	}
	durationMs, err := nicoWorkerPerfInt64("NICO_TIMELINE_WORKER_DURATION_MS", 6000)
	if err != nil {
		t.Fatal(err)
	}
	fpsNum, err := nicoWorkerPerfInt64("NICO_TIMELINE_WORKER_FPS_NUM", 30)
	if err != nil {
		t.Fatal(err)
	}
	fpsDen, err := nicoWorkerPerfInt64("NICO_TIMELINE_WORKER_FPS_DEN", 1)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := nicoWorkerPerfInt("NICO_TIMELINE_WORKER_READBACK_SLOTS", 3)
	if err != nil {
		t.Fatal(err)
	}
	encoder := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_ENCODER")))
	if encoder == "" {
		encoder = "nvenc"
	}
	gpuBackend := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_GPU_BACKEND")))
	if gpuBackend == "" {
		gpuBackend = "vulkan"
	}
	outputMode := strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_OUTPUT_MODE"))
	if outputMode == "" {
		outputMode = string(video.NicoOutputTee)
	}
	testCPUPercent, err := nicoWorkerPerfInt("NICO_TIMELINE_WORKER_TEST_CPU_PERCENT", 0)
	if err != nil {
		t.Fatal(err)
	}
	if testCPUPercent != 0 && testCPUPercent != 20 {
		t.Fatalf("NICO_TIMELINE_WORKER_TEST_CPU_PERCENT must be 0 or 20, got %d", testCPUPercent)
	}
	cpuOptions, err := nicoWorkerCPUOptionsForPerformanceTest(testCPUPercent)
	if err != nil {
		t.Fatal(err)
	}

	runID := "workerperf-" + randomSuffix()
	mediaID := "workerperf"
	runDir := filepath.Join(outputRoot, runID)
	if err := os.Mkdir(runDir, 0700); err != nil {
		t.Fatalf("create unique worker output directory: %v", err)
	}
	output := filepath.Join(runDir, "rendered.mp4")
	hlsDir := filepath.Join(runDir, "hls")
	request := nicoexportworker.Request{
		Version: nicoexportworker.ProtocolVersion, RunID: runID, MediaID: mediaID,
		SourcePath: source, SnapshotPath: snapshot, OutputPath: output, HLSStagingDir: hlsDir,
		OutputMode: outputMode, FFmpeg: ffmpeg, Backend: "timeline", TimelineEnabled: true,
		TimelineCompositor: helper, TimelineReadbackSlots: slots, TimelineGPUBackend: gpuBackend,
		Encoder: encoder, Width: width, Height: height, DurationMs: durationMs,
		FPSNum: fpsNum, FPSDen: fpsDen, CRF: 26, AudioBitrate: "160k",
	}
	oldWorkerExecutable := nicoWorkerExecutable
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }
	t.Cleanup(func() { nicoWorkerExecutable = oldWorkerExecutable })

	startedAt := time.Now().UTC()
	var workerDiagnostics strings.Builder
	result, budget, err := runNicoWorkerWithBudgetAndDiagnosticsAndCPUOptions(context.Background(), request, &workerDiagnostics, cpuOptions)
	if err != nil {
		t.Fatalf("real Nico worker failed: %v", err)
	}
	if !budget.Verified {
		t.Fatalf("worker CPU budget is unverified: %s", budget.Reason)
	}
	wantJobCPURate := cpuOptions.Percent * 100
	if budget.JobCPURate != wantJobCPURate {
		t.Fatalf("worker JobCPURate = %d, want %d for requested extra CPU cap %d%%", budget.JobCPURate, wantJobCPURate, testCPUPercent)
	}
	if workerDiagnostics.Len() != 0 {
		if err := os.WriteFile(filepath.Join(runDir, "worker-stderr.log"), []byte(workerDiagnostics.String()), 0600); err != nil {
			t.Fatalf("write worker diagnostics: %v", err)
		}
	}
	if result.WorkerWallSeconds <= 0 || result.ConvertWallSeconds <= 0 {
		t.Fatalf("worker timing fields missing: worker=%f convert=%f", result.WorkerWallSeconds, result.ConvertWallSeconds)
	}
	if result.Backend == "timeline-wgpu" && len(result.StageTimings) == 0 {
		t.Fatal("WGPU worker result omitted the stage timings required for the real-worker critical path")
	}
	if result.TimelineFallback {
		t.Logf("timeline fallback recorded; this sample must not be counted as a WGPU success: %s", result.FallbackNotice)
	}
	outputInfo, err := os.Stat(output)
	if err != nil {
		t.Fatalf("worker MP4 output missing: %v", err)
	}
	outputHash, err := nicoWorkerPerfSHA256(output)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash, err := nicoWorkerPerfSHA256(source)
	if err != nil {
		t.Fatal(err)
	}
	snapshotHash, err := nicoWorkerPerfSHA256(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	workerHash, err := nicoWorkerPerfSHA256(workerExe)
	if err != nil {
		t.Fatal(err)
	}
	ffmpegHash, err := nicoWorkerPerfSHA256(ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	configuredHelperHash, err := nicoWorkerPerfSHA256(helper)
	if err != nil {
		t.Fatal(err)
	}
	record := nicoWorkerPerformanceRecord{
		SchemaVersion: 1, RunID: runID, StartedAt: startedAt.Format(time.RFC3339Nano),
		WorkerWallSeconds: result.WorkerWallSeconds, ConvertWallSeconds: result.ConvertWallSeconds,
		StageTimings:         append([]video.NicoStageTiming(nil), result.StageTimings...),
		SpriteCaptureMetrics: result.SpriteCaptureMetrics,
		SourceSHA256:         sourceHash, SnapshotSHA256: snapshotHash, WorkerSHA256: workerHash,
		FFmpegSHA256: ffmpegHash, ConfiguredHelperSHA256: configuredHelperHash,
		HelperSHA256: result.HelperSHA256, OutputSHA256: outputHash, OutputBytes: outputInfo.Size(),
		Encoder: encoder, OutputMode: outputMode,
		Backend: result.Backend, GPUBackend: result.GPUBackend, GPUAdapter: result.GPUAdapter,
		ReadbackSlots: result.ReadbackSlots, TimelineFallback: result.TimelineFallback,
		FallbackNotice: result.FallbackNotice, Width: width, Height: height, DurationMs: durationMs,
		FPSNum: fpsNum, FPSDen: fpsDen, JobCPURate: budget.JobCPURate,
		TestCPUPercent: testCPUPercent, CPUSeconds: budget.CPUSeconds,
		OS: runtime.GOOS, Architecture: runtime.GOARCH, LogicalCPUs: runtime.NumCPU(),
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "worker-run.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("worker_wall=%.3fs convert_wall=%.3fs backend=%s encoder=%s fallback=%t output_sha256=%s record=%s",
		result.WorkerWallSeconds, result.ConvertWallSeconds, result.Backend, encoder, result.TimelineFallback, outputHash, runDir)
}

func TestNicoWorkerPerformanceCPUPercentMapsToBudgetOptions(t *testing.T) {
	tests := []struct {
		requested int
		want      uint32
	}{
		{requested: 0, want: 100},
		{requested: 20, want: 20},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.requested), func(t *testing.T) {
			options, err := nicoWorkerCPUOptionsForPerformanceTest(tt.requested)
			if err != nil {
				t.Fatal(err)
			}
			if options.Percent != tt.want {
				t.Fatalf("requested test CPU percent %d mapped to Job Object percent %d, want %d", tt.requested, options.Percent, tt.want)
			}
		})
	}
	if _, err := nicoWorkerCPUOptionsForPerformanceTest(10); err == nil {
		t.Fatal("unsupported test CPU percent 10 was accepted")
	}
}

func requireNicoWorkerPerfFile(t *testing.T, envName string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		t.Fatalf("%s is required", envName)
	}
	path, err := filepath.Abs(value)
	if err != nil {
		t.Fatalf("resolve %s: %v", envName, err)
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s must name an existing file: %s (%v)", envName, path, err)
	}
	return path
}

func nicoWorkerPerfInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func nicoWorkerPerfInt64(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func nicoWorkerPerfSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

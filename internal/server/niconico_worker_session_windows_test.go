//go:build windows

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/nicoexportworker"
)

// TestNicoWorkerSessionReusesBrowserPIDAcrossRunIDs is an opt-in Windows
// integration fixture. It requires the same real worker/browser/render inputs
// as the timeline performance test plus NICO_TIMELINE_BROWSER. The session
// process and each run are bounded; the manager always closes and waits.
func TestNicoWorkerSessionReusesBrowserPIDAcrossRunIDs(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_SESSION_REUSE") != "1" {
		t.Skip("set NICO_TIMELINE_SESSION_REUSE=1 to verify real browser PID reuse")
	}
	workerExe := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_EXE")
	source := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_SOURCE")
	snapshot := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_SNAPSHOT")
	ffmpeg := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_FFMPEG")
	helper := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_HELPER")
	browser := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_BROWSER")
	outputRoot := t.TempDir()
	oldWorkerExecutable := nicoWorkerExecutable
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }
	t.Cleanup(func() { nicoWorkerExecutable = oldWorkerExecutable })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	manager := newNicoWorkerSessionManager(ctx, nil, nil)
	t.Cleanup(func() {
		if err := manager.close(); err != nil {
			t.Errorf("close and wait for session: %v", err)
		}
	})

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
		outputMode = "tee"
	}

	runIDs := []string{"session-reuse-a-" + randomSuffix(), "session-reuse-b-" + randomSuffix()}
	var reports []nicoexportbudget.Report
	for index, runID := range runIDs {
		runDir := filepath.Join(outputRoot, fmt.Sprintf("run-%d", index+1))
		if err := os.Mkdir(runDir, 0700); err != nil {
			t.Fatal(err)
		}
		request := nicoexportworker.Request{
			Version: 1, RunID: runID, MediaID: "session-browser-reuse",
			SourcePath: source, SnapshotPath: snapshot, OutputPath: filepath.Join(runDir, "rendered.mp4"),
			HLSStagingDir: filepath.Join(runDir, "hls"), OutputMode: outputMode, FFmpeg: ffmpeg, BrowserPath: browser,
			Backend: "timeline", TimelineEnabled: true, TimelineCompositor: helper,
			TimelineReadbackSlots: slots, TimelineGPUBackend: gpuBackend, Encoder: encoder,
			Width: width, Height: height, DurationMs: durationMs, FPSNum: fpsNum, FPSDen: fpsDen, CRF: 26, AudioBitrate: "160k",
		}
		result, report, err := manager.run(ctx, request, nicoWorkerCPUOptions(), nil)
		if err != nil {
			t.Fatalf("session run %s failed: %v", runID, err)
		}
		if !report.Verified || !result.OK || result.RunID != runID {
			t.Fatalf("run %s result=%+v report=%+v", runID, result, report)
		}
		reports = append(reports, report)
	}

	wantBrowser := filepath.Clean(browser)
	var firstBrowserPIDs []uint32
	for _, pid := range reports[0].PIDs {
		if sameProcessImage(pid, wantBrowser) {
			firstBrowserPIDs = append(firstBrowserPIDs, pid)
		}
	}
	if len(firstBrowserPIDs) == 0 {
		t.Fatalf("first run Job snapshot did not include a process image matching browser %q; pids=%v", browser, reports[0].PIDs)
	}
	for _, pid := range firstBrowserPIDs {
		for _, secondPID := range reports[1].PIDs {
			if pid == secondPID && sameProcessImage(pid, wantBrowser) {
				t.Logf("RunIDs %s and %s reused browser PID %d", runIDs[0], runIDs[1], pid)
				return
			}
		}
	}
	t.Fatalf("two successful RunIDs did not share a live PID for browser %q: first=%v second=%v", browser, reports[0].PIDs, reports[1].PIDs)
}

func sameProcessImage(pid uint32, want string) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return false
	}
	got := filepath.Clean(windows.UTF16ToString(buffer[:size]))
	return strings.EqualFold(got, filepath.Clean(want))
}

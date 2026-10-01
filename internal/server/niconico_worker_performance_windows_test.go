//go:build windows

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/nicoexportworker"
	"imagepadserver/internal/niconico"
	"imagepadserver/internal/video"
)

type nicoWorkerSessionMemberEvidence struct {
	RunID                 string    `json:"run_id"`
	Role                  string    `json:"role"`
	PID                   uint32    `json:"pid"`
	ImagePath             string    `json:"image_path"`
	ObservedAt            time.Time `json:"observed_at"`
	JobSampleMilliseconds int64     `json:"job_sample_ms"`
}

type nicoWorkerSessionJobEvidence struct {
	RunID              string                            `json:"run_id"`
	WorkerWallSeconds  float64                           `json:"worker_wall_seconds"`
	ReportedCPUSeconds float64                           `json:"reported_cpu_seconds"`
	CumulativeBefore   float64                           `json:"cumulative_cpu_before"`
	CumulativeAfter    float64                           `json:"cumulative_cpu_after"`
	CumulativeDelta    float64                           `json:"cumulative_cpu_delta"`
	JobCPURate         uint32                            `json:"job_cpu_rate"`
	OutputSHA256       string                            `json:"output_sha256"`
	OutputBytes        int64                             `json:"output_bytes"`
	ObservedJobMembers []nicoWorkerSessionMemberEvidence `json:"observed_job_members"`
}

type nicoWorkerSessionConditionEvidence struct {
	SchemaVersion           int                            `json:"schema_version"`
	ConditionID             string                         `json:"condition_id"`
	StartedAt               string                         `json:"started_at"`
	RequestedCPUPercent     int                            `json:"requested_cpu_percent"`
	JobCPURate              uint32                         `json:"job_cpu_rate"`
	Width                   int                            `json:"width"`
	Height                  int                            `json:"height"`
	DurationMs              int64                          `json:"duration_ms"`
	FPSNum                  int64                          `json:"fps_num"`
	FPSDen                  int64                          `json:"fps_den"`
	Encoder                 string                         `json:"encoder"`
	OutputMode              string                         `json:"output_mode"`
	SourceSHA256            string                         `json:"source_sha256"`
	SnapshotSHA256          string                         `json:"snapshot_sha256"`
	WorkerSHA256            string                         `json:"worker_sha256"`
	FFmpegSHA256            string                         `json:"ffmpeg_sha256"`
	HelperSHA256            string                         `json:"helper_sha256"`
	BrowserSHA256           string                         `json:"browser_sha256"`
	SessionRetirementError  string                         `json:"session_retirement_error,omitempty"`
	SessionRetirementStderr string                         `json:"session_retirement_stderr,omitempty"`
	Jobs                    []nicoWorkerSessionJobEvidence `json:"jobs"`
}

type nicoWorkerSessionCancellationMemberEvidence struct {
	PID            uint32    `json:"pid"`
	ImagePath      string    `json:"image_path"`
	Role           string    `json:"role,omitempty"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	JobSampleMs    int64     `json:"job_sample_ms"`
	HandleSignaled bool      `json:"handle_signaled_after_wait"`
}

type nicoWorkerSessionCancellationEvidence struct {
	SchemaVersion        int                                           `json:"schema_version"`
	SessionID            string                                        `json:"session_id"`
	RunID                string                                        `json:"run_id"`
	StartedAt            string                                        `json:"started_at"`
	CancelAt             string                                        `json:"cancel_at,omitempty"`
	FinishedAt           string                                        `json:"finished_at,omitempty"`
	TeardownMilliseconds int64                                         `json:"teardown_ms"`
	CancelError          string                                        `json:"cancel_error,omitempty"`
	WorkerSHA256         string                                        `json:"worker_sha256"`
	SourceSHA256         string                                        `json:"source_sha256"`
	SnapshotSHA256       string                                        `json:"snapshot_sha256"`
	FFmpegSHA256         string                                        `json:"ffmpeg_sha256"`
	HelperSHA256         string                                        `json:"helper_sha256"`
	BrowserSHA256        string                                        `json:"browser_sha256"`
	ProfilePath          string                                        `json:"profile_path,omitempty"`
	ProfileRemoved       bool                                          `json:"profile_removed"`
	ScratchRoot          string                                        `json:"scratch_root"`
	ScratchRootContents  []string                                      `json:"scratch_root_contents_after_wait,omitempty"`
	ScratchRootRemoved   bool                                          `json:"scratch_root_removed"`
	SessionTempRoot      string                                        `json:"session_temp_root"`
	SessionTempRootGone  bool                                          `json:"session_temp_root_removed_after_wait"`
	SnapshotCommentCount int                                           `json:"snapshot_renderable_comment_count"`
	RenderFramesComplete int64                                         `json:"render_frames_completed_before_cancel"`
	RenderFramesTotal    int64                                         `json:"render_frames_total"`
	CancelPhase          string                                        `json:"cancel_phase,omitempty"`
	WorkerLiveAtCancel   bool                                          `json:"worker_live_at_cancel"`
	FFmpegPIDAtCancel    uint32                                        `json:"ffmpeg_pid_at_cancel,omitempty"`
	FFmpegLiveAtCancel   bool                                          `json:"ffmpeg_live_at_cancel"`
	RunPendingAtCancel   bool                                          `json:"run_pending_at_cancel"`
	PartialOutputBytes   int64                                         `json:"partial_output_bytes_before_cancel"`
	HLSStagingEntries    []string                                      `json:"hls_staging_entries_before_cancel,omitempty"`
	HLSStagingFiles      []nicoSessionCancellationHLSFile              `json:"hls_staging_files_before_cancel,omitempty"`
	HLSStagingBytes      int64                                         `json:"hls_staging_bytes_before_cancel"`
	TeeWorkspacePath     string                                        `json:"tee_workspace_path,omitempty"`
	TeeWorkspaceRemoved  bool                                          `json:"tee_workspace_removed_after_wait"`
	TimelineFallback     bool                                          `json:"timeline_fallback_before_cancel"`
	PartialOutputExisted bool                                          `json:"partial_output_existed_before_cancel"`
	OutputRemoved        bool                                          `json:"output_removed_after_wait"`
	HLSStagingExisted    bool                                          `json:"hls_staging_existed_before_cancel"`
	HLSStagingRemoved    bool                                          `json:"hls_staging_removed_after_wait"`
	Members              []nicoWorkerSessionCancellationMemberEvidence `json:"members"`
}

type nicoWorkerSessionOwnedHandle struct {
	evidence nicoWorkerSessionCancellationMemberEvidence
	handle   windows.Handle
}

type nicoWorkerSessionRunOutcome struct {
	result nicoexportworker.Event
	report nicoexportbudget.Report
	err    error
}

type nicoSessionCancellationHLSFile struct {
	Name  string
	Bytes int64
}

type nicoSessionCancellationTeeStaging struct {
	WorkspacePath string
	MP4Bytes      int64
	HLSFiles      []nicoSessionCancellationHLSFile
	HLSBytes      int64
}

type nicoWorkerSessionCancellationProgress struct {
	Completed        int64
	Total            int64
	TimelineFallback bool
	Phase            string
}

func nicoSessionCancellationPhase(progress nicoWorkerSessionCancellationProgress, ffmpegLive bool) string {
	if progress.TimelineFallback || !ffmpegLive || progress.Total <= 0 || progress.Completed <= 0 || progress.Completed > progress.Total {
		return ""
	}
	if progress.Completed < progress.Total {
		return "render"
	}
	return "encode"
}

func validateNicoSessionCancellationSnapshot(data []byte, minimumComments int) (int, error) {
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return 0, fmt.Errorf("decode cancellation snapshot: %w", err)
	}
	commentCount := 0
	for _, thread := range snapshot.RendererThreads() {
		for index, comment := range thread.Comments {
			if _, err := time.Parse(time.RFC3339Nano, comment.PostedAt); err != nil {
				return commentCount, fmt.Errorf("selected thread %q comment %d has invalid postedAt %q: %w", thread.ID, index, comment.PostedAt, err)
			}
			commentCount++
		}
	}
	if commentCount < minimumComments {
		return commentCount, fmt.Errorf("cancellation snapshot has %d renderable selected comments, want at least %d", commentCount, minimumComments)
	}
	return commentCount, nil
}

func TestNicoSessionCancellationSnapshotValidation(t *testing.T) {
	makeSnapshot := func(count int) niconico.Snapshot {
		comments := make([]niconico.Comment, count)
		for index := range comments {
			comments[index] = niconico.Comment{
				ID: fmt.Sprintf("comment-%03d", index+1), No: int64(index + 1),
				VposMs: int64(index * 250), Body: fmt.Sprintf("comment %03d", index+1),
				PostedAt: "2026-01-01T00:00:00Z",
			}
		}
		return niconico.Snapshot{
			SchemaVersion: 1, SelectedForks: []string{"main"},
			Threads: []niconico.Thread{{ID: "main", Fork: "main", Comments: comments}},
		}
	}
	encode := func(snapshot niconico.Snapshot) []byte {
		t.Helper()
		data, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	t.Run("accepts 120 selected comments with valid RFC3339 dates", func(t *testing.T) {
		count, err := validateNicoSessionCancellationSnapshot(encode(makeSnapshot(120)), 120)
		if err != nil || count != 120 {
			t.Fatalf("validated count=%d err=%v, want 120 and nil", count, err)
		}
	})
	t.Run("rejects missing postedAt before browser launch", func(t *testing.T) {
		snapshot := makeSnapshot(120)
		snapshot.Threads[0].Comments[0].PostedAt = ""
		if count, err := validateNicoSessionCancellationSnapshot(encode(snapshot), 120); err == nil || count != 0 {
			t.Fatalf("validated count=%d err=%v, want missing postedAt rejection", count, err)
		}
	})
	t.Run("rejects invalid postedAt before browser launch", func(t *testing.T) {
		snapshot := makeSnapshot(120)
		snapshot.Threads[0].Comments[0].PostedAt = "not-a-date"
		if count, err := validateNicoSessionCancellationSnapshot(encode(snapshot), 120); err == nil || count != 0 {
			t.Fatalf("validated count=%d err=%v, want invalid postedAt rejection", count, err)
		}
	})
	t.Run("rejects too few selected comments", func(t *testing.T) {
		if count, err := validateNicoSessionCancellationSnapshot(encode(makeSnapshot(119)), 120); err == nil || count != 119 {
			t.Fatalf("validated count=%d err=%v, want 119-comment rejection", count, err)
		}
	})
}

func TestNicoSessionCancellationPhase(t *testing.T) {
	tests := []struct {
		name       string
		progress   nicoWorkerSessionCancellationProgress
		ffmpegLive bool
		want       string
	}{
		{
			name:       "render with live ffmpeg",
			progress:   nicoWorkerSessionCancellationProgress{Completed: 3, Total: 10},
			ffmpegLive: true,
			want:       "render",
		},
		{
			name:       "encode after all render frames with live ffmpeg",
			progress:   nicoWorkerSessionCancellationProgress{Completed: 10, Total: 10},
			ffmpegLive: true,
			want:       "encode",
		},
		{
			name:     "incomplete render without live ffmpeg",
			progress: nicoWorkerSessionCancellationProgress{Completed: 3, Total: 10},
			want:     "",
		},
		{
			name:     "completed render without live ffmpeg",
			progress: nicoWorkerSessionCancellationProgress{Completed: 10, Total: 10},
			want:     "",
		},
		{
			name:       "rejects missing or inconsistent progress",
			progress:   nicoWorkerSessionCancellationProgress{Completed: 11, Total: 10},
			ffmpegLive: true,
			want:       "",
		},
		{
			name:       "rejects timeline fallback",
			progress:   nicoWorkerSessionCancellationProgress{Completed: 3, Total: 10, TimelineFallback: true},
			ffmpegLive: true,
			want:       "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nicoSessionCancellationPhase(test.progress, test.ffmpegLive); got != test.want {
				t.Fatalf("phase=%q, want %q", got, test.want)
			}
		})
	}
}

func TestNicoSessionCancellationTeeStaging(t *testing.T) {
	t.Run("no active tee workspace", func(t *testing.T) {
		root := t.TempDir()
		got, err := inspectNicoSessionCancellationTeeStaging(root)
		if err != nil {
			t.Fatal(err)
		}
		if got.WorkspacePath != "" || got.MP4Bytes != 0 || len(got.HLSFiles) != 0 || got.HLSBytes != 0 {
			t.Fatalf("staging snapshot = %+v, want empty", got)
		}
	})

	t.Run("reads only active direct-child tee workspace files", func(t *testing.T) {
		root := t.TempDir()
		outputRoot := filepath.Join(root, "output")
		if err := os.Mkdir(outputRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		teeRoot := filepath.Join(outputRoot, ".niconico-tee-run")
		if err := os.Mkdir(teeRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{
			"out.mp4":          []byte("partial mp4"),
			"playlist.m3u8":    []byte("#EXTM3U"),
			"segment-00000.ts": []byte("segment"),
			"unrelated.bin":    []byte("ignore this"),
		} {
			if err := os.WriteFile(filepath.Join(teeRoot, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		unrelatedWorkspace := filepath.Join(outputRoot, ".niconico-timeline-job-run")
		if err := os.Mkdir(unrelatedWorkspace, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unrelatedWorkspace, "out.mp4"), []byte("not tee output"), 0o600); err != nil {
			t.Fatal(err)
		}
		outsideRoot := filepath.Join(root, "outside")
		if err := os.Mkdir(outsideRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		outsideWorkspace := filepath.Join(outsideRoot, ".niconico-tee-outside")
		if err := os.Mkdir(outsideWorkspace, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outsideWorkspace, "out.mp4"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}

		got, err := inspectNicoSessionCancellationTeeStaging(outputRoot)
		if err != nil {
			t.Fatal(err)
		}
		wantWorkspace, err := filepath.Abs(teeRoot)
		if err != nil {
			t.Fatal(err)
		}
		if got.WorkspacePath != wantWorkspace {
			t.Fatalf("workspace = %q, want %q", got.WorkspacePath, wantWorkspace)
		}
		if got.MP4Bytes != int64(len("partial mp4")) {
			t.Fatalf("MP4 bytes = %d, want %d", got.MP4Bytes, len("partial mp4"))
		}
		if got.HLSBytes != int64(len("#EXTM3U")+len("segment")) {
			t.Fatalf("HLS bytes = %d, want %d", got.HLSBytes, len("#EXTM3U")+len("segment"))
		}
		if len(got.HLSFiles) != 2 {
			t.Fatalf("HLS files = %+v, want playlist and one segment", got.HLSFiles)
		}
		names := map[string]bool{}
		for _, file := range got.HLSFiles {
			names[file.Name] = true
		}
		if !names["playlist.m3u8"] || !names["segment-00000.ts"] || names["unrelated.bin"] {
			t.Fatalf("HLS files = %+v, want only playlist and segment", got.HLSFiles)
		}
	})

	t.Run("rejects ambiguous active tee workspaces", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{".niconico-tee-one", ".niconico-tee-two"} {
			if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := inspectNicoSessionCancellationTeeStaging(root); err == nil {
			t.Fatal("expected ambiguous tee workspaces to be rejected")
		}
	})
}

func inspectNicoSessionCancellationTeeStaging(outputRoot string) (nicoSessionCancellationTeeStaging, error) {
	root, err := filepath.Abs(outputRoot)
	if err != nil {
		return nicoSessionCancellationTeeStaging{}, fmt.Errorf("resolve tee output root: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nicoSessionCancellationTeeStaging{}, fmt.Errorf("stat tee output root: %w", err)
	}
	if !rootInfo.IsDir() {
		return nicoSessionCancellationTeeStaging{}, fmt.Errorf("tee output root is not a directory: %q", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nicoSessionCancellationTeeStaging{}, fmt.Errorf("read tee output root: %w", err)
	}
	var workspace string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".niconico-tee-") {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if !pathUnderRoot(root, candidate) {
			return nicoSessionCancellationTeeStaging{}, fmt.Errorf("tee workspace escaped owned output root: %q", candidate)
		}
		info, err := os.Lstat(candidate)
		if err != nil {
			return nicoSessionCancellationTeeStaging{}, fmt.Errorf("inspect tee workspace %q: %w", candidate, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nicoSessionCancellationTeeStaging{}, fmt.Errorf("tee workspace is not a real directory: %q", candidate)
		}
		if workspace != "" {
			return nicoSessionCancellationTeeStaging{}, fmt.Errorf("multiple active tee workspaces under owned output root %q", root)
		}
		workspace = candidate
	}
	if workspace == "" {
		return nicoSessionCancellationTeeStaging{}, nil
	}

	snapshot := nicoSessionCancellationTeeStaging{WorkspacePath: workspace}
	mp4Path := filepath.Join(workspace, "out.mp4")
	mp4Info, err := os.Lstat(mp4Path)
	if err == nil {
		if !mp4Info.Mode().IsRegular() {
			return nicoSessionCancellationTeeStaging{}, fmt.Errorf("tee MP4 is not a regular file: %q", mp4Path)
		}
		if mp4Info.Size() > 0 {
			snapshot.MP4Bytes = mp4Info.Size()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nicoSessionCancellationTeeStaging{}, fmt.Errorf("inspect tee MP4 %q: %w", mp4Path, err)
	}

	workspaceEntries, err := os.ReadDir(workspace)
	if err != nil {
		return nicoSessionCancellationTeeStaging{}, fmt.Errorf("read tee workspace %q: %w", workspace, err)
	}
	for _, entry := range workspaceEntries {
		name := entry.Name()
		isHLSFile := name == "playlist.m3u8" || (strings.HasPrefix(name, "segment-") && strings.HasSuffix(name, ".ts"))
		if !isHLSFile {
			continue
		}
		path := filepath.Join(workspace, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nicoSessionCancellationTeeStaging{}, fmt.Errorf("inspect tee HLS file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nicoSessionCancellationTeeStaging{}, fmt.Errorf("tee HLS entry is not a regular file: %q", path)
		}
		snapshot.HLSFiles = append(snapshot.HLSFiles, nicoSessionCancellationHLSFile{Name: name, Bytes: info.Size()})
		snapshot.HLSBytes += info.Size()
	}
	return snapshot, nil
}

func recordNicoSessionCancellationTeeStaging(evidence *nicoWorkerSessionCancellationEvidence, staging nicoSessionCancellationTeeStaging) {
	if staging.WorkspacePath == "" {
		return
	}
	evidence.TeeWorkspacePath = staging.WorkspacePath
	evidence.PartialOutputBytes = staging.MP4Bytes
	evidence.PartialOutputExisted = staging.MP4Bytes > 0
	evidence.HLSStagingFiles = append(evidence.HLSStagingFiles[:0], staging.HLSFiles...)
	evidence.HLSStagingEntries = evidence.HLSStagingEntries[:0]
	for _, file := range staging.HLSFiles {
		evidence.HLSStagingEntries = append(evidence.HLSStagingEntries, file.Name)
	}
	evidence.HLSStagingBytes = staging.HLSBytes
	evidence.HLSStagingExisted = len(staging.HLSFiles) > 0
}

// TestNicoWorkerSessionPerformanceOwnership is an opt-in Windows integration
// measurement for successful serial jobs under unrestricted and 20% Job caps.
// Main owns running this fixture and cleaning the caller-owned evidence root.
func TestNicoWorkerSessionPerformanceOwnership(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_SESSION_PERF") != "1" {
		t.Skip("set NICO_TIMELINE_SESSION_PERF=1 to measure session CPU and Job ownership")
	}
	workerExe := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_EXE")
	source := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_SOURCE")
	snapshot := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_SNAPSHOT")
	ffmpeg := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_FFMPEG")
	helper := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_HELPER")
	browser := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_BROWSER")
	evidenceRoot := strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_EVIDENCE_ROOT"))
	if evidenceRoot == "" {
		t.Fatal("NICO_TIMELINE_WORKER_EVIDENCE_ROOT must name an existing caller-owned directory")
	}
	evidenceRoot, err := filepath.Abs(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(evidenceRoot); err != nil || !info.IsDir() {
		t.Fatalf("evidence root must be an existing directory: %s (%v)", evidenceRoot, err)
	}

	durationMs, err := nicoWorkerPerfInt64("NICO_TIMELINE_WORKER_DURATION_MS", 12000)
	if err != nil || durationMs < 1000 || durationMs > 60000 {
		t.Fatalf("NICO_TIMELINE_WORKER_DURATION_MS must be 1000..60000, got %d (err=%v)", durationMs, err)
	}
	encoder := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_ENCODER")))
	if encoder == "" {
		encoder = "nvenc"
	}
	outputMode := strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_OUTPUT_MODE"))
	if outputMode == "" {
		outputMode = string(video.NicoOutputTee)
	}
	gpuBackend := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_GPU_BACKEND")))
	if gpuBackend == "" {
		gpuBackend = "vulkan"
	}
	slots, err := nicoWorkerPerfInt("NICO_TIMELINE_WORKER_READBACK_SLOTS", 3)
	if err != nil {
		t.Fatal(err)
	}

	workerHash := requireNicoPerfHash(t, workerExe)
	sourceHash := requireNicoPerfHash(t, source)
	snapshotHash := requireNicoPerfHash(t, snapshot)
	ffmpegHash := requireNicoPerfHash(t, ffmpeg)
	helperHash := requireNicoPerfHash(t, helper)
	browserHash := requireNicoPerfHash(t, browser)
	oldWorkerExecutable := nicoWorkerExecutable
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }
	t.Cleanup(func() { nicoWorkerExecutable = oldWorkerExecutable })

	for _, requestedCPU := range []int{0, 20} {
		startedAt := time.Now().UTC().Format(time.RFC3339Nano)
		evidence := runNicoWorkerSessionPerformanceCondition(t, nicoWorkerSessionPerfConfig{
			requestedCPU: requestedCPU,
			workerExe:    workerExe,
			source:       source,
			snapshot:     snapshot,
			ffmpeg:       ffmpeg,
			helper:       helper,
			browser:      browser,
			durationMs:   durationMs,
			encoder:      encoder,
			outputMode:   outputMode,
			gpuBackend:   gpuBackend,
			slots:        slots,
		})
		evidence.SchemaVersion = 1
		evidence.StartedAt = startedAt
		evidence.Width, evidence.Height = 1280, 720
		evidence.DurationMs, evidence.FPSNum, evidence.FPSDen = durationMs, 30, 1
		evidence.Encoder, evidence.OutputMode = encoder, outputMode
		evidence.SourceSHA256, evidence.SnapshotSHA256 = sourceHash, snapshotHash
		evidence.WorkerSHA256, evidence.FFmpegSHA256 = workerHash, ffmpegHash
		evidence.HelperSHA256, evidence.BrowserSHA256 = helperHash, browserHash
		evidencePath := filepath.Join(evidenceRoot, fmt.Sprintf("nico-session-cpu-%03d-%s.json", requestedCPU, randomSuffix()))
		data, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, append(data, '\n'), 0600); err != nil {
			t.Fatalf("write compact session evidence: %v", err)
		}
		t.Logf("CPU %d%% session evidence: %s", requestedCPU, evidencePath)
	}
}

// TestNicoWorkerSessionCancelReapsOwnedMembers is an opt-in Windows process
// cancellation fixture. It opens handles only for PIDs found in this session's
// active Job samples, then checks those same handles after Close/Wait.
func TestNicoWorkerSessionCancelReapsOwnedMembers(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_SESSION_CANCEL_PERF") != "1" {
		t.Skip("set NICO_TIMELINE_SESSION_CANCEL_PERF=1 to verify session cancellation and reaping")
	}
	workerExe := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_EXE")
	source := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_SOURCE")
	snapshot := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_SNAPSHOT")
	snapshotData, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatalf("read cancellation snapshot fixture: %v", err)
	}
	snapshotCommentCount, err := validateNicoSessionCancellationSnapshot(snapshotData, 120)
	if err != nil {
		t.Fatalf("invalid cancellation snapshot fixture: %v", err)
	}
	ffmpeg := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_FFMPEG")
	helper := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_WORKER_HELPER")
	browser := requireNicoWorkerPerfFile(t, "NICO_TIMELINE_BROWSER")
	evidenceRoot := strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_EVIDENCE_ROOT"))
	if evidenceRoot == "" {
		t.Fatal("NICO_TIMELINE_WORKER_EVIDENCE_ROOT must name an existing caller-owned directory")
	}
	evidenceRoot, err = filepath.Abs(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(evidenceRoot); err != nil || !info.IsDir() {
		t.Fatalf("evidence root must be an existing directory: %s (%v)", evidenceRoot, err)
	}
	durationMs, err := nicoWorkerPerfInt64("NICO_TIMELINE_WORKER_DURATION_MS", 30000)
	if err != nil || durationMs < 1000 || durationMs > 60000 {
		t.Fatalf("NICO_TIMELINE_WORKER_DURATION_MS must be 1000..60000, got %d (err=%v)", durationMs, err)
	}
	encoder := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_ENCODER")))
	if encoder == "" {
		encoder = "nvenc"
	}
	outputMode := strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_OUTPUT_MODE"))
	if outputMode == "" {
		outputMode = string(video.NicoOutputTee)
	}
	gpuBackend := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_WORKER_GPU_BACKEND")))
	if gpuBackend == "" {
		gpuBackend = "vulkan"
	}
	slots, err := nicoWorkerPerfInt("NICO_TIMELINE_WORKER_READBACK_SLOTS", 3)
	if err != nil {
		t.Fatal(err)
	}
	runID := "session-cancel-" + randomSuffix()
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "partial.mp4")
	hlsPath := filepath.Join(outputDir, "hls")
	scratchRoot, err := os.MkdirTemp("", "ncs-")
	if err != nil {
		t.Fatal(err)
	}
	scratchRoot, err = filepath.Abs(scratchRoot)
	if err != nil {
		t.Fatal(err)
	}

	evidence := nicoWorkerSessionCancellationEvidence{
		SchemaVersion:        1,
		SessionID:            "",
		RunID:                runID,
		StartedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		WorkerSHA256:         requireNicoPerfHash(t, workerExe),
		SourceSHA256:         requireNicoPerfHash(t, source),
		SnapshotSHA256:       requireNicoPerfHash(t, snapshot),
		FFmpegSHA256:         requireNicoPerfHash(t, ffmpeg),
		HelperSHA256:         requireNicoPerfHash(t, helper),
		BrowserSHA256:        requireNicoPerfHash(t, browser),
		ScratchRoot:          scratchRoot,
		SnapshotCommentCount: snapshotCommentCount,
		Members:              make([]nicoWorkerSessionCancellationMemberEvidence, 0, 16),
	}
	evidencePath := filepath.Join(evidenceRoot, "nico-session-cancel-"+randomSuffix()+".json")
	var ownedHandles []*nicoWorkerSessionOwnedHandle
	var manager *nicoWorkerSessionManager
	var session *nicoWorkerSession
	var cancelJob context.CancelFunc
	var runDone <-chan nicoWorkerSessionRunOutcome
	cleanupFinished := false
	defer func() {
		if cancelJob != nil {
			cancelJob()
		}
		if manager != nil {
			start := time.Now()
			if closeErr := manager.close(); closeErr != nil {
				t.Errorf("deferred cancel Close/Wait: %v", closeErr)
			}
			if !cleanupFinished && runDone != nil {
				select {
				case outcome := <-runDone:
					if outcome.err != nil && evidence.CancelError == "" {
						evidence.CancelError = outcome.err.Error()
					}
				case <-time.After(30 * time.Second):
					t.Errorf("deferred canceled session did not finish within 30 seconds")
				}
			}
			if evidence.TeardownMilliseconds == 0 {
				evidence.TeardownMilliseconds = time.Since(start).Milliseconds()
			}
		}
		if session != nil {
			if _, err := session.process.Wait(); err != nil && evidence.CancelError == "" {
				evidence.CancelError = err.Error()
			}
		}
		if err := verifyNicoSessionCancellationHandles(ownedHandles, 20*time.Second); err != nil {
			t.Errorf("deferred wait for owned Job members: %v", err)
		}
		if outputPath != "" {
			_, err := os.Stat(outputPath)
			evidence.OutputRemoved = errors.Is(err, os.ErrNotExist)
		}
		if hlsPath != "" {
			_, err := os.Stat(hlsPath)
			evidence.HLSStagingRemoved = errors.Is(err, os.ErrNotExist)
		}
		if evidence.TeeWorkspacePath != "" {
			_, err := os.Stat(evidence.TeeWorkspacePath)
			evidence.TeeWorkspaceRemoved = errors.Is(err, os.ErrNotExist)
			if !evidence.TeeWorkspaceRemoved {
				t.Errorf("tee workspace remains after Close/Wait: %q (err=%v)", evidence.TeeWorkspacePath, err)
			}
		}
		if evidence.ProfilePath != "" {
			_, err := os.Stat(evidence.ProfilePath)
			evidence.ProfileRemoved = errors.Is(err, os.ErrNotExist)
		}
		if evidence.SessionTempRoot != "" {
			_, err := os.Stat(evidence.SessionTempRoot)
			evidence.SessionTempRootGone = errors.Is(err, os.ErrNotExist)
		}
		entries, readErr := os.ReadDir(scratchRoot)
		if readErr != nil {
			evidence.ScratchRootContents = append(evidence.ScratchRootContents, "<read error: "+readErr.Error()+">")
		} else {
			for _, entry := range entries {
				evidence.ScratchRootContents = append(evidence.ScratchRootContents, entry.Name())
			}
			if len(entries) == 0 {
				if removeErr := os.Remove(scratchRoot); removeErr == nil {
					evidence.ScratchRootRemoved = true
				} else {
					t.Errorf("remove exact empty owned temp root %q: %v", scratchRoot, removeErr)
				}
			} else {
				t.Errorf("owned worker temp root retains entries after Close/Wait: %q: %v", scratchRoot, evidence.ScratchRootContents)
			}
		}
		for _, owned := range ownedHandles {
			evidence.Members = append(evidence.Members, owned.evidence)
			if owned.handle != 0 {
				windows.CloseHandle(owned.handle)
			}
		}
		data, err := json.Marshal(evidence)
		if err != nil {
			t.Errorf("marshal cancellation evidence: %v", err)
			return
		}
		if err := os.WriteFile(evidencePath, append(data, '\n'), 0600); err != nil {
			t.Errorf("write cancellation evidence %s: %v", evidencePath, err)
		}
	}()

	oldWorkerExecutable := nicoWorkerExecutable
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }
	t.Cleanup(func() { nicoWorkerExecutable = oldWorkerExecutable })
	t.Setenv("TEMP", scratchRoot)
	t.Setenv("TMP", scratchRoot)
	t.Setenv("TMPDIR", scratchRoot)
	profilePathProbe := filepath.Join(scratchRoot, "imagepad-niconico-session-1234567890", "imagepad-niconi-profile-1234567890", "DevToolsActivePort")
	if len(profilePathProbe) > 240 {
		t.Fatalf("test-owned Chrome DevToolsActivePort path exceeds safe length: %d (%q)", len(profilePathProbe), profilePathProbe)
	}
	if entries, err := os.ReadDir(scratchRoot); err != nil || len(entries) != 0 {
		t.Fatalf("owned worker temp root must start empty: %q entries=%v err=%v", scratchRoot, entries, err)
	}
	owner, cancelOwner := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelOwner()
	manager = newNicoWorkerSessionManager(owner, nil, nil)
	options, err := nicoWorkerCPUOptionsForPerformanceTest(0)
	if err != nil {
		t.Fatal(err)
	}
	options.SampleInterval = 50 * time.Millisecond

	request := nicoexportworker.Request{
		Version: 1, RunID: runID, MediaID: "session-cancel",
		SourcePath: source, SnapshotPath: snapshot, OutputPath: outputPath, HLSStagingDir: hlsPath,
		OutputMode: outputMode, FFmpeg: ffmpeg, BrowserPath: browser,
		Backend: "timeline", TimelineEnabled: true, TimelineCompositor: helper,
		TimelineReadbackSlots: slots, TimelineGPUBackend: gpuBackend, Encoder: encoder,
		Width: 1280, Height: 720, DurationMs: durationMs, FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k",
	}
	evidence.RunID = runID
	identity, err := nicoWorkerSessionIdentity(request, options)
	if err != nil {
		t.Fatal(err)
	}
	session, err = manager.ensureSession(options, identity, false)
	if err != nil {
		t.Fatalf("start owned session: %v", err)
	}
	evidence.SessionID = session.id
	evidence.SessionTempRoot = session.tempRoot
	before, err := session.process.Snapshot()
	if err != nil {
		t.Fatalf("snapshot before job: %v", err)
	}
	jobCtx, cancel := context.WithCancel(owner)
	cancelJob = cancel
	resultChannel := make(chan nicoWorkerSessionRunOutcome, 1)
	runDone = resultChannel
	progressEvents := make(chan nicoexportworker.Event, 1024)
	go func() {
		result, report, err := manager.run(jobCtx, request, options, func(event nicoexportworker.Event) {
			select {
			case progressEvents <- event:
			default:
			}
		})
		resultChannel <- nicoWorkerSessionRunOutcome{result: result, report: report, err: err}
	}()

	paths := map[string]string{
		"worker":  workerExe,
		"browser": browser,
		"helper":  helper,
		"ffmpeg":  ffmpeg,
	}
	profile, progress, err := observeNicoSessionCancellationMembers(t, session, before, paths, outputDir, session.tempRoot, resultChannel, progressEvents, &evidence, &ownedHandles)
	if err != nil {
		evidence.ProfilePath = profile
		evidence.RenderFramesComplete = progress.Completed
		evidence.RenderFramesTotal = progress.Total
		evidence.TimelineFallback = progress.TimelineFallback
		var earlyRunErr error
		select {
		case outcome := <-resultChannel:
			resultChannel <- outcome
			earlyRunErr = outcome.err
		default:
		}
		t.Fatalf("observe live session members before cancel: %v; early run error: %v; worker stderr: %q", err, earlyRunErr, session.stderr.String())
	}
progressEventsDrainLoop:
	for {
		select {
		case event := <-progressEvents:
			if event.Stage == "timeline_fallback" {
				progress.TimelineFallback = true
			}
			if event.Stage == "render" && event.Total > 1 {
				progress.Completed = event.Completed
				progress.Total = event.Total
			}
		default:
			break progressEventsDrainLoop
		}
	}
	evidence.ProfilePath = profile
	evidence.RenderFramesComplete = progress.Completed
	evidence.RenderFramesTotal = progress.Total
	evidence.TimelineFallback = progress.TimelineFallback
	if !hasNicoSessionCancellationRole(ownedHandles, "worker") || !hasNicoSessionCancellationRole(ownedHandles, "browser") || !hasNicoSessionCancellationRole(ownedHandles, "helper") || !hasNicoSessionCancellationRole(ownedHandles, "ffmpeg") {
		t.Fatalf("did not capture live handles for all required session members before cancellation: %+v", evidence.Members)
	}
	if evidence.ProfilePath == "" {
		t.Fatal("could not identify the worker-owned Chrome profile before cancellation")
	}
	if progress.TimelineFallback {
		t.Fatalf("timeline renderer fell back before cancellation: worker stderr=%q", session.stderr.String())
	}
	observedWorkspace := evidence.TeeWorkspacePath
	staging, err := inspectNicoSessionCancellationTeeStaging(outputDir)
	if err != nil {
		t.Fatalf("recheck live tee workspace immediately before cancellation: %v", err)
	}
	recordNicoSessionCancellationTeeStaging(&evidence, staging)
	if observedWorkspace == "" || staging.WorkspacePath != observedWorkspace {
		t.Fatalf("live tee workspace changed before cancellation: first=%q now=%q", observedWorkspace, staging.WorkspacePath)
	}
	if staging.MP4Bytes <= 0 || len(staging.HLSFiles) == 0 || staging.HLSBytes <= 0 {
		t.Fatalf("partial MP4/HLS staging disappeared before cancellation: workspace=%q mp4_bytes=%d hls_files=%+v hls_bytes=%d", staging.WorkspacePath, staging.MP4Bytes, staging.HLSFiles, staging.HLSBytes)
	}

	workerPID, workerLive, err := liveNicoSessionCancellationMember(ownedHandles, "worker")
	if err != nil || !workerLive {
		t.Fatalf("worker was not live immediately before cancellation: pid=%d live=%t err=%v", workerPID, workerLive, err)
	}
	ffmpegPID, ffmpegLive, err := liveNicoSessionCancellationMember(ownedHandles, "ffmpeg")
	if err != nil || !ffmpegLive {
		t.Fatalf("FFmpeg was not live immediately before cancellation: pid=%d live=%t err=%v", ffmpegPID, ffmpegLive, err)
	}
	phase := nicoSessionCancellationPhase(progress, ffmpegLive)
	if phase == "" {
		t.Fatalf("cancellation no longer has an active render/encode phase: progress=%d/%d ffmpeg_live=%t", progress.Completed, progress.Total, ffmpegLive)
	}
	if len(resultChannel) != 0 {
		t.Fatalf("worker run completed before cancellation could be issued")
	}
	progress.Phase = phase
	evidence.RenderFramesComplete = progress.Completed
	evidence.RenderFramesTotal = progress.Total
	evidence.TimelineFallback = progress.TimelineFallback
	evidence.CancelPhase = phase
	evidence.WorkerLiveAtCancel = workerLive
	evidence.FFmpegPIDAtCancel = ffmpegPID
	evidence.FFmpegLiveAtCancel = ffmpegLive
	evidence.RunPendingAtCancel = true
	evidence.CancelAt = time.Now().UTC().Format(time.RFC3339Nano)
	cancelStart := time.Now()
	cancel()
	select {
	case outcome := <-resultChannel:
		cleanupFinished = true
		if outcome.err == nil || !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("cancel result=%+v err=%v, want terminal context cancellation", outcome.result, outcome.err)
		}
		evidence.CancelError = outcome.err.Error()
	case <-time.After(30 * time.Second):
		t.Fatal("canceled manager.run did not finish Close/Wait within 30 seconds")
	}
	if err := manager.close(); err != nil {
		t.Fatalf("manager Close/Wait after cancellation: %v", err)
	}
	if _, err := session.process.Wait(); err != nil {
		t.Fatalf("cached session Wait after cancellation: %v", err)
	}
	evidence.TeardownMilliseconds = time.Since(cancelStart).Milliseconds()
	evidence.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if evidence.TeardownMilliseconds >= 30000 {
		t.Fatalf("Close/Wait exceeded the bounded teardown target: %dms", evidence.TeardownMilliseconds)
	}
	if err := verifyNicoSessionCancellationHandles(ownedHandles, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(evidence.ProfilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker-owned Chrome profile remains after Close/Wait: %q (err=%v)", evidence.ProfilePath, err)
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial MP4 remains after cancellation: %q (err=%v)", outputPath, err)
	}
	if _, err := os.Stat(hlsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("HLS staging remains after cancellation: %q (err=%v)", hlsPath, err)
	}
	if _, err := os.Stat(evidence.TeeWorkspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tee workspace remains after cancellation: %q (err=%v)", evidence.TeeWorkspacePath, err)
	}
	evidence.TeeWorkspaceRemoved = true
	if _, err := os.Stat(session.tempRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session-owned temp root remains after cancellation: %q (err=%v)", session.tempRoot, err)
	}
	evidence.SessionTempRootGone = true
	cleanupFinished = true
}

func observeNicoSessionCancellationMembers(
	t *testing.T,
	session *nicoWorkerSession,
	before nicoexportbudget.Report,
	paths map[string]string,
	stagingRoot, scratchRoot string,
	runResults chan nicoWorkerSessionRunOutcome,
	progressEvents <-chan nicoexportworker.Event,
	evidence *nicoWorkerSessionCancellationEvidence,
	ownedHandles *[]*nicoWorkerSessionOwnedHandle,
) (string, nicoWorkerSessionCancellationProgress, error) {
	t.Helper()
	startSample := len(before.Samples)
	seen := make(map[uint32]bool)
	profile := ""
	progress := nicoWorkerSessionCancellationProgress{}
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			return profile, progress, fmt.Errorf("timed out waiting for live worker/browser/helper/ffmpeg, active render/encode phase, and partial outputs")
		case <-ticker.C:
			progressEventsDrained := false
			for !progressEventsDrained {
				select {
				case event := <-progressEvents:
					if event.Stage == "timeline_fallback" {
						progress.TimelineFallback = true
					}
					if event.Stage == "render" && event.Total > 1 {
						progress.Completed = event.Completed
						progress.Total = event.Total
					}
				default:
					progressEventsDrained = true
				}
			}
			report, err := session.process.Snapshot()
			if err != nil {
				return profile, progress, fmt.Errorf("snapshot cancellation Job: %w", err)
			}
			if startSample > len(report.Samples) {
				startSample = len(report.Samples)
			}
			for _, sample := range report.Samples[startSample:] {
				for _, pid := range sample.PIDs {
					if seen[pid] {
						continue
					}
					handle, image, live, err := openNicoSessionLiveMember(pid)
					if err != nil || !live {
						continue
					}
					role := ""
					for candidate, expected := range paths {
						if strings.EqualFold(filepath.Clean(image), filepath.Clean(expected)) {
							role = candidate
							break
						}
					}
					if role == "" {
						_ = windows.CloseHandle(handle)
						continue
					}
					seen[pid] = true
					owned := &nicoWorkerSessionOwnedHandle{
						evidence: nicoWorkerSessionCancellationMemberEvidence{
							PID: pid, ImagePath: image, Role: role, FirstSeenAt: time.Now().UTC(), JobSampleMs: sample.At.Milliseconds(),
						},
						handle: handle,
					}
					*ownedHandles = append(*ownedHandles, owned)
					if role == "browser" && profile == "" {
						profile, err = nicoSessionProfileUnderRoot(scratchRoot)
						if err != nil {
							return profile, progress, fmt.Errorf("locate browser profile under isolated worker temp root: %w", err)
						}
					}
				}
			}
			if profile == "" {
				profile, err = nicoSessionProfileUnderRoot(scratchRoot)
				if err != nil {
					return profile, progress, fmt.Errorf("locate browser profile under isolated worker temp root: %w", err)
				}
			}
			profileLive := false
			if profile != "" {
				if !pathUnderRoot(scratchRoot, profile) || !strings.HasPrefix(filepath.Base(profile), "imagepad-niconi-profile-") {
					return profile, progress, fmt.Errorf("browser profile is outside isolated worker temp root: %q", profile)
				}
				if info, statErr := os.Stat(profile); statErr == nil && info.IsDir() {
					profileLive = true
				} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
					return profile, progress, fmt.Errorf("inspect sampled browser profile %q: %w", profile, statErr)
				}
			}
			if _, _, err := liveNicoSessionCancellationMember(*ownedHandles, "worker"); err != nil {
				return profile, progress, fmt.Errorf("check sampled worker exit state: %w", err)
			}
			startSample = len(report.Samples)
			roles := make(map[string]bool)
			for _, handle := range *ownedHandles {
				roles[handle.evidence.Role] = true
			}
			staging, stagingErr := inspectNicoSessionCancellationTeeStaging(stagingRoot)
			if stagingErr != nil {
				return profile, progress, fmt.Errorf("inspect active tee staging: %w", stagingErr)
			}
			recordNicoSessionCancellationTeeStaging(evidence, staging)
			_, workerLive, workerErr := liveNicoSessionCancellationMember(*ownedHandles, "worker")
			if workerErr != nil {
				return profile, progress, fmt.Errorf("recheck worker liveness before cancellation gate: %w", workerErr)
			}
			_, ffmpegLive, ffmpegErr := liveNicoSessionCancellationMember(*ownedHandles, "ffmpeg")
			if ffmpegErr != nil {
				return profile, progress, fmt.Errorf("check active FFmpeg liveness: %w", ffmpegErr)
			}
			phase := nicoSessionCancellationPhase(progress, ffmpegLive)
			var completedOutcome *nicoWorkerSessionRunOutcome
			select {
			case outcome := <-runResults:
				runResults <- outcome
				completedOutcome = &outcome
			default:
			}
			if roles["worker"] && roles["browser"] && roles["helper"] && roles["ffmpeg"] && profileLive &&
				workerLive && completedOutcome == nil && phase != "" && staging.MP4Bytes > 0 && len(staging.HLSFiles) > 0 && staging.HLSBytes > 0 {
				progress.Phase = phase
				return profile, progress, nil
			}
			if completedOutcome != nil {
				return profile, progress, fmt.Errorf("worker run finished before live tee staging and active render/encode phase were observed: result_err=%v frames=%d/%d staging=%q mp4_bytes=%d hls_files=%+v hls_bytes=%d", completedOutcome.err, progress.Completed, progress.Total, staging.WorkspacePath, staging.MP4Bytes, staging.HLSFiles, staging.HLSBytes)
			}
			if !workerLive && hasNicoSessionCancellationRole(*ownedHandles, "worker") {
				return profile, progress, fmt.Errorf("sampled worker exited before live tee staging and active progress were observed: staging=%q mp4_bytes=%d hls_files=%+v hls_bytes=%d", staging.WorkspacePath, staging.MP4Bytes, staging.HLSFiles, staging.HLSBytes)
			}
		}
	}
}

func liveNicoSessionCancellationMember(ownedHandles []*nicoWorkerSessionOwnedHandle, role string) (uint32, bool, error) {
	for _, owned := range ownedHandles {
		if owned.evidence.Role != role || owned.handle == 0 {
			continue
		}
		status, err := windows.WaitForSingleObject(owned.handle, 0)
		if err != nil {
			return 0, false, fmt.Errorf("check %s PID %d liveness: %w", role, owned.evidence.PID, err)
		}
		if status == uint32(windows.WAIT_TIMEOUT) {
			return owned.evidence.PID, true, nil
		}
		if status != uint32(windows.WAIT_OBJECT_0) {
			return 0, false, fmt.Errorf("check %s PID %d liveness returned wait status %d", role, owned.evidence.PID, status)
		}
	}
	return 0, false, nil
}

func openNicoSessionLiveMember(pid uint32) (windows.Handle, string, bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return 0, "", false, err
	}
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		_ = windows.CloseHandle(handle)
		return 0, "", false, err
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		_ = windows.CloseHandle(handle)
		return 0, "", false, err
	}
	status, err = windows.WaitForSingleObject(handle, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		_ = windows.CloseHandle(handle)
		return 0, "", false, err
	}
	return handle, filepath.Clean(windows.UTF16ToString(buffer[:size])), true, nil
}

func TestNicoSessionProfileUnderRoot(t *testing.T) {
	t.Run("not created yet", func(t *testing.T) {
		root := t.TempDir()
		got, err := nicoSessionProfileUnderRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("profile = %q, want empty while Chrome has not created it", got)
		}
	})
	t.Run("one owned directory", func(t *testing.T) {
		root := t.TempDir()
		profile := filepath.Join(root, "imagepad-niconi-profile-test")
		if err := os.Mkdir(profile, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "imagepad-niconi-profile-marker"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := nicoSessionProfileUnderRoot(root)
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.Abs(profile)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("profile = %q, want %q", got, want)
		}
	})
	t.Run("multiple directories are ambiguous", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"imagepad-niconi-profile-one", "imagepad-niconi-profile-two"} {
			if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := nicoSessionProfileUnderRoot(root); err == nil {
			t.Fatalf("profile = %q, want an ambiguity error", got)
		}
	})
}

func nicoSessionProfileUnderRoot(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var profile string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "imagepad-niconi-profile-") {
			continue
		}
		if profile != "" {
			return "", fmt.Errorf("multiple worker profiles found under isolated temp root %q", root)
		}
		profile = filepath.Join(root, entry.Name())
	}
	if profile == "" {
		return "", nil
	}
	return filepath.Abs(profile)
}

func pathUnderRoot(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hasNicoSessionCancellationRole(handles []*nicoWorkerSessionOwnedHandle, role string) bool {
	for _, owned := range handles {
		if owned.evidence.Role == role && owned.handle != 0 {
			return true
		}
	}
	return false
}

func verifyNicoSessionCancellationHandles(handles []*nicoWorkerSessionOwnedHandle, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for _, owned := range handles {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("timed out waiting for sampled PID %d (%s)", owned.evidence.PID, owned.evidence.Role)
		}
		ms := uint32(remaining / time.Millisecond)
		if ms == 0 {
			ms = 1
		}
		status, err := windows.WaitForSingleObject(owned.handle, ms)
		if err != nil {
			return fmt.Errorf("wait sampled PID %d: %w", owned.evidence.PID, err)
		}
		if status != uint32(windows.WAIT_OBJECT_0) {
			return fmt.Errorf("sampled PID %d (%s) did not signal before deadline (status=%d)", owned.evidence.PID, owned.evidence.Role, status)
		}
		owned.evidence.HandleSignaled = true
	}
	return nil
}

type nicoWorkerSessionPerfConfig struct {
	requestedCPU       int
	workerExe          string
	freshBrowserPerJob bool
	source             string
	snapshot           string
	ffmpeg             string
	helper             string
	browser            string
	durationMs         int64
	encoder            string
	outputMode         string
	gpuBackend         string
	slots              int
}

func runNicoWorkerSessionPerformanceCondition(t *testing.T, config nicoWorkerSessionPerfConfig) nicoWorkerSessionConditionEvidence {
	t.Helper()
	options, err := nicoWorkerCPUOptionsForPerformanceTest(config.requestedCPU)
	if err != nil {
		t.Fatal(err)
	}
	options.SampleInterval = 50 * time.Millisecond
	owner, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	manager := newNicoWorkerSessionManager(owner, nil, nil)
	managerClosed := false
	t.Cleanup(func() {
		if !managerClosed {
			if err := manager.close(); err != nil {
				t.Errorf("close/wait CPU %d session manager: %v", config.requestedCPU, err)
			}
		}
	})

	evidence := nicoWorkerSessionConditionEvidence{
		ConditionID:         fmt.Sprintf("cpu-%03d-%s", config.requestedCPU, randomSuffix()),
		RequestedCPUPercent: config.requestedCPU,
		JobCPURate:          options.Percent * 100,
		Jobs:                make([]nicoWorkerSessionJobEvidence, 0, 2),
	}
	var retirementSession *nicoWorkerSession
	for jobIndex := 0; jobIndex < 2; jobIndex++ {
		runID := fmt.Sprintf("%s-job-%d", evidence.ConditionID, jobIndex+1)
		runDir := filepath.Join(t.TempDir(), runID)
		if err := os.Mkdir(runDir, 0700); err != nil {
			t.Fatal(err)
		}
		request := nicoexportworker.Request{
			Version: 1, RunID: runID, MediaID: evidence.ConditionID,
			SourcePath: config.source, SnapshotPath: config.snapshot, OutputPath: filepath.Join(runDir, "rendered.mp4"),
			HLSStagingDir: filepath.Join(runDir, "hls"), OutputMode: config.outputMode, FFmpeg: config.ffmpeg, BrowserPath: config.browser,
			Backend: "timeline", TimelineEnabled: true, TimelineCompositor: config.helper,
			TimelineReadbackSlots: config.slots, TimelineGPUBackend: config.gpuBackend, Encoder: config.encoder,
			Width: 1280, Height: 720, DurationMs: config.durationMs, FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k",
		}
		identity, err := nicoWorkerSessionIdentity(request, options)
		if err != nil {
			t.Fatalf("resolve session identity: %v", err)
		}
		session, err := manager.ensureSession(options, identity, false)
		if err != nil {
			t.Fatalf("start CPU %d session: %v", config.requestedCPU, err)
		}
		retirementSession = session
		before, err := session.process.Snapshot()
		if err != nil {
			t.Fatalf("snapshot before %s: %v", runID, err)
		}

		outcome := make(chan nicoWorkerSessionRunOutcome, 1)
		go func() {
			result, report, err := manager.run(owner, request, options, nil)
			outcome <- nicoWorkerSessionRunOutcome{result: result, report: report, err: err}
		}()
		members, runOutcome := observeNicoWorkerSessionRun(t, manager, owner, session, before, runID, request.BrowserPath, request.TimelineCompositor, request.FFmpeg, outcome)
		if runOutcome.err != nil {
			t.Fatalf("CPU %d run %s: %v", config.requestedCPU, runID, runOutcome.err)
		}
		if !runOutcome.result.OK || runOutcome.result.RunID != runID || runOutcome.result.MediaID != request.MediaID {
			t.Fatalf("CPU %d run %s result=%+v", config.requestedCPU, runID, runOutcome.result)
		}
		if !runOutcome.report.Verified || runOutcome.report.JobCPURate != options.Percent*100 {
			t.Fatalf("CPU %d run %s unverified report=%+v", config.requestedCPU, runID, runOutcome.report)
		}
		for _, role := range []string{"browser", "helper", "ffmpeg"} {
			if !hasNicoSessionMemberRole(members, role) {
				t.Fatalf("CPU %d run %s did not observe a live %s process in a Job sample; observations=%+v", config.requestedCPU, runID, role, members)
			}
		}
		after, err := session.process.Snapshot()
		if err != nil {
			t.Fatalf("snapshot after %s: %v", runID, err)
		}
		cumulativeDelta := after.CPUSeconds - before.CPUSeconds
		if cumulativeDelta < 0 {
			t.Fatalf("CPU %d run %s cumulative CPU regressed: before=%f after=%f", config.requestedCPU, runID, before.CPUSeconds, after.CPUSeconds)
		}
		tolerance := options.SampleInterval + 100*time.Millisecond
		if math.Abs(runOutcome.report.CPUSeconds-cumulativeDelta) > tolerance.Seconds() {
			t.Fatalf("CPU %d run %s reported CPU delta %f differs from cumulative snapshots %f (before=%f after=%f tolerance=%s)", config.requestedCPU, runID, runOutcome.report.CPUSeconds, cumulativeDelta, before.CPUSeconds, after.CPUSeconds, tolerance)
		}
		outputInfo, err := os.Stat(request.OutputPath)
		if err != nil {
			t.Fatalf("CPU %d run %s output missing: %v", config.requestedCPU, runID, err)
		}
		outputHash, err := nicoWorkerPerfSHA256(request.OutputPath)
		if err != nil {
			t.Fatal(err)
		}
		evidence.Jobs = append(evidence.Jobs, nicoWorkerSessionJobEvidence{
			RunID: runID, WorkerWallSeconds: runOutcome.result.WorkerWallSeconds,
			ReportedCPUSeconds: runOutcome.report.CPUSeconds,
			CumulativeBefore:   before.CPUSeconds, CumulativeAfter: after.CPUSeconds,
			CumulativeDelta: cumulativeDelta, JobCPURate: runOutcome.report.JobCPURate,
			OutputSHA256: outputHash, OutputBytes: outputInfo.Size(), ObservedJobMembers: members,
		})
	}
	if err := manager.close(); err != nil {
		managerClosed = true
		evidence.SessionRetirementError = err.Error()
		if retirementSession != nil && retirementSession.stderr != nil {
			evidence.SessionRetirementStderr = strings.TrimSpace(retirementSession.stderr.String())
		}
		t.Errorf("close/wait CPU %d session before next condition: %v; stderr tail: %q", config.requestedCPU, err, evidence.SessionRetirementStderr)
	} else {
		managerClosed = true
	}
	return evidence
}

func observeNicoWorkerSessionRun(
	t *testing.T,
	manager *nicoWorkerSessionManager,
	owner context.Context,
	session *nicoWorkerSession,
	before nicoexportbudget.Report,
	runID, browserPath, helperPath, ffmpegPath string,
	outcome <-chan nicoWorkerSessionRunOutcome,
) ([]nicoWorkerSessionMemberEvidence, nicoWorkerSessionRunOutcome) {
	t.Helper()
	start := time.Now()
	startSample := len(before.Samples)
	paths := map[string]string{
		"browser": browserPath,
		"helper":  helperPath,
		"ffmpeg":  ffmpegPath,
	}
	observed := make(map[string]nicoWorkerSessionMemberEvidence, len(paths))
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case result := <-outcome:
			members := make([]nicoWorkerSessionMemberEvidence, 0, len(observed))
			for _, member := range observed {
				members = append(members, member)
			}
			return members, result
		case <-owner.Done():
			t.Fatalf("session performance run %s exceeded owner deadline: %v", runID, owner.Err())
		case <-ticker.C:
			manager.mu.Lock()
			current := manager.current
			manager.mu.Unlock()
			if current != session {
				continue
			}
			report, err := session.process.Snapshot()
			if err != nil {
				t.Fatalf("snapshot live Job during %s: %v", runID, err)
			}
			if startSample > len(report.Samples) {
				startSample = len(report.Samples)
			}
			for _, sample := range report.Samples[startSample:] {
				for _, pid := range sample.PIDs {
					image, live, err := nicoSessionLiveProcessImage(pid)
					if err != nil || !live {
						continue
					}
					for role, expected := range paths {
						if strings.EqualFold(filepath.Clean(image), filepath.Clean(expected)) {
							if _, exists := observed[role]; !exists {
								observed[role] = nicoWorkerSessionMemberEvidence{
									RunID: runID, Role: role, PID: pid, ImagePath: image,
									ObservedAt: time.Now().UTC(), JobSampleMilliseconds: sample.At.Milliseconds(),
								}
							}
						}
					}
				}
			}
			startSample = len(report.Samples)
			if len(observed) == len(paths) {
				select {
				case result := <-outcome:
					members := make([]nicoWorkerSessionMemberEvidence, 0, len(observed))
					for _, member := range observed {
						members = append(members, member)
					}
					return members, result
				default:
				}
			}
			if time.Since(start) > 10*time.Minute {
				t.Fatalf("session performance run %s did not finish within 10 minutes", runID)
			}
		}
	}
}

func nicoSessionLiveProcessImage(pid uint32) (string, bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return "", false, err
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		return "", false, err
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", false, err
	}
	status, err = windows.WaitForSingleObject(handle, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		return "", false, err
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:size])), true, nil
}

func hasNicoSessionMemberRole(members []nicoWorkerSessionMemberEvidence, role string) bool {
	for _, member := range members {
		if member.Role == role {
			return true
		}
	}
	return false
}

func requireNicoPerfHash(t *testing.T, path string) string {
	t.Helper()
	hash, err := nicoWorkerPerfSHA256(path)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hash
}

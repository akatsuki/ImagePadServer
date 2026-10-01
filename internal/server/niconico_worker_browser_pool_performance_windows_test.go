//go:build windows

package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/nicoexportworker"
	"imagepadserver/internal/video"
)

const (
	nicoWorkerT3SourceSHA256    = "8aa46ff529686272ca233babfd24884f2b28ef25a24534f278e95560e755ae20"
	nicoWorkerT3SnapshotSHA256  = "b7be86d86016175c99925f5f1b5651d43279eeffcc54deb1c22c5b2e6e15e946"
	nicoWorkerT3HelperSHA256    = "a605dccee55f392cc81fc82d424d3dc214ce9c91c8a332657ab7126d9b377fbe"
	nicoWorkerT3RandomSeed      = uint32(0x54423331)
	nicoWorkerT3PairsPerSession = 10
	nicoWorkerT3Sessions        = 2
)

type nicoWorkerT3ArmEvidence struct {
	RunID                 string   `json:"run_id"`
	WallSeconds           float64  `json:"wall_seconds"`
	WorkerWallSeconds     float64  `json:"worker_wall_seconds"`
	ReportedCPUSeconds    float64  `json:"reported_cpu_seconds"`
	JobCPURate            uint32   `json:"job_cpu_rate"`
	Verified              bool     `json:"verified"`
	ObservedBrowserPID    uint32   `json:"observed_browser_pid,omitempty"`
	ObservedWorkerPID     uint32   `json:"observed_worker_pid,omitempty"`
	ObservedJobPIDs       []uint32 `json:"observed_job_pids,omitempty"`
	ObservedJobPIDCount   int      `json:"observed_job_pid_count"`
	OutputSHA256          string   `json:"output_sha256,omitempty"`
	OutputBytes           int64    `json:"output_bytes,omitempty"`
	DecodedFrameCount     int      `json:"decoded_frame_count,omitempty"`
	DecodedFrameSHA256    string   `json:"decoded_frame_sha256,omitempty"`
	HLSPlaylistSHA256     string   `json:"hls_playlist_sha256,omitempty"`
	HLSSegmentCount       int      `json:"hls_segment_count,omitempty"`
	HLSDecodedFrameCount  int      `json:"hls_decoded_frame_count,omitempty"`
	HLSDecodedFrameSHA256 string   `json:"hls_decoded_frame_sha256,omitempty"`
}

type nicoWorkerT3PairEvidence struct {
	PairNumber               int                     `json:"pair_number"`
	Order                    string                  `json:"order"`
	FreshWorker              nicoWorkerT3ArmEvidence `json:"fresh_worker"`
	SessionCandidate         nicoWorkerT3ArmEvidence `json:"session_candidate"`
	MP4FrameParityPassed     bool                    `json:"mp4_frame_parity_passed"`
	MP4FrameDifferenceCount  int                     `json:"mp4_frame_difference_count"`
	MP4FirstDifferenceIndex  int                     `json:"mp4_first_difference_index"`
	FreshHLSMatchesMP4       bool                    `json:"fresh_hls_matches_mp4"`
	SessionHLSMatchesMP4     bool                    `json:"session_hls_matches_mp4"`
	PairedSavingSeconds      float64                 `json:"paired_saving_seconds"`
	SessionRetirementSeconds float64                 `json:"session_retirement_seconds,omitempty"`
}

type nicoWorkerT3SessionEvidence struct {
	SessionNumber       int                        `json:"session_number"`
	SessionSetupSeconds float64                    `json:"session_setup_seconds"`
	Warmup              nicoWorkerT3ArmEvidence    `json:"warmup"`
	CandidateMode       string                     `json:"candidate_mode"`
	BrowserPID          uint32                     `json:"browser_pid,omitempty"`
	WorkerPID           uint32                     `json:"worker_pid"`
	Pairs               []nicoWorkerT3PairEvidence `json:"pairs"`
	RetirementSeconds   float64                    `json:"retirement_seconds"`
	RetirementError     string                     `json:"retirement_error,omitempty"`
}

type nicoWorkerT3Summary struct {
	ColdPairCount            int       `json:"cold_pair_count"`
	ColdFreshMedianSeconds   float64   `json:"cold_fresh_median_seconds"`
	ColdSessionMedianSeconds float64   `json:"cold_session_median_seconds"`
	ColdMedianSavingSeconds  float64   `json:"cold_median_saving_seconds"`
	ColdBootstrap95CISeconds []float64 `json:"cold_bootstrap_95_ci_seconds"`
	ColdBootstrapSeed        int64     `json:"cold_bootstrap_seed"`
	ColdBootstrapIterations  int       `json:"cold_bootstrap_iterations"`
	ColdBootstrapSamples     []float64 `json:"cold_bootstrap_samples_seconds,omitempty"`
	ColdNoRegressionPassed   bool      `json:"cold_no_regression_passed"`
	PairCountPerSession      int       `json:"pair_count_per_session"`
	FreshMedianSeconds       float64   `json:"fresh_median_seconds"`
	SessionMedianSeconds     float64   `json:"session_median_seconds"`
	MedianSavingSeconds      float64   `json:"median_saving_seconds"`
	SessionMedianSavings     []float64 `json:"session_median_savings_seconds"`
	BootstrapIterations      int       `json:"bootstrap_iterations"`
	BootstrapSeed            int64     `json:"warm_bootstrap_seed"`
	BootstrapSamples         []float64 `json:"warm_bootstrap_samples_seconds,omitempty"`
	Bootstrap95CISeconds     []float64 `json:"bootstrap_95_ci_seconds"`
	CorrectnessPassed        bool      `json:"correctness_passed"`
	SpeedGatePassed          bool      `json:"speed_gate_passed"`
}

type nicoWorkerT3PerfEvidence struct {
	SchemaVersion       int                           `json:"schema_version"`
	StartedAt           time.Time                     `json:"started_at"`
	CompletedAt         time.Time                     `json:"completed_at"`
	SourceSHA256        string                        `json:"source_sha256"`
	SnapshotSHA256      string                        `json:"snapshot_sha256"`
	WorkerSHA256        string                        `json:"worker_sha256"`
	FFmpegSHA256        string                        `json:"ffmpeg_sha256"`
	HelperSHA256        string                        `json:"helper_sha256"`
	BrowserSHA256       string                        `json:"browser_sha256"`
	Width               int                           `json:"width"`
	Height              int                           `json:"height"`
	DurationMs          int64                         `json:"duration_ms"`
	FPSNum              int64                         `json:"fps_num"`
	FPSDen              int64                         `json:"fps_den"`
	CommentCount        int                           `json:"comment_count"`
	Encoder             string                        `json:"encoder"`
	GPUBackend          string                        `json:"gpu_backend"`
	OutputMode          string                        `json:"output_mode"`
	RequestedCPUPercent int                           `json:"requested_cpu_percent"`
	JobCPURate          uint32                        `json:"job_cpu_rate"`
	PairOrder           string                        `json:"pair_order"`
	TimelineRandomSeed  uint32                        `json:"timeline_random_seed"`
	CandidateMode       string                        `json:"candidate_mode"`
	MediaArtifactsRoot  string                        `json:"media_artifacts_root,omitempty"`
	ColdPairs           []nicoWorkerT3PairEvidence    `json:"cold_pairs"`
	Sessions            []nicoWorkerT3SessionEvidence `json:"sessions"`
	Summary             nicoWorkerT3Summary           `json:"summary"`
}

type nicoWorkerT3RawArm struct {
	evidence nicoWorkerT3ArmEvidence
	output   string
	hlsDir   string
}

type nicoWorkerT3RawPair struct {
	evidence nicoWorkerT3PairEvidence
	fresh    nicoWorkerT3RawArm
	session  nicoWorkerT3RawArm
}

// TestNicoWorkerSessionBrowserPoolPairedPerformance is an opt-in Windows
// end-to-end comparison of the fresh one-request worker and the warmed
// session/BrowserPool route. CPU20 applies only to these measured Job objects.
func TestNicoWorkerSessionBrowserPoolPairedPerformance(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_T3_PERF") != "1" {
		t.Skip("set NICO_TIMELINE_T3_PERF=1 to run the BrowserPool/session A/B")
	}
	runNicoWorkerSessionPairedPerformance(t, false)
}

// TestNicoWorkerSessionFreshBrowserPerJobPairedPerformance isolates worker
// session reuse from BrowserPool reuse while holding the worker process open.
func TestNicoWorkerSessionFreshBrowserPerJobPairedPerformance(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_T4_ONLY_PERF") != "1" {
		t.Skip("set NICO_TIMELINE_T4_ONLY_PERF=1 to run the T4-only session A/B")
	}
	t.Setenv(nicoexportworker.SessionFreshBrowserPerJobEnv, "1")
	runNicoWorkerSessionPairedPerformance(t, true)
}

func runNicoWorkerSessionPairedPerformance(t *testing.T, freshBrowserPerJob bool) {
	t.Helper()
	mode := "browser_pool"
	prefix := "t3-browser-pool-cpu020"
	pairCountEnv := "NICO_TIMELINE_T3_PERF_PAIRS"
	preserveEnv := "NICO_TIMELINE_T3_PERF_PRESERVE_OUTPUTS"
	if freshBrowserPerJob {
		mode = "fresh_browser_per_job"
		prefix = "t4-worker-session-fresh-browser-cpu020"
		pairCountEnv = "NICO_TIMELINE_T4_ONLY_PERF_PAIRS"
		preserveEnv = "NICO_TIMELINE_T4_ONLY_PERF_PRESERVE_OUTPUTS"
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
	pairCount, err := nicoWorkerPerfInt(pairCountEnv, nicoWorkerT3PairsPerSession)
	if err != nil || pairCount < 1 || pairCount > nicoWorkerT3PairsPerSession {
		t.Fatalf("%s must be 1..%d, got %d (err=%v)", pairCountEnv, nicoWorkerT3PairsPerSession, pairCount, err)
	}

	sourceHash := requireNicoPerfHash(t, source)
	snapshotHash := requireNicoPerfHash(t, snapshot)
	workerHash := requireNicoPerfHash(t, workerExe)
	ffmpegHash := requireNicoPerfHash(t, ffmpeg)
	helperHash := requireNicoPerfHash(t, helper)
	browserHash := requireNicoPerfHash(t, browser)
	if sourceHash != nicoWorkerT3SourceSHA256 || snapshotHash != nicoWorkerT3SnapshotSHA256 {
		t.Fatalf("T3 fixture identity mismatch: source=%s snapshot=%s", sourceHash, snapshotHash)
	}
	if helperHash != nicoWorkerT3HelperSHA256 {
		t.Fatalf("T3 requires the T14-qualified NCT2 helper %s, got %s", nicoWorkerT3HelperSHA256, helperHash)
	}

	oldWorkerExecutable := nicoWorkerExecutable
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }
	t.Cleanup(func() { nicoWorkerExecutable = oldWorkerExecutable })

	options, err := nicoWorkerCPUOptionsForPerformanceTest(20)
	if err != nil {
		t.Fatal(err)
	}
	options.SampleInterval = 50 * time.Millisecond
	const durationMs = int64(6000)
	const width, height = 1280, 720
	const fpsNum, fpsDen = int64(30), int64(1)
	const encoder = "nvenc"
	const gpuBackend = "vulkan"
	const outputMode = string(video.NicoOutputTee)
	const slots = 3

	evidence := nicoWorkerT3PerfEvidence{
		SchemaVersion: 1, StartedAt: time.Now().UTC(),
		SourceSHA256: sourceHash, SnapshotSHA256: snapshotHash,
		WorkerSHA256: workerHash, FFmpegSHA256: ffmpegHash, HelperSHA256: helperHash, BrowserSHA256: browserHash,
		Width: width, Height: height, DurationMs: durationMs, FPSNum: fpsNum, FPSDen: fpsDen,
		CommentCount: 362, Encoder: encoder, GPUBackend: gpuBackend, OutputMode: outputMode,
		RequestedCPUPercent: 20, JobCPURate: options.Percent * 100,
		PairOrder:          "cold pairs alternate candidate-first/fresh-first; warm session 1 alternates, session 2 reverses",
		TimelineRandomSeed: nicoWorkerT3RandomSeed,
		CandidateMode:      mode,
		ColdPairs:          make([]nicoWorkerT3PairEvidence, 0, pairCount),
		Sessions:           make([]nicoWorkerT3SessionEvidence, 0, nicoWorkerT3Sessions),
	}
	runRoot := t.TempDir()
	if os.Getenv(preserveEnv) == "1" {
		runRoot, err = os.MkdirTemp(evidenceRoot, prefix+"-preserved-")
		if err != nil {
			t.Fatalf("create preserved T3 media root: %v", err)
		}
		evidence.MediaArtifactsRoot = runRoot
		t.Logf("%s media outputs are preserved under %s; remove only after auditing this exact root", prefix, runRoot)
	}
	allColdRawPairs := make([]nicoWorkerT3RawPair, 0, pairCount)
	allRawPairs := make([][]nicoWorkerT3RawPair, 0, nicoWorkerT3Sessions)
	allWarmups := make([]nicoWorkerT3RawArm, 0, nicoWorkerT3Sessions)
	allObservedPIDs := make(map[uint32]struct{})
	for pairIndex := 0; pairIndex < pairCount; pairIndex++ {
		coldPair, rawPair, observedPIDs := runNicoWorkerT3ColdPerfPair(t, runRoot, pairIndex, options, nicoWorkerSessionPerfConfig{
			requestedCPU: 20, workerExe: workerExe, source: source, snapshot: snapshot, ffmpeg: ffmpeg,
			helper: helper, browser: browser, durationMs: durationMs, encoder: encoder,
			outputMode: outputMode, gpuBackend: gpuBackend, slots: slots, freshBrowserPerJob: freshBrowserPerJob,
		})
		evidence.ColdPairs = append(evidence.ColdPairs, coldPair)
		allColdRawPairs = append(allColdRawPairs, rawPair)
		for pid := range observedPIDs {
			allObservedPIDs[pid] = struct{}{}
		}
	}
	for sessionIndex := 0; sessionIndex < nicoWorkerT3Sessions; sessionIndex++ {
		sessionEvidence, rawPairs, warmup, observedPIDs := runNicoWorkerT3PerfSession(t, runRoot, sessionIndex, pairCount, options, nicoWorkerSessionPerfConfig{
			requestedCPU: 20, workerExe: workerExe, source: source, snapshot: snapshot, ffmpeg: ffmpeg,
			helper: helper, browser: browser, durationMs: durationMs, encoder: encoder,
			outputMode: outputMode, gpuBackend: gpuBackend, slots: slots, freshBrowserPerJob: freshBrowserPerJob,
		})
		evidence.Sessions = append(evidence.Sessions, sessionEvidence)
		allRawPairs = append(allRawPairs, rawPairs)
		allWarmups = append(allWarmups, warmup)
		for pid := range observedPIDs {
			allObservedPIDs[pid] = struct{}{}
		}
	}

	for pairIndex := range evidence.ColdPairs {
		rawPair := &allColdRawPairs[pairIndex]
		freshFrames, freshHLSFrames, err := finalizeNicoWorkerT3Media(ffmpeg, &rawPair.fresh, durationMs, fpsNum)
		if err != nil {
			t.Fatalf("cold pair %d fresh output validation: %v", pairIndex+1, err)
		}
		sessionFrames, sessionHLSFrames, err := finalizeNicoWorkerT3Media(ffmpeg, &rawPair.session, durationMs, fpsNum)
		if err != nil {
			t.Fatalf("cold pair %d session output validation: %v", pairIndex+1, err)
		}
		setNicoWorkerT3PairMediaEvidence(&rawPair.evidence, freshFrames, freshHLSFrames, sessionFrames, sessionHLSFrames)
		rawPair.evidence.FreshWorker = rawPair.fresh.evidence
		rawPair.evidence.SessionCandidate = rawPair.session.evidence
		if !rawPair.evidence.MP4FrameParityPassed {
			t.Errorf("cold pair %d decoded MP4 frames differ: %d differing frames, first frame=%d (fresh=%s session=%s)", pairIndex+1, rawPair.evidence.MP4FrameDifferenceCount, rawPair.evidence.MP4FirstDifferenceIndex, rawPair.fresh.evidence.DecodedFrameSHA256, rawPair.session.evidence.DecodedFrameSHA256)
		}
		if !rawPair.evidence.FreshHLSMatchesMP4 {
			t.Errorf("cold pair %d fresh HLS frames differ from its MP4 (mp4=%s hls=%s)", pairIndex+1, rawPair.fresh.evidence.DecodedFrameSHA256, rawPair.fresh.evidence.HLSDecodedFrameSHA256)
		}
		if !rawPair.evidence.SessionHLSMatchesMP4 {
			t.Errorf("cold pair %d session HLS frames differ from its MP4 (mp4=%s hls=%s)", pairIndex+1, rawPair.session.evidence.DecodedFrameSHA256, rawPair.session.evidence.HLSDecodedFrameSHA256)
		}
		evidence.ColdPairs[pairIndex] = rawPair.evidence
	}
	for sessionIndex := range evidence.Sessions {
		_, _, err := finalizeNicoWorkerT3Media(ffmpeg, &allWarmups[sessionIndex], durationMs, fpsNum)
		if err != nil {
			t.Fatalf("session %d warmup output validation: %v", sessionIndex+1, err)
		}
		evidence.Sessions[sessionIndex].Warmup = allWarmups[sessionIndex].evidence
		for pairIndex := range evidence.Sessions[sessionIndex].Pairs {
			rawPair := &allRawPairs[sessionIndex][pairIndex]
			freshFrames, freshHLSFrames, err := finalizeNicoWorkerT3Media(ffmpeg, &rawPair.fresh, durationMs, fpsNum)
			if err != nil {
				t.Fatalf("session %d pair %d fresh output validation: %v", sessionIndex+1, pairIndex+1, err)
			}
			sessionFrames, sessionHLSFrames, err := finalizeNicoWorkerT3Media(ffmpeg, &rawPair.session, durationMs, fpsNum)
			if err != nil {
				t.Fatalf("session %d pair %d candidate output validation: %v", sessionIndex+1, pairIndex+1, err)
			}
			setNicoWorkerT3PairMediaEvidence(&rawPair.evidence, freshFrames, freshHLSFrames, sessionFrames, sessionHLSFrames)
			rawPair.evidence.FreshWorker = rawPair.fresh.evidence
			rawPair.evidence.SessionCandidate = rawPair.session.evidence
			if !rawPair.evidence.MP4FrameParityPassed {
				t.Errorf("session %d pair %d decoded MP4 frames differ: %d differing frames, first frame=%d (fresh=%s session=%s)", sessionIndex+1, pairIndex+1, rawPair.evidence.MP4FrameDifferenceCount, rawPair.evidence.MP4FirstDifferenceIndex, rawPair.fresh.evidence.DecodedFrameSHA256, rawPair.session.evidence.DecodedFrameSHA256)
			}
			if !rawPair.evidence.FreshHLSMatchesMP4 {
				t.Errorf("session %d pair %d fresh HLS frames differ from its MP4 (mp4=%s hls=%s)", sessionIndex+1, pairIndex+1, rawPair.fresh.evidence.DecodedFrameSHA256, rawPair.fresh.evidence.HLSDecodedFrameSHA256)
			}
			if !rawPair.evidence.SessionHLSMatchesMP4 {
				t.Errorf("session %d pair %d candidate HLS frames differ from its MP4 (mp4=%s hls=%s)", sessionIndex+1, pairIndex+1, rawPair.session.evidence.DecodedFrameSHA256, rawPair.session.evidence.HLSDecodedFrameSHA256)
			}
			evidence.Sessions[sessionIndex].Pairs[pairIndex] = rawPair.evidence
		}
	}

	if err := assertNicoWorkerT3PIDsExited(allObservedPIDs, workerExe, browser, helper, ffmpeg); err != nil {
		t.Errorf("owned T3 process remains after measurement: %v", err)
	}
	evidence.CompletedAt = time.Now().UTC()
	evidence.Summary = summarizeNicoWorkerT3Perf(evidence.ColdPairs, evidence.Sessions, pairCount, freshBrowserPerJob)
	evidencePath := filepath.Join(evidenceRoot, fmt.Sprintf("%s-%s.json", prefix, randomSuffix()))
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, append(data, '\n'), 0600); err != nil {
		t.Fatalf("write T3 performance evidence: %v", err)
	}
	t.Logf("%s paired evidence: %s", prefix, evidencePath)
	t.Logf("%s correctness=%t speedGate=%t coldMedianSaving=%.6fs cold95%%CI=[%.6f, %.6f]s warmPairs/session=%d warmMedianSaving=%.6fs warm95%%CI=[%.6f, %.6f]s warmSessionMedians=%v",
		prefix, evidence.Summary.CorrectnessPassed, evidence.Summary.SpeedGatePassed,
		evidence.Summary.ColdMedianSavingSeconds, evidence.Summary.ColdBootstrap95CISeconds[0], evidence.Summary.ColdBootstrap95CISeconds[1], pairCount,
		evidence.Summary.MedianSavingSeconds, evidence.Summary.Bootstrap95CISeconds[0], evidence.Summary.Bootstrap95CISeconds[1],
		evidence.Summary.SessionMedianSavings)
	t.Log("T3 CPU20 is a test-only Job cap; this test does not change production worker CPU options")
}

func runNicoWorkerT3PerfSession(
	t *testing.T,
	runRoot string,
	sessionIndex, pairCount int,
	options nicoexportbudget.Options,
	config nicoWorkerSessionPerfConfig,
) (nicoWorkerT3SessionEvidence, []nicoWorkerT3RawPair, nicoWorkerT3RawArm, map[uint32]struct{}) {
	t.Helper()
	mode := "browser_pool"
	if config.freshBrowserPerJob {
		mode = "fresh_browser_per_job"
	}
	evidence := nicoWorkerT3SessionEvidence{SessionNumber: sessionIndex + 1, CandidateMode: mode, Pairs: make([]nicoWorkerT3PairEvidence, 0, pairCount)}
	rawPairs := make([]nicoWorkerT3RawPair, 0, pairCount)
	observedPIDs := make(map[uint32]struct{})
	owner, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	manager := newNicoWorkerSessionManager(owner, nil, nil)
	managerClosed := false
	t.Cleanup(func() {
		if !managerClosed {
			if err := manager.close(); err != nil {
				t.Errorf("close T3 session %d after early failure: %v", sessionIndex+1, err)
			}
		}
	})

	warmupRequest := newNicoWorkerT3Request(t, runRoot, config, fmt.Sprintf("t3-s%02d-warmup", sessionIndex+1))
	identity, err := nicoWorkerSessionIdentity(warmupRequest, options)
	if err != nil {
		t.Fatal(err)
	}
	setupStarted := time.Now()
	session, err := manager.ensureSession(options, identity, false)
	evidence.SessionSetupSeconds = time.Since(setupStarted).Seconds()
	if err != nil {
		t.Fatalf("start T3 session %d: %v", sessionIndex+1, err)
	}

	warmup, browserPID := runNicoWorkerT3SessionArm(t, manager, session, owner, warmupRequest, options, config)
	if !config.freshBrowserPerJob && browserPID == 0 {
		t.Fatalf("T3 session %d warmup did not observe its dedicated Chrome process", sessionIndex+1)
	}
	evidence.BrowserPID = browserPID
	evidence.WorkerPID = warmup.evidence.ObservedWorkerPID
	if evidence.WorkerPID == 0 {
		t.Fatalf("worker session %d warmup did not observe its persistent worker process", sessionIndex+1)
	}
	evidence.Warmup = warmup.evidence
	for pid := range nicoWorkerT3ArmPIDs(warmup.evidence) {
		observedPIDs[pid] = struct{}{}
	}
	if !config.freshBrowserPerJob && warmup.evidence.ObservedBrowserPID != browserPID {
		t.Fatalf("T3 session %d warmup browser PID changed: got %d want %d", sessionIndex+1, warmup.evidence.ObservedBrowserPID, browserPID)
	}

	for pairIndex := 0; pairIndex < pairCount; pairIndex++ {
		freshRequest := newNicoWorkerT3Request(t, runRoot, config, fmt.Sprintf("t3-s%02d-p%02d-fresh", sessionIndex+1, pairIndex+1))
		sessionRequest := newNicoWorkerT3Request(t, runRoot, config, fmt.Sprintf("t3-s%02d-p%02d-session", sessionIndex+1, pairIndex+1))
		sessionFirst := (sessionIndex+pairIndex)%2 == 0
		var fresh, pooled nicoWorkerT3RawArm
		order := "fresh-first"
		if sessionFirst {
			order = "session-first"
			pooled, browserPID = runNicoWorkerT3SessionArm(t, manager, session, owner, sessionRequest, options, config)
			fresh = runNicoWorkerT3FreshArm(t, owner, freshRequest, options)
		} else {
			fresh = runNicoWorkerT3FreshArm(t, owner, freshRequest, options)
			pooled, browserPID = runNicoWorkerT3SessionArm(t, manager, session, owner, sessionRequest, options, config)
		}
		if pooled.evidence.ObservedWorkerPID != evidence.WorkerPID {
			t.Fatalf("candidate worker process changed in session %d: initial=%d current=%d", sessionIndex+1, evidence.WorkerPID, pooled.evidence.ObservedWorkerPID)
		}
		if config.freshBrowserPerJob && (browserPID != 0 || pooled.evidence.ObservedBrowserPID != 0) {
			t.Fatalf("T4 candidate retained Chrome after job %s: pid=%d sample=%d", sessionRequest.RunID, browserPID, pooled.evidence.ObservedBrowserPID)
		}
		if !config.freshBrowserPerJob && (browserPID != evidence.BrowserPID || pooled.evidence.ObservedBrowserPID != evidence.BrowserPID) {
			t.Fatalf("T3 session %d Chrome process was not reused: initial=%d current=%d sample=%d", sessionIndex+1, evidence.BrowserPID, browserPID, pooled.evidence.ObservedBrowserPID)
		}
		for pid := range nicoWorkerT3ArmPIDs(fresh.evidence) {
			observedPIDs[pid] = struct{}{}
		}
		for pid := range nicoWorkerT3ArmPIDs(pooled.evidence) {
			observedPIDs[pid] = struct{}{}
		}
		pair := nicoWorkerT3PairEvidence{
			PairNumber: pairIndex + 1, Order: order,
			FreshWorker: fresh.evidence, SessionCandidate: pooled.evidence,
			PairedSavingSeconds: fresh.evidence.WallSeconds - pooled.evidence.WallSeconds,
		}
		evidence.Pairs = append(evidence.Pairs, pair)
		rawPairs = append(rawPairs, nicoWorkerT3RawPair{evidence: pair, fresh: fresh, session: pooled})
	}

	retirementStarted := time.Now()
	closingSession := nicoWorkerT3CurrentSession(manager)
	closeErr := manager.close()
	managerClosed = true
	if closeErr != nil {
		evidence.RetirementError = closeErr.Error()
		t.Fatalf("retire T3 session %d: %v (%s)", sessionIndex+1, closeErr, nicoWorkerT3SessionDiagnostic(closingSession))
	}
	evidence.RetirementSeconds = time.Since(retirementStarted).Seconds()
	return evidence, rawPairs, warmup, observedPIDs
}

func runNicoWorkerT3ColdPerfPair(
	t *testing.T,
	runRoot string,
	pairIndex int,
	options nicoexportbudget.Options,
	config nicoWorkerSessionPerfConfig,
) (nicoWorkerT3PairEvidence, nicoWorkerT3RawPair, map[uint32]struct{}) {
	t.Helper()
	owner, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	manager := newNicoWorkerSessionManager(owner, nil, nil)
	managerClosed := false
	t.Cleanup(func() {
		if !managerClosed {
			if err := manager.close(); err != nil {
				t.Errorf("close cold T3 session pair %d after early failure: %v", pairIndex+1, err)
			}
		}
	})
	freshRequest := newNicoWorkerT3Request(t, runRoot, config, fmt.Sprintf("t3-cold-p%02d-fresh", pairIndex+1))
	sessionRequest := newNicoWorkerT3Request(t, runRoot, config, fmt.Sprintf("t3-cold-p%02d-session", pairIndex+1))
	var fresh, pooled nicoWorkerT3RawArm
	order := "fresh-first"
	if pairIndex%2 == 0 {
		order = "session-first"
		pooled = runNicoWorkerT3ColdSessionArm(t, manager, owner, sessionRequest, options, config)
		fresh = runNicoWorkerT3FreshArm(t, owner, freshRequest, options)
	} else {
		fresh = runNicoWorkerT3FreshArm(t, owner, freshRequest, options)
		pooled = runNicoWorkerT3ColdSessionArm(t, manager, owner, sessionRequest, options, config)
	}
	retirementStarted := time.Now()
	closingSession := nicoWorkerT3CurrentSession(manager)
	closeErr := manager.close()
	managerClosed = true
	if closeErr != nil {
		t.Fatalf("retire cold T3 session pair %d: %v (%s)", pairIndex+1, closeErr, nicoWorkerT3SessionDiagnostic(closingSession))
	}
	pids := nicoWorkerT3ArmPIDs(fresh.evidence)
	for pid := range nicoWorkerT3ArmPIDs(pooled.evidence) {
		pids[pid] = struct{}{}
	}
	pair := nicoWorkerT3PairEvidence{
		PairNumber: pairIndex + 1, Order: order,
		FreshWorker: fresh.evidence, SessionCandidate: pooled.evidence,
		PairedSavingSeconds:      fresh.evidence.WallSeconds - pooled.evidence.WallSeconds,
		SessionRetirementSeconds: time.Since(retirementStarted).Seconds(),
	}
	return pair, nicoWorkerT3RawPair{evidence: pair, fresh: fresh, session: pooled}, pids
}

func nicoWorkerT3CurrentSession(manager *nicoWorkerSessionManager) *nicoWorkerSession {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.current
}

func nicoWorkerT3SessionDiagnostic(session *nicoWorkerSession) string {
	if session == nil {
		return "session unavailable"
	}
	return fmt.Sprintf("child exit=%d stderr=%q", session.report.ExitCode, strings.TrimSpace(session.stderr.String()))
}

func runNicoWorkerT3ColdSessionArm(
	t *testing.T,
	manager *nicoWorkerSessionManager,
	owner context.Context,
	request nicoexportworker.Request,
	options nicoexportbudget.Options,
	config nicoWorkerSessionPerfConfig,
) nicoWorkerT3RawArm {
	t.Helper()
	runCtx, cancel := context.WithTimeout(owner, 3*time.Minute)
	defer cancel()
	started := time.Now()
	result, report, err := manager.run(runCtx, request, options, nil)
	wall := time.Since(started).Seconds()
	assertNicoWorkerT3Result(t, "cold session candidate", request, result, report, err, options)
	manager.mu.Lock()
	session := manager.current
	manager.mu.Unlock()
	if session == nil {
		t.Fatalf("cold T3 session was not created for %s", request.RunID)
	}
	processReport, err := session.process.Snapshot()
	if err != nil {
		t.Fatalf("snapshot cold T3 session %s: %v", request.RunID, err)
	}
	var browserPID uint32
	if config.freshBrowserPerJob {
		if err := nicoWorkerT3AssertNoLiveBrowser(processReport.Samples, config.browser); err != nil {
			t.Fatalf("cold T4 candidate kept Chrome after %s: %v", request.RunID, err)
		}
	} else {
		browserPID, err = nicoWorkerT3FindLiveBrowserPID(processReport.Samples, config.browser)
		if err != nil {
			t.Fatalf("find cold T3 browser after %s: %v", request.RunID, err)
		}
	}
	pids := nicoWorkerT3PIDs(processReport.Samples)
	workerPID, err := nicoWorkerT3FindLiveWorkerPID(processReport.Samples, config.workerExe)
	if err != nil {
		t.Fatalf("find persistent candidate worker after %s: %v", request.RunID, err)
	}
	return nicoWorkerT3RawArm{
		evidence: nicoWorkerT3ArmEvidence{
			RunID: request.RunID, WallSeconds: wall, WorkerWallSeconds: result.WorkerWallSeconds,
			ReportedCPUSeconds: report.CPUSeconds, JobCPURate: report.JobCPURate, Verified: report.Verified,
			ObservedBrowserPID: browserPID, ObservedWorkerPID: workerPID, ObservedJobPIDs: nicoWorkerT3PIDsToSorted(pids), ObservedJobPIDCount: len(pids),
		},
		output: request.OutputPath, hlsDir: request.HLSStagingDir,
	}
}

func newNicoWorkerT3Request(t *testing.T, runRoot string, config nicoWorkerSessionPerfConfig, runID string) nicoexportworker.Request {
	t.Helper()
	runDir := filepath.Join(runRoot, runID)
	if err := os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	randomSeed := nicoWorkerT3RandomSeed
	return nicoexportworker.Request{
		Version: 1, RunID: runID, MediaID: "t3-browser-pool-perf",
		SourcePath: config.source, SnapshotPath: config.snapshot,
		OutputPath: filepath.Join(runDir, "rendered.mp4"), HLSStagingDir: filepath.Join(runDir, "hls"),
		OutputMode: config.outputMode, FFmpeg: config.ffmpeg, BrowserPath: config.browser,
		Backend: "timeline", TimelineEnabled: true, TimelineCompositor: config.helper,
		TimelineReadbackSlots: config.slots, TimelineGPUBackend: config.gpuBackend,
		TimelineRandomSeed: &randomSeed,
		Encoder:            config.encoder, Width: 1280, Height: 720, DurationMs: config.durationMs,
		FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k",
	}
}

func runNicoWorkerT3FreshArm(
	t *testing.T,
	owner context.Context,
	request nicoexportworker.Request,
	options nicoexportbudget.Options,
) nicoWorkerT3RawArm {
	t.Helper()
	runCtx, cancel := context.WithTimeout(owner, 3*time.Minute)
	defer cancel()
	started := time.Now()
	result, report, err := runNicoWorkerWithBudgetAndDiagnosticsAndCPUOptions(runCtx, request, nil, options)
	wall := time.Since(started).Seconds()
	assertNicoWorkerT3Result(t, "fresh worker", request, result, report, err, options)
	return nicoWorkerT3RawArm{
		evidence: nicoWorkerT3ArmEvidence{
			RunID: request.RunID, WallSeconds: wall, WorkerWallSeconds: result.WorkerWallSeconds,
			ReportedCPUSeconds: report.CPUSeconds, JobCPURate: report.JobCPURate, Verified: report.Verified,
			ObservedJobPIDs: nicoWorkerT3PIDsSorted(report.Samples), ObservedJobPIDCount: nicoWorkerT3PIDCount(report.Samples),
		},
		output: request.OutputPath, hlsDir: request.HLSStagingDir,
	}
}

func runNicoWorkerT3SessionArm(
	t *testing.T,
	manager *nicoWorkerSessionManager,
	session *nicoWorkerSession,
	owner context.Context,
	request nicoexportworker.Request,
	options nicoexportbudget.Options,
	config nicoWorkerSessionPerfConfig,
) (nicoWorkerT3RawArm, uint32) {
	t.Helper()
	manager.mu.Lock()
	current := manager.current
	manager.mu.Unlock()
	if current != session {
		t.Fatalf("T3 session changed before run %s", request.RunID)
	}
	before, err := session.process.Snapshot()
	if err != nil {
		t.Fatalf("snapshot T3 session before %s: %v", request.RunID, err)
	}
	runCtx, cancel := context.WithTimeout(owner, 3*time.Minute)
	defer cancel()
	started := time.Now()
	result, report, err := manager.run(runCtx, request, options, nil)
	wall := time.Since(started).Seconds()
	assertNicoWorkerT3Result(t, "session candidate", request, result, report, err, options)
	after, err := session.process.Snapshot()
	if err != nil {
		t.Fatalf("snapshot T3 session after %s: %v", request.RunID, err)
	}
	cumulativeDelta := after.CPUSeconds - before.CPUSeconds
	cpuDeltaTolerance := 2*options.SampleInterval + 100*time.Millisecond
	if cumulativeDelta < 0 || math.Abs(report.CPUSeconds-cumulativeDelta) > cpuDeltaTolerance.Seconds() {
		t.Fatalf("T3 session %s CPU delta mismatch: report=%f cumulative=%f tolerance=%s", request.RunID, report.CPUSeconds, cumulativeDelta, cpuDeltaTolerance)
	}
	manager.mu.Lock()
	stillCurrent := manager.current == session
	manager.mu.Unlock()
	if !stillCurrent {
		t.Fatalf("T3 session was replaced during successful run %s", request.RunID)
	}
	var browserPID uint32
	if config.freshBrowserPerJob {
		if err := nicoWorkerT3AssertNoLiveBrowser(after.Samples, config.browser); err != nil {
			t.Fatalf("T4 candidate kept Chrome after %s: %v", request.RunID, err)
		}
	} else {
		browserPID, err = nicoWorkerT3FindLiveBrowserPID(after.Samples, config.browser)
		if err != nil {
			t.Fatalf("find reused T3 browser after %s: %v", request.RunID, err)
		}
	}
	memberPIDs := nicoWorkerT3PIDs(after.Samples)
	workerPID, err := nicoWorkerT3FindLiveWorkerPID(after.Samples, config.workerExe)
	if err != nil {
		t.Fatalf("find persistent worker after %s: %v", request.RunID, err)
	}
	return nicoWorkerT3RawArm{
		evidence: nicoWorkerT3ArmEvidence{
			RunID: request.RunID, WallSeconds: wall, WorkerWallSeconds: result.WorkerWallSeconds,
			ReportedCPUSeconds: report.CPUSeconds, JobCPURate: report.JobCPURate, Verified: report.Verified,
			ObservedBrowserPID: browserPID, ObservedWorkerPID: workerPID, ObservedJobPIDs: nicoWorkerT3PIDsToSorted(memberPIDs), ObservedJobPIDCount: len(memberPIDs),
		},
		output: request.OutputPath, hlsDir: request.HLSStagingDir,
	}, browserPID
}

func assertNicoWorkerT3Result(
	t *testing.T,
	arm string,
	request nicoexportworker.Request,
	result nicoexportworker.Event,
	report nicoexportbudget.Report,
	err error,
	options nicoexportbudget.Options,
) {
	t.Helper()
	if err != nil {
		t.Fatalf("T3 %s run %s: %v", arm, request.RunID, err)
	}
	if !result.OK || result.RunID != request.RunID || result.MediaID != request.MediaID {
		t.Fatalf("T3 %s run %s returned wrong result: %+v", arm, request.RunID, result)
	}
	if !report.Verified || report.JobCPURate != options.Percent*100 {
		t.Fatalf("T3 %s run %s did not verify CPU20 Job cap: %+v", arm, request.RunID, report)
	}
}

func finalizeNicoWorkerT3Media(ffmpeg string, arm *nicoWorkerT3RawArm, durationMs, fpsNum int64) ([]string, []string, error) {
	outputInfo, err := os.Stat(arm.output)
	if err != nil {
		return nil, nil, fmt.Errorf("MP4 missing: %w", err)
	}
	if outputInfo.Size() <= 0 {
		return nil, nil, fmt.Errorf("MP4 is empty")
	}
	arm.evidence.OutputBytes = outputInfo.Size()
	arm.evidence.OutputSHA256, err = nicoWorkerPerfSHA256(arm.output)
	if err != nil {
		return nil, nil, err
	}
	frames, err := decodeNicoWorkerT3Frames(ffmpeg, arm.output)
	if err != nil {
		return nil, nil, fmt.Errorf("MP4 full decode: %w", err)
	}
	wantFrames := int(durationMs * fpsNum / 1000)
	if len(frames) != wantFrames {
		return nil, nil, fmt.Errorf("MP4 decoded %d frames, want %d", len(frames), wantFrames)
	}
	arm.evidence.DecodedFrameCount = len(frames)
	arm.evidence.DecodedFrameSHA256 = nicoWorkerT3FrameDigest(frames)
	playlist := filepath.Join(arm.hlsDir, "playlist.m3u8")
	playlistInfo, err := os.Stat(playlist)
	if err != nil {
		return nil, nil, fmt.Errorf("HLS playlist missing: %w", err)
	}
	if playlistInfo.Size() <= 0 {
		return nil, nil, fmt.Errorf("HLS playlist is empty")
	}
	arm.evidence.HLSPlaylistSHA256, err = nicoWorkerPerfSHA256(playlist)
	if err != nil {
		return nil, nil, err
	}
	segments, err := filepath.Glob(filepath.Join(arm.hlsDir, "segment-*.ts"))
	if err != nil {
		return nil, nil, err
	}
	if len(segments) == 0 {
		return nil, nil, fmt.Errorf("HLS playlist has no media segments")
	}
	arm.evidence.HLSSegmentCount = len(segments)
	hlsFrames, err := decodeNicoWorkerT3Frames(ffmpeg, playlist)
	if err != nil {
		return nil, nil, fmt.Errorf("HLS full decode: %w", err)
	}
	if len(hlsFrames) != wantFrames {
		return nil, nil, fmt.Errorf("HLS decoded %d frames, want %d", len(hlsFrames), wantFrames)
	}
	arm.evidence.HLSDecodedFrameCount = len(hlsFrames)
	arm.evidence.HLSDecodedFrameSHA256 = nicoWorkerT3FrameDigest(hlsFrames)
	return frames, hlsFrames, nil
}

func setNicoWorkerT3PairMediaEvidence(pair *nicoWorkerT3PairEvidence, freshMP4, freshHLS, sessionMP4, sessionHLS []string) {
	pair.MP4FrameParityPassed = equalNicoWorkerT3Frames(freshMP4, sessionMP4)
	pair.MP4FrameDifferenceCount, pair.MP4FirstDifferenceIndex = nicoWorkerT3FrameDifference(freshMP4, sessionMP4)
	pair.FreshHLSMatchesMP4 = equalNicoWorkerT3Frames(freshMP4, freshHLS)
	pair.SessionHLSMatchesMP4 = equalNicoWorkerT3Frames(sessionMP4, sessionHLS)
}

func nicoWorkerT3FrameDifference(a, b []string) (count, firstIndex int) {
	firstIndex = -1
	limit := max(len(a), len(b))
	for index := 0; index < limit; index++ {
		if index >= len(a) || index >= len(b) || a[index] != b[index] {
			count++
			if firstIndex < 0 {
				firstIndex = index
			}
		}
	}
	return count, firstIndex
}

func decodeNicoWorkerT3Frames(ffmpeg, input string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-v", "error", "-nostdin", "-i", input,
		"-map", "0:v:0", "-an", "-sn", "-dn", "-f", "framemd5", "-")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("decoder timed out: %w: %s", ctx.Err(), strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("decoder failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	frames := make([]string, 0, 180)
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 6 {
			return nil, fmt.Errorf("invalid framemd5 row %q", line)
		}
		hash := strings.TrimSpace(fields[len(fields)-1])
		if len(hash) != 32 {
			return nil, fmt.Errorf("invalid frame MD5 %q", hash)
		}
		frames = append(frames, hash)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return frames, nil
}

func nicoWorkerT3FrameDigest(frames []string) string {
	h := sha256.New()
	for _, frame := range frames {
		_, _ = h.Write([]byte(frame))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func equalNicoWorkerT3Frames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func TestNicoWorkerT3PairCorrectRejectsDifferentMP4Digests(t *testing.T) {
	pair := nicoWorkerT3PairEvidence{
		MP4FrameParityPassed: true, FreshHLSMatchesMP4: true, SessionHLSMatchesMP4: true,
		FreshWorker: nicoWorkerT3ArmEvidence{
			Verified: true, JobCPURate: 2000, ObservedJobPIDCount: 1,
			DecodedFrameSHA256: "fresh-mp4", HLSDecodedFrameSHA256: "fresh-hls",
		},
		SessionCandidate: nicoWorkerT3ArmEvidence{
			Verified: true, JobCPURate: 2000, ObservedBrowserPID: 1234,
			DecodedFrameSHA256: "session-mp4", HLSDecodedFrameSHA256: "session-mp4",
		},
	}
	pair.FreshWorker.HLSDecodedFrameSHA256 = pair.FreshWorker.DecodedFrameSHA256
	if nicoWorkerT3PairCorrect(pair, false) {
		t.Fatal("pair with different decoded MP4 frame digests must fail correctness")
	}
}

func TestNicoWorkerT3BootstrapSamplesAreRetainedAndDeterministic(t *testing.T) {
	coldValues := []float64{-0.4, -0.2, -0.1, 0, 0.05, 0.1, 0.2, 0.3, 0.35, 0.5}
	coldLow, coldHigh, coldSamples := nicoWorkerT3BootstrapIndependentCI(coldValues, 10000, 0x74331a)
	if len(coldSamples) != 10000 {
		t.Fatalf("cold bootstrap sample count=%d, want 10000", len(coldSamples))
	}
	coldSorted := append([]float64(nil), coldSamples...)
	sort.Float64s(coldSorted)
	if coldLow != nicoWorkerT3Quantile(coldSorted, 0.025) || coldHigh != nicoWorkerT3Quantile(coldSorted, 0.975) {
		t.Fatalf("cold confidence interval [%f,%f] does not match saved samples", coldLow, coldHigh)
	}
	_, _, coldAgain := nicoWorkerT3BootstrapIndependentCI(coldValues, 10000, 0x74331a)
	for index := range coldSamples {
		if coldSamples[index] != coldAgain[index] {
			t.Fatalf("cold bootstrap sample %d is not reproducible", index)
		}
	}

	sessions := []nicoWorkerT3SessionEvidence{
		{Pairs: makeBootstrapEvidencePairs([]float64{0.1, 0.2, 0.3, 0.4, 0.5})},
		{Pairs: makeBootstrapEvidencePairs([]float64{0.2, 0.3, 0.4, 0.5, 0.6})},
	}
	warmLow, warmHigh, warmSamples := nicoWorkerT3BootstrapCI(sessions, 10000, 0x743319)
	if len(warmSamples) != 10000 {
		t.Fatalf("warm bootstrap sample count=%d, want 10000", len(warmSamples))
	}
	warmSorted := append([]float64(nil), warmSamples...)
	sort.Float64s(warmSorted)
	if warmLow != nicoWorkerT3Quantile(warmSorted, 0.025) || warmHigh != nicoWorkerT3Quantile(warmSorted, 0.975) {
		t.Fatalf("warm confidence interval [%f,%f] does not match saved samples", warmLow, warmHigh)
	}
}

func TestNicoWorkerT4BootstrapEvidenceReplay(t *testing.T) {
	sourcePath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_T4_BOOTSTRAP_EVIDENCE"))
	if sourcePath == "" {
		t.Skip("set NICO_TIMELINE_T4_BOOTSTRAP_EVIDENCE to replay and preserve bootstrap samples")
	}
	sourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence nicoWorkerT3PerfEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatalf("decode source evidence: %v", err)
	}
	freshBrowserPerJob := evidence.CandidateMode == "fresh_browser_per_job"
	recomputed := summarizeNicoWorkerT3Perf(evidence.ColdPairs, evidence.Sessions, evidence.Summary.PairCountPerSession, freshBrowserPerJob)
	if !equalNicoWorkerT3FloatSlices(recomputed.ColdBootstrap95CISeconds, evidence.Summary.ColdBootstrap95CISeconds) || !equalNicoWorkerT3FloatSlices(recomputed.Bootstrap95CISeconds, evidence.Summary.Bootstrap95CISeconds) {
		t.Fatalf("replayed confidence intervals differ: cold=%v want=%v warm=%v want=%v", recomputed.ColdBootstrap95CISeconds, evidence.Summary.ColdBootstrap95CISeconds, recomputed.Bootstrap95CISeconds, evidence.Summary.Bootstrap95CISeconds)
	}
	coldInputs := make([]float64, len(evidence.ColdPairs))
	for index, pair := range evidence.ColdPairs {
		coldInputs[index] = pair.PairedSavingSeconds
	}
	warmInputs := make([][]float64, len(evidence.Sessions))
	for sessionIndex, session := range evidence.Sessions {
		warmInputs[sessionIndex] = make([]float64, len(session.Pairs))
		for pairIndex, pair := range session.Pairs {
			warmInputs[sessionIndex][pairIndex] = pair.PairedSavingSeconds
		}
	}
	outPath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_T4_BOOTSTRAP_OUTPUT"))
	if outPath == "" {
		extension := filepath.Ext(sourcePath)
		outPath = strings.TrimSuffix(sourcePath, extension) + "-bootstrap-samples" + extension
	}
	outPath, err = filepath.Abs(outPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.Sum256(data)
	supplement := struct {
		SchemaVersion        int                 `json:"schema_version"`
		GeneratedAt          time.Time           `json:"generated_at"`
		SourceEvidence       string              `json:"source_evidence"`
		SourceEvidenceSHA256 string              `json:"source_evidence_sha256"`
		CandidateMode        string              `json:"candidate_mode"`
		ColdInputs           []float64           `json:"cold_paired_savings_seconds"`
		WarmInputs           [][]float64         `json:"warm_session_paired_savings_seconds"`
		Summary              nicoWorkerT3Summary `json:"summary_with_all_bootstrap_samples"`
	}{
		SchemaVersion: 1, GeneratedAt: time.Now().UTC(), SourceEvidence: sourcePath,
		SourceEvidenceSHA256: hex.EncodeToString(sourceHash[:]),
		CandidateMode:        evidence.CandidateMode, ColdInputs: coldInputs, WarmInputs: warmInputs, Summary: recomputed,
	}
	encoded, err := json.MarshalIndent(supplement, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0600); err != nil {
		t.Fatalf("write bootstrap evidence supplement: %v", err)
	}
	t.Logf("bootstrap samples retained in %s (cold=%d warm=%d source_sha256=%s)", outPath, len(recomputed.ColdBootstrapSamples), len(recomputed.BootstrapSamples), supplement.SourceEvidenceSHA256)
}

func equalNicoWorkerT3FloatSlices(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func makeBootstrapEvidencePairs(values []float64) []nicoWorkerT3PairEvidence {
	pairs := make([]nicoWorkerT3PairEvidence, len(values))
	for index, value := range values {
		pairs[index].PairedSavingSeconds = value
	}
	return pairs
}

func nicoWorkerT3DecodeFramesOrFatal(t *testing.T, ffmpeg, hlsDir string) []string {
	t.Helper()
	frames, err := decodeNicoWorkerT3Frames(ffmpeg, filepath.Join(hlsDir, "playlist.m3u8"))
	if err != nil {
		t.Fatalf("HLS full decode: %v", err)
	}
	return frames
}

func nicoWorkerT3FindLiveBrowserPID(samples []nicoexportbudget.Sample, browserPath string) (uint32, error) {
	want := filepath.Clean(browserPath)
	for _, sample := range samples {
		for _, pid := range sample.PIDs {
			image, live, err := nicoSessionLiveProcessImage(pid)
			if err == nil && live && strings.EqualFold(filepath.Clean(image), want) {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("no live process from %s was present in the session Job samples", browserPath)
}

func nicoWorkerT3AssertNoLiveBrowser(samples []nicoexportbudget.Sample, browserPath string) error {
	want := filepath.Clean(browserPath)
	for _, sample := range samples {
		for _, pid := range sample.PIDs {
			image, live, err := nicoSessionLiveProcessImage(pid)
			if err == nil && live && strings.EqualFold(filepath.Clean(image), want) {
				return fmt.Errorf("Chrome still running after job: pid=%d image=%s", pid, image)
			}
		}
	}
	return nil
}

func nicoWorkerT3FindLiveWorkerPID(samples []nicoexportbudget.Sample, workerPath string) (uint32, error) {
	want := filepath.Clean(workerPath)
	for _, sample := range samples {
		for _, pid := range sample.PIDs {
			image, live, err := nicoSessionLiveProcessImage(pid)
			if err == nil && live && strings.EqualFold(filepath.Clean(image), want) {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("no live worker process from %s was present in the session Job samples", workerPath)
}

func nicoWorkerT3PIDs(samples []nicoexportbudget.Sample) map[uint32]struct{} {
	pids := make(map[uint32]struct{})
	for _, sample := range samples {
		for _, pid := range sample.PIDs {
			if pid != 0 {
				pids[pid] = struct{}{}
			}
		}
	}
	return pids
}

func nicoWorkerT3ArmPIDs(evidence nicoWorkerT3ArmEvidence) map[uint32]struct{} {
	pids := make(map[uint32]struct{})
	for _, pid := range evidence.ObservedJobPIDs {
		if pid != 0 {
			pids[pid] = struct{}{}
		}
	}
	return pids
}

func nicoWorkerT3PIDCount(samples []nicoexportbudget.Sample) int {
	return len(nicoWorkerT3PIDs(samples))
}

func nicoWorkerT3PIDsSorted(samples []nicoexportbudget.Sample) []uint32 {
	return nicoWorkerT3PIDsToSorted(nicoWorkerT3PIDs(samples))
}

func nicoWorkerT3PIDsToSorted(pids map[uint32]struct{}) []uint32 {
	values := make([]uint32, 0, len(pids))
	for pid := range pids {
		values = append(values, pid)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func assertNicoWorkerT3PIDsExited(pids map[uint32]struct{}, worker, browser, helper, ffmpeg string) error {
	wanted := map[string]struct{}{}
	for _, path := range []string{worker, browser, helper, ffmpeg} {
		wanted[strings.ToLower(filepath.Clean(path))] = struct{}{}
	}
	for pid := range pids {
		image, live, err := nicoSessionLiveProcessImage(pid)
		if err != nil || !live {
			continue
		}
		if _, ownedImage := wanted[strings.ToLower(filepath.Clean(image))]; ownedImage {
			return fmt.Errorf("owned T3 process remains after worker/session close: pid=%d image=%s", pid, image)
		}
	}
	return nil
}

func summarizeNicoWorkerT3Perf(coldPairs []nicoWorkerT3PairEvidence, sessions []nicoWorkerT3SessionEvidence, pairCount int, freshBrowserPerJob bool) nicoWorkerT3Summary {
	var coldFresh, coldSession, coldSavings []float64
	for _, pair := range coldPairs {
		coldFresh = append(coldFresh, pair.FreshWorker.WallSeconds)
		coldSession = append(coldSession, pair.SessionCandidate.WallSeconds)
		coldSavings = append(coldSavings, pair.PairedSavingSeconds)
	}
	coldSeed := int64(0x74331a)
	coldLow, coldHigh, coldSamples := nicoWorkerT3BootstrapIndependentCI(coldSavings, 10000, coldSeed)
	var fresh, pooled, savings []float64
	sessionMedians := make([]float64, 0, len(sessions))
	for _, session := range sessions {
		local := make([]float64, 0, len(session.Pairs))
		for _, pair := range session.Pairs {
			fresh = append(fresh, pair.FreshWorker.WallSeconds)
			pooled = append(pooled, pair.SessionCandidate.WallSeconds)
			savings = append(savings, pair.PairedSavingSeconds)
			local = append(local, pair.PairedSavingSeconds)
		}
		sessionMedians = append(sessionMedians, nicoWorkerT3Median(local))
	}
	warmSeed := int64(0x743319)
	low, high, warmSamples := nicoWorkerT3BootstrapCI(sessions, 10000, warmSeed)
	correctness := len(coldPairs) == pairCount && len(sessions) == nicoWorkerT3Sessions && pairCount > 0
	for _, pair := range coldPairs {
		if !nicoWorkerT3PairCorrect(pair, freshBrowserPerJob) {
			correctness = false
		}
	}
	for _, session := range sessions {
		if session.RetirementError != "" || len(session.Pairs) != pairCount || session.WorkerPID == 0 || (freshBrowserPerJob && session.BrowserPID != 0) || (!freshBrowserPerJob && session.BrowserPID == 0) {
			correctness = false
		}
		for _, pair := range session.Pairs {
			if !nicoWorkerT3PairCorrect(pair, freshBrowserPerJob) {
				correctness = false
			}
		}
	}
	coldMedian := nicoWorkerT3Median(coldSavings)
	coldNoRegression := pairCount == nicoWorkerT3PairsPerSession && coldMedian >= 0 && coldLow >= 0
	speedGate := correctness && pairCount == nicoWorkerT3PairsPerSession && len(sessionMedians) == 2 && sessionMedians[0] > 0 && sessionMedians[1] > 0 && low > 0 && coldNoRegression
	return nicoWorkerT3Summary{
		ColdPairCount:            len(coldPairs),
		ColdFreshMedianSeconds:   nicoWorkerT3Median(coldFresh),
		ColdSessionMedianSeconds: nicoWorkerT3Median(coldSession),
		ColdMedianSavingSeconds:  coldMedian,
		ColdBootstrap95CISeconds: []float64{coldLow, coldHigh},
		ColdBootstrapSeed:        coldSeed,
		ColdBootstrapIterations:  10000,
		ColdBootstrapSamples:     coldSamples,
		ColdNoRegressionPassed:   coldNoRegression,
		PairCountPerSession:      pairCount,
		FreshMedianSeconds:       nicoWorkerT3Median(fresh),
		SessionMedianSeconds:     nicoWorkerT3Median(pooled),
		MedianSavingSeconds:      nicoWorkerT3Median(savings),
		SessionMedianSavings:     sessionMedians,
		BootstrapIterations:      10000,
		BootstrapSeed:            warmSeed,
		BootstrapSamples:         warmSamples,
		Bootstrap95CISeconds:     []float64{low, high},
		CorrectnessPassed:        correctness, SpeedGatePassed: speedGate,
	}
}

func nicoWorkerT3PairCorrect(pair nicoWorkerT3PairEvidence, freshBrowserPerJob bool) bool {
	browserLifecyclePassed := pair.SessionCandidate.ObservedBrowserPID != 0
	if freshBrowserPerJob {
		browserLifecyclePassed = pair.SessionCandidate.ObservedBrowserPID == 0
	}
	return pair.FreshWorker.Verified && pair.SessionCandidate.Verified &&
		pair.FreshWorker.JobCPURate == 2000 && pair.SessionCandidate.JobCPURate == 2000 &&
		pair.MP4FrameParityPassed && pair.FreshHLSMatchesMP4 && pair.SessionHLSMatchesMP4 &&
		pair.FreshWorker.DecodedFrameSHA256 == pair.SessionCandidate.DecodedFrameSHA256 &&
		pair.FreshWorker.HLSDecodedFrameSHA256 == pair.FreshWorker.DecodedFrameSHA256 &&
		pair.SessionCandidate.HLSDecodedFrameSHA256 == pair.SessionCandidate.DecodedFrameSHA256 &&
		pair.FreshWorker.DecodedFrameSHA256 != "" && pair.SessionCandidate.DecodedFrameSHA256 != "" &&
		pair.FreshWorker.HLSDecodedFrameSHA256 != "" && pair.SessionCandidate.HLSDecodedFrameSHA256 != "" &&
		pair.FreshWorker.ObservedJobPIDCount > 0 && pair.SessionCandidate.ObservedWorkerPID != 0 && browserLifecyclePassed
}

func nicoWorkerT3BootstrapIndependentCI(values []float64, iterations int, seed int64) (float64, float64, []float64) {
	if len(values) == 0 || iterations <= 0 {
		return 0, 0, nil
	}
	rng := rand.New(rand.NewSource(seed))
	boot := make([]float64, iterations)
	for iteration := range boot {
		sample := make([]float64, 0, len(values))
		for range values {
			sample = append(sample, values[rng.Intn(len(values))])
		}
		boot[iteration] = nicoWorkerT3Median(sample)
	}
	ordered := append([]float64(nil), boot...)
	sort.Float64s(ordered)
	return nicoWorkerT3Quantile(ordered, 0.025), nicoWorkerT3Quantile(ordered, 0.975), boot
}

func nicoWorkerT3BootstrapCI(sessions []nicoWorkerT3SessionEvidence, iterations int, seed int64) (float64, float64, []float64) {
	if len(sessions) == 0 || iterations <= 0 {
		return 0, 0, nil
	}
	rng := rand.New(rand.NewSource(seed))
	boot := make([]float64, 0, iterations)
	for iteration := 0; iteration < iterations; iteration++ {
		var samples []float64
		for _, session := range sessions {
			if len(session.Pairs) == 0 {
				continue
			}
			local := make([]float64, 0, len(session.Pairs))
			for range session.Pairs {
				local = append(local, session.Pairs[rng.Intn(len(session.Pairs))].PairedSavingSeconds)
			}
			samples = append(samples, nicoWorkerT3Median(local))
		}
		if len(samples) > 0 {
			boot = append(boot, nicoWorkerT3Median(samples))
		}
	}
	if len(boot) == 0 {
		return 0, 0, nil
	}
	ordered := append([]float64(nil), boot...)
	sort.Float64s(ordered)
	return nicoWorkerT3Quantile(ordered, 0.025), nicoWorkerT3Quantile(ordered, 0.975), boot
}

func nicoWorkerT3Median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	mid := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[mid]
	}
	return (ordered[mid-1] + ordered[mid]) / 2
}

func nicoWorkerT3Quantile(ordered []float64, probability float64) float64 {
	if len(ordered) == 0 {
		return 0
	}
	position := probability * float64(len(ordered)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return ordered[lower]
	}
	fraction := position - float64(lower)
	return ordered[lower]*(1-fraction) + ordered[upper]*fraction
}

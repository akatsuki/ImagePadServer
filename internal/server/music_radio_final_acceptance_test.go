package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/video"
)

const (
	finalAcceptanceGate              = "#20B"
	finalAcceptanceMinimumDuration   = 1800 * time.Second
	finalAcceptanceDurationTolerance = 2 * time.Second
)

type finalAcceptanceProfile struct {
	Name               string `json:"name"`
	GOPFrames          int    `json:"gopFrames"`
	RequiresHLS        bool   `json:"requiresHLS"`
	RequiresRTSP       bool   `json:"requiresRTSP"`
	ExpectedHLSVariant string `json:"expectedHLSVariant"`
}

type finalAcceptanceProfileEvidence struct {
	Name                    string               `json:"name"`
	ExpectedGOP             int                  `json:"expectedGOP"`
	ObservedGOP             int                  `json:"observedGOP"`
	ObservedCodec           rtspCodecSignature   `json:"observedCodec"`
	HLSReady                bool                 `json:"hlsReady"`
	RTSPReady               bool                 `json:"rtspReady"`
	HLSVariant              string               `json:"hlsVariant"`
	PacketCount             int                  `json:"packetCount"`
	Packets                 []rtspPacketEvidence `json:"packets"`
	Reconnects              int                  `json:"reconnects"`
	PathLosses              int                  `json:"pathLosses"`
	ReaderStderr            string               `json:"readerStderr"`
	StartedAt               time.Time            `json:"startedAt"`
	FinishedAt              time.Time            `json:"finishedAt"`
	ReceiverEvidence        string               `json:"receiverEvidence"`
	ReceiverSHA256          string               `json:"receiverSHA256"`
	ReceiverBytes           int64                `json:"receiverBytes"`
	ReceiverStartedAt       time.Time            `json:"receiverStartedAt"`
	ReceiverFinishedAt      time.Time            `json:"receiverFinishedAt"`
	ReceiverDurationSeconds float64              `json:"receiverDurationSeconds"`
}

type finalAcceptanceHostSample struct {
	Profile      string    `json:"profile"`
	At           time.Time `json:"at"`
	CPU          string    `json:"cpu"`
	GPU          string    `json:"gpu"`
	FFmpeg       string    `json:"ffmpeg"`
	Reader       string    `json:"reader"`
	MediaMTX     string    `json:"mediaMTX"`
	FFmpegPIDs   []int     `json:"ffmpegPIDs"`
	ReaderPIDs   []int     `json:"readerPIDs"`
	MediaMTXPIDs []int     `json:"mediaMTXPIDs"`
	Error        string    `json:"error"`
}

type finalAcceptanceTelemetryTargets struct {
	FFmpegPIDs   []int
	ReaderPIDs   []int
	MediaMTXPIDs []int
}

type finalAcceptanceSoakEvidence struct {
	Requested      bool                        `json:"requested"`
	TotalSeconds   int                         `json:"totalSeconds"`
	PerProfileSecs int                         `json:"perProfileSeconds"`
	HostSamples    []finalAcceptanceHostSample `json:"hostSamples"`
}

type finalAcceptanceMetrics struct {
	SchemaVersion int                              `json:"schemaVersion"`
	Gate          string                           `json:"gate"`
	Status        string                           `json:"status"`
	Accepted      bool                             `json:"accepted"`
	StartedAt     time.Time                        `json:"startedAt"`
	FinishedAt    time.Time                        `json:"finishedAt"`
	Binaries      []rtspBinaryEvidence             `json:"binaries"`
	Profiles      []finalAcceptanceProfileEvidence `json:"profiles"`
	Soak          finalAcceptanceSoakEvidence      `json:"soak"`
	BlockedReason string                           `json:"blockedReason,omitempty"`
	Failure       string                           `json:"failure,omitempty"`
}

type finalAcceptanceRunConfig struct {
	SoakRequested     bool
	TotalSeconds      int
	PerProfileSeconds int
}

func TestMusicRadioFinalAcceptanceProfileMatrix(t *testing.T) {
	profiles := finalAcceptanceProfileMatrix()
	if len(profiles) != 5 {
		t.Fatalf("profile count = %d, want 5", len(profiles))
	}
	wantGOP := map[string]int{"hls-high": 120, "hls": 30, "rtsp-low": 60, "rtsp-ultra": 30, "rtsp-realtime": 15}
	for _, profile := range profiles {
		t.Run(profile.Name, func(t *testing.T) {
			if got := obsrtmp.NormalizeLatencyProfile(profile.Name).Mode; got != profile.Name {
				t.Fatalf("normalized profile = %q, want %q", got, profile.Name)
			}
			if profile.GOPFrames != wantGOP[profile.Name] {
				t.Fatalf("matrix GOP = %d, want %d", profile.GOPFrames, wantGOP[profile.Name])
			}
			preset := video.QualityPreset{Height: 360, VideoBitrate: "900k", MaxRate: "1100k", BufferSize: "2200k", AudioBitrate: "128k", RadioLatency: profile.Name}
			args := video.RadioProgramEncoderArgs(preset, video.CPUVideoEncoder(video.EncoderLowLatency), 640, 360, "tcp://127.0.0.1:1", "tcp://127.0.0.1:2")
			if got := finalAcceptanceOption(args, "-g"); got != strconv.Itoa(profile.GOPFrames) {
				t.Fatalf("program GOP = %q, want %d", got, profile.GOPFrames)
			}
			if got := finalAcceptanceOption(args, "-c:a"); got != "aac" {
				t.Fatalf("program audio codec = %q, want aac", got)
			}
		})
	}
}

func TestMusicRadioFinalAcceptanceRecordStartsInvalidated(t *testing.T) {
	if got := newFinalAcceptanceRecord().Status; got != "invalidated" {
		t.Fatalf("initial status = %q, want invalidated", got)
	}
}

func TestMusicRadioFinalAcceptanceMetricsValidatorRejectsBlockedRecord(t *testing.T) {
	metrics := newFinalAcceptanceRecord()
	metrics.Status = "blocked"
	metrics.BlockedReason = "missing pins"
	if err := validateFinalAcceptanceMetrics(metrics); err == nil {
		t.Fatal("blocked final acceptance record was accepted")
	}
}

func TestMusicRadioFinalAcceptanceMetricsRejectsPromotionBypasses(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*finalAcceptanceMetrics)
	}{
		{
			name: "ordinary short matrix",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Soak.Requested = false
				metrics.Soak.TotalSeconds = 60
				metrics.Soak.PerProfileSecs = 12
			},
		},
		{
			name: "divided thirty minute soak",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Soak.TotalSeconds = 1800
				metrics.Soak.PerProfileSecs = 360
			},
		},
		{
			name: "one packet receiver",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Profiles[0].PacketCount = 1
			},
		},
		{
			name: "missing receiver capture",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Profiles[0].ReceiverEvidence = ""
			},
		},
		{
			name: "placeholder telemetry",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Soak.HostSamples = []finalAcceptanceHostSample{{Error: "probe unavailable"}, {}}
			},
		},
		{
			name: "one telemetry sample per profile",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Soak.HostSamples = []finalAcceptanceHostSample{
					metrics.Soak.HostSamples[0],
					metrics.Soak.HostSamples[3],
					metrics.Soak.HostSamples[6],
					metrics.Soak.HostSamples[9],
					metrics.Soak.HostSamples[12],
				}
			},
		},
		{
			name: "telemetry process payload omits declared PID",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Soak.HostSamples[0].FFmpeg = `[]`
			},
		},
		{
			name: "reader telemetry payload omits declared PID",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Soak.HostSamples[0].Reader = `[]`
			},
		},
		{
			name: "combined telemetry includes an unrelated process",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Soak.HostSamples[0].CPU = `[{"ProcessName":"ffmpeg","Id":101,"CPU":1,"WorkingSet64":1},{"ProcessName":"ffprobe","Id":202,"CPU":1,"WorkingSet64":1},{"ProcessName":"mediamtx","Id":303,"CPU":1,"WorkingSet64":1},{"ProcessName":"ffmpeg","Id":404,"CPU":1,"WorkingSet64":1}]`
			},
		},
		{
			name: "receiver capture is below the strict 1800 second floor",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Profiles[0].ReceiverDurationSeconds = 1799.999
			},
		},
		{
			name: "receiver capture timestamps do not cover declared soak",
			mutate: func(metrics *finalAcceptanceMetrics) {
				metrics.Profiles[0].ReceiverFinishedAt = metrics.Profiles[0].ReceiverStartedAt.Add(1799 * time.Second)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := completeFinalAcceptanceMetricsValidated(t)
			test.mutate(&metrics)
			if err := validateFinalAcceptanceMetrics(metrics); err == nil {
				t.Fatal("promotion bypass was accepted")
			}
		})
	}
}

func TestCompleteFinalAcceptanceMetricsValidates(t *testing.T) {
	_ = completeFinalAcceptanceMetricsValidated(t)
}

func TestMusicRadioFinalAcceptanceRejectsMissingMandatoryRawJSON(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "profile reconnect count",
			mutate: func(record map[string]any) {
				delete(record["profiles"].([]any)[0].(map[string]any), "reconnects")
			},
		},
		{
			name: "RTSP profile HLS readiness",
			mutate: func(record map[string]any) {
				delete(record["profiles"].([]any)[2].(map[string]any), "hlsReady")
			},
		},
		{
			name: "telemetry error field",
			mutate: func(record map[string]any) {
				soak := record["soak"].(map[string]any)
				delete(soak["hostSamples"].([]any)[0].(map[string]any), "error")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := completeFinalAcceptanceMetricsValidated(t)
			data, err := json.Marshal(metrics)
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			test.mutate(record)
			data, err = json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateFinalAcceptanceMetricsJSON(data); err == nil {
				t.Fatal("missing mandatory raw JSON field was accepted")
			}
		})
	}
}

func completeFinalAcceptanceMetricsValidated(t *testing.T) finalAcceptanceMetrics {
	t.Helper()
	metrics := completeFinalAcceptanceMetrics(t)
	if err := validateFinalAcceptanceMetrics(metrics); err != nil {
		t.Fatalf("complete fixture is invalid: %v", err)
	}
	data, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateFinalAcceptanceMetricsJSON(data); err != nil {
		t.Fatalf("complete fixture serialized evidence is invalid: %v", err)
	}
	return metrics
}

func TestFinalAcceptanceTelemetryTargetsUseOnlyOwnedProcessIDs(t *testing.T) {
	targets, err := newFinalAcceptanceTelemetryTargets(101, 202, []int{303, 303})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.FFmpegPIDs) != 1 || targets.FFmpegPIDs[0] != 101 {
		t.Fatalf("ffmpeg targets = %v, want [101]", targets.FFmpegPIDs)
	}
	if len(targets.ReaderPIDs) != 1 || targets.ReaderPIDs[0] != 202 {
		t.Fatalf("reader targets = %v, want [202]", targets.ReaderPIDs)
	}
	if len(targets.MediaMTXPIDs) != 1 || targets.MediaMTXPIDs[0] != 303 {
		t.Fatalf("MediaMTX targets = %v, want [303]", targets.MediaMTXPIDs)
	}
	script := finalAcceptanceTelemetryPowerShell(targets)
	for _, want := range []string{"$ffmpegPIDs = @(101)", "$readerPIDs = @(202)", "$mediaMTXPIDs = @(303)", "Get-Process -Id $allPIDs"} {
		if !strings.Contains(script, want) {
			t.Fatalf("telemetry script does not target %q: %s", want, script)
		}
	}
	if strings.Contains(script, "Get-Process -Name") {
		t.Fatalf("telemetry script scans process names: %s", script)
	}
}

func TestFinalAcceptanceTelemetryScheduleAvoidsCaptureDeadline(t *testing.T) {
	for _, test := range []struct {
		name     string
		duration time.Duration
		interval time.Duration
	}{
		{name: "ordinary matrix", duration: 12 * time.Second, interval: 4 * time.Second},
		{name: "thirty minute soak", duration: 30 * time.Minute, interval: 5 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			interval, periodicSamples := finalAcceptanceTelemetrySchedule(test.duration)
			if interval != test.interval || periodicSamples != 2 {
				t.Fatalf("schedule = %s/%d, want %s/2", interval, periodicSamples, test.interval)
			}
			if interval*time.Duration(periodicSamples) >= test.duration {
				t.Fatalf("telemetry schedule reaches capture deadline: interval=%s samples=%d duration=%s", interval, periodicSamples, test.duration)
			}
		})
	}
}

func TestFinalAcceptanceTelemetryTargetsRejectMissingOwnedIdentity(t *testing.T) {
	for _, test := range []struct {
		name        string
		capturePID  int
		readerPID   int
		mediaMTXPID []int
	}{
		{name: "capture child", readerPID: 202, mediaMTXPID: []int{303}},
		{name: "reader", capturePID: 101, mediaMTXPID: []int{303}},
		{name: "MediaMTX child", capturePID: 101, readerPID: 202},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newFinalAcceptanceTelemetryTargets(test.capturePID, test.readerPID, test.mediaMTXPID); err == nil {
				t.Fatal("missing owned process identity was accepted")
			}
		})
	}
}

func completeFinalAcceptanceMetrics(t *testing.T) finalAcceptanceMetrics {
	t.Helper()
	started := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	profiles := finalAcceptanceProfileMatrix()
	evidence := make([]finalAcceptanceProfileEvidence, 0, len(profiles))
	for index, profile := range profiles {
		profileStarted := started.Add(time.Duration(index*1800) * time.Second)
		receiverStarted := profileStarted.Add(time.Second)
		packets := completeFinalAcceptancePackets()
		evidence = append(evidence, finalAcceptanceProfileEvidence{
			Name:                    profile.Name,
			ExpectedGOP:             profile.GOPFrames,
			ObservedGOP:             profile.GOPFrames,
			ObservedCodec:           rtspCodecSignature{VideoCodec: "h264", AudioCodec: "aac"},
			HLSReady:                true,
			RTSPReady:               true,
			HLSVariant:              profile.ExpectedHLSVariant,
			PacketCount:             len(packets),
			Packets:                 packets,
			StartedAt:               profileStarted,
			FinishedAt:              profileStarted.Add(1801 * time.Second),
			ReceiverEvidence:        filepath.Join(t.TempDir(), profile.Name+".ts"),
			ReceiverSHA256:          strings.Repeat("a", 64),
			ReceiverBytes:           1,
			ReceiverStartedAt:       receiverStarted,
			ReceiverFinishedAt:      receiverStarted.Add(1800 * time.Second),
			ReceiverDurationSeconds: 1800,
		})
	}
	return finalAcceptanceMetrics{
		SchemaVersion: 1,
		Gate:          finalAcceptanceGate,
		Status:        "accepted",
		Accepted:      true,
		StartedAt:     started,
		FinishedAt:    started.Add(9001 * time.Second),
		Binaries:      []rtspBinaryEvidence{{Name: "mediamtx"}, {Name: "ffmpeg"}, {Name: "ffprobe"}},
		Profiles:      evidence,
		Soak: finalAcceptanceSoakEvidence{
			Requested:      true,
			TotalSeconds:   9000,
			PerProfileSecs: 1800,
			HostSamples:    completeFinalAcceptanceHostSamples(started),
		},
	}
}

func completeFinalAcceptancePackets() []rtspPacketEvidence {
	packets := make([]rtspPacketEvidence, 0, 60)
	for index := 0; index < 60; index++ {
		stream := index % 2
		timestamp := float64(index/2) * 0.033
		packets = append(packets, rtspPacketEvidence{ArrivalMillis: int64(index * 10), StreamIndex: stream, PTS: timestamp, DTS: timestamp})
	}
	return packets
}

func completeFinalAcceptanceHostSamples(started time.Time) []finalAcceptanceHostSample {
	samples := make([]finalAcceptanceHostSample, 0, len(finalAcceptanceProfileMatrix())*3)
	for index, profile := range finalAcceptanceProfileMatrix() {
		for _, offset := range []int{1, 900, 1799} {
			samples = append(samples, finalAcceptanceHostSample{
				Profile:      profile.Name,
				At:           started.Add(time.Duration(index*1800+offset) * time.Second),
				CPU:          `[{"ProcessName":"ffmpeg","Id":101,"CPU":1,"WorkingSet64":1},{"ProcessName":"ffprobe","Id":202,"CPU":1,"WorkingSet64":1},{"ProcessName":"mediamtx","Id":303,"CPU":1,"WorkingSet64":1}]`,
				GPU:          `[]`,
				FFmpeg:       `[{"ProcessName":"ffmpeg","Id":101,"CPU":1,"WorkingSet64":1}]`,
				Reader:       `[{"ProcessName":"ffprobe","Id":202,"CPU":1,"WorkingSet64":1}]`,
				MediaMTX:     `[{"ProcessName":"mediamtx","Id":303,"CPU":1,"WorkingSet64":1}]`,
				FFmpegPIDs:   []int{101},
				ReaderPIDs:   []int{202},
				MediaMTXPIDs: []int{303},
			})
		}
	}
	return samples
}

func TestMusicRadioFinalAcceptanceRunPlanRequiresThirtyMinutesPerProfile(t *testing.T) {
	plan, err := finalAcceptanceRunPlan(true, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.TotalSeconds != 9000 || plan.PerProfileSeconds != 1800 {
		t.Fatalf("soak plan = %+v, want five 1800-second profiles", plan)
	}
}

// TestMusicRadioFinalAcceptance is an opt-in real-binary acceptance test. It
// always persists an explicit blocked, failed, or accepted record when given
// an absolute output target; the build-tagged promotion test never accepts a
// blocked record.
func TestMusicRadioFinalAcceptance(t *testing.T) {
	started := time.Now().UTC()
	outputPath := strings.TrimSpace(os.Getenv("IMAGEPAD_FINAL_ACCEPTANCE_OUTPUT"))
	metrics := newFinalAcceptanceRecord()
	metrics.StartedAt = started
	if filepath.IsAbs(outputPath) {
		if err := writeFinalAcceptanceMetrics(outputPath, metrics); err != nil {
			t.Fatalf("invalidate stale #20B metrics: %v", err)
		}
	}
	pins, blocked := pinnedRTSPAcceptanceBinaries()
	if os.Getenv("IMAGEPAD_FINAL_ACCEPTANCE") != "1" {
		blocked = append(blocked, "IMAGEPAD_FINAL_ACCEPTANCE=1 is required")
	}
	if !filepath.IsAbs(outputPath) {
		blocked = append(blocked, "IMAGEPAD_FINAL_ACCEPTANCE_OUTPUT must be an absolute path")
	}
	if len(blocked) != 0 {
		metrics.Status = "blocked"
		metrics.BlockedReason = strings.Join(blocked, "; ")
		metrics.FinishedAt = time.Now().UTC()
		if filepath.IsAbs(outputPath) {
			if err := writeFinalAcceptanceMetrics(outputPath, metrics); err != nil {
				t.Fatalf("persist blocked #20B evidence: %v", err)
			}
		}
		t.Skipf("BLOCKED #20B FINAL ACCEPTANCE: %s", metrics.BlockedReason)
	}
	plan, err := finalAcceptanceRunPlan(os.Getenv("IMAGEPAD_FINAL_ACCEPTANCE_SOAK") == "1", os.Getenv("IMAGEPAD_FINAL_ACCEPTANCE_SOAK_SECONDS"))
	if err != nil {
		metrics.Status, metrics.Failure, metrics.FinishedAt = "failed", err.Error(), time.Now().UTC()
		_ = writeFinalAcceptanceMetrics(outputPath, metrics)
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(plan.TotalSeconds+len(finalAcceptanceProfileMatrix())*90)*time.Second)
	defer cancel()
	binaries, err := inspectRTSPAcceptanceBinaries(ctx, pins)
	if err != nil {
		metrics.Status, metrics.Failure, metrics.FinishedAt = "failed", err.Error(), time.Now().UTC()
		_ = writeFinalAcceptanceMetrics(outputPath, metrics)
		t.Fatal(err)
	}
	metrics.Binaries = binaries
	metrics.Soak = finalAcceptanceSoakEvidence{Requested: plan.SoakRequested, TotalSeconds: plan.TotalSeconds, PerProfileSecs: plan.PerProfileSeconds, HostSamples: []finalAcceptanceHostSample{}}
	captureDir := filepath.Join(filepath.Dir(outputPath), "playlist-final-receiver-captures")
	for _, profile := range finalAcceptanceProfileMatrix() {
		evidence, samples, err := runFinalAcceptanceProfile(ctx, t, pins, profile, time.Duration(plan.PerProfileSeconds)*time.Second, captureDir)
		metrics.Profiles = append(metrics.Profiles, evidence)
		metrics.Soak.HostSamples = append(metrics.Soak.HostSamples, samples...)
		if err != nil {
			metrics.Status, metrics.Failure = "failed", err.Error()
			metrics.FinishedAt = time.Now().UTC()
			_ = writeFinalAcceptanceMetrics(outputPath, metrics)
			t.Fatalf("#20B profile %s: %v", profile.Name, err)
		}
	}
	metrics.Status, metrics.Accepted, metrics.FinishedAt = "accepted", true, time.Now().UTC()
	if plan.SoakRequested {
		if err := validateFinalAcceptanceMetrics(metrics); err != nil {
			metrics.Status, metrics.Accepted, metrics.Failure = "failed", false, err.Error()
			_ = writeFinalAcceptanceMetrics(outputPath, metrics)
			t.Fatal(err)
		}
	}
	if err := writeFinalAcceptanceMetrics(outputPath, metrics); err != nil {
		t.Fatal(err)
	}
	t.Logf("#20B accepted; metrics=%s", outputPath)
}

func finalAcceptanceProfileMatrix() []finalAcceptanceProfile {
	return []finalAcceptanceProfile{
		{Name: obsrtmp.LatencyModeHLSHigh, GOPFrames: 120, RequiresHLS: true, ExpectedHLSVariant: "fmp4"},
		{Name: obsrtmp.LatencyModeHLS, GOPFrames: 30, RequiresHLS: true, ExpectedHLSVariant: "fmp4"},
		{Name: obsrtmp.LatencyModeRTSPLow, GOPFrames: 60, RequiresRTSP: true, ExpectedHLSVariant: "lowLatency"},
		{Name: obsrtmp.LatencyModeRTSPUltra, GOPFrames: 30, RequiresRTSP: true, ExpectedHLSVariant: "lowLatency"},
		{Name: obsrtmp.LatencyModeRTSPRealtime, GOPFrames: 15, RequiresRTSP: true, ExpectedHLSVariant: "lowLatency"},
	}
}

func newFinalAcceptanceRecord() finalAcceptanceMetrics {
	return finalAcceptanceMetrics{SchemaVersion: 1, Gate: finalAcceptanceGate, Status: "invalidated", StartedAt: time.Now().UTC(), Profiles: []finalAcceptanceProfileEvidence{}, Soak: finalAcceptanceSoakEvidence{HostSamples: []finalAcceptanceHostSample{}}}
}

func finalAcceptanceRunPlan(soak bool, rawSeconds string) (finalAcceptanceRunConfig, error) {
	seconds := 12
	if soak {
		seconds = 1800
	}
	if strings.TrimSpace(rawSeconds) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(rawSeconds))
		if err != nil || parsed <= 0 {
			return finalAcceptanceRunConfig{}, fmt.Errorf("invalid IMAGEPAD_FINAL_ACCEPTANCE_SOAK_SECONDS %q", rawSeconds)
		}
		seconds = parsed
	}
	if soak && seconds < 1800 {
		return finalAcceptanceRunConfig{}, errors.New("IMAGEPAD_FINAL_ACCEPTANCE_SOAK_SECONDS must be at least 1800 for final acceptance")
	}
	profiles := len(finalAcceptanceProfileMatrix())
	return finalAcceptanceRunConfig{SoakRequested: soak, TotalSeconds: seconds * profiles, PerProfileSeconds: seconds}, nil
}

func runFinalAcceptanceProfile(ctx context.Context, t *testing.T, pins map[string]string, profile finalAcceptanceProfile, duration time.Duration, captureDir string) (finalAcceptanceProfileEvidence, []finalAcceptanceHostSample, error) {
	started := time.Now().UTC()
	evidence := finalAcceptanceProfileEvidence{Name: profile.Name, ExpectedGOP: profile.GOPFrames, StartedAt: started}
	for name, path := range map[string]string{"IMAGEPAD_MEDIAMTX": pins["mediamtx"], "IMAGEPAD_FFMPEG": pins["ffmpeg"], "IMAGEPAD_FFPROBE": pins["ffprobe"]} {
		t.Setenv(name, path)
	}
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	defer srv.radio.Stop(10 * time.Second)
	preset := video.QualityPreset{Height: 360, VideoBitrate: "900k", MaxRate: "1100k", BufferSize: "2200k", AudioBitrate: "128k", RadioLatency: profile.Name}
	srv.activeCanonicalHeight = 360
	srv.radio.SetOutputMode(func() obsrtmp.RadioOutputMode { return obsrtmp.RadioOutputModeProgram })
	srv.radio.SetLatencyProfile(func() obsrtmp.LatencyProfile { return obsrtmp.NormalizeLatencyProfile(profile.Name) })
	srv.radio.SetFallbackPreset(func() video.QualityPreset { return preset })
	enableMusicMode(t)
	mappings := newRTSPAcceptanceMappingRecorder(started)
	srv.mapRTSPPort = mappings.mapPort
	readyEndpoints := make(chan obsrtmp.RTSPEndpoint, 1)
	srv.onRadioRTSPReady = func(endpoint obsrtmp.RTSPEndpoint) {
		select {
		case readyEndpoints <- endpoint:
		default:
		}
	}
	if err := postRTSPAcceptance(mux, "/api/music/playlist/play", `{}`); err != nil {
		return evidence, nil, err
	}
	status, err := waitFinalAcceptanceState(ctx, srv, 75*time.Second, func(status obsrtmp.RadioStatus) bool {
		return status.Running && status.RTSPReady && status.HLSReady && status.Path != ""
	})
	if err != nil {
		return evidence, nil, fmt.Errorf("readiness: %w", err)
	}
	if status.ActiveSession == nil || status.ActiveSession.DeliveryProfile != profile.Name || status.ActiveSession.OutputMode != obsrtmp.RadioOutputModeProgram {
		return evidence, nil, fmt.Errorf("active session contract is not program/%s: %+v", profile.Name, status.ActiveSession)
	}
	evidence.RTSPReady, evidence.HLSReady, evidence.HLSVariant = status.RTSPReady, status.HLSReady, status.ActiveSession.HLSVariant
	path := status.Path
	var endpoint obsrtmp.RTSPEndpoint
	select {
	case endpoint = <-readyEndpoints:
	case <-ctx.Done():
		return evidence, nil, fmt.Errorf("capture RTSP endpoint: %w", ctx.Err())
	}
	readerURL, err := rtspAcceptanceReaderURL(status, endpoint)
	if err != nil {
		return evidence, nil, err
	}
	reader, err := startRTSPAcceptanceReader(ctx, pins["ffprobe"], readerURL, started)
	if err != nil {
		return evidence, nil, err
	}
	defer reader.killAndWait()
	if _, err := reader.waitForPacketCount(ctx, 60); err != nil {
		return evidence, nil, fmt.Errorf("initial receiver packets: %w", err)
	}
	capturePath, captureStartedAt, capturePID, captureDone, err := startFinalAcceptanceReceiverCapture(ctx, pins["ffmpeg"], readerURL, captureDir, profile.Name, duration)
	if err != nil {
		return evidence, nil, err
	}
	mediaMTXOwner, ok := srv.radio.(interface{ OwnedMediaMTXPIDs() []int })
	if !ok {
		return evidence, nil, errors.New("final acceptance radio does not expose its owned MediaMTX child")
	}
	targets, err := newFinalAcceptanceTelemetryTargets(capturePID, finalAcceptanceCommandPID(reader.cmd), mediaMTXOwner.OwnedMediaMTXPIDs())
	if err != nil {
		return evidence, nil, err
	}
	evidence.ReceiverStartedAt = captureStartedAt
	select {
	case err := <-captureDone:
		return evidence, nil, fmt.Errorf("receiver capture exited before telemetry: %w", err)
	case <-time.After(250 * time.Millisecond):
	}
	samples := make([]finalAcceptanceHostSample, 0, 4)
	captureSample := func() error {
		sample := sampleFinalAcceptanceHost(ctx, profile.Name, targets)
		if err := validateFinalAcceptanceHostSample(sample, profile.Name, captureStartedAt, time.Time{}); err != nil {
			return fmt.Errorf("%w: sample=%+v", err, sample)
		}
		samples = append(samples, sample)
		return nil
	}
	if err := captureSample(); err != nil {
		return evidence, samples, err
	}
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	pathLosses, monitorDone := monitorRTSPAcceptancePath(monitorCtx, srv, path)
	remaining := time.Until(captureStartedAt.Add(duration))
	if remaining <= 0 {
		return evidence, samples, errors.New("receiver capture elapsed before continuity monitoring began")
	}
	if err := waitFinalAcceptanceContinuity(ctx, srv, reader, profile, path, remaining, captureSample); err != nil {
		stopMonitor()
		<-monitorDone
		return evidence, samples, err
	}
	select {
	case err := <-captureDone:
		if err != nil {
			stopMonitor()
			<-monitorDone
			return evidence, samples, fmt.Errorf("receiver capture: %w", err)
		}
	case <-time.After(20 * time.Second):
		stopMonitor()
		<-monitorDone
		return evidence, samples, errors.New("receiver capture did not finish")
	case <-ctx.Done():
		stopMonitor()
		<-monitorDone
		return evidence, samples, ctx.Err()
	}
	stopMonitor()
	<-monitorDone
	if err := postRTSPAcceptance(mux, "/api/music/playlist/stop", `{}`); err != nil {
		return evidence, samples, err
	}
	if _, err := waitFinalAcceptanceState(ctx, srv, 15*time.Second, func(status obsrtmp.RadioStatus) bool {
		return !status.Running && status.Phase == obsrtmp.RadioPhaseStopped
	}); err != nil {
		return evidence, samples, fmt.Errorf("stop: %w", err)
	}
	if _, ok := reader.waitForExit(10 * time.Second); !ok {
		return evidence, samples, errors.New("receiver remained connected after intentional stop")
	}
	packets, _, stderr := reader.snapshot()
	evidence.Reconnects = strings.Count(strings.ToLower(stderr), "reconnect")
	evidence.ReaderStderr = strings.TrimSpace(stderr)
	if len(packets) < 60 || countRTSPPacketStreams(packets) < 2 {
		return evidence, samples, fmt.Errorf("live receiver packet evidence is incomplete: packets=%d streams=%d", len(packets), countRTSPPacketStreams(packets))
	}
	observedPackets, err := finalAcceptanceCapturePackets(ctx, pins["ffprobe"], capturePath)
	if err != nil {
		return evidence, samples, err
	}
	evidence.PacketCount = len(observedPackets)
	evidence.Packets = observedPackets
	if err := validateFinalAcceptancePackets(observedPackets); err != nil {
		return evidence, samples, err
	}
	observed, err := video.ObserveRadioAsset(ctx, pins["ffprobe"], capturePath)
	if err != nil {
		return evidence, samples, fmt.Errorf("observe receiver capture: %w", err)
	}
	evidence.ObservedGOP = observed.Video.GOPFrames
	evidence.ObservedCodec = rtspCodecSignature{VideoCodec: observed.Video.Codec, Width: observed.Video.Width, Height: observed.Video.Height, PixelFormat: observed.Video.PixelFormat, FrameRate: fmt.Sprintf("%d/%d", observed.Video.FrameRate.Numerator, observed.Video.FrameRate.Denominator), AudioCodec: observed.Audio.Codec, SampleRate: observed.Audio.SampleRate, AudioChannels: observed.Audio.Channels}
	evidence.PathLosses = int(pathLosses.Load())
	evidence.ReceiverEvidence = capturePath
	evidence.ReceiverSHA256, err = sha256File(capturePath)
	if err != nil {
		return evidence, samples, fmt.Errorf("hash receiver capture: %w", err)
	}
	info, err := os.Stat(capturePath)
	if err != nil {
		return evidence, samples, fmt.Errorf("stat receiver capture: %w", err)
	}
	evidence.ReceiverBytes = info.Size()
	evidence.ReceiverDurationSeconds, err = finalAcceptanceCaptureDuration(ctx, pins["ffprobe"], capturePath)
	if err != nil {
		return evidence, samples, err
	}
	evidence.ReceiverFinishedAt = evidence.ReceiverStartedAt.Add(time.Duration(evidence.ReceiverDurationSeconds * float64(time.Second)))
	evidence.FinishedAt = time.Now().UTC()
	if evidence.ObservedGOP != profile.GOPFrames {
		return evidence, samples, fmt.Errorf("observed GOP = %d, want %d", evidence.ObservedGOP, profile.GOPFrames)
	}
	if evidence.ObservedCodec.VideoCodec != "h264" || evidence.ObservedCodec.AudioCodec != "aac" {
		return evidence, samples, fmt.Errorf("observed codec = %s/%s, want h264/aac", evidence.ObservedCodec.VideoCodec, evidence.ObservedCodec.AudioCodec)
	}
	if profile.RequiresHLS && (!evidence.HLSReady || evidence.HLSVariant != profile.ExpectedHLSVariant) {
		return evidence, samples, fmt.Errorf("HLS artifact/readiness = %v/%q, want true/%q", evidence.HLSReady, evidence.HLSVariant, profile.ExpectedHLSVariant)
	}
	if profile.RequiresRTSP && !evidence.RTSPReady {
		return evidence, samples, errors.New("RTSP readiness was not observed")
	}
	if evidence.Reconnects != 0 || evidence.PathLosses != 0 || evidence.ReaderStderr != "" {
		return evidence, samples, fmt.Errorf("receiver continuity failure: reconnects=%d pathLosses=%d stderr=%q", evidence.Reconnects, evidence.PathLosses, evidence.ReaderStderr)
	}
	if err := mappings.waitClosed(ctx); err != nil {
		created, closed := mappings.counts()
		return evidence, samples, fmt.Errorf("mapping cleanup: created=%d closed=%d err=%v", created, closed, err)
	}
	created, closed := mappings.counts()
	if created == 0 || created != closed {
		return evidence, samples, fmt.Errorf("mapping cleanup: created=%d closed=%d", created, closed)
	}
	ffmpegKilled, ffmpegErr := video.CleanupTrackedFFmpeg()
	mediaMTXKilled, mediaMTXErr := obsrtmp.CleanupStaleMediaMTX()
	if ffmpegErr != nil || mediaMTXErr != nil || ffmpegKilled != 0 || mediaMTXKilled != 0 {
		return evidence, samples, fmt.Errorf("process cleanup intervention: ffmpeg=%d/%v mediamtx=%d/%v", ffmpegKilled, ffmpegErr, mediaMTXKilled, mediaMTXErr)
	}
	return evidence, samples, nil
}

func waitFinalAcceptanceState(ctx context.Context, srv *Server, timeout time.Duration, predicate func(obsrtmp.RadioStatus) bool) (obsrtmp.RadioStatus, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		status := srv.radio.Status()
		if predicate(status) {
			return status, nil
		}
		if status.Phase == obsrtmp.RadioPhaseFailed {
			return status, fmt.Errorf("radio failed: %s", status.LastError)
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-deadline.C:
			return status, fmt.Errorf("state timeout: %+v", status)
		case <-ticker.C:
		}
	}
}

func waitFinalAcceptanceContinuity(ctx context.Context, srv *Server, reader *rtspAcceptanceReader, profile finalAcceptanceProfile, path string, duration time.Duration, sample func() error) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	sampleInterval, periodicSamples := finalAcceptanceTelemetrySchedule(duration)
	sampleTicker := time.NewTicker(sampleInterval)
	defer sampleTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-sampleTicker.C:
			if periodicSamples == 0 {
				continue
			}
			periodicSamples--
			if err := sample(); err != nil {
				return err
			}
		case <-ticker.C:
			status := srv.radio.Status()
			if status.Phase == obsrtmp.RadioPhaseFailed || !status.Running || !status.RTSPReady || status.Path != path {
				return fmt.Errorf("RTSP path lost during %s: %+v", profile.Name, status)
			}
			if profile.RequiresHLS && !status.HLSReady {
				return fmt.Errorf("HLS readiness lost during %s", profile.Name)
			}
			if reader.exited() {
				return errors.New("receiver exited before intentional stop")
			}
		}
	}
}

// finalAcceptanceTelemetrySchedule keeps all three required samples inside the
// capture window: one immediate sample plus two periodic samples.
func finalAcceptanceTelemetrySchedule(duration time.Duration) (time.Duration, int) {
	interval := duration / 3
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	if interval <= 0 {
		interval = time.Second
	}
	return interval, 2
}

func startFinalAcceptanceReceiverCapture(ctx context.Context, ffmpeg, sourceURL, dir, profile string, duration time.Duration) (string, time.Time, int, <-chan error, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", time.Time{}, 0, nil, fmt.Errorf("create receiver capture directory: %w", err)
	}
	path := filepath.Join(dir, profile+".ts")
	args := []string{"-hide_banner", "-loglevel", "error", "-rtsp_transport", "tcp", "-timeout", "10000000", "-i", sourceURL, "-map", "0:v:0", "-map", "0:a:0", "-c", "copy", "-t", strconv.FormatFloat(duration.Seconds(), 'f', 3, 64), "-f", "mpegts", "-y", path}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	started := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		return "", time.Time{}, 0, nil, fmt.Errorf("start receiver capture: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if err != nil {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		done <- err
		close(done)
	}()
	return path, started, cmd.Process.Pid, done, nil
}

func validateFinalAcceptancePackets(packets []rtspPacketEvidence) error {
	if len(packets) < 60 || countRTSPPacketStreams(packets) < 2 {
		return fmt.Errorf("receiver packet evidence is incomplete: packets=%d streams=%d", len(packets), countRTSPPacketStreams(packets))
	}
	lastPTS := map[int]float64{}
	lastDTS := map[int]float64{}
	seen := map[int]bool{}
	for index, packet := range packets {
		if seen[packet.StreamIndex] && (packet.PTS < lastPTS[packet.StreamIndex] || packet.DTS < lastDTS[packet.StreamIndex]) {
			return fmt.Errorf("non-monotonic PTS/DTS on stream %d at packet %d: previous=%.6f/%.6f observed=%.6f/%.6f", packet.StreamIndex, index, lastPTS[packet.StreamIndex], lastDTS[packet.StreamIndex], packet.PTS, packet.DTS)
		}
		if seen[packet.StreamIndex] && (packet.DTS-lastDTS[packet.StreamIndex])*1000 > float64(rtspMaxPacketGapMillis) {
			return fmt.Errorf("packet DTS gap %.3fms exceeds %dms on stream %d", (packet.DTS-lastDTS[packet.StreamIndex])*1000, rtspMaxPacketGapMillis, packet.StreamIndex)
		}
		seen[packet.StreamIndex] = true
		lastPTS[packet.StreamIndex], lastDTS[packet.StreamIndex] = packet.PTS, packet.DTS
	}
	return nil
}

func sampleFinalAcceptanceHost(ctx context.Context, profile string, targets finalAcceptanceTelemetryTargets) finalAcceptanceHostSample {
	sample := finalAcceptanceHostSample{
		Profile:      profile,
		FFmpegPIDs:   append([]int(nil), targets.FFmpegPIDs...),
		ReaderPIDs:   append([]int(nil), targets.ReaderPIDs...),
		MediaMTXPIDs: append([]int(nil), targets.MediaMTXPIDs...),
	}
	if runtime.GOOS != "windows" {
		sample.Error = "CPU/GPU probe requires Windows PowerShell"
		return sample
	}
	script := finalAcceptanceTelemetryPowerShell(targets)
	output, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		sample.Error = fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(output)))
		return sample
	}
	var observed struct {
		CPU      json.RawMessage `json:"cpu"`
		GPU      json.RawMessage `json:"gpu"`
		FFmpeg   json.RawMessage `json:"ffmpeg"`
		Reader   json.RawMessage `json:"reader"`
		MediaMTX json.RawMessage `json:"mediaMTX"`
	}
	if err := json.Unmarshal(output, &observed); err != nil {
		sample.Error = fmt.Sprintf("decode telemetry: %v", err)
		return sample
	}
	sample.At = time.Now().UTC()
	sample.CPU = string(observed.CPU)
	sample.GPU = string(observed.GPU)
	sample.FFmpeg, sample.Reader, sample.MediaMTX = string(observed.FFmpeg), string(observed.Reader), string(observed.MediaMTX)
	return sample
}

func newFinalAcceptanceTelemetryTargets(capturePID, readerPID int, mediaMTXPIDs []int) (finalAcceptanceTelemetryTargets, error) {
	targets := finalAcceptanceTelemetryTargets{FFmpegPIDs: []int{capturePID}, ReaderPIDs: []int{readerPID}, MediaMTXPIDs: finalAcceptanceUniquePIDs(mediaMTXPIDs)}
	if capturePID <= 0 || readerPID <= 0 || len(targets.MediaMTXPIDs) == 0 {
		return finalAcceptanceTelemetryTargets{}, errors.New("final acceptance telemetry requires owned capture, reader, and MediaMTX PIDs")
	}
	for _, pid := range append(append([]int{}, targets.FFmpegPIDs...), append(targets.ReaderPIDs, targets.MediaMTXPIDs...)...) {
		if pid <= 0 {
			return finalAcceptanceTelemetryTargets{}, errors.New("final acceptance telemetry contains an invalid owned PID")
		}
	}
	return targets, nil
}

func finalAcceptanceUniquePIDs(pids []int) []int {
	seen := make(map[int]struct{}, len(pids))
	unique := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		unique = append(unique, pid)
	}
	return unique
}

func finalAcceptanceTelemetryPowerShell(targets finalAcceptanceTelemetryTargets) string {
	join := func(pids []int) string {
		values := make([]string, 0, len(pids))
		for _, pid := range pids {
			values = append(values, strconv.Itoa(pid))
		}
		return strings.Join(values, ",")
	}
	return fmt.Sprintf(`$ffmpegPIDs = @(%s); $readerPIDs = @(%s); $mediaMTXPIDs = @(%s); $allPIDs = @($ffmpegPIDs + $readerPIDs + $mediaMTXPIDs | Select-Object -Unique); $p = @(Get-Process -Id $allPIDs -ErrorAction Stop | Select-Object ProcessName,Id,CPU,WorkingSet64); $g = @(Get-CimInstance Win32_PerfFormattedData_GPUPerformanceCounters_GPUEngine -ErrorAction SilentlyContinue | Select-Object -First 64 Name,UtilizationPercentage); [pscustomobject]@{cpu=$p;gpu=$g;ffmpeg=@($p | Where-Object ProcessName -eq "ffmpeg");reader=@($p | Where-Object ProcessName -eq "ffprobe");mediaMTX=@($p | Where-Object ProcessName -eq "mediamtx")} | ConvertTo-Json -Compress -Depth 4`, join(targets.FFmpegPIDs), join(targets.ReaderPIDs), join(targets.MediaMTXPIDs))
}

func finalAcceptanceCommandPID(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}

func finalAcceptanceOption(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func writeFinalAcceptanceMetrics(path string, metrics finalAcceptanceMetrics) error {
	if !filepath.IsAbs(path) {
		return errors.New("final acceptance metrics path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".final-acceptance-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func validateFinalAcceptanceMetricsFile(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("IMAGEPAD_FINAL_ACCEPTANCE_OUTPUT must be an absolute path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	metrics, err := validateFinalAcceptanceMetricsJSON(data)
	if err != nil {
		return err
	}
	if err := validateRTSPBinaryEvidence(metrics.Binaries); err != nil {
		return fmt.Errorf("#20B pinned binary evidence: %w", err)
	}
	// Promotion independently probes every durable half-hour capture. Give the
	// aggregate context enough room for the five bounded per-file probes.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := validateRTSPPromotionToolVersions(ctx, metrics.Binaries, probeRTSPToolVersion); err != nil {
		return fmt.Errorf("#20B pinned binary versions: %w", err)
	}
	return validateFinalAcceptanceCaptureFiles(ctx, metrics)
}

func validateFinalAcceptanceMetricsJSON(data []byte) (finalAcceptanceMetrics, error) {
	if err := validateFinalAcceptanceRawJSON(data); err != nil {
		return finalAcceptanceMetrics{}, err
	}
	var metrics finalAcceptanceMetrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return finalAcceptanceMetrics{}, err
	}
	if err := validateFinalAcceptanceMetrics(metrics); err != nil {
		return finalAcceptanceMetrics{}, err
	}
	return metrics, nil
}

func validateFinalAcceptanceRawJSON(data []byte) error {
	record, err := finalAcceptanceRequiredJSONObject(data, "record", "schemaVersion", "gate", "status", "accepted", "startedAt", "finishedAt", "binaries", "profiles", "soak")
	if err != nil {
		return err
	}
	if err := finalAcceptanceValidateRawArray(record["binaries"], "binaries", func(index int, value json.RawMessage) error {
		_, err := finalAcceptanceRequiredJSONObject(value, fmt.Sprintf("binary[%d]", index), "name", "path", "version", "sha256")
		return err
	}); err != nil {
		return err
	}
	if err := finalAcceptanceValidateRawArray(record["profiles"], "profiles", func(index int, value json.RawMessage) error {
		profile, err := finalAcceptanceRequiredJSONObject(value, fmt.Sprintf("profile[%d]", index), "name", "expectedGOP", "observedGOP", "observedCodec", "hlsReady", "rtspReady", "hlsVariant", "packetCount", "packets", "reconnects", "pathLosses", "readerStderr", "startedAt", "finishedAt", "receiverEvidence", "receiverSHA256", "receiverBytes", "receiverStartedAt", "receiverFinishedAt", "receiverDurationSeconds")
		if err != nil {
			return err
		}
		if _, err := finalAcceptanceRequiredJSONObject(profile["observedCodec"], fmt.Sprintf("profile[%d].observedCodec", index), "videoCodec", "width", "height", "pixelFormat", "frameRate", "audioCodec", "sampleRate", "audioChannels"); err != nil {
			return err
		}
		return finalAcceptanceValidateRawArray(profile["packets"], fmt.Sprintf("profile[%d].packets", index), func(packetIndex int, packet json.RawMessage) error {
			_, err := finalAcceptanceRequiredJSONObject(packet, fmt.Sprintf("profile[%d].packets[%d]", index, packetIndex), "arrivalMillis", "streamIndex", "pts", "dts")
			return err
		})
	}); err != nil {
		return err
	}
	soak, err := finalAcceptanceRequiredJSONObject(record["soak"], "soak", "requested", "totalSeconds", "perProfileSeconds", "hostSamples")
	if err != nil {
		return err
	}
	return finalAcceptanceValidateRawArray(soak["hostSamples"], "soak.hostSamples", func(index int, value json.RawMessage) error {
		_, err := finalAcceptanceRequiredJSONObject(value, fmt.Sprintf("soak.hostSamples[%d]", index), "profile", "at", "cpu", "gpu", "ffmpeg", "reader", "mediaMTX", "ffmpegPIDs", "readerPIDs", "mediaMTXPIDs", "error")
		return err
	})
}

func finalAcceptanceRequiredJSONObject(data json.RawMessage, scope string, fields ...string) (map[string]json.RawMessage, error) {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("%s is not a JSON object", scope)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("decode %s JSON object: %w", scope, err)
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok || len(bytes.TrimSpace(value)) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("%s is missing required raw JSON field %q", scope, field)
		}
	}
	return object, nil
}

func finalAcceptanceValidateRawArray(data json.RawMessage, scope string, validate func(int, json.RawMessage) error) error {
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("decode %s JSON array: %w", scope, err)
	}
	for index, value := range values {
		if err := validate(index, value); err != nil {
			return err
		}
	}
	return nil
}

func validateFinalAcceptanceMetrics(metrics finalAcceptanceMetrics) error {
	if metrics.SchemaVersion != 1 || metrics.Gate != finalAcceptanceGate || metrics.Status != "accepted" || !metrics.Accepted {
		return fmt.Errorf("final acceptance record is not accepted: gate=%q status=%q accepted=%v", metrics.Gate, metrics.Status, metrics.Accepted)
	}
	if metrics.StartedAt.IsZero() || metrics.FinishedAt.IsZero() || !metrics.FinishedAt.After(metrics.StartedAt) {
		return errors.New("final acceptance timestamps are invalid")
	}
	if len(metrics.Binaries) != 3 {
		return fmt.Errorf("binary evidence count = %d, want 3", len(metrics.Binaries))
	}
	if !metrics.Soak.Requested || metrics.Soak.PerProfileSecs < 1800 {
		return errors.New("final promotion requires at least 1800 seconds for every profile")
	}
	profiles := finalAcceptanceProfileMatrix()
	minimumTotal := metrics.Soak.PerProfileSecs * len(profiles)
	if metrics.Soak.TotalSeconds < minimumTotal {
		return fmt.Errorf("total soak duration = %d seconds, want at least %d", metrics.Soak.TotalSeconds, minimumTotal)
	}
	if len(metrics.Profiles) != len(profiles) {
		return fmt.Errorf("profile evidence count = %d, want %d", len(metrics.Profiles), len(profiles))
	}
	for index, expected := range profiles {
		observed := metrics.Profiles[index]
		if observed.Name != expected.Name || observed.ExpectedGOP != expected.GOPFrames || observed.ObservedGOP != expected.GOPFrames {
			return fmt.Errorf("profile %q GOP evidence is invalid", expected.Name)
		}
		if observed.ObservedCodec.VideoCodec != "h264" || observed.ObservedCodec.AudioCodec != "aac" || observed.PacketCount == 0 {
			return fmt.Errorf("profile %q codec or receiver evidence is incomplete", expected.Name)
		}
		requiredDuration := time.Duration(metrics.Soak.PerProfileSecs) * time.Second
		if observed.StartedAt.IsZero() || observed.FinishedAt.IsZero() || observed.FinishedAt.Before(observed.StartedAt) || observed.FinishedAt.Sub(observed.StartedAt) < finalAcceptanceMinimumDuration || observed.FinishedAt.Sub(observed.StartedAt) < requiredDuration {
			return fmt.Errorf("profile %q soak duration is incomplete", expected.Name)
		}
		if observed.ReceiverStartedAt.IsZero() || observed.ReceiverFinishedAt.IsZero() || observed.ReceiverFinishedAt.Before(observed.ReceiverStartedAt) || observed.ReceiverStartedAt.Before(observed.StartedAt) || observed.ReceiverFinishedAt.After(observed.FinishedAt) || observed.ReceiverFinishedAt.Sub(observed.ReceiverStartedAt) < finalAcceptanceMinimumDuration || observed.ReceiverFinishedAt.Sub(observed.ReceiverStartedAt) < requiredDuration {
			return fmt.Errorf("profile %q receiver capture timestamps do not cover the declared soak", expected.Name)
		}
		if observed.ReceiverDurationSeconds < finalAcceptanceMinimumDuration.Seconds() || observed.ReceiverDurationSeconds < float64(metrics.Soak.PerProfileSecs) {
			return fmt.Errorf("profile %q receiver capture duration = %.3fs, want at least %d seconds", expected.Name, observed.ReceiverDurationSeconds, metrics.Soak.PerProfileSecs)
		}
		if math.Abs(observed.ReceiverDurationSeconds-observed.ReceiverFinishedAt.Sub(observed.ReceiverStartedAt).Seconds()) > finalAcceptanceDurationTolerance.Seconds() {
			return fmt.Errorf("profile %q receiver capture duration %.3fs does not match capture timestamps", expected.Name, observed.ReceiverDurationSeconds)
		}
		if observed.PacketCount != len(observed.Packets) {
			return fmt.Errorf("profile %q packet count does not match packet evidence", expected.Name)
		}
		if err := validateFinalAcceptancePackets(observed.Packets); err != nil {
			return fmt.Errorf("profile %q receiver packets: %w", expected.Name, err)
		}
		if !filepath.IsAbs(observed.ReceiverEvidence) || observed.ReceiverBytes <= 0 || strings.TrimSpace(observed.ReceiverSHA256) == "" {
			return fmt.Errorf("profile %q receiver capture evidence is incomplete", expected.Name)
		}
		digest, err := hex.DecodeString(observed.ReceiverSHA256)
		if err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("profile %q receiver capture SHA-256 is invalid", expected.Name)
		}
		if observed.Reconnects != 0 || observed.PathLosses != 0 || strings.TrimSpace(observed.ReaderStderr) != "" {
			return fmt.Errorf("profile %q reported reconnect, path loss, or reader stderr", expected.Name)
		}
		if expected.RequiresHLS && (!observed.HLSReady || observed.HLSVariant != expected.ExpectedHLSVariant) {
			return fmt.Errorf("profile %q HLS readiness evidence is invalid", expected.Name)
		}
		if expected.RequiresRTSP && !observed.RTSPReady {
			return fmt.Errorf("profile %q RTSP readiness evidence is invalid", expected.Name)
		}
	}
	if len(metrics.Soak.HostSamples) < len(profiles) {
		return errors.New("soak telemetry does not cover every profile")
	}
	profileEvidence := make(map[string]finalAcceptanceProfileEvidence, len(metrics.Profiles))
	for _, profile := range metrics.Profiles {
		profileEvidence[profile.Name] = profile
	}
	telemetryCount := make(map[string]int, len(profiles))
	for _, sample := range metrics.Soak.HostSamples {
		profile, ok := profileEvidence[sample.Profile]
		if !ok {
			return fmt.Errorf("telemetry references unknown profile %q", sample.Profile)
		}
		if err := validateFinalAcceptanceHostSample(sample, sample.Profile, profile.ReceiverStartedAt, profile.ReceiverFinishedAt); err != nil {
			return err
		}
		telemetryCount[sample.Profile]++
	}
	for _, profile := range profiles {
		if telemetryCount[profile.Name] < 3 {
			return fmt.Errorf("profile %q has %d telemetry samples, want at least 3", profile.Name, telemetryCount[profile.Name])
		}
	}
	return nil
}

func validateFinalAcceptanceHostSample(sample finalAcceptanceHostSample, profile string, started, finished time.Time) error {
	if sample.Profile != profile || sample.At.IsZero() || strings.TrimSpace(sample.Error) != "" || strings.TrimSpace(sample.CPU) == "" || strings.TrimSpace(sample.GPU) == "" || strings.TrimSpace(sample.FFmpeg) == "" || strings.TrimSpace(sample.Reader) == "" || strings.TrimSpace(sample.MediaMTX) == "" || len(sample.FFmpegPIDs) == 0 || len(sample.ReaderPIDs) == 0 || len(sample.MediaMTXPIDs) == 0 {
		return fmt.Errorf("profile %q has invalid or empty live telemetry", profile)
	}
	if !finished.IsZero() && (sample.At.Before(started) || sample.At.After(finished)) {
		return fmt.Errorf("profile %q telemetry was recorded outside the receiver run", profile)
	}
	for _, value := range []string{sample.CPU, sample.GPU, sample.FFmpeg, sample.Reader, sample.MediaMTX} {
		if !json.Valid([]byte(value)) {
			return fmt.Errorf("profile %q telemetry is not valid JSON", profile)
		}
	}
	if strings.TrimSpace(sample.CPU) == "[]" || strings.TrimSpace(sample.CPU) == "null" {
		return fmt.Errorf("profile %q telemetry has no process resource data", profile)
	}
	if err := validateFinalAcceptanceProcessTelemetry(sample.FFmpeg, "ffmpeg", sample.FFmpegPIDs); err != nil {
		return fmt.Errorf("profile %q ffmpeg telemetry: %w", profile, err)
	}
	if err := validateFinalAcceptanceProcessTelemetry(sample.Reader, "ffprobe", sample.ReaderPIDs); err != nil {
		return fmt.Errorf("profile %q reader telemetry: %w", profile, err)
	}
	if err := validateFinalAcceptanceProcessTelemetry(sample.MediaMTX, "mediamtx", sample.MediaMTXPIDs); err != nil {
		return fmt.Errorf("profile %q mediamtx telemetry: %w", profile, err)
	}
	return validateFinalAcceptanceCombinedProcessTelemetry(sample.CPU, sample.FFmpegPIDs, sample.ReaderPIDs, sample.MediaMTXPIDs)
}

func validateFinalAcceptanceCombinedProcessTelemetry(data string, ffmpegPIDs, readerPIDs, mediaMTXPIDs []int) error {
	expected := make(map[int]string, len(ffmpegPIDs)+len(readerPIDs)+len(mediaMTXPIDs))
	for name, pids := range map[string][]int{"ffmpeg": ffmpegPIDs, "ffprobe": readerPIDs, "mediamtx": mediaMTXPIDs} {
		for _, pid := range pids {
			if pid <= 0 || expected[pid] != "" {
				return errors.New("combined telemetry PID identity is invalid")
			}
			expected[pid] = name
		}
	}
	var processes []struct {
		ProcessName string `json:"ProcessName"`
		ID          int    `json:"Id"`
	}
	if err := json.Unmarshal([]byte(data), &processes); err != nil || len(processes) != len(expected) {
		return errors.New("combined telemetry process payload is incomplete or malformed")
	}
	for _, process := range processes {
		if !strings.EqualFold(process.ProcessName, expected[process.ID]) {
			return errors.New("combined telemetry includes an unexpected process")
		}
		delete(expected, process.ID)
	}
	if len(expected) != 0 {
		return errors.New("combined telemetry omits an expected process")
	}
	return nil
}

func validateFinalAcceptanceProcessTelemetry(data, processName string, pids []int) error {
	var processes []struct {
		ProcessName string `json:"ProcessName"`
		ID          int    `json:"Id"`
	}
	if err := json.Unmarshal([]byte(data), &processes); err != nil || len(processes) == 0 {
		return errors.New("process payload is empty or malformed")
	}
	declared := make(map[int]bool, len(pids))
	for _, pid := range pids {
		if pid <= 0 || declared[pid] {
			return errors.New("declared process PIDs are invalid")
		}
		declared[pid] = true
	}
	if len(processes) != len(declared) {
		return errors.New("process payload and declared PID count differ")
	}
	for _, process := range processes {
		if process.ID <= 0 || !strings.EqualFold(process.ProcessName, processName) || !declared[process.ID] {
			return errors.New("process payload does not identify the declared live process")
		}
		delete(declared, process.ID)
	}
	if len(declared) != 0 {
		return errors.New("declared process PID is absent from process payload")
	}
	return nil
}

func finalAcceptanceCaptureDuration(ctx context.Context, ffprobe, path string) (float64, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffprobe receiver capture duration: %w: %s", err, strings.TrimSpace(string(output)))
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return 0, fmt.Errorf("invalid ffprobe receiver capture duration %q", strings.TrimSpace(string(output)))
	}
	return duration, nil
}

func finalAcceptanceCapturePackets(ctx context.Context, ffprobe, path string) ([]rtspPacketEvidence, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, ffprobe, "-v", "error", "-show_packets", "-show_entries", "packet=stream_index,pts_time,dts_time", "-of", "compact=p=1:nk=0", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe receiver packet timeline: %w", err)
	}
	packets := make([]rtspPacketEvidence, 0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		values := parseFFprobeCompactLine(scanner.Text())
		if values["_kind"] != "packet" {
			continue
		}
		stream, streamErr := strconv.Atoi(values["stream_index"])
		pts, ptsErr := strconv.ParseFloat(values["pts_time"], 64)
		dts, dtsErr := strconv.ParseFloat(values["dts_time"], 64)
		if streamErr != nil || ptsErr != nil || dtsErr != nil {
			return nil, fmt.Errorf("invalid ffprobe receiver packet timeline: %q", scanner.Text())
		}
		packets = append(packets, rtspPacketEvidence{StreamIndex: stream, PTS: pts, DTS: dts})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ffprobe receiver packet timeline: %w", err)
	}
	return packets, nil
}

func validateFinalAcceptanceCaptureFiles(ctx context.Context, metrics finalAcceptanceMetrics) error {
	ffprobePath := ""
	for _, binary := range metrics.Binaries {
		if binary.Name == "ffprobe" {
			ffprobePath = binary.Path
			break
		}
	}
	if ffprobePath == "" {
		return errors.New("#20B ffprobe capture validation is unavailable")
	}
	for _, profile := range metrics.Profiles {
		info, err := os.Stat(profile.ReceiverEvidence)
		if err != nil || !info.Mode().IsRegular() || info.Size() != profile.ReceiverBytes || info.Size() <= 0 {
			return fmt.Errorf("profile %q receiver capture path is missing or changed", profile.Name)
		}
		digest, err := sha256File(profile.ReceiverEvidence)
		if err != nil || !strings.EqualFold(digest, profile.ReceiverSHA256) {
			return fmt.Errorf("profile %q receiver capture digest does not match", profile.Name)
		}
		observed, err := video.ObserveRadioAsset(ctx, ffprobePath, profile.ReceiverEvidence)
		if err != nil {
			return fmt.Errorf("profile %q receiver capture probe: %w", profile.Name, err)
		}
		if observed.Video.GOPFrames != profile.ObservedGOP || observed.Video.Codec != profile.ObservedCodec.VideoCodec || observed.Audio.Codec != profile.ObservedCodec.AudioCodec {
			return fmt.Errorf("profile %q receiver capture does not match recorded codec or GOP", profile.Name)
		}
		duration, err := finalAcceptanceCaptureDuration(ctx, ffprobePath, profile.ReceiverEvidence)
		if err != nil {
			return fmt.Errorf("profile %q receiver capture duration: %w", profile.Name, err)
		}
		if duration < finalAcceptanceMinimumDuration.Seconds() || duration < float64(metrics.Soak.PerProfileSecs) || math.Abs(duration-profile.ReceiverDurationSeconds) > finalAcceptanceDurationTolerance.Seconds() {
			return fmt.Errorf("profile %q receiver capture duration %.3fs does not align with declared %.3fs soak evidence", profile.Name, duration, profile.ReceiverDurationSeconds)
		}
	}
	return nil
}

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
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/playlist"
	"imagepadserver/internal/upnp"
	"imagepadserver/internal/video"
)

const (
	rtspAcceptanceTimeout      = 135 * time.Second
	rtspTransitionTimeout      = 12 * time.Second
	rtspMaxPacketGapMillis     = int64(5000)
	rtspMaxTransitionGapMillis = int64(5000)
)

type rtspCodecSignature struct {
	VideoCodec    string `json:"videoCodec"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	PixelFormat   string `json:"pixelFormat"`
	FrameRate     string `json:"frameRate"`
	AudioCodec    string `json:"audioCodec"`
	SampleRate    int    `json:"sampleRate"`
	AudioChannels int    `json:"audioChannels"`
}

type rtspPacketEvidence struct {
	ArrivalMillis int64   `json:"arrivalMillis"`
	StreamIndex   int     `json:"streamIndex"`
	PTS           float64 `json:"pts"`
	DTS           float64 `json:"dts"`
}

type rtspTransitionEvidence struct {
	Name               string `json:"name"`
	RequestedMillis    int64  `json:"requestedMillis"`
	FirstPacketMillis  int64  `json:"firstPacketMillis"`
	GapMillis          int64  `json:"gapMillis"`
	Path               string `json:"path"`
	IntentionalStop    bool   `json:"intentionalStop"`
	StopRequestedNanos int64  `json:"stopRequestedNanos"`
	ReaderExitedNanos  int64  `json:"readerExitedNanos"`
	RadioStoppedNanos  int64  `json:"radioStoppedNanos"`
}

type rtspAcceptanceTrace struct {
	Contract               rtspCodecSignature       `json:"contract"`
	ObservedSignatures     []rtspCodecSignature     `json:"observedSignatures"`
	Packets                []rtspPacketEvidence     `json:"packets"`
	Transitions            []rtspTransitionEvidence `json:"transitions"`
	Reconnects             int                      `json:"reconnects"`
	PathDisappearances     int                      `json:"pathDisappearances"`
	Errors                 []string                 `json:"errors"`
	MaxPacketGapMillis     int64                    `json:"maxPacketGapMillis"`
	MaxTransitionGapMillis int64                    `json:"maxTransitionGapMillis"`
}

type rtspAcceptanceFaultFixture struct {
	Name  string              `json:"name"`
	Trace rtspAcceptanceTrace `json:"trace"`
}

type rtspBinaryEvidence struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type rtspMappingEvidence struct {
	Action       string `json:"action"`
	Protocol     string `json:"protocol"`
	InternalPort int    `json:"internalPort"`
	ExternalPort int    `json:"externalPort"`
	AtMillis     int64  `json:"atMillis"`
}

type rtspCleanupEvidence struct {
	ReaderExited          bool `json:"readerExited"`
	RadioStopped          bool `json:"radioStopped"`
	MappingsCreated       int  `json:"mappingsCreated"`
	MappingsClosed        int  `json:"mappingsClosed"`
	TrackedFFmpegKilled   int  `json:"trackedFfmpegKilled"`
	TrackedMediaMTXKilled int  `json:"trackedMediaMtxKilled"`
}

type rtspBaselineMetrics struct {
	SchemaVersion  int                   `json:"schemaVersion"`
	Gate           string                `json:"gate"`
	OutputMode     string                `json:"outputMode"`
	Status         string                `json:"status"`
	Accepted       bool                  `json:"accepted"`
	StartedAt      time.Time             `json:"startedAt"`
	FinishedAt     time.Time             `json:"finishedAt"`
	DurationMillis int64                 `json:"durationMillis"`
	Binaries       []rtspBinaryEvidence  `json:"binaries"`
	StreamContract rtspCodecSignature    `json:"streamContract"`
	PacketCount    int                   `json:"packetCount"`
	StreamCount    int                   `json:"streamCount"`
	Trace          rtspAcceptanceTrace   `json:"trace"`
	ReaderStderr   string                `json:"readerStderr"`
	ServerErrors   []string              `json:"serverErrors"`
	Mappings       []rtspMappingEvidence `json:"mappingLifecycle"`
	Cleanup        rtspCleanupEvidence   `json:"cleanup"`
	BlockedReason  string                `json:"blockedReason,omitempty"`
}

type rtspAcceptanceTimeline struct {
	mu      sync.Mutex
	started time.Time
	last    int64
}

func newRTSPAcceptanceTimeline(started time.Time) *rtspAcceptanceTimeline {
	return &rtspAcceptanceTimeline{started: started}
}

func (t *rtspAcceptanceTimeline) mark(at time.Time) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	marker := at.Sub(t.started).Nanoseconds()
	if marker <= t.last {
		marker = t.last + 1
	}
	t.last = marker
	return marker
}

func TestMusicRadioRTSPAcceptanceFaultFixturesAreRejected(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "music_radio_rtsp_acceptance_faults.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []rtspAcceptanceFaultFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 9 {
		t.Fatalf("fault fixture count = %d, want 9", len(fixtures))
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			err := validateRTSPAcceptanceTrace(fixture.Trace)
			if err == nil {
				t.Fatalf("fault %q was accepted", fixture.Name)
			}
			if !strings.Contains(err.Error(), fixture.Name) {
				t.Fatalf("fault %q produced non-specific rejection: %v", fixture.Name, err)
			}
		})
	}
}

func TestMusicRadioRTSPAcceptanceValidFixtureIsAccepted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "music_radio_rtsp_acceptance_valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var trace rtspAcceptanceTrace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatal(err)
	}
	if err := validateRTSPAcceptanceTrace(trace); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
}

func TestMusicRadioRTSPAcceptanceRejectsMissingMediaObservations(t *testing.T) {
	contract := rtspCodecSignature{VideoCodec: "h264", Width: 640, Height: 360, PixelFormat: "yuv420p", FrameRate: "30/1", AudioCodec: "aac", SampleRate: 48000, AudioChannels: 2}
	base := rtspAcceptanceTrace{
		Contract: contract,
		Packets: []rtspPacketEvidence{
			{ArrivalMillis: 0, StreamIndex: 0, PTS: 0, DTS: 0},
			{ArrivalMillis: 33, StreamIndex: 0, PTS: 0.033, DTS: 0.033},
		},
		MaxPacketGapMillis: rtspMaxPacketGapMillis, MaxTransitionGapMillis: rtspMaxTransitionGapMillis,
	}
	tests := []struct {
		name      string
		signature rtspCodecSignature
		want      string
	}{
		{name: "missing audio", signature: rtspCodecSignature{VideoCodec: "h264", Width: 640, Height: 360, PixelFormat: "yuv420p", FrameRate: "30/1"}, want: "missing audio"},
		{name: "missing video", signature: rtspCodecSignature{AudioCodec: "aac", SampleRate: 48000, AudioChannels: 2}, want: "missing video"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace := base
			trace.ObservedSignatures = []rtspCodecSignature{test.signature}
			err := validateRTSPAcceptanceTrace(trace)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q rejection", err, test.want)
			}
		})
	}
}

func TestMusicRadioRTSPAcceptanceRejectsTransitionOnlyTimeout(t *testing.T) {
	contract := rtspCodecSignature{VideoCodec: "h264", Width: 640, Height: 360, PixelFormat: "yuv420p", FrameRate: "30/1", AudioCodec: "aac", SampleRate: 48000, AudioChannels: 2}
	trace := rtspAcceptanceTrace{
		Contract: contract, ObservedSignatures: []rtspCodecSignature{contract},
		Packets: []rtspPacketEvidence{
			{ArrivalMillis: 0, StreamIndex: 0, PTS: 0, DTS: 0},
			{ArrivalMillis: 6001, StreamIndex: 0, PTS: 0.033, DTS: 0.033},
		},
		Transitions:        []rtspTransitionEvidence{{Name: "pause", RequestedMillis: 1, FirstPacketMillis: 6001, GapMillis: 6001, Path: "radio_test"}},
		MaxPacketGapMillis: rtspMaxPacketGapMillis, MaxTransitionGapMillis: rtspMaxTransitionGapMillis,
	}
	err := validateRTSPAcceptanceTrace(trace)
	if err == nil || !strings.Contains(err.Error(), "transition timeout") {
		t.Fatalf("error = %v, want transition timeout rejection", err)
	}
}

func TestRTSPAcceptanceUsesBackendLoopbackWhenPublicRTSPIsUnavailable(t *testing.T) {
	status := obsrtmp.RadioStatus{
		Running:    true,
		RTSPReady:  true,
		HLSReady:   true,
		Path:       "radio_acceptance",
		RTSPURL:    "",
		RTSPPublic: false,
	}
	endpoint := obsrtmp.RTSPEndpoint{
		Path:       "radio_acceptance",
		BackendURL: "rtsp://127.0.0.1:18554/radio_acceptance",
	}

	got, err := rtspAcceptanceReaderURL(status, endpoint)
	if err != nil {
		t.Fatalf("select direct RTSP reader URL: %v", err)
	}
	if want := endpoint.BackendURL; got != want {
		t.Fatalf("reader URL = %q, want backend loopback %q", got, want)
	}
}

func TestRTSPAcceptanceTimelinePreservesCausalOrderWithinOneClockTick(t *testing.T) {
	started := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	timeline := newRTSPAcceptanceTimeline(started)
	at := started.Add(10 * time.Millisecond)
	requested := timeline.mark(at)
	stopped := timeline.mark(at)

	if stopped != requested+1 {
		t.Fatalf("same-tick causal markers = %d, %d; want consecutive nanoseconds", requested, stopped)
	}
}

func TestValidateRTSPPromotionMetricsRejectsBlockedRecord(t *testing.T) {
	metrics := rtspBaselineMetrics{Gate: "#20A", Status: "blocked", Accepted: false}
	if err := validateRTSPPromotionMetrics(metrics); err == nil {
		t.Fatal("blocked metrics record was accepted for promotion")
	}
	metrics = completeRTSPPromotionMetrics(t)
	if err := validateRTSPPromotionMetrics(metrics); err != nil {
		t.Fatalf("accepted metrics record rejected: %v", err)
	}
}

func TestValidateRTSPPromotionMetricsFileIsDeterministic(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "music_radio_rtsp_acceptance_valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var trace rtspAcceptanceTrace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "metrics.json")
	blocked := rtspBaselineMetrics{SchemaVersion: 1, Gate: "#20A", Status: "blocked", Accepted: false, BlockedReason: "fixture blocked"}
	if err := writeRTSPBaselineMetrics(path, blocked); err != nil {
		t.Fatal(err)
	}
	if err := validateRTSPPromotionMetricsFile(path); err == nil {
		t.Fatal("blocked metrics file was accepted")
	}
	accepted := completeRTSPPromotionMetrics(t)
	if err := writeRTSPBaselineMetrics(path, accepted); err != nil {
		t.Fatal(err)
	}
	probe := recordedRTSPVersionProbe(accepted.Binaries)
	if err := validateRTSPPromotionMetricsFileWithProbe(path, probe); err != nil {
		t.Fatalf("accepted metrics file rejected: %v", err)
	}
}

func TestValidateRTSPPromotionMetricsFileRejectsPreStopTerminalEvidence(t *testing.T) {
	metrics := completeRTSPPromotionMetrics(t)
	data, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	transitions := root["trace"].(map[string]any)["transitions"].([]any)
	stop := transitions[len(transitions)-1].(map[string]any)
	stop["readerExitedNanos"] = float64(599000000)
	stop["radioStoppedNanos"] = float64(599000000)
	data, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pre-stop-terminal-evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateRTSPPromotionMetricsFileWithProbe(path, recordedRTSPVersionProbe(metrics.Binaries)); err == nil {
		t.Fatal("pre-stop terminal evidence was accepted")
	}
}

func completeRTSPPromotionMetrics(t *testing.T) rtspBaselineMetrics {
	t.Helper()
	contract := rtspCodecSignature{VideoCodec: "h264", Width: 640, Height: 360, PixelFormat: "yuv420p", FrameRate: "30/1", AudioCodec: "aac", SampleRate: 48000, AudioChannels: 2}
	packets := []rtspPacketEvidence{
		{ArrivalMillis: 0, StreamIndex: 0, PTS: 0, DTS: 0},
		{ArrivalMillis: 10, StreamIndex: 1, PTS: 0, DTS: 0},
		{ArrivalMillis: 90, StreamIndex: 0, PTS: 0.090, DTS: 0.090},
		{ArrivalMillis: 120, StreamIndex: 1, PTS: 0.110, DTS: 0.110},
		{ArrivalMillis: 190, StreamIndex: 0, PTS: 0.190, DTS: 0.190},
		{ArrivalMillis: 220, StreamIndex: 1, PTS: 0.210, DTS: 0.210},
		{ArrivalMillis: 290, StreamIndex: 0, PTS: 0.290, DTS: 0.290},
		{ArrivalMillis: 320, StreamIndex: 1, PTS: 0.310, DTS: 0.310},
		{ArrivalMillis: 390, StreamIndex: 0, PTS: 0.390, DTS: 0.390},
		{ArrivalMillis: 420, StreamIndex: 1, PTS: 0.410, DTS: 0.410},
		{ArrivalMillis: 490, StreamIndex: 0, PTS: 0.490, DTS: 0.490},
		{ArrivalMillis: 520, StreamIndex: 1, PTS: 0.510, DTS: 0.510},
		{ArrivalMillis: 590, StreamIndex: 0, PTS: 0.590, DTS: 0.590},
		{ArrivalMillis: 620, StreamIndex: 1, PTS: 0.610, DTS: 0.610},
	}
	transitions := []rtspTransitionEvidence{
		{Name: "fallback-to-track", RequestedMillis: 100, FirstPacketMillis: 120, GapMillis: 30, Path: "radio_fixture"},
		{Name: "pause", RequestedMillis: 200, FirstPacketMillis: 220, GapMillis: 30, Path: "radio_fixture"},
		{Name: "resume", RequestedMillis: 300, FirstPacketMillis: 320, GapMillis: 30, Path: "radio_fixture"},
		{Name: "seek", RequestedMillis: 400, FirstPacketMillis: 420, GapMillis: 30, Path: "radio_fixture"},
		{Name: "next", RequestedMillis: 500, FirstPacketMillis: 520, GapMillis: 30, Path: "radio_fixture"},
		{Name: "stop", RequestedMillis: 600, Path: "radio_fixture", IntentionalStop: true, StopRequestedNanos: 600000000, ReaderExitedNanos: 630000000, RadioStoppedNanos: 610000000},
	}
	trace := rtspAcceptanceTrace{
		Contract: contract, ObservedSignatures: []rtspCodecSignature{contract}, Packets: packets, Transitions: transitions,
		Errors:             []string{},
		MaxPacketGapMillis: rtspMaxPacketGapMillis, MaxTransitionGapMillis: rtspMaxTransitionGapMillis,
	}
	started := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	base := t.TempDir()
	binaryPath := func(name string) (string, string) {
		t.Helper()
		path := filepath.Join(base, name+".exe")
		content := []byte("pinned " + name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		return path, hex.EncodeToString(sum[:])
	}
	mediaMTXPath, mediaMTXSHA := binaryPath("mediamtx")
	ffmpegPath, ffmpegSHA := binaryPath("ffmpeg")
	ffprobePath, ffprobeSHA := binaryPath("ffprobe")
	binaries := []rtspBinaryEvidence{
		{Name: "mediamtx", Path: mediaMTXPath, Version: "v1.19.2", SHA256: mediaMTXSHA},
		{Name: "ffmpeg", Path: ffmpegPath, Version: "ffmpeg version 8.1.1", SHA256: ffmpegSHA},
		{Name: "ffprobe", Path: ffprobePath, Version: "ffprobe version 8.1.1", SHA256: ffprobeSHA},
	}
	mappings := []rtspMappingEvidence{
		{Action: "create", Protocol: "TCP", InternalPort: 8554, ExternalPort: 8554, AtMillis: 50},
		{Action: "create", Protocol: "UDP", InternalPort: 8000, ExternalPort: 8000, AtMillis: 51},
		{Action: "create", Protocol: "UDP", InternalPort: 8001, ExternalPort: 8001, AtMillis: 52},
		{Action: "close", Protocol: "TCP", InternalPort: 8554, ExternalPort: 8554, AtMillis: 700},
		{Action: "close", Protocol: "UDP", InternalPort: 8000, ExternalPort: 8000, AtMillis: 701},
		{Action: "close", Protocol: "UDP", InternalPort: 8001, ExternalPort: 8001, AtMillis: 702},
	}
	return rtspBaselineMetrics{
		SchemaVersion: 1, Gate: "#20A", OutputMode: string(obsrtmp.RadioOutputModeCompatibilityCopy), Status: "accepted", Accepted: true,
		StartedAt: started, FinishedAt: started.Add(time.Second), DurationMillis: 1000,
		Binaries: binaries, StreamContract: contract, PacketCount: len(packets), StreamCount: 2, Trace: trace,
		Mappings:     mappings,
		Cleanup:      rtspCleanupEvidence{ReaderExited: true, RadioStopped: true, MappingsCreated: 3, MappingsClosed: 3},
		ReaderStderr: "", ServerErrors: []string{},
	}
}

func TestMusicRadioRTSPPromotionGateRejectsAcceptedIncompleteArtifact(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "music_radio_rtsp_acceptance_valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var trace rtspAcceptanceTrace
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatal(err)
	}
	metrics := rtspBaselineMetrics{
		SchemaVersion:  1,
		Gate:           "#20A",
		Status:         "accepted",
		Accepted:       true,
		StreamContract: trace.Contract,
		Trace:          trace,
	}
	path := filepath.Join(t.TempDir(), "accepted-but-incomplete.json")
	if err := writeRTSPBaselineMetrics(path, metrics); err != nil {
		t.Fatal(err)
	}
	if err := validateRTSPPromotionMetricsFile(path); err == nil {
		t.Fatal("accepted-but-incomplete metrics artifact passed the promotion gate")
	}
}

func TestValidateRTSPPromotionMetricsRequiresAuthoritativeEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*rtspBaselineMetrics)
	}{
		{name: "program output", mutate: func(m *rtspBaselineMetrics) { m.OutputMode = string(obsrtmp.RadioOutputModeProgram) }},
		{name: "relative binary path", mutate: func(m *rtspBaselineMetrics) { m.Binaries[0].Path = "mediamtx.exe" }},
		{name: "empty binary version", mutate: func(m *rtspBaselineMetrics) { m.Binaries[1].Version = "" }},
		{name: "empty binary sha", mutate: func(m *rtspBaselineMetrics) { m.Binaries[2].SHA256 = "" }},
		{name: "binary sha mismatch", mutate: func(m *rtspBaselineMetrics) { m.Binaries[0].SHA256 = strings.Repeat("f", 64) }},
		{name: "duration mismatch", mutate: func(m *rtspBaselineMetrics) { m.DurationMillis++ }},
		{name: "reader not exited", mutate: func(m *rtspBaselineMetrics) { m.Cleanup.ReaderExited = false }},
		{name: "radio not stopped", mutate: func(m *rtspBaselineMetrics) { m.Cleanup.RadioStopped = false }},
		{name: "cleanup intervention", mutate: func(m *rtspBaselineMetrics) { m.Cleanup.TrackedFFmpegKilled = 1 }},
		{name: "mapping lifecycle incomplete", mutate: func(m *rtspBaselineMetrics) { m.Mappings = m.Mappings[:len(m.Mappings)-1] }},
		{name: "packet counter mismatch", mutate: func(m *rtspBaselineMetrics) { m.PacketCount++ }},
		{name: "stream counter mismatch", mutate: func(m *rtspBaselineMetrics) { m.StreamCount++ }},
		{name: "reconnect observed", mutate: func(m *rtspBaselineMetrics) { m.Trace.Reconnects = 1 }},
		{name: "path loss observed", mutate: func(m *rtspBaselineMetrics) { m.Trace.PathDisappearances = 1 }},
		{name: "reader stderr", mutate: func(m *rtspBaselineMetrics) { m.ReaderStderr = "reader error" }},
		{name: "server error", mutate: func(m *rtspBaselineMetrics) { m.ServerErrors = []string{"server error"} }},
		{name: "transition evidence missing", mutate: func(m *rtspBaselineMetrics) { m.Trace.Transitions = m.Trace.Transitions[:5] }},
		{name: "negative transition timestamp", mutate: func(m *rtspBaselineMetrics) { m.Trace.Transitions[0].RequestedMillis = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := completeRTSPPromotionMetrics(t)
			test.mutate(&metrics)
			if err := validateRTSPPromotionMetrics(metrics); err == nil {
				t.Fatalf("accepted metrics passed without authoritative %s evidence", test.name)
			}
		})
	}
}

func TestInvalidateRTSPMetricsRemovesStaleAcceptedArtifactBeforeWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := writeRTSPBaselineMetrics(path, completeRTSPPromotionMetrics(t)); err != nil {
		t.Fatal(err)
	}
	writeFailure := func(string, rtspBaselineMetrics) error { return errors.New("forced write failure") }
	if err := invalidateRTSPMetricsTarget(path, time.Now().UTC(), writeFailure); err == nil {
		t.Fatal("invalidation write failure was ignored")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale accepted artifact survived invalidation failure: %v", err)
	}
	if err := validateRTSPPromotionMetricsFile(path); err == nil {
		t.Fatal("missing artifact promoted after invalidation failure")
	}
}

func TestPersistRTSPBlockedEvidenceReportsWriteFailure(t *testing.T) {
	writeFailure := func(string, rtspBaselineMetrics) error { return errors.New("forced write failure") }
	err := persistRTSPBlockedEvidence(filepath.Join(t.TempDir(), "metrics.json"), time.Now().UTC(), "blocked", writeFailure)
	if err == nil {
		t.Fatal("blocked evidence write failure was ignored")
	}
}

func TestRTSPPromotionJSONRequiresEveryAuthoritativeField(t *testing.T) {
	data, err := json.Marshal(completeRTSPPromotionMetrics(t))
	if err != nil {
		t.Fatal(err)
	}
	paths := [][]string{
		{"schemaVersion"}, {"gate"}, {"outputMode"}, {"status"}, {"accepted"}, {"startedAt"}, {"finishedAt"}, {"durationMillis"},
		{"binaries"}, {"streamContract"}, {"packetCount"}, {"streamCount"}, {"trace"}, {"readerStderr"},
		{"serverErrors"}, {"mappingLifecycle"}, {"cleanup"},
		{"trace", "errors"}, {"trace", "reconnects"}, {"trace", "pathDisappearances"}, {"trace", "packets"},
		{"trace", "transitions"}, {"trace", "observedSignatures"}, {"trace", "maxPacketGapMillis"}, {"trace", "maxTransitionGapMillis"},
	}
	for _, path := range paths {
		t.Run(strings.Join(path, "."), func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			deleteRTSPRawField(raw, path...)
			mutated, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateRTSPPromotionJSONPresence(mutated); err == nil {
				t.Fatalf("promotion JSON passed without %s", strings.Join(path, "."))
			}
		})
	}
}

func TestRTSPPromotionRejectsForgedTransitionWithoutPacketEvidence(t *testing.T) {
	metrics := completeRTSPPromotionMetrics(t)
	metrics.Trace.Packets = metrics.Trace.Packets[:2]
	metrics.PacketCount = len(metrics.Trace.Packets)
	metrics.StreamCount = countRTSPPacketStreams(metrics.Trace.Packets)
	if err := validateRTSPPromotionMetrics(metrics); err == nil {
		t.Fatal("forged successful transitions passed without packets at or after their request timestamps")
	}
}

func TestRTSPPromotionRejectsForgedRecordedToolVersion(t *testing.T) {
	metrics := completeRTSPPromotionMetrics(t)
	metrics.Binaries[0].Version = "forged version"
	probe := func(context.Context, string, string) (string, error) { return "v1.19.2", nil }
	if err := validateRTSPPromotionToolVersions(context.Background(), metrics.Binaries, probe); err == nil {
		t.Fatal("forged recorded tool version passed live version cross-check")
	}
}

func deleteRTSPRawField(root map[string]any, path ...string) {
	current := root
	for _, part := range path[:len(path)-1] {
		next, _ := current[part].(map[string]any)
		current = next
	}
	delete(current, path[len(path)-1])
}

func recordedRTSPVersionProbe(binaries []rtspBinaryEvidence) rtspToolVersionProbe {
	versions := make(map[string]string, len(binaries))
	for _, binary := range binaries {
		versions[binary.Name+"\x00"+binary.Path] = binary.Version
	}
	return func(_ context.Context, name, path string) (string, error) {
		version, ok := versions[name+"\x00"+path]
		if !ok {
			return "", fmt.Errorf("unexpected version probe %s %s", name, path)
		}
		return version, nil
	}
}

func TestParseRTSPToolVersionRejectsEmptyOutput(t *testing.T) {
	for _, output := range [][]byte{nil, []byte(""), []byte(" \r\n\t")} {
		if _, err := parseRTSPToolVersion("ffmpeg", output); err == nil {
			t.Fatalf("empty version output %q was accepted", output)
		}
	}
	version, err := parseRTSPToolVersion("ffmpeg", []byte("ffmpeg version 8.1.1\nconfiguration"))
	if err != nil || version != "ffmpeg version 8.1.1" {
		t.Fatalf("version = %q, err = %v", version, err)
	}
}

func validateRTSPAcceptanceTrace(trace rtspAcceptanceTrace) error {
	if trace.MaxPacketGapMillis != rtspMaxPacketGapMillis || trace.MaxTransitionGapMillis != rtspMaxTransitionGapMillis {
		return fmt.Errorf("altered timeout limit: packet=%dms transition=%dms, want %dms/%dms", trace.MaxPacketGapMillis, trace.MaxTransitionGapMillis, rtspMaxPacketGapMillis, rtspMaxTransitionGapMillis)
	}
	if len(trace.ObservedSignatures) == 0 {
		return errors.New("signature change: no observed codec signature")
	}
	for _, signature := range trace.ObservedSignatures {
		if signature.VideoCodec == "" {
			return errors.New("missing video observation")
		}
		if signature.AudioCodec == "" {
			return errors.New("missing audio observation")
		}
		if signature != trace.Contract {
			return fmt.Errorf("signature change: got %+v, want %+v", signature, trace.Contract)
		}
	}
	if trace.Reconnects != 0 {
		return fmt.Errorf("reconnect: observed %d reconnects", trace.Reconnects)
	}
	if trace.PathDisappearances != 0 {
		return fmt.Errorf("disappearance: observed %d pre-stop path losses", trace.PathDisappearances)
	}
	if len(trace.Errors) != 0 {
		return fmt.Errorf("stderr error: %s", strings.Join(trace.Errors, "; "))
	}
	if len(trace.Packets) < 2 {
		return errors.New("timeout: fewer than two packets")
	}
	for _, transition := range trace.Transitions {
		if transition.Name == "stop" {
			if !transition.IntentionalStop {
				return errors.New("stop transition is not marked intentional")
			}
			if transition.FirstPacketMillis != 0 || transition.GapMillis != 0 {
				return errors.New("stop transition must not claim post-stop packet evidence")
			}
			if transition.StopRequestedNanos <= 0 || transition.StopRequestedNanos/int64(time.Millisecond) != transition.RequestedMillis || transition.ReaderExitedNanos <= transition.StopRequestedNanos || transition.RadioStoppedNanos <= transition.StopRequestedNanos {
				return errors.New("stop terminal evidence predates the stop request")
			}
			continue
		}
		if transition.IntentionalStop || transition.StopRequestedNanos != 0 || transition.ReaderExitedNanos != 0 || transition.RadioStoppedNanos != 0 {
			return fmt.Errorf("transition %s contains stop-only terminal evidence", transition.Name)
		}
		firstPacket, gap, err := deriveRTSPTransitionPacketEvidence(trace.Packets, transition.RequestedMillis)
		if err != nil {
			return fmt.Errorf("transition %s: %w", transition.Name, err)
		}
		if transition.FirstPacketMillis != firstPacket || transition.GapMillis != gap {
			return fmt.Errorf("forged transition evidence: %s claimed first/gap=%d/%d, observed=%d/%d", transition.Name, transition.FirstPacketMillis, transition.GapMillis, firstPacket, gap)
		}
		requestGap := firstPacket - transition.RequestedMillis
		if requestGap > rtspMaxTransitionGapMillis || gap > rtspMaxTransitionGapMillis {
			return fmt.Errorf("transition timeout: %s request=%dms continuity=%dms exceeds %dms", transition.Name, requestGap, gap, rtspMaxTransitionGapMillis)
		}
	}
	lastPTS := map[int]float64{}
	lastDTS := map[int]float64{}
	seen := map[int]bool{}
	for i, packet := range trace.Packets {
		if packet.ArrivalMillis < 0 || math.IsNaN(packet.PTS) || math.IsInf(packet.PTS, 0) || math.IsNaN(packet.DTS) || math.IsInf(packet.DTS, 0) {
			return fmt.Errorf("invalid packet evidence at index %d", i)
		}
		if seen[packet.StreamIndex] {
			if packet.PTS < lastPTS[packet.StreamIndex] || packet.DTS < lastDTS[packet.StreamIndex] {
				return fmt.Errorf("nonmonotonic timestamp: packet %d stream %d pts/dts %.6f/%.6f after %.6f/%.6f", i, packet.StreamIndex, packet.PTS, packet.DTS, lastPTS[packet.StreamIndex], lastDTS[packet.StreamIndex])
			}
		}
		if i > 0 {
			if packet.ArrivalMillis < trace.Packets[i-1].ArrivalMillis {
				return fmt.Errorf("nonmonotonic packet arrival at index %d", i)
			}
			gap := packet.ArrivalMillis - trace.Packets[i-1].ArrivalMillis
			if gap > rtspMaxPacketGapMillis {
				return fmt.Errorf("packet timeout: gap %dms exceeds %dms", gap, rtspMaxPacketGapMillis)
			}
		}
		seen[packet.StreamIndex] = true
		lastPTS[packet.StreamIndex] = packet.PTS
		lastDTS[packet.StreamIndex] = packet.DTS
	}
	return nil
}

func deriveRTSPTransitionPacketEvidence(packets []rtspPacketEvidence, requestedMillis int64) (firstPacketMillis, gapMillis int64, err error) {
	if requestedMillis < 0 {
		return 0, 0, errors.New("negative transition request timestamp")
	}
	for i, packet := range packets {
		if packet.ArrivalMillis < requestedMillis {
			continue
		}
		if i == 0 {
			return 0, 0, errors.New("no preceding packet for transition gap")
		}
		return packet.ArrivalMillis, packet.ArrivalMillis - packets[i-1].ArrivalMillis, nil
	}
	return 0, 0, errors.New("no packet at or after transition request")
}

func validateRTSPPromotionMetrics(metrics rtspBaselineMetrics) error {
	if metrics.SchemaVersion != 1 {
		return fmt.Errorf("#20A metrics schemaVersion = %d, want 1", metrics.SchemaVersion)
	}
	if metrics.Gate != "#20A" {
		return fmt.Errorf("metrics gate = %q, want #20A", metrics.Gate)
	}
	if metrics.OutputMode != string(obsrtmp.RadioOutputModeCompatibilityCopy) {
		return fmt.Errorf("#20A copy promotion requires outputMode=%q, got %q", obsrtmp.RadioOutputModeCompatibilityCopy, metrics.OutputMode)
	}
	if metrics.Status != "accepted" || !metrics.Accepted {
		return fmt.Errorf("#20A promotion blocked: status=%q accepted=%v reason=%q", metrics.Status, metrics.Accepted, metrics.BlockedReason)
	}
	if strings.TrimSpace(metrics.BlockedReason) != "" {
		return errors.New("accepted #20A metrics contain a blocked reason")
	}
	if metrics.StartedAt.IsZero() || metrics.FinishedAt.IsZero() || metrics.FinishedAt.Before(metrics.StartedAt) {
		return errors.New("#20A run timestamps are missing or invalid")
	}
	duration := metrics.FinishedAt.Sub(metrics.StartedAt).Milliseconds()
	if metrics.DurationMillis <= 0 || metrics.DurationMillis != duration || metrics.DurationMillis >= rtspAcceptanceTimeout.Milliseconds() {
		return fmt.Errorf("#20A duration evidence is invalid: recorded=%dms timestamps=%dms", metrics.DurationMillis, duration)
	}
	if err := validateRTSPBinaryEvidence(metrics.Binaries); err != nil {
		return err
	}
	if strings.TrimSpace(metrics.ReaderStderr) != "" || len(metrics.ServerErrors) != 0 {
		return errors.New("#20A metrics contain reader or server errors")
	}
	if metrics.StreamContract != metrics.Trace.Contract {
		return errors.New("#20A stream contract does not match trace contract")
	}
	if err := validateRTSPAcceptanceTrace(metrics.Trace); err != nil {
		return fmt.Errorf("#20A trace rejected: %w", err)
	}
	streamCount := countRTSPPacketStreams(metrics.Trace.Packets)
	if metrics.PacketCount != len(metrics.Trace.Packets) || metrics.PacketCount < 2 {
		return fmt.Errorf("#20A packet count mismatch: recorded=%d observed=%d", metrics.PacketCount, len(metrics.Trace.Packets))
	}
	if metrics.StreamCount != streamCount || streamCount < 2 {
		return fmt.Errorf("#20A stream count mismatch: recorded=%d observed=%d", metrics.StreamCount, streamCount)
	}
	if last := metrics.Trace.Packets[len(metrics.Trace.Packets)-1].ArrivalMillis; last > metrics.DurationMillis {
		return fmt.Errorf("packet evidence exceeds run duration: %dms > %dms", last, metrics.DurationMillis)
	}
	if err := validateRTSPPromotionTransitions(metrics.Trace.Transitions, metrics.DurationMillis); err != nil {
		return err
	}
	if err := validateRTSPPromotionCleanup(metrics.Cleanup, metrics.Mappings, metrics.DurationMillis); err != nil {
		return err
	}
	return nil
}

func validateRTSPBinaryEvidence(binaries []rtspBinaryEvidence) error {
	want := map[string]bool{"mediamtx": false, "ffmpeg": false, "ffprobe": false}
	if len(binaries) != len(want) {
		return fmt.Errorf("#20A binary evidence count = %d, want %d", len(binaries), len(want))
	}
	for _, binary := range binaries {
		if _, ok := want[binary.Name]; !ok || want[binary.Name] {
			return fmt.Errorf("unexpected or duplicate binary evidence %q", binary.Name)
		}
		if !filepath.IsAbs(binary.Path) || strings.TrimSpace(binary.Version) == "" {
			return fmt.Errorf("binary %s is missing absolute path or version", binary.Name)
		}
		digest, err := hex.DecodeString(binary.SHA256)
		if err != nil || len(digest) != sha256.Size {
			return fmt.Errorf("binary %s has invalid SHA-256", binary.Name)
		}
		info, err := os.Stat(binary.Path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("binary %s path is not a local regular file", binary.Name)
		}
		actual, err := sha256File(binary.Path)
		if err != nil || !strings.EqualFold(actual, binary.SHA256) {
			return fmt.Errorf("binary %s SHA-256 does not match the pinned file", binary.Name)
		}
		want[binary.Name] = true
	}
	return nil
}

func countRTSPPacketStreams(packets []rtspPacketEvidence) int {
	streams := make(map[int]struct{})
	for _, packet := range packets {
		streams[packet.StreamIndex] = struct{}{}
	}
	return len(streams)
}

func validateRTSPPromotionTransitions(transitions []rtspTransitionEvidence, durationMillis int64) error {
	want := []string{"fallback-to-track", "pause", "resume", "seek", "next", "stop"}
	if len(transitions) != len(want) {
		return fmt.Errorf("#20A transition evidence count = %d, want %d", len(transitions), len(want))
	}
	path := ""
	lastRequested := int64(-1)
	for i, transition := range transitions {
		if transition.Name != want[i] {
			return fmt.Errorf("transition %d = %q, want %q", i, transition.Name, want[i])
		}
		if transition.Path == "" || (path != "" && transition.Path != path) {
			return fmt.Errorf("transition %s has missing or changed RTSP path", transition.Name)
		}
		path = transition.Path
		if transition.RequestedMillis < 0 || transition.RequestedMillis < lastRequested || transition.RequestedMillis > durationMillis {
			return fmt.Errorf("transition %s request timestamp is outside run duration", transition.Name)
		}
		lastRequested = transition.RequestedMillis
		if transition.Name == "stop" {
			if !transition.IntentionalStop {
				return errors.New("stop transition is not marked intentional")
			}
			durationNanos := durationMillis * int64(time.Millisecond)
			if transition.FirstPacketMillis != 0 || transition.GapMillis != 0 || transition.StopRequestedNanos <= 0 || transition.StopRequestedNanos/int64(time.Millisecond) != transition.RequestedMillis || transition.ReaderExitedNanos <= transition.StopRequestedNanos || transition.RadioStoppedNanos <= transition.StopRequestedNanos || transition.ReaderExitedNanos > durationNanos || transition.RadioStoppedNanos > durationNanos {
				return errors.New("stop terminal evidence is missing, pre-request, or outside run duration")
			}
			continue
		}
		if transition.IntentionalStop || transition.FirstPacketMillis > durationMillis || transition.StopRequestedNanos != 0 || transition.ReaderExitedNanos != 0 || transition.RadioStoppedNanos != 0 {
			return fmt.Errorf("transition %s evidence is invalid", transition.Name)
		}
	}
	return nil
}

func validateRTSPPromotionCleanup(cleanup rtspCleanupEvidence, mappings []rtspMappingEvidence, durationMillis int64) error {
	if !cleanup.ReaderExited || !cleanup.RadioStopped || cleanup.TrackedFFmpegKilled != 0 || cleanup.TrackedMediaMTXKilled != 0 {
		return fmt.Errorf("#20A process cleanup is incomplete: %+v", cleanup)
	}
	if cleanup.MappingsCreated <= 0 || cleanup.MappingsCreated != cleanup.MappingsClosed || len(mappings) != cleanup.MappingsCreated+cleanup.MappingsClosed {
		return fmt.Errorf("#20A mapping counts are incomplete: created=%d closed=%d events=%d", cleanup.MappingsCreated, cleanup.MappingsClosed, len(mappings))
	}
	created := make(map[string]bool, cleanup.MappingsCreated)
	closed := make(map[string]bool, cleanup.MappingsClosed)
	lastAt := int64(-1)
	for _, event := range mappings {
		if event.AtMillis < lastAt || event.AtMillis < 0 || event.AtMillis > durationMillis || event.Protocol == "" || event.InternalPort <= 0 || event.ExternalPort <= 0 {
			return fmt.Errorf("invalid mapping lifecycle event: %+v", event)
		}
		lastAt = event.AtMillis
		key := fmt.Sprintf("%s/%d/%d", event.Protocol, event.InternalPort, event.ExternalPort)
		switch event.Action {
		case "create":
			if created[key] {
				return fmt.Errorf("duplicate mapping create: %s", key)
			}
			created[key] = true
		case "close":
			if !created[key] || closed[key] {
				return fmt.Errorf("unpaired mapping close: %s", key)
			}
			closed[key] = true
		default:
			return fmt.Errorf("unknown mapping action %q", event.Action)
		}
	}
	if len(created) != cleanup.MappingsCreated || len(closed) != cleanup.MappingsClosed {
		return errors.New("mapping lifecycle does not match cleanup counters")
	}
	return nil
}

type rtspToolVersionProbe func(context.Context, string, string) (string, error)

func validateRTSPPromotionMetricsFile(path string) error {
	return validateRTSPPromotionMetricsFileWithProbe(path, probeRTSPToolVersion)
}

func validateRTSPPromotionMetricsFileWithProbe(path string, probe rtspToolVersionProbe) error {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("IMAGEPAD_RTSP_ACCEPTANCE_OUTPUT must be an absolute metrics path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read #20A metrics: %w", err)
	}
	var metrics rtspBaselineMetrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return fmt.Errorf("decode #20A metrics: %w", err)
	}
	if metrics.Status != "accepted" || !metrics.Accepted {
		return validateRTSPPromotionMetrics(metrics)
	}
	if err := validateRTSPPromotionJSONPresence(data); err != nil {
		return err
	}
	if err := validateRTSPPromotionMetrics(metrics); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return validateRTSPPromotionToolVersions(ctx, metrics.Binaries, probe)
}

func validateRTSPPromotionToolVersions(ctx context.Context, binaries []rtspBinaryEvidence, probe rtspToolVersionProbe) error {
	for _, binary := range binaries {
		version, err := probe(ctx, binary.Name, binary.Path)
		if err != nil {
			return fmt.Errorf("rerun pinned %s version: %w", binary.Name, err)
		}
		if version != binary.Version {
			return fmt.Errorf("pinned %s version mismatch: recorded=%q observed=%q", binary.Name, binary.Version, version)
		}
	}
	return nil
}

func probeRTSPToolVersion(ctx context.Context, name, path string) (string, error) {
	args := []string{"-version"}
	if name == "mediamtx" {
		args = []string{"--version"}
	}
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, path, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return parseRTSPToolVersion(name, output)
}

func validateRTSPPromotionJSONPresence(data []byte) error {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("decode #20A metrics schema: %w", err)
	}
	if err := requireRTSPJSONFields(root, "metrics", "schemaVersion", "gate", "outputMode", "status", "accepted", "startedAt", "finishedAt", "durationMillis", "binaries", "streamContract", "packetCount", "streamCount", "trace", "readerStderr", "serverErrors", "mappingLifecycle", "cleanup"); err != nil {
		return err
	}
	binaries, err := requireRTSPJSONArray(root, "binaries", "metrics")
	if err != nil {
		return err
	}
	for i, item := range binaries {
		object, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("metrics.binaries[%d] must be an object", i)
		}
		if err := requireRTSPJSONFields(object, fmt.Sprintf("metrics.binaries[%d]", i), "name", "path", "version", "sha256"); err != nil {
			return err
		}
	}
	if err := requireRTSPCodecSignatureFields(root["streamContract"], "metrics.streamContract"); err != nil {
		return err
	}
	if _, err := requireRTSPJSONArray(root, "serverErrors", "metrics"); err != nil {
		return err
	}
	mappings, err := requireRTSPJSONArray(root, "mappingLifecycle", "metrics")
	if err != nil {
		return err
	}
	for i, item := range mappings {
		object, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("metrics.mappingLifecycle[%d] must be an object", i)
		}
		if err := requireRTSPJSONFields(object, fmt.Sprintf("metrics.mappingLifecycle[%d]", i), "action", "protocol", "internalPort", "externalPort", "atMillis"); err != nil {
			return err
		}
	}
	cleanup, ok := root["cleanup"].(map[string]any)
	if !ok {
		return errors.New("metrics.cleanup must be an object")
	}
	if err := requireRTSPJSONFields(cleanup, "metrics.cleanup", "readerExited", "radioStopped", "mappingsCreated", "mappingsClosed", "trackedFfmpegKilled", "trackedMediaMtxKilled"); err != nil {
		return err
	}
	trace, ok := root["trace"].(map[string]any)
	if !ok {
		return errors.New("metrics.trace must be an object")
	}
	if err := requireRTSPJSONFields(trace, "metrics.trace", "contract", "observedSignatures", "packets", "transitions", "reconnects", "pathDisappearances", "errors", "maxPacketGapMillis", "maxTransitionGapMillis"); err != nil {
		return err
	}
	if err := requireRTSPCodecSignatureFields(trace["contract"], "metrics.trace.contract"); err != nil {
		return err
	}
	signatures, err := requireRTSPJSONArray(trace, "observedSignatures", "metrics.trace")
	if err != nil {
		return err
	}
	for i, signature := range signatures {
		if err := requireRTSPCodecSignatureFields(signature, fmt.Sprintf("metrics.trace.observedSignatures[%d]", i)); err != nil {
			return err
		}
	}
	packets, err := requireRTSPJSONArray(trace, "packets", "metrics.trace")
	if err != nil {
		return err
	}
	for i, item := range packets {
		object, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("metrics.trace.packets[%d] must be an object", i)
		}
		if err := requireRTSPJSONFields(object, fmt.Sprintf("metrics.trace.packets[%d]", i), "arrivalMillis", "streamIndex", "pts", "dts"); err != nil {
			return err
		}
	}
	transitions, err := requireRTSPJSONArray(trace, "transitions", "metrics.trace")
	if err != nil {
		return err
	}
	for i, item := range transitions {
		object, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("metrics.trace.transitions[%d] must be an object", i)
		}
		if err := requireRTSPJSONFields(object, fmt.Sprintf("metrics.trace.transitions[%d]", i), "name", "requestedMillis", "firstPacketMillis", "gapMillis", "path", "intentionalStop", "stopRequestedNanos", "readerExitedNanos", "radioStoppedNanos"); err != nil {
			return err
		}
	}
	if _, err := requireRTSPJSONArray(trace, "errors", "metrics.trace"); err != nil {
		return err
	}
	return nil
}

func requireRTSPJSONFields(object map[string]any, label string, fields ...string) error {
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return fmt.Errorf("mandatory JSON field %s.%s is missing", label, field)
		}
	}
	return nil
}

func requireRTSPJSONArray(object map[string]any, field, label string) ([]any, error) {
	value, ok := object[field]
	if !ok {
		return nil, fmt.Errorf("mandatory JSON field %s.%s is missing", label, field)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s.%s must be an explicit JSON array", label, field)
	}
	return items, nil
}

func requireRTSPCodecSignatureFields(value any, label string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", label)
	}
	return requireRTSPJSONFields(object, label, "videoCodec", "width", "height", "pixelFormat", "frameRate", "audioCodec", "sampleRate", "audioChannels")
}

type rtspMetricsWriter func(string, rtspBaselineMetrics) error

func invalidateRTSPMetricsTarget(path string, started time.Time, writer rtspMetricsWriter) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale #20A metrics: %w", err)
	}
	metrics := rtspBaselineMetrics{
		SchemaVersion: 1, Gate: "#20A", Status: "invalidated", Accepted: false,
		StartedAt: started, FinishedAt: time.Now().UTC(), BlockedReason: "producer has not completed",
	}
	if err := writer(path, metrics); err != nil {
		return fmt.Errorf("persist invalidated #20A metrics: %w", err)
	}
	return nil
}

func persistRTSPBlockedEvidence(path string, started time.Time, reason string, writer rtspMetricsWriter) error {
	metrics := rtspBaselineMetrics{
		SchemaVersion: 1, Gate: "#20A", Status: "blocked", Accepted: false,
		StartedAt: started, FinishedAt: time.Now().UTC(), BlockedReason: reason,
	}
	if err := writer(path, metrics); err != nil {
		return fmt.Errorf("persist blocked #20A metrics: %w", err)
	}
	return nil
}

func TestMusicRadioRTSPBaselineAcceptance(t *testing.T) {
	started := time.Now().UTC()
	outputPath := strings.TrimSpace(os.Getenv("IMAGEPAD_RTSP_ACCEPTANCE_OUTPUT"))
	if filepath.IsAbs(outputPath) {
		if err := invalidateRTSPMetricsTarget(outputPath, started, writeRTSPBaselineMetrics); err != nil {
			t.Fatalf("invalidate stale #20A metrics: %v", err)
		}
	}
	pins, blocked := pinnedRTSPAcceptanceBinaries()
	if os.Getenv("IMAGEPAD_RTSP_ACCEPTANCE") != "1" {
		blocked = append(blocked, "IMAGEPAD_RTSP_ACCEPTANCE=1 is required")
	}
	if outputPath == "" || !filepath.IsAbs(outputPath) {
		blocked = append(blocked, "IMAGEPAD_RTSP_ACCEPTANCE_OUTPUT must be an absolute path")
	}
	if len(blocked) != 0 {
		reason := strings.Join(blocked, "; ")
		if filepath.IsAbs(outputPath) {
			if err := persistRTSPBlockedEvidence(outputPath, started, reason, writeRTSPBaselineMetrics); err != nil {
				t.Fatalf("persist blocked #20A evidence: %v", err)
			}
		}
		t.Skipf("BLOCKED #19 PROMOTION GATE: %s", reason)
	}

	ctx, cancel := context.WithTimeout(context.Background(), rtspAcceptanceTimeout)
	defer cancel()
	binaries, err := inspectRTSPAcceptanceBinaries(ctx, pins)
	if err != nil {
		if err := persistRTSPBlockedEvidence(outputPath, started, err.Error(), writeRTSPBaselineMetrics); err != nil {
			t.Fatalf("persist binary-inspection block: %v", err)
		}
		t.Skipf("BLOCKED #19 PROMOTION GATE: %v", err)
	}

	metrics, runErr := runMusicRadioRTSPAcceptance(ctx, t, pins, binaries, started)
	metrics.FinishedAt = time.Now().UTC()
	metrics.DurationMillis = metrics.FinishedAt.Sub(started).Milliseconds()
	if runErr != nil {
		metrics.Status = "failed"
		metrics.Accepted = false
		metrics.ServerErrors = append(metrics.ServerErrors, runErr.Error())
	} else {
		metrics.Status = "accepted"
		metrics.Accepted = true
	}
	if err := writeRTSPBaselineMetrics(outputPath, metrics); err != nil {
		t.Fatalf("persist #20A metrics: %v", err)
	}
	if runErr != nil {
		t.Fatalf("#20A RTSP baseline rejected (metrics %s): %v", outputPath, runErr)
	}
	if metrics.DurationMillis >= rtspAcceptanceTimeout.Milliseconds() {
		t.Fatalf("acceptance exceeded timeout: %dms", metrics.DurationMillis)
	}
	t.Logf("#20A accepted; metrics=%s", outputPath)
}

func pinnedRTSPAcceptanceBinaries() (map[string]string, []string) {
	envs := []struct {
		name string
		env  string
	}{
		{name: "mediamtx", env: "IMAGEPAD_MEDIAMTX"},
		{name: "ffmpeg", env: "IMAGEPAD_FFMPEG"},
		{name: "ffprobe", env: "IMAGEPAD_FFPROBE"},
	}
	pins := make(map[string]string, len(envs))
	var blocked []string
	for _, item := range envs {
		path := strings.TrimSpace(os.Getenv(item.env))
		if path == "" {
			blocked = append(blocked, item.env+" is unset")
			continue
		}
		if !filepath.IsAbs(path) {
			blocked = append(blocked, item.env+" is not absolute")
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			blocked = append(blocked, item.env+" does not name a local regular file")
			continue
		}
		pins[item.name] = filepath.Clean(path)
	}
	return pins, blocked
}

func inspectRTSPAcceptanceBinaries(ctx context.Context, pins map[string]string) ([]rtspBinaryEvidence, error) {
	order := []string{"mediamtx", "ffmpeg", "ffprobe"}
	result := make([]rtspBinaryEvidence, 0, len(order))
	for _, name := range order {
		path := pins[name]
		sum, err := sha256File(path)
		if err != nil {
			return nil, fmt.Errorf("hash pinned %s: %w", name, err)
		}
		version, err := probeRTSPToolVersion(ctx, name, path)
		if err != nil {
			return nil, fmt.Errorf("pinned %s version: %w", name, err)
		}
		result = append(result, rtspBinaryEvidence{Name: name, Path: path, Version: version, SHA256: sum})
	}
	return result, nil
}

func parseRTSPToolVersion(name string, output []byte) (string, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "", fmt.Errorf("pinned %s version output is empty", name)
	}
	version := strings.TrimSpace(strings.SplitN(trimmed, "\n", 2)[0])
	if version == "" {
		return "", fmt.Errorf("pinned %s version output is empty", name)
	}
	return version, nil
}

func sha256File(path string) (string, error) {
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

func runMusicRadioRTSPAcceptance(ctx context.Context, t *testing.T, pins map[string]string, binaries []rtspBinaryEvidence, started time.Time) (metrics rtspBaselineMetrics, runErr error) {
	metrics = rtspBaselineMetrics{SchemaVersion: 1, Gate: "#20A", Status: "running", StartedAt: started, Binaries: binaries, ServerErrors: []string{}}
	timeline := newRTSPAcceptanceTimeline(started)
	for name, path := range map[string]string{"IMAGEPAD_MEDIAMTX": pins["mediamtx"], "IMAGEPAD_FFMPEG": pins["ffmpeg"], "IMAGEPAD_FFPROBE": pins["ffprobe"]} {
		t.Setenv(name, path)
	}

	trackDir := t.TempDir()
	trackA := filepath.Join(trackDir, "acceptance-a.ts")
	trackB := filepath.Join(trackDir, "acceptance-b.ts")
	contract := rtspCodecSignature{VideoCodec: "h264", Width: 640, Height: 360, PixelFormat: "yuv420p", FrameRate: "30/1", AudioCodec: "aac", SampleRate: 48000, AudioChannels: 2}
	metrics.StreamContract = contract
	if err := generateRTSPAcceptanceTrack(ctx, pins["ffmpeg"], trackA, 440); err != nil {
		return metrics, err
	}
	if err := generateRTSPAcceptanceTrack(ctx, pins["ffmpeg"], trackB, 660); err != nil {
		return metrics, err
	}

	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	if os.Getenv("IMAGEPAD_RTSP_PROGRAM") != "1" {
		srv.radio.SetOutputMode(func() obsrtmp.RadioOutputMode {
			return obsrtmp.RadioOutputModeCompatibilityCopy
		})
	}
	for name, path := range map[string]string{"IMAGEPAD_MEDIAMTX": pins["mediamtx"], "IMAGEPAD_FFMPEG": pins["ffmpeg"], "IMAGEPAD_FFPROBE": pins["ffprobe"]} {
		t.Setenv(name, path)
	}
	srv.activeCanonicalHeight = 360
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
	defer func() {
		srv.radio.Stop(8 * time.Second)
	}()

	if err := postRTSPAcceptance(mux, "/api/music/playlist/play", `{}`); err != nil {
		return metrics, err
	}
	status, err := waitRTSPAcceptanceState(ctx, srv, func(status obsrtmp.RadioStatus) bool {
		return status.Running && status.RTSPReady && status.Path != ""
	})
	if err != nil {
		return metrics, fmt.Errorf("fallback readiness: %w", err)
	}
	if status.ActiveSession == nil {
		return metrics, errors.New("fallback readiness did not expose an active session contract")
	}
	metrics.OutputMode = string(status.ActiveSession.OutputMode)
	path := status.Path
	var endpoint obsrtmp.RTSPEndpoint
	select {
	case endpoint = <-readyEndpoints:
	case <-ctx.Done():
		return metrics, fmt.Errorf("capture backend RTSP endpoint: %w", ctx.Err())
	}
	readerURL, err := rtspAcceptanceReaderURL(status, endpoint)
	if err != nil {
		return metrics, err
	}
	reader, err := startRTSPAcceptanceReader(ctx, pins["ffprobe"], readerURL, started)
	if err != nil {
		return metrics, err
	}
	defer reader.killAndWait()
	if _, err := reader.waitForPacketCount(ctx, 40); err != nil {
		return metrics, fmt.Errorf("fallback packets: %w", err)
	}
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	pathLosses, monitorDone := monitorRTSPAcceptancePath(monitorCtx, srv, path)
	defer func() {
		stopMonitor()
		<-monitorDone
	}()

	a := srv.musicQueue.Add(playlist.Track{Title: "Acceptance A", Status: playlist.TrackReady, MediaPath: trackA, DurationSeconds: 24})
	b := srv.musicQueue.Add(playlist.Track{Title: "Acceptance B", Status: playlist.TrackReady, MediaPath: trackB, DurationSeconds: 24})
	transitions := make([]rtspTransitionEvidence, 0, 6)
	transition := func(name string, action func() error, predicate func(obsrtmp.RadioStatus) bool) error {
		beforeCount, _ := reader.packetPosition()
		requested := time.Now()
		requestedMillis := requested.Sub(started).Milliseconds()
		if err := action(); err != nil {
			return fmt.Errorf("%s action: %w", name, err)
		}
		current, err := waitRTSPAcceptanceState(ctx, srv, predicate)
		if err != nil {
			return fmt.Errorf("%s state: %w", name, err)
		}
		if current.Path != path || !current.RTSPReady {
			return fmt.Errorf("%s path changed/lost: %q ready=%v, want %q", name, current.Path, current.RTSPReady, path)
		}
		if reader.exited() {
			return fmt.Errorf("%s reader exited before stop", name)
		}
		if _, err := reader.waitForFirstPacketAfter(ctx, beforeCount, requested, 20); err != nil {
			return fmt.Errorf("%s packets: %w", name, err)
		}
		firstPacketMillis, gapMillis, err := reader.deriveTransitionEvidence(requestedMillis)
		if err != nil {
			return fmt.Errorf("%s evidence: %w", name, err)
		}
		transitions = append(transitions, rtspTransitionEvidence{Name: name, RequestedMillis: requestedMillis, FirstPacketMillis: firstPacketMillis, GapMillis: gapMillis, Path: current.Path})
		if current.LastError != "" {
			metrics.ServerErrors = append(metrics.ServerErrors, name+": "+current.LastError)
		}
		return nil
	}

	if err := transition("fallback-to-track", func() error { srv.radio.Wake(); return nil }, func(status obsrtmp.RadioStatus) bool { return status.CurrentTrackID == a.ID }); err != nil {
		return metrics, err
	}
	if err := transition("pause", func() error { return postRTSPAcceptance(mux, "/api/music/playlist/pause", `{}`) }, func(status obsrtmp.RadioStatus) bool {
		srv.musicPendingMu.Lock()
		defer srv.musicPendingMu.Unlock()
		return srv.musicPaused && status.CurrentTrackID == ""
	}); err != nil {
		return metrics, err
	}
	if err := transition("resume", func() error { return postRTSPAcceptance(mux, "/api/music/playlist/play", `{}`) }, func(status obsrtmp.RadioStatus) bool { return status.CurrentTrackID == a.ID }); err != nil {
		return metrics, err
	}
	if err := transition("seek", func() error { return postRTSPAcceptance(mux, "/api/music/playlist/seek", `{"seconds":4}`) }, func(status obsrtmp.RadioStatus) bool {
		return status.CurrentTrackID == a.ID && status.BaseOffsetSeconds == 4
	}); err != nil {
		return metrics, err
	}
	if err := transition("next", func() error { return postRTSPAcceptance(mux, "/api/music/playlist/next", `{}`) }, func(status obsrtmp.RadioStatus) bool { return status.CurrentTrackID == b.ID }); err != nil {
		return metrics, err
	}

	stopMonitor()
	<-monitorDone
	stopRequested, err := postRTSPAcceptanceAt(mux, "/api/music/playlist/stop", `{}`)
	if err != nil {
		return metrics, err
	}
	stopRequestedNanos := timeline.mark(stopRequested)
	stopped, err := waitRTSPAcceptanceState(ctx, srv, func(status obsrtmp.RadioStatus) bool {
		return !status.Running && status.Phase == obsrtmp.RadioPhaseStopped && !status.StoppedAt.IsZero()
	})
	if err != nil {
		return metrics, fmt.Errorf("stop state: %w", err)
	}
	readerExitedAt, readerExited := reader.waitForExit(10 * time.Second)
	if !readerExited {
		return metrics, errors.New("reader did not exit after intentional stop")
	}
	if readerExitedAt.Before(stopRequested) || stopped.StoppedAt.Before(stopRequested) {
		return metrics, fmt.Errorf("stop terminal evidence was observed before the stop request: requested=%s reader=%s radio=%s", stopRequested.Format(time.RFC3339Nano), readerExitedAt.Format(time.RFC3339Nano), stopped.StoppedAt.Format(time.RFC3339Nano))
	}
	radioStoppedNanos := timeline.mark(stopped.StoppedAt)
	readerExitedNanos := timeline.mark(readerExitedAt)
	transitions = append(transitions, rtspTransitionEvidence{
		Name: "stop", RequestedMillis: stopRequestedNanos / int64(time.Millisecond), Path: path, IntentionalStop: true,
		StopRequestedNanos: stopRequestedNanos,
		ReaderExitedNanos:  readerExitedNanos,
		RadioStoppedNanos:  radioStoppedNanos,
	})

	packets, signatures, stderr := reader.snapshot()
	traceErrors := append([]string{}, metrics.ServerErrors...)
	if strings.TrimSpace(stderr) != "" {
		traceErrors = append(traceErrors, strings.TrimSpace(stderr))
	}
	trace := rtspAcceptanceTrace{
		Contract: contract, ObservedSignatures: signatures, Packets: packets, Transitions: transitions,
		Reconnects: strings.Count(strings.ToLower(stderr), "reconnect"), PathDisappearances: int(pathLosses.Load()), Errors: traceErrors,
		MaxPacketGapMillis: rtspMaxPacketGapMillis, MaxTransitionGapMillis: rtspMaxTransitionGapMillis,
	}
	metrics.Trace = trace
	metrics.PacketCount = len(packets)
	metrics.StreamCount = countRTSPPacketStreams(packets)
	metrics.ReaderStderr = stderr
	metrics.Mappings = mappings.snapshot()
	metrics.Cleanup.ReaderExited = readerExited
	metrics.Cleanup.RadioStopped = !srv.radio.Running()
	metrics.Cleanup.MappingsCreated, metrics.Cleanup.MappingsClosed = mappings.counts()
	if err := mappings.waitClosed(ctx); err != nil {
		return metrics, err
	}
	metrics.Mappings = mappings.snapshot()
	metrics.Cleanup.MappingsCreated, metrics.Cleanup.MappingsClosed = mappings.counts()

	ffmpegKilled, ffmpegErr := video.CleanupTrackedFFmpeg()
	mediaMTXKilled, mediaMTXErr := obsrtmp.CleanupStaleMediaMTX()
	metrics.Cleanup.TrackedFFmpegKilled = ffmpegKilled
	metrics.Cleanup.TrackedMediaMTXKilled = mediaMTXKilled
	if ffmpegErr != nil || mediaMTXErr != nil || ffmpegKilled != 0 || mediaMTXKilled != 0 {
		return metrics, fmt.Errorf("process cleanup required intervention: ffmpeg=%d/%v mediamtx=%d/%v", ffmpegKilled, ffmpegErr, mediaMTXKilled, mediaMTXErr)
	}
	if metrics.Cleanup.MappingsCreated == 0 || metrics.Cleanup.MappingsCreated != metrics.Cleanup.MappingsClosed {
		return metrics, fmt.Errorf("mapping lifecycle incomplete: created=%d closed=%d", metrics.Cleanup.MappingsCreated, metrics.Cleanup.MappingsClosed)
	}
	if !metrics.Cleanup.RadioStopped {
		return metrics, errors.New("radio still running after stop")
	}
	if err := validateRTSPAcceptanceTrace(trace); err != nil {
		return metrics, err
	}
	return metrics, nil
}

func generateRTSPAcceptanceTrack(ctx context.Context, ffmpeg, output string, frequency int) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=24",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000:duration=24", frequency),
		"-map", "0:v:0", "-map", "1:a:0", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-pix_fmt", "yuv420p", "-r", "30", "-g", "30", "-keyint_min", "30", "-sc_threshold", "0", "-bf", "0",
		"-b:v", "900k", "-maxrate", "1100k", "-bufsize", "2200k", "-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2",
		"-f", "mpegts", output,
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("generate %s: %w: %s", filepath.Base(output), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func postRTSPAcceptance(mux *http.ServeMux, path, body string) error {
	_, err := postRTSPAcceptanceAt(mux, path, body)
	return err
}

func postRTSPAcceptanceAt(mux *http.ServeMux, path, body string) (time.Time, error) {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	requested := time.Now()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return requested, fmt.Errorf("%s = %d: %s", path, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	return requested, nil
}

func waitRTSPAcceptanceState(ctx context.Context, srv *Server, predicate func(obsrtmp.RadioStatus) bool) (obsrtmp.RadioStatus, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(rtspTransitionTimeout)
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

func rtspAcceptanceReaderURL(status obsrtmp.RadioStatus, endpoint obsrtmp.RTSPEndpoint) (string, error) {
	if !status.Running || !status.RTSPReady || status.Path == "" {
		return "", errors.New("radio path is not ready for a direct RTSP reader")
	}
	if endpoint.Path != status.Path {
		return "", fmt.Errorf("captured RTSP path = %q, want %q", endpoint.Path, status.Path)
	}
	backendURL := strings.TrimSpace(endpoint.BackendURL)
	parsed, err := url.Parse(backendURL)
	if err != nil || parsed.Scheme != "rtsp" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || strings.TrimPrefix(parsed.EscapedPath(), "/") != status.Path {
		return "", fmt.Errorf("invalid backend loopback RTSP endpoint %q", backendURL)
	}
	return backendURL, nil
}

func monitorRTSPAcceptancePath(ctx context.Context, srv *Server, path string) (*atomic.Int64, <-chan struct{}) {
	losses := &atomic.Int64{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		lost := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				status := srv.radio.Status()
				missing := !status.RTSPReady || status.Path != path
				if missing && !lost {
					losses.Add(1)
				}
				lost = missing
			}
		}
	}()
	return losses, done
}

type rtspAcceptanceReader struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	started  time.Time
	packets  []rtspPacketEvidence
	streams  map[int]map[string]string
	frames   []map[string]string
	stderr   bytes.Buffer
	notify   chan struct{}
	done     chan error
	scanDone chan struct{}
	exitedAt time.Time
}

func startRTSPAcceptanceReader(ctx context.Context, ffprobe, url string, started time.Time) (*rtspAcceptanceReader, error) {
	reader := &rtspAcceptanceReader{started: started, streams: map[int]map[string]string{}, notify: make(chan struct{}, 1), done: make(chan error, 1), scanDone: make(chan struct{})}
	args := []string{
		"-v", "error", "-rtsp_transport", "tcp", "-rw_timeout", "10000000", "-i", url,
		"-show_packets", "-show_frames", "-show_streams",
		"-show_entries", "packet=stream_index,pts_time,dts_time:frame=media_type,width,height,pix_fmt,sample_rate,channels:stream=index,codec_type,codec_name,width,height,pix_fmt,r_frame_rate,sample_rate,channels",
		"-of", "compact=p=1:nk=0",
	}
	reader.cmd = exec.CommandContext(ctx, ffprobe, args...)
	stdout, err := reader.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	reader.cmd.Stderr = &reader.stderr
	if err := reader.cmd.Start(); err != nil {
		return nil, fmt.Errorf("start RTSP/TCP reader: %w", err)
	}
	go reader.scan(stdout)
	go func() {
		err := reader.cmd.Wait()
		reader.mu.Lock()
		reader.exitedAt = time.Now()
		reader.mu.Unlock()
		reader.done <- err
		close(reader.done)
	}()
	return reader, nil
}

func (r *rtspAcceptanceReader) scan(stdout io.Reader) {
	defer close(r.scanDone)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		values := parseFFprobeCompactLine(scanner.Text())
		kind := values["_kind"]
		r.mu.Lock()
		switch kind {
		case "packet":
			stream, streamErr := strconv.Atoi(values["stream_index"])
			pts, ptsErr := strconv.ParseFloat(values["pts_time"], 64)
			dts, dtsErr := strconv.ParseFloat(values["dts_time"], 64)
			if streamErr == nil && ptsErr == nil && dtsErr == nil {
				r.packets = append(r.packets, rtspPacketEvidence{ArrivalMillis: time.Since(r.started).Milliseconds(), StreamIndex: stream, PTS: pts, DTS: dts})
			}
		case "stream":
			if index, err := strconv.Atoi(values["index"]); err == nil {
				r.streams[index] = values
			}
		case "frame":
			r.frames = append(r.frames, values)
		}
		r.mu.Unlock()
		select {
		case r.notify <- struct{}{}:
		default:
		}
	}
}

func parseFFprobeCompactLine(line string) map[string]string {
	parts := strings.Split(line, "|")
	values := map[string]string{}
	if len(parts) != 0 {
		values["_kind"] = parts[0]
	}
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func (r *rtspAcceptanceReader) packetPosition() (int, rtspPacketEvidence) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.packets) == 0 {
		return 0, rtspPacketEvidence{}
	}
	return len(r.packets), r.packets[len(r.packets)-1]
}

func (r *rtspAcceptanceReader) deriveTransitionEvidence(requestedMillis int64) (int64, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return deriveRTSPTransitionPacketEvidence(r.packets, requestedMillis)
}

func (r *rtspAcceptanceReader) waitForPacketCount(ctx context.Context, count int) (rtspPacketEvidence, error) {
	deadline := time.NewTimer(rtspTransitionTimeout)
	defer deadline.Stop()
	for {
		r.mu.Lock()
		if len(r.packets) >= count {
			packet := r.packets[count-1]
			r.mu.Unlock()
			return packet, nil
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return rtspPacketEvidence{}, ctx.Err()
		case <-deadline.C:
			return rtspPacketEvidence{}, fmt.Errorf("packet timeout waiting for count %d", count)
		case <-r.done:
			return rtspPacketEvidence{}, errors.New("reader exited")
		case <-r.notify:
		}
	}
}

func (r *rtspAcceptanceReader) waitForFirstPacketAfter(ctx context.Context, previousCount int, requested time.Time, additional int) (rtspPacketEvidence, error) {
	if _, err := r.waitForPacketCount(ctx, previousCount+additional); err != nil {
		return rtspPacketEvidence{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	requestedMillis := requested.Sub(r.started).Milliseconds()
	for _, packet := range r.packets[previousCount:] {
		if packet.ArrivalMillis >= requestedMillis {
			return packet, nil
		}
	}
	return rtspPacketEvidence{}, errors.New("no packet arrived after transition request")
}

func (r *rtspAcceptanceReader) exited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *rtspAcceptanceReader) waitForExit(timeout time.Duration) (time.Time, bool) {
	deadline := time.After(timeout)
	select {
	case <-r.done:
	case <-deadline:
		return time.Time{}, false
	}
	select {
	case <-r.scanDone:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.exitedAt, !r.exitedAt.IsZero()
	case <-deadline:
		return time.Time{}, false
	}
}

func (r *rtspAcceptanceReader) killAndWait() {
	if r.cmd != nil && r.cmd.Process != nil && !r.exited() {
		_ = r.cmd.Process.Kill()
		_, _ = r.waitForExit(3 * time.Second)
	}
}

func (r *rtspAcceptanceReader) snapshot() ([]rtspPacketEvidence, []rtspCodecSignature, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	packets := append([]rtspPacketEvidence(nil), r.packets...)
	variants := map[rtspCodecSignature]struct{}{}
	var base rtspCodecSignature
	for _, stream := range r.streams {
		switch stream["codec_type"] {
		case "video":
			base.VideoCodec = stream["codec_name"]
			base.Width, _ = strconv.Atoi(stream["width"])
			base.Height, _ = strconv.Atoi(stream["height"])
			base.PixelFormat = stream["pix_fmt"]
			base.FrameRate = stream["r_frame_rate"]
		case "audio":
			base.AudioCodec = stream["codec_name"]
			base.SampleRate, _ = strconv.Atoi(stream["sample_rate"])
			base.AudioChannels, _ = strconv.Atoi(stream["channels"])
		}
	}
	variants[base] = struct{}{}
	for _, frame := range r.frames {
		candidate := base
		switch frame["media_type"] {
		case "video":
			candidate.Width = acceptancePositiveInt(frame["width"], candidate.Width)
			candidate.Height = acceptancePositiveInt(frame["height"], candidate.Height)
			if frame["pix_fmt"] != "" {
				candidate.PixelFormat = frame["pix_fmt"]
			}
		case "audio":
			candidate.SampleRate = acceptancePositiveInt(frame["sample_rate"], candidate.SampleRate)
			candidate.AudioChannels = acceptancePositiveInt(frame["channels"], candidate.AudioChannels)
		}
		variants[candidate] = struct{}{}
	}
	signatures := make([]rtspCodecSignature, 0, len(variants))
	for signature := range variants {
		signatures = append(signatures, signature)
	}
	return packets, signatures, r.stderr.String()
}

func acceptancePositiveInt(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

type rtspAcceptanceMappingRecorder struct {
	mu      sync.Mutex
	started time.Time
	events  []rtspMappingEvidence
	created int
	closed  int
}

type rtspAcceptanceMapping struct {
	recorder *rtspAcceptanceMappingRecorder
	protocol string
	internal int
	external int
	closed   atomic.Bool
}

func newRTSPAcceptanceMappingRecorder(started time.Time) *rtspAcceptanceMappingRecorder {
	return &rtspAcceptanceMappingRecorder{started: started}
}

func (r *rtspAcceptanceMappingRecorder) mapPort(protocol string, internalPort, externalPort int, _ string) (rtspMappingHandle, upnp.Result) {
	mapping := &rtspAcceptanceMapping{recorder: r, protocol: protocol, internal: internalPort, external: externalPort}
	r.mu.Lock()
	r.created++
	r.events = append(r.events, rtspMappingEvidence{Action: "create", Protocol: protocol, InternalPort: internalPort, ExternalPort: externalPort, AtMillis: time.Since(r.started).Milliseconds()})
	r.mu.Unlock()
	return mapping, upnp.Result{OK: true, ExternalIP: "127.0.0.1"}
}

func (m *rtspAcceptanceMapping) ExternalIP() string { return "127.0.0.1" }
func (m *rtspAcceptanceMapping) ExternalPort() int  { return m.external }
func (m *rtspAcceptanceMapping) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}
	m.recorder.mu.Lock()
	m.recorder.closed++
	m.recorder.events = append(m.recorder.events, rtspMappingEvidence{Action: "close", Protocol: m.protocol, InternalPort: m.internal, ExternalPort: m.external, AtMillis: time.Since(m.recorder.started).Milliseconds()})
	m.recorder.mu.Unlock()
	return nil
}

func (r *rtspAcceptanceMappingRecorder) snapshot() []rtspMappingEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rtspMappingEvidence(nil), r.events...)
}

func (r *rtspAcceptanceMappingRecorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.created, r.closed
}

func (r *rtspAcceptanceMappingRecorder) waitClosed(ctx context.Context) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		created, closed := r.counts()
		if created > 0 && created == closed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("mapping close timeout: created=%d closed=%d", created, closed)
		case <-ticker.C:
		}
	}
}

func writeRTSPBaselineMetrics(path string, metrics rtspBaselineMetrics) error {
	if !filepath.IsAbs(path) {
		return errors.New("metrics path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rtsp-baseline-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

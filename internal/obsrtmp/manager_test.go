package obsrtmp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestFFmpegArgsUseNormalHLS(t *testing.T) {
	manager := newTestManager(t, LatencyModeHLS)
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	wantValues := map[string]string{
		"-hls_time":      "1",
		"-hls_list_size": "8",
		"-g":             "30",
		"-keyint_min":    "30",
		"-preset":        "ultrafast",
		"-tune":          "zerolatency",
	}
	for flag, want := range wantValues {
		if got := valueAfter(args, flag); got != want {
			t.Fatalf("%s = %q, want %q\nargs: %s", flag, got, want, strings.Join(args, " "))
		}
	}

	if !slices.Contains(args, "independent_segments+program_date_time") {
		t.Fatalf("expected stable HLS playlist flags in args: %s", strings.Join(args, " "))
	}
	if slices.Contains(args, "-hls_delete_threshold") || strings.Contains(strings.Join(args, " "), "delete_segments") {
		t.Fatalf("HLS output must not delete live segments while VRChat may still retry them: %s", strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{"-c:v", "libx264"}) {
		t.Fatalf("expected HLS output to be re-encoded with libx264: %s", strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{video.PlaylistName("media123"), "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart", "recording.mp4"}) {
		t.Fatalf("expected recording output to remain stream-copy MP4 after HLS output: %s", strings.Join(args, " "))
	}
}

func TestFFmpegArgsUseInjectedHardwareEncoder(t *testing.T) {
	manager := newTestManager(t, LatencyModeHLS)
	profile := video.NewVideoEncoderProfile("h264_nvenc", video.EncoderLowLatency)
	args := manager.ffmpegArgsWithEncoder("media123", "recording.mp4", video.ResolveQuality("720", 0), profile)

	if !containsSubsequence(args, []string{"-c:v", "h264_nvenc"}) {
		t.Fatalf("expected injected NVENC encoder: %s", strings.Join(args, " "))
	}
	if got := valueAfter(args, "-preset"); got != "p1" {
		t.Fatalf("preset = %q, want p1: %s", got, strings.Join(args, " "))
	}
	if got := valueAfter(args, "-tune"); got != "ull" {
		t.Fatalf("tune = %q, want ull: %s", got, strings.Join(args, " "))
	}
	if slices.Contains(args, "libx264") {
		t.Fatalf("hardware args must not include libx264: %s", strings.Join(args, " "))
	}
}

func TestFFmpegArgsUseAutoLatencyProfile(t *testing.T) {
	manager := newTestManager(t, "auto")
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	if got := valueAfter(args, "-hls_time"); got != "1" {
		t.Fatalf("hls_time = %q, want 1\nargs: %s", got, strings.Join(args, " "))
	}
	if got := valueAfter(args, "-hls_list_size"); got != "8" {
		t.Fatalf("hls_list_size = %q, want 8\nargs: %s", got, strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{"-c:v", "libx264"}) {
		t.Fatalf("expected auto alias to re-encode HLS for predictable segments: %s", strings.Join(args, " "))
	}
}

func TestFFmpegArgsUseHighestQualityHLSProfile(t *testing.T) {
	manager := newTestManager(t, LatencyModeHLSHigh)
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	wantValues := map[string]string{
		"-hls_time":      "4",
		"-hls_list_size": "6",
		"-g":             "120",
		"-keyint_min":    "120",
	}
	for flag, want := range wantValues {
		if got := valueAfter(args, flag); got != want {
			t.Fatalf("%s = %q, want %q\nargs: %s", flag, got, want, strings.Join(args, " "))
		}
	}
}

func TestFFmpegArgsUseDVRListSize(t *testing.T) {
	manager := New(t.TempDir(), "127.0.0.1", 1935, "secret", nil, func() LatencyProfile {
		return EnableDVR(NormalizeLatencyProfile(LatencyModeHLS))
	}, Callbacks{})
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	if got := valueAfter(args, "-hls_time"); got != "1" {
		t.Fatalf("hls_time = %q, want 1\nargs: %s", got, strings.Join(args, " "))
	}
	if got := valueAfter(args, "-hls_list_size"); got != "1800" {
		t.Fatalf("hls_list_size = %q, want 1800\nargs: %s", got, strings.Join(args, " "))
	}
}

func TestNormalizeLatencyModeAndProfile(t *testing.T) {
	cases := []struct {
		name           string
		input          string
		wantMode       string
		wantLabel      string
		wantMultiplier int
	}{
		{name: "highest hls", input: LatencyModeHLSHigh, wantMode: LatencyModeHLSHigh, wantLabel: "最高画質HLS（遅延増）", wantMultiplier: 1},
		{name: "normal hls", input: LatencyModeHLS, wantMode: LatencyModeHLS, wantLabel: "高画質HLS（通常遅延）", wantMultiplier: 1},
		{name: "low rtsp", input: LatencyModeRTSPLow, wantMode: LatencyModeRTSPLow, wantLabel: "低遅延RTSP", wantMultiplier: 1},
		{name: "ultra rtsp", input: LatencyModeRTSPUltra, wantMode: LatencyModeRTSPUltra, wantLabel: "超低遅延RTSP", wantMultiplier: 2},
		{name: "realtime rtsp", input: LatencyModeRTSPRealtime, wantMode: LatencyModeRTSPRealtime, wantLabel: "リアルタイムRTSP", wantMultiplier: 0},
		{name: "legacy auto", input: "  AUTO  ", wantMode: LatencyModeHLS, wantLabel: "高画質HLS（通常遅延）", wantMultiplier: 1},
		{name: "legacy normal", input: "normal", wantMode: LatencyModeHLS, wantLabel: "高画質HLS（通常遅延）", wantMultiplier: 1},
		{name: "legacy low", input: "low", wantMode: LatencyModeRTSPLow, wantLabel: "低遅延RTSP", wantMultiplier: 1},
		{name: "legacy ultra", input: "ultra", wantMode: LatencyModeRTSPUltra, wantLabel: "超低遅延RTSP", wantMultiplier: 2},
		{name: "legacy lhls", input: "lhls", wantMode: LatencyModeRTSPLow, wantLabel: "低遅延RTSP", wantMultiplier: 1},
		{name: "legacy llhls", input: "llhls", wantMode: LatencyModeRTSPUltra, wantLabel: "超低遅延RTSP", wantMultiplier: 2},
		{name: "legacy rtspt", input: "rtspt", wantMode: LatencyModeRTSPRealtime, wantLabel: "リアルタイムRTSP", wantMultiplier: 0},
		{name: "unknown", input: "not-a-mode", wantMode: LatencyModeHLS, wantLabel: "高画質HLS（通常遅延）", wantMultiplier: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeLatencyMode(tc.input); got != tc.wantMode {
				t.Fatalf("NormalizeLatencyMode(%q) = %q, want %q", tc.input, got, tc.wantMode)
			}
			profile := NormalizeLatencyProfile(tc.input)
			if profile.Mode != tc.wantMode {
				t.Fatalf("NormalizeLatencyProfile(%q).Mode = %q, want %q", tc.input, profile.Mode, tc.wantMode)
			}
			if profile.Label != tc.wantLabel {
				t.Fatalf("NormalizeLatencyProfile(%q).Label = %q, want %q", tc.input, profile.Label, tc.wantLabel)
			}
			if profile.Experimental {
				t.Fatalf("NormalizeLatencyProfile(%q).Experimental = true, want false", tc.input)
			}
			if profile.BitrateMultiplier != tc.wantMultiplier {
				t.Fatalf("NormalizeLatencyProfile(%q).BitrateMultiplier = %d, want %d", tc.input, profile.BitrateMultiplier, tc.wantMultiplier)
			}
		})
	}
}

func TestLatencyCapabilitiesExposeFiveProductionModes(t *testing.T) {
	caps := LatencyCapabilities()
	want := []string{LatencyModeHLSHigh, LatencyModeHLS, LatencyModeRTSPLow, LatencyModeRTSPUltra, LatencyModeRTSPRealtime}
	if len(caps) != len(want) {
		t.Fatalf("capability count = %d, want %d: %#v", len(caps), len(want), caps)
	}
	for i, mode := range want {
		if caps[i].Mode != mode {
			t.Fatalf("capability[%d].Mode = %q, want %q", i, caps[i].Mode, mode)
		}
		if caps[i].Experimental {
			t.Fatalf("capability[%d] unexpectedly experimental: %#v", i, caps[i])
		}
	}
}

func TestScaledLatencyPresetAppliesStreamingBitratePolicy(t *testing.T) {
	base := video.ResolveQuality("1080", 0)
	tests := []struct {
		multiplier int
		wantVideo  string
		wantMax    string
		wantBuffer string
	}{
		{multiplier: 1, wantVideo: "4500k", wantMax: "5200k", wantBuffer: "9000k"},
		{multiplier: 2, wantVideo: "9000k", wantMax: "10400k", wantBuffer: "18000k"},
		{multiplier: 0, wantVideo: "2500k", wantMax: "3000k", wantBuffer: "5000k"},
	}
	for _, tc := range tests {
		got := scaledLatencyPreset(base, tc.multiplier)
		if got.VideoBitrate != tc.wantVideo || got.MaxRate != tc.wantMax || got.BufferSize != tc.wantBuffer {
			t.Fatalf("x%d preset = %s/%s/%s, want %s/%s/%s",
				tc.multiplier, got.VideoBitrate, got.MaxRate, got.BufferSize, tc.wantVideo, tc.wantMax, tc.wantBuffer)
		}
	}
}

func TestRealtimeRTSPUsesReceiverFriendlyBitrate(t *testing.T) {
	profile := NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	preset := scaledLatencyPreset(video.ResolveQuality("1080", 0), profile.BitrateMultiplier)
	if preset.Height > 720 {
		t.Fatalf("realtime RTSP height = %d, want <= 720", preset.Height)
	}
	if preset.VideoBitrate != "2500k" || preset.MaxRate != "3000k" || preset.BufferSize != "5000k" {
		t.Fatalf("realtime RTSP bitrate = %s/%s/%s, want 2500k/3000k/5000k",
			preset.VideoBitrate, preset.MaxRate, preset.BufferSize)
	}
}

func TestFFmpegRTSPArgsKeepAACNormalizationAtRTSPMuxer(t *testing.T) {
	manager := newTestManager(t, LatencyModeRTSPRealtime)
	args := strings.Join(manager.ffmpegRTSPArgs("media123", "recording.mp4", "rtsp://127.0.0.1:8554/obs", video.ResolveQuality("720", 0), video.CPUVideoEncoder(video.EncoderLowLatency)), " ")

	for _, want := range []string{"-c:a aac", "-b:a 128k", "-ar 48000", "-ac 2", "-rtsp_transport tcp", "-pkt_size 1200"} {
		if !strings.Contains(args, want) {
			t.Fatalf("OBS RTSP args missing receiver boundary option %q: %s", want, args)
		}
	}
	if strings.Contains(args, "-c:a copy -flush_packets") || strings.Contains(args, "-c:a copy -f rtsp") {
		t.Fatalf("OBS RTSP publisher must not copy arbitrary audio into RTSP: %s", args)
	}
}

func newTestManager(t *testing.T, latencyMode string) *Manager {
	t.Helper()
	return New(t.TempDir(), "127.0.0.1", 1935, "secret", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(latencyMode)
	}, Callbacks{})
}

func TestSetRTSPURLRejectsStaleSession(t *testing.T) {
	manager := newTestManager(t, "rtspt")
	manager.current = &Session{ID: "current"}
	manager.status.RTSPTURL = "rtsp://127.0.0.1:8554/current"

	if manager.SetRTSPURL("stale", "rtsp://8.8.8.8:8554/stale", "public") {
		t.Fatal("stale session update was accepted")
	}
	if got, want := manager.status.RTSPTURL, "rtsp://127.0.0.1:8554/current"; got != want {
		t.Fatalf("RTSPTURL = %q, want %q", got, want)
	}
	if !manager.SetRTSPURL("current", "rtsp://8.8.8.8:8554/current", "public") {
		t.Fatal("current session update was rejected")
	}
	if got, want := manager.status.RTSPTURL, "rtsp://8.8.8.8:8554/current"; got != want {
		t.Fatalf("RTSPTURL = %q, want %q", got, want)
	}
	if got, want := manager.status.Message, "public"; got != want {
		t.Fatalf("Message = %q, want %q", got, want)
	}
}

func TestStartPublishingReemitsReadyRTSPEndpoint(t *testing.T) {
	manager := newTestManager(t, "rtspt")
	endpoint := RTSPEndpoint{
		SessionID: "current",
		Host:      "192.168.1.20",
		Port:      49152,
		Path:      "obs_current",
		LocalURL:  "rtsp://192.168.1.20:49152/obs_current",
	}
	manager.current = &Session{ID: "current"}
	manager.status.Connected = true
	manager.rtspEndpoint = &endpoint

	var started []string
	var ready []RTSPEndpoint
	manager.cb = Callbacks{
		OnStart: func(session Session) {
			started = append(started, session.ID)
		},
		OnRTSPReady: func(got RTSPEndpoint) {
			ready = append(ready, got)
		},
	}

	if !manager.StartPublishing() {
		t.Fatal("StartPublishing returned false")
	}
	if len(started) != 1 || started[0] != "current" {
		t.Fatalf("OnStart sessions = %#v", started)
	}
	if len(ready) != 1 || ready[0] != endpoint {
		t.Fatalf("OnRTSPReady endpoints = %#v, want %#v", ready, endpoint)
	}
}

func TestStartPublishingReemitsReadyRTSPEndpointForAlreadyPublishedSession(t *testing.T) {
	manager := newTestManager(t, "rtspt")
	endpoint := RTSPEndpoint{
		SessionID: "current",
		Host:      "192.168.1.20",
		Port:      49152,
		Path:      "obs_current",
		LocalURL:  "rtsp://192.168.1.20:49152/obs_current",
	}
	manager.current = &Session{ID: "current", Published: true}
	manager.status.Connected = true
	manager.rtspEndpoint = &endpoint

	var started []string
	var ready []RTSPEndpoint
	manager.cb = Callbacks{
		OnStart: func(session Session) {
			started = append(started, session.ID)
		},
		OnRTSPReady: func(got RTSPEndpoint) {
			ready = append(ready, got)
		},
	}

	if !manager.StartPublishing() {
		t.Fatal("StartPublishing returned false")
	}
	if len(started) != 0 {
		t.Fatalf("OnStart sessions = %#v, want no duplicate start", started)
	}
	if len(ready) != 1 || ready[0] != endpoint {
		t.Fatalf("OnRTSPReady endpoints = %#v, want %#v", ready, endpoint)
	}
}

func TestSetAndClearRTSPEndpointFollowCurrentSession(t *testing.T) {
	manager := newTestManager(t, "rtspt")
	manager.current = &Session{ID: "current"}
	manager.status.Publishing = true
	endpoint := RTSPEndpoint{
		SessionID: "current",
		Host:      "192.168.1.20",
		Port:      49152,
		Path:      "obs_current",
		LocalURL:  "rtsp://192.168.1.20:49152/obs_current",
	}

	var ready []RTSPEndpoint
	var done []RTSPEndpoint
	manager.cb = Callbacks{
		OnRTSPReady: func(got RTSPEndpoint) {
			ready = append(ready, got)
		},
		OnRTSPDone: func(endpoint RTSPEndpoint) {
			done = append(done, endpoint)
		},
	}

	if !manager.setRTSPEndpoint(endpoint) {
		t.Fatal("current endpoint was rejected")
	}
	if manager.rtspEndpoint == nil || *manager.rtspEndpoint != endpoint {
		t.Fatalf("stored endpoint = %#v, want %#v", manager.rtspEndpoint, endpoint)
	}
	if got, want := manager.status.RTSPTURL, ""; got != want {
		t.Fatalf("RTSPTURL = %q, want %q", got, want)
	}
	if len(ready) != 1 || ready[0] != endpoint {
		t.Fatalf("ready callbacks = %#v, want %#v", ready, endpoint)
	}

	manager.clearRTSPEndpoint("stale")
	if manager.rtspEndpoint == nil {
		t.Fatal("stale clear removed current endpoint")
	}
	if len(done) != 0 {
		t.Fatalf("stale clear callbacks = %#v", done)
	}

	manager.clearRTSPEndpoint("current")
	if manager.rtspEndpoint != nil {
		t.Fatalf("endpoint not cleared: %#v", manager.rtspEndpoint)
	}
	if len(done) != 1 || done[0] != endpoint {
		t.Fatalf("done callbacks = %#v, want %#v", done, endpoint)
	}
}

func TestOBSActiveSessionContractFreezesRunningTransportAndUsesDesiredSettingsForNextSession(t *testing.T) {
	profileA := NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	profileB := NormalizeLatencyProfile(LatencyModeHLSHigh)
	presetA := video.ResolveQuality("720", 0)
	presetB := video.ResolveQuality("1080", 0)
	encoderA := video.CPUVideoEncoder(video.EncoderLowLatency)
	encoderB := video.NewVideoEncoderProfile("h264_nvenc", video.EncoderLowLatency)
	latencyCalls, presetCalls := 0, 0
	manager := New(t.TempDir(), "127.0.0.1", 1935, "stream-a", func() video.QualityPreset {
		presetCalls++
		return presetA
	}, func() LatencyProfile {
		latencyCalls++
		return profileA
	}, Callbacks{})

	active := manager.captureOBSActiveSessionContract("active", encoderA)
	if latencyCalls != 1 || presetCalls != 1 {
		t.Fatalf("active snapshot callbacks = latency %d, preset %d; want one each", latencyCalls, presetCalls)
	}
	manager.current = &Session{ID: active.SessionID, ActiveContract: &active}
	manager.status.Connected = true

	hls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/obs_active/index.m3u8"; got != want {
			t.Fatalf("proxied path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	t.Cleanup(hls.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(hls.URL, "http://"))
	if err != nil {
		t.Fatalf("parse HLS test server: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse HLS test port: %v", err)
	}
	manager.mtx = newMediaMTXRuntime("test", mediaMTXSessionConfig{
		Path:  "obs_active",
		Ports: mediaMTXPorts{HLS: port},
	})
	manager.mtx.httpClient = hls.Client()

	manager.preset = func() video.QualityPreset {
		presetCalls++
		return presetB
	}
	manager.latency = func() LatencyProfile {
		latencyCalls++
		return profileB
	}

	for _, args := range [][]string{
		manager.ffmpegRTSPArgsForContract(active, "recording.mp4", "rtsp://127.0.0.1:8554/obs_active"),
		manager.ffmpegLHLSArgsForContract(active, "recording.mp4", "http://127.0.0.1:65000/sink/stream.mpd"),
	} {
		if got, want := valueAfter(args, "-g"), profileA.GOPFrames; got != want {
			t.Fatalf("active GOP = %q, want %q: %s", got, want, strings.Join(args, " "))
		}
		if !containsSubsequence(args, []string{"-c:v", encoderA.Name}) {
			t.Fatalf("active encoder changed: %s", strings.Join(args, " "))
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/stream/active/current.m3u8", nil)
	if !manager.ProxyLLHLS(recorder, request, "active", "current.m3u8") {
		t.Fatal("active RTSP contract must proxy its MediaMTX HLS artifact")
	}
	if got := recorder.Body.String(); got != "#EXTM3U\n" {
		t.Fatalf("proxied body = %q", got)
	}
	if !manager.HLSPreviewReady("active", "current.m3u8") {
		t.Fatal("active RTSP contract must use its MediaMTX HLS readiness")
	}
	if !manager.SetRTSPURL("active", "rtsp://198.51.100.1:8554/obs_active", "published") {
		t.Fatal("active RTSP contract rejected its endpoint")
	}
	status := manager.Status()
	if status.ActiveSession == nil || status.ActiveSession.SessionID != active.SessionID || status.Latency != profileA {
		t.Fatalf("active status = %+v, want contract %+v", status, active)
	}
	if status.StreamKey != "stream-a" || status.Port != 1935 {
		t.Fatalf("active ingest status = key %q port %d", status.StreamKey, status.Port)
	}
	manager.mtx = nil
	_ = manager.ConnectionRows(time.Millisecond)
	if latencyCalls != 1 || presetCalls != 1 {
		t.Fatalf("running session re-read desired callbacks: latency %d, preset %d", latencyCalls, presetCalls)
	}

	next := manager.captureOBSActiveSessionContract("next", encoderB)
	if next.LatencyProfile != profileB || next.QualityPreset != presetB || next.VideoEncoderProfile != encoderB {
		t.Fatalf("next contract = %+v, want desired B", next)
	}
	if latencyCalls != 2 || presetCalls != 2 {
		t.Fatalf("next snapshot callbacks = latency %d, preset %d; want two total", latencyCalls, presetCalls)
	}
}

func TestOBSActiveContractCallbackMutationDoesNotChangeManagerSession(t *testing.T) {
	profileA := NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	profileB := NormalizeLatencyProfile(LatencyModeHLSHigh)
	manager := newTestManager(t, LatencyModeHLS)
	active := manager.captureOBSActiveSessionContract("active", video.CPUVideoEncoder(video.EncoderLowLatency))
	active.LatencyProfile = profileA
	manager.current = &Session{ID: active.SessionID, ActiveContract: &active}
	manager.status.Connected = true
	manager.cb.OnStart = func(session Session) {
		if session.ActiveContract == nil {
			t.Fatal("OnStart did not receive the active contract")
		}
		session.ActiveContract.LatencyProfile = profileB
		session.ActiveContract.QualityPreset = video.ResolveQuality("1080", 0)
	}

	if !manager.StartPublishing() {
		t.Fatal("StartPublishing returned false")
	}
	if status := manager.Status(); status.ActiveSession == nil || status.ActiveSession.LatencyProfile != profileA {
		t.Fatalf("callback mutation changed active status: %+v", status)
	}
	if !manager.SetRTSPURL("active", "rtsp://198.51.100.1:8554/obs_active", "published") {
		t.Fatal("callback mutation changed the active RTSP endpoint gate")
	}
}

func TestTimedOutOBSRestartPreventsOldFinalizerFromClearingNewAcceptedSession(t *testing.T) {
	manager := newTestManager(t, LatencyModeRTSPRealtime)
	oldLoopRelease := make(chan struct{})
	manager.loopRunner = func(ctx context.Context, generation uint64) {
		if generation == 1 {
			<-oldLoopRelease
			return
		}
		<-ctx.Done()
	}
	var doneCalls []string
	manager.cb.OnDone = func(session Session) { doneCalls = append(doneCalls, session.ID) }
	manager.Start()
	oldGeneration := manager.listenerGeneration
	manager.StartPublishing()
	oldContract := manager.captureOBSActiveSessionContract("old", video.CPUVideoEncoder(video.EncoderLowLatency))
	oldSession := Session{ID: "old", ActiveContract: &oldContract}
	if started, err := manager.waitForStart(context.Background(), oldGeneration, oldSession, make(chan error), func() bool { return true }); err != nil || !started {
		t.Fatalf("accept old session = started %v err %v", started, err)
	}

	finalizeEntered := make(chan struct{})
	releaseFinalize := make(chan struct{})
	manager.beforeSessionFinalize = func(Session) {
		close(finalizeEntered)
		<-releaseFinalize
	}
	oldFinalized := make(chan bool, 1)
	go func() { oldFinalized <- manager.finalizeAcceptedSession(&oldSession, oldGeneration) }()
	select {
	case <-finalizeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("old finalizer did not block")
	}

	manager.StopAndWait(10 * time.Millisecond)
	manager.Start()
	newGeneration := manager.listenerGeneration
	manager.StartPublishing()
	newContract := manager.captureOBSActiveSessionContract("new", video.CPUVideoEncoder(video.EncoderLowLatency))
	newSession := Session{ID: "new", ActiveContract: &newContract}
	if started, err := manager.waitForStart(context.Background(), newGeneration, newSession, make(chan error), func() bool { return true }); err != nil || !started {
		t.Fatalf("accept new session = started %v err %v", started, err)
	}
	endpoint := RTSPEndpoint{SessionID: "new", Host: "192.168.1.20", Port: 49152, Path: "obs_new", LocalURL: "rtsp://192.168.1.20:49152/obs_new"}
	if !manager.setRTSPEndpoint(endpoint) || !manager.SetRTSPURL("new", "rtsp://198.51.100.1:49152/obs_new", "published") {
		t.Fatal("new session endpoint was not accepted")
	}
	endpoint.Generation = manager.current.Generation

	close(releaseFinalize)
	select {
	case finalized := <-oldFinalized:
		if finalized {
			t.Fatal("old finalizer retained ownership after timed-out restart")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old finalizer did not return")
	}
	status := manager.Status()
	if !status.Connected || status.MediaID != "new" || !status.Publishing || status.RTSPTURL != "rtsp://198.51.100.1:49152/obs_new" || status.ActiveSession == nil || status.ActiveSession.SessionID != "new" {
		t.Fatalf("old finalizer changed new session status: %+v", status)
	}
	if manager.rtspEndpoint == nil || *manager.rtspEndpoint != endpoint {
		t.Fatalf("old finalizer changed new endpoint: %#v", manager.rtspEndpoint)
	}
	if len(doneCalls) != 0 {
		t.Fatalf("old finalizer emitted stale callbacks: %#v", doneCalls)
	}

	close(oldLoopRelease)
	manager.StopAndWait(time.Second)
}

func TestStaleOBSGenerationDoesNotClearNewRTSPEndpoint(t *testing.T) {
	manager := newTestManager(t, LatencyModeRTSPRealtime)
	manager.listenerGeneration = 2
	contract := manager.captureOBSActiveSessionContract("new", video.CPUVideoEncoder(video.EncoderLowLatency))
	contract.LatencyProfile = NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	manager.current = &Session{ID: "new", ActiveContract: &contract}
	manager.status.RTSPTURL = "rtsp://198.51.100.1:49152/obs_new"
	endpoint := RTSPEndpoint{SessionID: "new", Host: "192.168.1.20", Port: 49152, Path: "obs_new"}
	manager.rtspEndpoint = &endpoint
	var done []RTSPEndpoint
	manager.cb.OnRTSPDone = func(endpoint RTSPEndpoint) { done = append(done, endpoint) }

	manager.clearRTSPEndpointForGeneration("old", 1)
	if manager.rtspEndpoint == nil || *manager.rtspEndpoint != endpoint || manager.status.RTSPTURL != "rtsp://198.51.100.1:49152/obs_new" {
		t.Fatalf("stale cleanup changed endpoint state: endpoint=%#v status=%+v", manager.rtspEndpoint, manager.status)
	}
	if len(done) != 0 {
		t.Fatalf("stale cleanup emitted callbacks: %#v", done)
	}
}

func TestTimedOutOBSRestartRejectsOldSetupSharedWrites(t *testing.T) {
	manager := newTestManager(t, LatencyModeRTSPRealtime)
	oldLoopRelease := make(chan struct{})
	manager.loopRunner = func(ctx context.Context, generation uint64) {
		if generation == 1 {
			<-oldLoopRelease
			return
		}
		<-ctx.Done()
	}
	manager.Start()
	oldGeneration := manager.listenerGeneration

	setupAEntered := make(chan struct{})
	releaseSetupA := make(chan struct{})
	oldInstalled := make(chan bool, 1)
	oldSink := &lhlsSink{}
	oldRuntime := newMediaMTXRuntime("old", mediaMTXSessionConfig{Path: "old"})
	oldGate := &rtspGate{}
	go func() {
		close(setupAEntered)
		<-releaseSetupA
		oldInstalled <- manager.installLHLSSinkForGeneration(oldGeneration, oldSink)
		if manager.installRTSPRuntimeForGeneration(oldGeneration, oldRuntime, oldGate) {
			oldInstalled <- true
			return
		}
		oldInstalled <- false
	}()
	<-setupAEntered

	manager.StopAndWait(10 * time.Millisecond)
	manager.Start()
	newGeneration := manager.listenerGeneration
	newSink := &lhlsSink{}
	newRuntime := newMediaMTXRuntime("new", mediaMTXSessionConfig{Path: "new"})
	newGate := &rtspGate{}
	if !manager.installLHLSSinkForGeneration(newGeneration, newSink) || !manager.installRTSPRuntimeForGeneration(newGeneration, newRuntime, newGate) {
		t.Fatal("new listener could not install its setup state")
	}
	if !manager.setStatusForGeneration(newGeneration, func(status *Status) {
		status.Message = "new listener setup"
		status.EncoderName = "new"
	}) {
		t.Fatal("new listener could not publish setup status")
	}

	close(releaseSetupA)
	if installed := <-oldInstalled; installed {
		t.Fatal("old listener installed an LHLS sink after replacement")
	}
	if installed := <-oldInstalled; installed {
		t.Fatal("old listener installed a MediaMTX runtime after replacement")
	}
	if manager.setStatusForGeneration(oldGeneration, func(status *Status) { status.Message = "old listener" }) {
		t.Fatal("old listener updated status after replacement")
	}
	manager.mu.Lock()
	if manager.sink != newSink || manager.mtx != newRuntime || manager.rtspGate != newGate || manager.status.Message != "new listener setup" || manager.status.EncoderName != "new" {
		t.Fatalf("old setup changed replacement state: sink=%p runtime=%p gate=%p status=%+v", manager.sink, manager.mtx, manager.rtspGate, manager.status)
	}
	manager.mu.Unlock()

	close(oldLoopRelease)
	manager.StopAndWait(time.Second)
}

func TestOBSBlockedOldCallbackDoesNotDelayReplacementAcceptance(t *testing.T) {
	manager := newTestManager(t, LatencyModeRTSPRealtime)
	manager.loopRunner = func(ctx context.Context, _ uint64) { <-ctx.Done() }
	oldCallbackEntered := make(chan struct{})
	releaseOldCallback := make(chan struct{})
	var callbackMu sync.Mutex
	serverCurrentMedia := ""
	manager.cb.OnStart = func(session Session) {
		if session.ID == "old" {
			close(oldCallbackEntered)
			<-releaseOldCallback
		}
		if manager.IsSessionActive(session.ID, session.Generation) {
			callbackMu.Lock()
			serverCurrentMedia = session.ID
			callbackMu.Unlock()
		}
	}

	manager.Start()
	oldGeneration := manager.listenerGeneration
	manager.StartPublishing()
	oldContract := manager.captureOBSActiveSessionContract("old", video.CPUVideoEncoder(video.EncoderLowLatency))
	oldSession := Session{ID: "old", ActiveContract: &oldContract}
	oldAccepted := make(chan bool, 1)
	go func() {
		started, err := manager.waitForStart(context.Background(), oldGeneration, oldSession, make(chan error), func() bool { return true })
		oldAccepted <- err == nil && started
	}()
	select {
	case <-oldCallbackEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("old OnStart did not block")
	}

	stopStarted := time.Now()
	manager.StopAndWait(10 * time.Millisecond)
	if elapsed := time.Since(stopStarted); elapsed > 250*time.Millisecond {
		t.Fatalf("StopAndWait waited for blocked callback: %s", elapsed)
	}
	manager.Start()
	newGeneration := manager.listenerGeneration
	if started := manager.StartPublishing(); started {
		t.Fatal("replacement publish arm reported an old active session")
	}
	newContract := manager.captureOBSActiveSessionContract("new", video.CPUVideoEncoder(video.EncoderLowLatency))
	newSession := Session{ID: "new", ActiveContract: &newContract}
	newAccepted := make(chan bool, 1)
	go func() {
		started, err := manager.waitForStart(context.Background(), newGeneration, newSession, make(chan error), func() bool { return true })
		newAccepted <- err == nil && started
	}()
	if accepted := <-newAccepted; !accepted {
		t.Fatal("replacement accepted session did not complete")
	}
	status := manager.Status()
	if !status.Connected || status.MediaID != "new" || status.ActiveSession == nil || status.ActiveSession.SessionID != "new" {
		t.Fatalf("replacement accepted state = %+v", status)
	}

	close(releaseOldCallback)
	if accepted := <-oldAccepted; !accepted {
		t.Fatal("old accepted session did not complete its callback")
	}
	callbackMu.Lock()
	gotCurrent := serverCurrentMedia
	callbackMu.Unlock()
	if gotCurrent != "new" {
		t.Fatalf("old callback rewound server current media to %q", gotCurrent)
	}
	status = manager.Status()
	if !status.Connected || status.MediaID != "new" || status.ActiveSession == nil || status.ActiveSession.SessionID != "new" {
		t.Fatalf("replacement accepted state = %+v", status)
	}
	manager.StopAndWait(time.Second)
}

func valueAfter(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func containsSubsequence(args, want []string) bool {
	if len(want) == 0 {
		return true
	}
	next := 0
	for _, arg := range args {
		if arg == want[next] {
			next++
			if next == len(want) {
				return true
			}
		}
	}
	return false
}

package obsrtmp

import (
	"slices"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

func TestFFmpegArgsUseLowLatencyHLS(t *testing.T) {
	manager := newTestManager(t, "lhls")
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	wantValues := map[string]string{
		"-hls_time":      "0.5",
		"-hls_list_size": "12",
		"-g":             "15",
		"-keyint_min":    "15",
		"-preset":        "ultrafast",
		"-tune":          "zerolatency",
	}
	for flag, want := range wantValues {
		if got := valueAfter(args, flag); got != want {
			t.Fatalf("%s = %q, want %q\nargs: %s", flag, got, want, strings.Join(args, " "))
		}
	}

	if !slices.Contains(args, "delete_segments+independent_segments+program_date_time") {
		t.Fatalf("expected live sliding playlist flags in args: %s", strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{"-c:v", "libx264"}) {
		t.Fatalf("expected HLS output to be re-encoded with libx264: %s", strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{video.PlaylistName("media123"), "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart", "recording.mp4"}) {
		t.Fatalf("expected recording output to remain stream-copy MP4 after HLS output: %s", strings.Join(args, " "))
	}
}

func TestFFmpegArgsUseInjectedHardwareEncoder(t *testing.T) {
	manager := newTestManager(t, "low")
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

func TestFFmpegArgsUseNormalLatencyProfile(t *testing.T) {
	manager := newTestManager(t, "normal")
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	wantValues := map[string]string{
		"-hls_time":      "1",
		"-hls_list_size": "8",
		"-g":             "30",
		"-keyint_min":    "30",
	}
	for flag, want := range wantValues {
		if got := valueAfter(args, flag); got != want {
			t.Fatalf("%s = %q, want %q\nargs: %s", flag, got, want, strings.Join(args, " "))
		}
	}
}

func TestFFmpegArgsUseUltraLowLatencyProfile(t *testing.T) {
	manager := newTestManager(t, "llhls")
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	wantValues := map[string]string{
		"-hls_time":      "0.5",
		"-hls_list_size": "16",
		"-g":             "15",
		"-keyint_min":    "15",
	}
	for flag, want := range wantValues {
		if got := valueAfter(args, flag); got != want {
			t.Fatalf("%s = %q, want %q\nargs: %s", flag, got, want, strings.Join(args, " "))
		}
	}
}

func TestFFmpegArgsUseDVRListSize(t *testing.T) {
	manager := New(t.TempDir(), "127.0.0.1", 1935, "secret", nil, func() LatencyProfile {
		return EnableDVR(NormalizeLatencyProfile(LatencyModeLHLS))
	}, Callbacks{})
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	if got := valueAfter(args, "-hls_time"); got != "0.5" {
		t.Fatalf("hls_time = %q, want 0.5\nargs: %s", got, strings.Join(args, " "))
	}
	if got := valueAfter(args, "-hls_list_size"); got != "3600" {
		t.Fatalf("hls_list_size = %q, want 3600\nargs: %s", got, strings.Join(args, " "))
	}
}

func TestNormalizeLatencyModeAndProfile(t *testing.T) {
	cases := []struct {
		name             string
		input            string
		wantMode         string
		wantLabel        string
		wantExperimental bool
	}{
		{name: "canonical hls", input: "hls", wantMode: LatencyModeHLS, wantLabel: "通常遅延（HLS）"},
		{name: "canonical lhls", input: "lhls", wantMode: LatencyModeLHLS, wantLabel: "低遅延（LHLS, 実験）", wantExperimental: true},
		{name: "canonical llhls", input: "llhls", wantMode: LatencyModeLLHLS, wantLabel: "超低遅延（LL-HLS, 実験）", wantExperimental: true},
		{name: "canonical rtspt", input: "rtspt", wantMode: LatencyModeRTSPT, wantLabel: "リアルタイム（RTSP TCP）"},
		{name: "legacy auto", input: "  AUTO  ", wantMode: LatencyModeHLS, wantLabel: "通常遅延（HLS）"},
		{name: "legacy normal", input: "normal", wantMode: LatencyModeHLS, wantLabel: "通常遅延（HLS）"},
		{name: "legacy low", input: "low", wantMode: LatencyModeLHLS, wantLabel: "低遅延（LHLS, 実験）", wantExperimental: true},
		{name: "legacy ultra", input: "ultra", wantMode: LatencyModeLLHLS, wantLabel: "超低遅延（LL-HLS, 実験）", wantExperimental: true},
		{name: "unknown", input: "not-a-mode", wantMode: LatencyModeHLS, wantLabel: "通常遅延（HLS）"},
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
			if profile.Experimental != tc.wantExperimental {
				t.Fatalf("NormalizeLatencyProfile(%q).Experimental = %v, want %v", tc.input, profile.Experimental, tc.wantExperimental)
			}
		})
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
	var done []string
	manager.cb = Callbacks{
		OnRTSPReady: func(got RTSPEndpoint) {
			ready = append(ready, got)
		},
		OnRTSPDone: func(sessionID string) {
			done = append(done, sessionID)
		},
	}

	if !manager.setRTSPEndpoint(endpoint) {
		t.Fatal("current endpoint was rejected")
	}
	if manager.rtspEndpoint == nil || *manager.rtspEndpoint != endpoint {
		t.Fatalf("stored endpoint = %#v, want %#v", manager.rtspEndpoint, endpoint)
	}
	if got, want := manager.status.RTSPTURL, endpoint.LocalURL; got != want {
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
	if len(done) != 1 || done[0] != "current" {
		t.Fatalf("done callbacks = %#v, want current", done)
	}
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

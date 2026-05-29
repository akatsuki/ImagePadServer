package obsrtmp

import (
	"slices"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

func TestFFmpegArgsUseLowLatencyHLS(t *testing.T) {
	manager := newTestManager(t, "low")
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

func TestFFmpegArgsUseAutoLatencyProfile(t *testing.T) {
	manager := newTestManager(t, "auto")
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	if got := valueAfter(args, "-hls_time"); got != "2" {
		t.Fatalf("hls_time = %q, want 2\nargs: %s", got, strings.Join(args, " "))
	}
	if got := valueAfter(args, "-hls_list_size"); got != "5" {
		t.Fatalf("hls_list_size = %q, want 5\nargs: %s", got, strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{"-c:v", "libx264"}) {
		t.Fatalf("expected auto latency to re-encode HLS for predictable segments: %s", strings.Join(args, " "))
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
	manager := newTestManager(t, "ultra")
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
		return EnableDVR(NormalizeLatencyProfile("low"))
	}, Callbacks{})
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	if got := valueAfter(args, "-hls_time"); got != "0.5" {
		t.Fatalf("hls_time = %q, want 0.5\nargs: %s", got, strings.Join(args, " "))
	}
	if got := valueAfter(args, "-hls_list_size"); got != "3600" {
		t.Fatalf("hls_list_size = %q, want 3600\nargs: %s", got, strings.Join(args, " "))
	}
}

func TestResolveLatencyProfileAutoUsesUploadBandwidth(t *testing.T) {
	fast := ResolveLatencyProfile("auto", 12)
	if fast.Target != "5s" || fast.SegmentSeconds != "1" {
		t.Fatalf("fast auto profile = %+v, want 5s target with 1s segments", fast)
	}
	slow := ResolveLatencyProfile("auto", 2)
	if slow.Target != "16s" || slow.SegmentSeconds != "4" {
		t.Fatalf("slow auto profile = %+v, want 16s target with 4s segments", slow)
	}
	manual := ResolveLatencyProfile("low", 2)
	if manual.Mode != "low" || manual.Target != "1s" {
		t.Fatalf("manual profile = %+v, want low profile unchanged", manual)
	}
}

func newTestManager(t *testing.T, latencyMode string) *Manager {
	t.Helper()
	return New(t.TempDir(), "127.0.0.1", 1935, "secret", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(latencyMode)
	}, Callbacks{})
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

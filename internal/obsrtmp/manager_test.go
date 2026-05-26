package obsrtmp

import (
	"slices"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

func TestFFmpegArgsUseLowLatencyHLS(t *testing.T) {
	manager := New(t.TempDir(), "127.0.0.1", 1935, "secret", nil, Callbacks{})
	args := manager.ffmpegArgs("media123", "recording.mp4", video.ResolveQuality("720", 0))

	wantValues := map[string]string{
		"-hls_time":      lowLatencySegmentSeconds,
		"-hls_list_size": lowLatencyListSize,
		"-g":             lowLatencyGOPFrames,
		"-keyint_min":    lowLatencyGOPFrames,
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

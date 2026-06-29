package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStartupCleanupRunsFFmpegThenMediaMTXAndContinuesOnErrors(t *testing.T) {
	oldTracked := cleanupTrackedFFmpeg
	oldPort := cleanupFFmpegOnPort
	oldMediaMTX := cleanupStaleMediaMTX
	t.Cleanup(func() {
		cleanupTrackedFFmpeg = oldTracked
		cleanupFFmpegOnPort = oldPort
		cleanupStaleMediaMTX = oldMediaMTX
	})

	var calls []string
	cleanupTrackedFFmpeg = func() (int, error) {
		calls = append(calls, "ffmpeg-tracked")
		return 0, errors.New("tracked failed")
	}
	cleanupFFmpegOnPort = func(port int) (int, error) {
		calls = append(calls, "ffmpeg-port")
		if port != 1935 {
			t.Fatalf("port = %d, want 1935", port)
		}
		return 1, nil
	}
	cleanupStaleMediaMTX = func() (int, error) {
		calls = append(calls, "mediamtx")
		return 2, errors.New("mediamtx warning")
	}
	var logs []string
	cleanupStaleHelpers(func(format string, args ...any) {
		logs = append(logs, format)
	})

	if !reflect.DeepEqual(calls, []string{"ffmpeg-tracked", "ffmpeg-port", "mediamtx"}) {
		t.Fatalf("cleanup order = %v", calls)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"failed to clean up stale FFmpeg processes", "stopped %d stale FFmpeg process", "failed to clean up stale MediaMTX"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs missing %q: %s", want, joined)
		}
	}
}

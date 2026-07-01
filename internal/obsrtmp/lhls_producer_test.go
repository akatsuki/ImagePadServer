package obsrtmp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestFFmpegLHLSArgsUseDASHPrefetchOutput(t *testing.T) {
	manager := newTestManager(t, "lhls")
	output := "http://127.0.0.1:65000/tok/stream.mpd"
	args := manager.ffmpegLHLSArgs("media123", "recording.mp4", output, video.ResolveQuality("720", 0), video.CPUVideoEncoder(video.EncoderLowLatency))

	wantValues := map[string]string{
		"-f":            "dash",
		"-method":       "PUT",
		"-streaming":    "1",
		"-lhls":         "1",
		"-hls_playlist": "1",
		"-seg_duration": "1",
	}
	for flag, want := range wantValues {
		if got := valueAfter(args, flag); got != want {
			t.Fatalf("%s = %q, want %q\nargs: %s", flag, got, want, strings.Join(args, " "))
		}
	}
	if !slices.Contains(args, "experimental") {
		t.Fatalf("expected -strict experimental in args: %s", strings.Join(args, " "))
	}
	if !slices.Contains(args, output) {
		t.Fatalf("expected DASH output to target the sink URL: %s", strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{output, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart", "recording.mp4"}) {
		t.Fatalf("expected separate stream-copy MP4 recording after the LHLS output: %s", strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{"-c:v", "libx264"}) {
		t.Fatalf("expected the LHLS output to be re-encoded: %s", strings.Join(args, " "))
	}
}

func TestLHLSPublicFileGating(t *testing.T) {
	manager := newTestManager(t, "lhls")
	sink, err := newLHLSSink(t.TempDir(), "vid1", 1<<20)
	if err != nil {
		t.Fatalf("newLHLSSink: %v", err)
	}
	t.Cleanup(func() { _ = sink.close() })
	if err := os.WriteFile(filepath.Join(sink.dir, "media_0.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatalf("seed media playlist: %v", err)
	}

	// No active session yet: nothing resolves.
	if _, ok := manager.LHLSPublicFile("vid1", "media_0.m3u8"); ok {
		t.Fatal("expected no resolution without an active session")
	}

	manager.mu.Lock()
	manager.sink = sink
	manager.current = &Session{ID: "vid1"}
	manager.mu.Unlock()

	if path, ok := manager.LHLSPublicFile("vid1", "media_0.m3u8"); !ok || path != filepath.Join(sink.dir, "media_0.m3u8") {
		t.Fatalf("expected allowlisted existing file to resolve, got ok=%v path=%q", ok, path)
	}
	if _, ok := manager.LHLSPublicFile("vid1", "stream.mpd"); ok {
		t.Fatal("the DASH .mpd must not be publicly resolvable")
	}
	if _, ok := manager.LHLSPublicFile("other", "media_0.m3u8"); ok {
		t.Fatal("a non-active session id must not resolve")
	}
	if _, ok := manager.LHLSPublicFile("vid1", "media_9.m3u8"); ok {
		t.Fatal("a missing file must not resolve")
	}
}

// TestLHLSProducerArtifacts runs the real FFmpeg DASH/LHLS muxer against the
// private sink and asserts the prefetch/init/segment artifacts appear. It needs
// a pinned FFmpeg and is opt-in so the default suite never executes FFmpeg.
func TestLHLSProducerArtifacts(t *testing.T) {
	if os.Getenv("IMAGEPAD_LHLS_FFMPEG_TEST") == "" {
		t.Skip("set IMAGEPAD_LHLS_FFMPEG_TEST=1 (and IMAGEPAD_FFMPEG) to run the LHLS producer test")
	}
	ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
	if ffmpeg == "" {
		t.Skip("set IMAGEPAD_FFMPEG to a pinned FFmpeg to run the LHLS producer test")
	}

	sink, err := newLHLSSink(t.TempDir(), "producer", 0)
	if err != nil {
		t.Fatalf("newLHLSSink: %v", err)
	}
	t.Cleanup(func() { _ = sink.close() })
	if err := sink.start(); err != nil {
		t.Fatalf("start sink: %v", err)
	}

	// Stream long enough to observe the live LHLS state. The #EXT-X-PREFETCH
	// tag only exists while FFmpeg is mid-stream; once the muxer finalizes the
	// playlist it drops the prefetch, which is exactly why the readiness gate
	// must be evaluated live (as waitForStart does), not post-mortem.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "8",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "15", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k", "-ar", "48000", "-ac", "2",
		"-strict", "experimental", "-f", "dash", "-method", "PUT",
		"-streaming", "1", "-lhls", "1", "-hls_playlist", "1",
		"-seg_duration", "1", "-window_size", "12",
		sink.baseURL() + "/stream.mpd",
	}
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ffmpeg: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	prefetchLive := func() bool {
		if !sink.ready() {
			return false
		}
		entries, _ := os.ReadDir(sink.dir)
		for _, e := range entries {
			if !lhlsManifestRe.MatchString(e.Name()) {
				continue
			}
			data, _ := os.ReadFile(filepath.Join(sink.dir, e.Name()))
			if strings.Contains(string(data), lhlsManifestPrefetchTag) {
				return true
			}
		}
		return false
	}

	deadline := time.Now().Add(10 * time.Second)
	var sawReady bool
	for time.Now().Before(deadline) {
		if prefetchLive() {
			sawReady = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sawReady {
		entries, _ := os.ReadDir(sink.dir)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("sink never reached live readiness; produced: %v\nffmpeg stderr:\n%s", names, stderr.String())
	}
	if sink.publicReadable("stream.mpd") {
		t.Fatal("the DASH .mpd must not be publicly readable")
	}
}

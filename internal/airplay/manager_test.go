package airplay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestFeatureEnabledCanBeDisabled(t *testing.T) {
	t.Setenv(envFeatureFlag, "0")
	if FeatureEnabled() {
		t.Fatal("FeatureEnabled() = true with IMAGEPAD_AIRPLAY=0")
	}
	t.Setenv(envFeatureFlag, "true")
	if !FeatureEnabled() {
		t.Fatal("FeatureEnabled() = false with IMAGEPAD_AIRPLAY=true")
	}
	t.Setenv(envFeatureFlag, "")
	if FeatureEnabled() {
		t.Fatal("FeatureEnabled() = true without explicit opt-in")
	}
}

func TestResolveReceiverPathFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	receiver := filepath.Join(dir, "uxplay-test")
	if err := os.WriteFile(receiver, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envUxPlayPath, receiver)
	t.Setenv(envReceiverPath, "")
	got, err := ResolveReceiverPath()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(receiver)
	if got != want {
		t.Fatalf("ResolveReceiverPath() = %q, want %q", got, want)
	}
}

func TestReserveRTPPortsAvoidsRTCPOverlap(t *testing.T) {
	for i := 0; i < 50; i++ {
		videoInputPort, videoPort, audioInputPort, audioPort, err := reserveRTPPorts()
		if err != nil {
			t.Fatalf("reserveRTPPorts: %v", err)
		}
		ports := []int{videoInputPort, videoInputPort + 1, videoPort, videoPort + 1, audioInputPort, audioInputPort + 1, audioPort, audioPort + 1}
		seen := make(map[int]bool, len(ports))
		for _, port := range ports {
			if port <= 0 || port > 65535 {
				t.Fatalf("invalid reserved port %d from video input=%d video=%d audio input=%d audio=%d", port, videoInputPort, videoPort, audioInputPort, audioPort)
			}
			if seen[port] {
				t.Fatalf("overlapping RTP/RTCP ports: video input=%d/%d video=%d/%d audio input=%d/%d audio=%d/%d", videoInputPort, videoInputPort+1, videoPort, videoPort+1, audioInputPort, audioInputPort+1, audioPort, audioPort+1)
			}
			seen[port] = true
		}
	}
}

func TestBuildReceiverArgsUsesLocalRTPPorts(t *testing.T) {
	args := BuildReceiverArgs(41001, 41002, "Test Receiver")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-n", "Test-Receiver", "-vrtp", "config-interval=-1", "port=41001", "-artp", "port=41002"} {
		if !strings.Contains(joined, want) {
			t.Errorf("receiver args %q do not contain %q", joined, want)
		}
	}
	if !strings.Contains(args[5], "	!	udpsink	") || !strings.Contains(args[7], "	!	udpsink	") {
		t.Errorf("RTP pipelines must use tab separators: %q", args)
	}
}

func TestReplayDecoderRefreshesCoversBridgeUDPBindWindow(t *testing.T) {
	ctx := context.Background()
	calls := 0
	replayDecoderRefreshes(ctx, []time.Duration{0, time.Millisecond, time.Millisecond}, func() {
		calls++
	})
	if calls != 3 {
		t.Fatalf("replay calls = %d, want 3", calls)
	}
}

func TestReplayDecoderRefreshesStopsWhenSessionEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	replayDecoderRefreshes(ctx, []time.Duration{time.Hour}, func() {
		calls++
	})
	if calls != 0 {
		t.Fatalf("replay calls after cancellation = %d, want 0", calls)
	}
}

func TestBuildBridgeArgsMapsVideoAndAudioToRTMP(t *testing.T) {
	encoder := video.CPUVideoEncoder(video.EncoderLowLatency)
	preset := video.ResolveQualityForUpload("1080", 20, 0)
	args := BuildBridgeArgs("session.sdp", "rtmp://127.0.0.1:1935/live/key", encoder, preset)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"-protocol_whitelist file,udp,rtp",
		"-buffer_size 4194304",
		"-reorder_queue_size 4096",
		"-analyzeduration 2000000",
		"-probesize 5000000",
		"-fflags +genpts+discardcorrupt",
		"-i session.sdp",
		"-map 0:v:0",
		"-map 0:a:0",
		"-c:v libx264",
		"-preset ultrafast",
		"-tune zerolatency",
		"-c:a aac",
		"-af aresample=async=1:first_pts=0,asetpts=N/SR/TB",
		"-ar 48000",
		"-avoid_negative_ts make_zero",
		"-f flv",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in %q", expected, joined)
		}
	}
	inputCount := 0
	for _, arg := range args {
		if arg == "-i" {
			inputCount++
		}
	}
	if inputCount != 1 {
		t.Fatalf("expected one SDP input, got %d in %q", inputCount, joined)
	}
	if strings.Contains(joined, "86400000000") {
		t.Fatalf("bridge must not retain the 24-hour probe window: %q", joined)
	}
}

func TestBuildSDPUsesCRLFAndExpectedPayloads(t *testing.T) {
	sdp := BuildSessionSDP(5000, 5002)
	for _, expected := range []string{
		"m=video 5000 RTP/AVP 96",
		"a=rtpmap:96 H264/90000",
		"a=fmtp:96 packetization-mode=1",
		"m=audio 5002 RTP/AVP 96",
		"a=rtpmap:96 L16/44100/2",
	} {
		if !strings.Contains(sdp, expected) {
			t.Fatalf("missing %q in %q", expected, sdp)
		}
	}
	if strings.Contains(strings.ReplaceAll(sdp, "\r\n", ""), "\n") {
		t.Fatalf("SDP must use CRLF line endings: %q", sdp)
	}
	if strings.Index(sdp, "m=video") > strings.Index(sdp, "m=audio") {
		t.Fatalf("video media section must precede audio: %q", sdp)
	}
}

func TestStatusReportsDisabledFeature(t *testing.T) {
	t.Setenv(envFeatureFlag, "off")
	m := New(nil)
	status := m.Status()
	if status.Enabled || status.Available || status.Running {
		t.Fatalf("disabled status = %+v", status)
	}
	if !strings.Contains(status.Message, "無効") {
		t.Fatalf("disabled status message = %q", status.Message)
	}
}

const helperProcessEnv = "IMAGEPAD_AIRPLAY_TEST_HELPER"

func init() {
	if os.Getenv(helperProcessEnv) == "1" {
		select {}
	}
}

func TestManagerStartStopWithRealChildProcesses(t *testing.T) {
	t.Setenv(envFeatureFlag, "1")
	t.Setenv(envUxPlayPath, os.Args[0])
	t.Setenv(helperProcessEnv, "1")

	m := New(nil)
	if err := m.Start(context.Background(), os.Args[0], "rtmp://127.0.0.1:1935/live/test"); err != nil {
		t.Fatal(err)
	}
	if status := m.Status(); !status.Running || !status.ReceiverRunning || !status.BridgeRunning {
		t.Fatalf("running status = %+v", status)
	}
	if !m.Stop(5 * time.Second) {
		t.Fatal("manager did not stop child processes before timeout")
	}
	if status := m.Status(); status.Running || status.ReceiverRunning || status.BridgeRunning {
		t.Fatalf("stopped status = %+v", status)
	}
}

func TestManagerStartIsSerialized(t *testing.T) {
	t.Setenv(envFeatureFlag, "1")
	t.Setenv(envUxPlayPath, os.Args[0])
	t.Setenv(helperProcessEnv, "1")

	m := New(nil)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- m.Start(context.Background(), os.Args[0], "rtmp://127.0.0.1:1935/live/test")
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAlreadyRunning):
		default:
			t.Errorf("concurrent Start returned unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent Start successes = %d, want exactly one", successes)
	}
	if !m.Stop(5 * time.Second) {
		t.Fatal("serialized manager did not stop child processes before timeout")
	}
}

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

func TestBuildReceiverArgsUsesLocalRTPPorts(t *testing.T) {
	args := BuildReceiverArgs(41001, 41002, "Test Receiver")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-n", "Test Receiver", "-vrtp", "port=41001", "-artp", "port=41002"} {
		if !strings.Contains(joined, want) {
			t.Errorf("receiver args %q do not contain %q", joined, want)
		}
	}
}

func TestBuildBridgeArgsMapsVideoAndAudioToRTMP(t *testing.T) {
	args := BuildBridgeArgs("video.sdp", "audio.sdp", "rtmp://127.0.0.1:1935/live/test")
	joined := strings.Join(args, " ")
	for _, want := range []string{"video.sdp", "audio.sdp", "-map 0:v:0", "-map 1:a:0", "-c:v copy", "-c:a aac", "-f flv"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bridge args %q do not contain %q", joined, want)
		}
	}
}

func TestBuildSDPUsesCRLFAndExpectedPayloads(t *testing.T) {
	video := BuildVideoSDP(42001)
	if !strings.Contains(video, "\r\nm=video 42001 RTP/AVP 96\r\n") {
		t.Fatalf("video SDP has invalid line endings or port: %q", video)
	}
	if !strings.Contains(video, "a=rtpmap:96 H264/90000\r\n") {
		t.Fatalf("video SDP does not declare H264: %q", video)
	}
	audio := BuildAudioSDP(42002)
	if !strings.Contains(audio, "m=audio 42002 RTP/AVP 96\r\n") || !strings.Contains(audio, "L16/44100/2") {
		t.Fatalf("audio SDP is invalid: %q", audio)
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

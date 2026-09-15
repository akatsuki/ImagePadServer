package airplay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestGStreamerPipelineModeIsExplicit(t *testing.T) {
	for _, value := range []string{"", "ffmpeg", "auto", "0", "false"} {
		t.Setenv(envAirPlayPipeline, value)
		if gstreamerPipelineEnabled() {
			t.Fatalf("pipeline mode %q unexpectedly enables GStreamer", value)
		}
	}
	for _, value := range []string{"gstreamer", "gst", "1", "true"} {
		t.Setenv(envAirPlayPipeline, value)
		if !gstreamerPipelineEnabled() {
			t.Fatalf("pipeline mode %q does not enable GStreamer", value)
		}
	}
}

func TestResolveGStreamerBridgePathUsesExplicitEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "airplay-gstreamer-bridge.exe")
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAirPlayGStreamerBridgePath, path)
	got, err := ResolveGStreamerBridgePath()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ResolveGStreamerBridgePath() = %q, want %q", got, want)
	}
}

func TestConfigureGStreamerBridgeLeavesStdoutForBinaryPipe(t *testing.T) {
	cmd := exec.Command("airplay-gstreamer-bridge")
	configureGStreamerBridgeProcess(cmd, &limitedBuffer{max: 128}, "airplay-gstreamer-bridge")
	if cmd.Stdout != nil {
		t.Fatal("GStreamer bridge configuration must not replace the binary stdout pipe")
	}
}

func TestDirectPublisherDiagnosticsStayInSessionDirectory(t *testing.T) {
	stopFile := filepath.Join(t.TempDir(), "stop.request")
	want := filepath.Join(filepath.Dir(stopFile), "gstreamer-direct.log")
	if got := directPublisherLogPath(stopFile); got != want {
		t.Fatalf("directPublisherLogPath() = %q, want %q", got, want)
	}
}

func TestDirectPublisherDiagnosticsFollowGenerationStopPath(t *testing.T) {
	stopFile := filepath.Join(t.TempDir(), "airplay", "0123456789abcdef", "publisher-0002.stop")
	want := filepath.Join(filepath.Dir(stopFile), "publisher-0002.log")
	if got := directPublisherLogPath(stopFile); got != want {
		t.Fatalf("directPublisherLogPath() = %q, want %q", got, want)
	}
}

func TestDirectPublisherStopRequestIsPublishedAfterCompleteWrite(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "publisher-0001.stop")
	if err := writeDirectPublisherStopRequest(path, "no-signal\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "no-signal\n" {
		t.Fatalf("stop request = %q", got)
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".publisher-0001.stop.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary stop requests remain: %v", temporary)
	}
}

func TestUnresponsiveDirectPublisherProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_TEST_UNRESPONSIVE_DIRECT_PUBLISHER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

func TestGracefulDirectPublisherProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_TEST_GRACEFUL_DIRECT_PUBLISHER") != "1" {
		return
	}
	stopFile := os.Getenv("IMAGEPAD_TEST_DIRECT_PUBLISHER_STOP_FILE")
	observedFile := os.Getenv("IMAGEPAD_TEST_DIRECT_PUBLISHER_OBSERVED_FILE")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(stopFile); err == nil {
			if err := os.WriteFile(observedFile, data, 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("graceful stop request was not observed")
}

func TestDirectPublisherOutlivesSharedContextUntilGracefulStop(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_GRACEFUL_DIRECT_PUBLISHER", "1")
	root := t.TempDir()
	stopFile := filepath.Join(root, "publisher-0001.stop")
	observedFile := filepath.Join(root, "observed-stop.txt")
	t.Setenv("IMAGEPAD_TEST_DIRECT_PUBLISHER_STOP_FILE", stopFile)
	t.Setenv("IMAGEPAD_TEST_DIRECT_PUBLISHER_OBSERVED_FILE", observedFile)
	ctx, cancel := context.WithCancel(context.Background())
	process, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{"-test.run=TestGracefulDirectPublisherProcess$"}, stopFile)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)
	if err := process.stopAndWait(2 * time.Second); err != nil {
		t.Fatalf("graceful stop after shared context cancellation: %v", err)
	}
	data, err := os.ReadFile(observedFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "stop\n" {
		t.Fatalf("observed stop request = %q, want %q", got, "stop\\n")
	}
}

func TestLegacyDirectPublisherRemainsContextBound(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_UNRESPONSIVE_DIRECT_PUBLISHER", "1")
	ctx, cancel := context.WithCancel(context.Background())
	process, err := startGStreamerDirectProcess(ctx, os.Args[0], []string{"-test.run=TestUnresponsiveDirectPublisherProcess$"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- process.wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("context-bound legacy publisher unexpectedly exited cleanly")
		}
	case <-time.After(2 * time.Second):
		process.forceStop()
		t.Fatal("context cancellation did not stop legacy publisher")
	}
}

func TestDirectPublisherExistingWaitReceivesNoSignalReason(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_GRACEFUL_DIRECT_PUBLISHER", "1")
	root := t.TempDir()
	stopFile := filepath.Join(root, "publisher-0001.stop")
	observedFile := filepath.Join(root, "observed-stop.txt")
	t.Setenv("IMAGEPAD_TEST_DIRECT_PUBLISHER_STOP_FILE", stopFile)
	t.Setenv("IMAGEPAD_TEST_DIRECT_PUBLISHER_OBSERVED_FILE", observedFile)
	process, err := startSourceClockGStreamerProcess(context.Background(), os.Args[0], []string{"-test.run=TestGracefulDirectPublisherProcess$"}, stopFile)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.wait() }()
	if err := process.stopAndWaitDone(done, directPublisherStopNoSignal, 2*time.Second); err != nil {
		t.Fatalf("graceful no-signal stop: %v", err)
	}
	data, err := os.ReadFile(observedFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "no-signal\n" {
		t.Fatalf("observed stop request = %q, want %q", got, "no-signal\\n")
	}
}

func TestDirectPublisherStopAndWaitEscalatesAfterDeadline(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_UNRESPONSIVE_DIRECT_PUBLISHER", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopFile := filepath.Join(t.TempDir(), "publisher-0001.stop")
	process, err := startGStreamerDirectProcess(ctx, os.Args[0], []string{"-test.run=TestUnresponsiveDirectPublisherProcess$"}, stopFile)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := process.stopAndWait(50 * time.Millisecond); err == nil {
		t.Fatal("forced publisher termination unexpectedly returned a clean exit")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("bounded publisher stop took %s", elapsed)
	}
	if _, err := os.Stat(stopFile); err != nil {
		t.Fatalf("graceful stop request was not attempted before escalation: %v", err)
	}
}

func TestDirectPublisherStopAndWaitReportsUnconfirmedExit(t *testing.T) {
	process := &gstreamerDirectProcess{process: &exec.Cmd{}}
	done := make(chan error)
	err := process.stopAndWaitDone(done, directPublisherStopNormal, time.Millisecond)
	if !errors.Is(err, errDirectGStreamerProcessExitUnconfirmed) {
		t.Fatalf("error=%v, want unconfirmed process exit", err)
	}
}

func TestDirectPublisherStopAndWaitWithinUsesOneDeadline(t *testing.T) {
	// This fixture intentionally has no producer for done. It is the only
	// valid case for asserting an unconfirmed exit after the final budget.
	process := &gstreamerDirectProcess{process: &exec.Cmd{}}
	done := make(chan error)
	started := time.Now()
	err := process.stopAndWaitDoneWithin(done, directPublisherStopNormal, 50*time.Millisecond)
	if !errors.Is(err, errDirectGStreamerProcessExitUnconfirmed) {
		t.Fatalf("error=%v, want unconfirmed process exit", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("single-deadline stop took %s", elapsed)
	}
}

func TestDirectPublisherStopAndWaitWithinReservesKillConfirmationBudget(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_UNRESPONSIVE_DIRECT_PUBLISHER", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{"-test.run=TestUnresponsiveDirectPublisherProcess$"}, filepath.Join(t.TempDir(), "publisher.stop"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		process.forceStop()
		_ = process.wait()
	}()

	var forced atomic.Bool
	originalForceCancel := process.forceCancel
	process.forceCancel = func() {
		forced.Store(true)
		if originalForceCancel != nil {
			originalForceCancel()
		}
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(90 * time.Millisecond)
		done <- nil
	}()
	if err := process.stopAndWaitDoneWithin(done, directPublisherStopNormal, 100*time.Millisecond); err != nil {
		t.Fatalf("stopAndWaitDoneWithin() = %v, want delayed clean completion", err)
	}
	if !forced.Load() {
		t.Fatal("forceStop was not started during the reserved kill-confirmation window")
	}
}

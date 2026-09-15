package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

func TestDirectRecordingFFprobeHelperProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_TEST_DIRECT_FFPROBE") != "1" {
		return
	}
	if marker := os.Getenv("IMAGEPAD_TEST_DIRECT_FFPROBE_STARTED"); marker != "" {
		if err := os.WriteFile(marker, []byte("started"), 0600); err != nil {
			os.Exit(25)
		}
	}
	switch os.Getenv("IMAGEPAD_TEST_DIRECT_FFPROBE_MODE") {
	case "success":
		fmt.Print("{\"streams\":[{\"codec_type\":\"video\",\"width\":1280,\"height\":720}],\"format\":{\"duration\":\"2.5\"}}")
		os.Exit(0)
	case "failure":
		fmt.Fprint(os.Stderr, "invalid mp4 fixture")
		os.Exit(23)
	case "hang":
		time.Sleep(30 * time.Second)
	case "oversized":
		fmt.Print(strings.Repeat("x", directRecordingProbeOutputLimit+1))
		os.Exit(0)
	default:
		os.Exit(24)
	}
}

func directRecordingProbeHelperCommand(mode string, extraEnvironment ...string) directRecordingProbeCommandFunc {
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDirectRecordingFFprobeHelperProcess$")
		cmd.Env = append(os.Environ(),
			"IMAGEPAD_TEST_DIRECT_FFPROBE=1",
			"IMAGEPAD_TEST_DIRECT_FFPROBE_MODE="+mode,
		)
		cmd.Env = append(cmd.Env, extraEnvironment...)
		return cmd
	}
}

func directRecordingProbeFixture(t *testing.T) (airplaycontract.PublisherArtifacts, airplaycontract.Event) {
	t.Helper()
	artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), "0123456789abcdef", 3)
	event := airplaycontract.Event{
		Schema: 2, SessionID: artifacts.SessionID, PublisherGeneration: artifacts.Generation,
		Event: "recording-finalized", At: time.Unix(300, 0).UTC(),
		RecordingPath: artifacts.Recording, RecordingClosed: true,
	}
	return artifacts, event
}

func TestProbeDirectRecordingSkipsProbeWithoutAdmissionEvidence(t *testing.T) {
	artifacts, finalized := directRecordingProbeFixture(t)
	for _, test := range []struct {
		name         string
		hasRealVideo bool
		finalized    airplaycontract.Event
		wantReason   string
	}{
		{name: "no real video", finalized: finalized, wantReason: recordingReasonNoRealVideo},
		{name: "close not confirmed", hasRealVideo: true, wantReason: recordingReasonCloseNotConfirmed},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			outcome := probeDirectRecording(t.Context(), time.Second, artifacts, test.finalized, test.hasRealVideo, 1920, 1080, func(context.Context, string) (video.MediaProbe, error) {
				called = true
				return video.MediaProbe{}, nil
			})
			if called || outcome.ProbeOK || outcome.Reason != test.wantReason {
				t.Fatalf("called=%t outcome=%+v", called, outcome)
			}
		})
	}
}

func TestProbeDirectRecordingPassesExactPathAndDeadline(t *testing.T) {
	artifacts, finalized := directRecordingProbeFixture(t)
	called := false
	outcome := probeDirectRecording(t.Context(), time.Second, artifacts, finalized, true, 1280, 720, func(ctx context.Context, path string) (video.MediaProbe, error) {
		called = true
		if path != artifacts.Recording {
			t.Fatalf("probe path=%q, want %q", path, artifacts.Recording)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
			t.Fatalf("probe deadline=%v ok=%t", deadline, ok)
		}
		return video.MediaProbe{Duration: 2, Streams: []video.MediaStream{{CodecType: "video", Width: 1280, Height: 720}}}, nil
	})
	if !called || !outcome.ProbeOK || outcome.Reason != recordingReasonVerified || outcome.DurationNS != 2_000_000_000 {
		t.Fatalf("called=%t outcome=%+v", called, outcome)
	}
}

func TestProbeDirectRecordingGenerationUsesDescriptorDimensions(t *testing.T) {
	artifacts, finalized := directRecordingProbeFixture(t)
	descriptor := airplaycontract.DeliveryGeneration{
		SessionID: artifacts.SessionID, SessionEpoch: 1, RequestID: "quality-360",
		Generation: artifacts.Generation, ArtifactPaths: artifacts,
		ExpectedRecordingWidth: 640, ExpectedRecordingHeight: 360,
	}
	outcome := probeDirectRecordingGeneration(t.Context(), time.Second, descriptor, finalized, true, func(context.Context, string) (video.MediaProbe, error) {
		return video.MediaProbe{Duration: 2, Streams: []video.MediaStream{{CodecType: "video", Width: 640, Height: 360}}}, nil
	})
	if !outcome.ProbeOK || outcome.Reason != recordingReasonVerified {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestProbeDirectRecordingClassifiesDeadline(t *testing.T) {
	artifacts, finalized := directRecordingProbeFixture(t)
	started := time.Now()
	outcome := probeDirectRecording(t.Context(), 20*time.Millisecond, artifacts, finalized, true, 1920, 1080, func(ctx context.Context, _ string) (video.MediaProbe, error) {
		<-ctx.Done()
		return video.MediaProbe{}, ctx.Err()
	})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded probe took %s", elapsed)
	}
	if outcome.ProbeOK || outcome.Reason != recordingReasonProbeTimeout {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestProbeDirectRecordingUsesExpiredContextEvenIfProbeReturnsNil(t *testing.T) {
	artifacts, finalized := directRecordingProbeFixture(t)
	outcome := probeDirectRecording(t.Context(), time.Millisecond, artifacts, finalized, true, 1920, 1080, func(ctx context.Context, _ string) (video.MediaProbe, error) {
		<-ctx.Done()
		return video.MediaProbe{Duration: 1, Streams: []video.MediaStream{{CodecType: "video", Width: 1920, Height: 1080}}}, nil
	})
	if outcome.ProbeOK || outcome.Reason != recordingReasonProbeTimeout {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestProbeDirectRecordingRejectsMissingProbeFunction(t *testing.T) {
	artifacts, finalized := directRecordingProbeFixture(t)
	outcome := probeDirectRecording(t.Context(), time.Second, artifacts, finalized, true, 1920, 1080, nil)
	if outcome.ProbeOK || outcome.Reason != recordingReasonProbeFailed {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestProbeDirectRecordingPreservesGenericProbeFailure(t *testing.T) {
	artifacts, finalized := directRecordingProbeFixture(t)
	outcome := probeDirectRecording(t.Context(), time.Second, artifacts, finalized, true, 1920, 1080, func(context.Context, string) (video.MediaProbe, error) {
		return video.MediaProbe{}, errors.New("invalid mp4")
	})
	if outcome.ProbeOK || outcome.Reason != recordingReasonProbeFailed {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestRunDirectRecordingFFprobeParsesActualChildProcess(t *testing.T) {
	const recording = "C:\\recordings\\publisher-0001.mp4"
	var gotExecutable string
	var gotArgs []string
	command := directRecordingProbeHelperCommand("success")
	probe, err := runDirectRecordingFFprobeWithCommand(t.Context(), "ffprobe-fixture.exe", recording, func(ctx context.Context, executable string, args ...string) *exec.Cmd {
		gotExecutable = executable
		gotArgs = append([]string(nil), args...)
		return command(ctx, executable, args...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotExecutable != "ffprobe-fixture.exe" {
		t.Fatalf("executable=%q", gotExecutable)
	}
	wantArgs := []string{"-v", "error", "-show_streams", "-show_format", "-of", "json", recording}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args=%q, want %q", gotArgs, wantArgs)
	}
	if probe.Duration != 2.5 || len(probe.Streams) != 1 || probe.Streams[0].Width != 1280 || probe.Streams[0].Height != 720 {
		t.Fatalf("probe=%+v", probe)
	}
}

func TestRunDirectRecordingFFprobeHardTimeoutStopsActualChildProcess(t *testing.T) {
	startedMarker := filepath.Join(t.TempDir(), "child-started")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	var child *exec.Cmd
	command := directRecordingProbeHelperCommand("hang", "IMAGEPAD_TEST_DIRECT_FFPROBE_STARTED="+startedMarker)
	_, err := runDirectRecordingFFprobeWithCommand(ctx, "ffprobe-fixture.exe", "recording.mp4", func(ctx context.Context, executable string, args ...string) *exec.Cmd {
		child = command(ctx, executable, args...)
		return child
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want deadline exceeded", err)
	}
	if _, statErr := os.Stat(startedMarker); statErr != nil {
		t.Fatalf("deadline elapsed before helper child started: %v", statErr)
	}
	if child == nil {
		t.Fatal("ffprobe command was not constructed")
	}
	if child.ProcessState == nil {
		t.Fatal("helper child was not reaped after deadline")
	}
	if runtime.GOOS == "windows" && !child.ProcessState.Exited() {
		t.Fatalf("Windows helper child did not report an exited state: %v", child.ProcessState)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("hard timeout returned after %s", elapsed)
	}
}

func TestRunDirectRecordingFFprobePreservesChildFailure(t *testing.T) {
	_, err := runDirectRecordingFFprobeWithCommand(t.Context(), "ffprobe-fixture.exe", "recording.mp4", directRecordingProbeHelperCommand("failure"))
	if err == nil || !strings.Contains(err.Error(), "invalid mp4 fixture") {
		t.Fatalf("error=%v", err)
	}
}

func TestRunDirectRecordingFFprobeRejectsOversizedOutput(t *testing.T) {
	_, err := runDirectRecordingFFprobeWithCommand(t.Context(), "ffprobe-fixture.exe", "recording.mp4", directRecordingProbeHelperCommand("oversized"))
	if !errors.Is(err, errDirectRecordingProbeOutputTooLarge) {
		t.Fatalf("error=%v, want output-too-large", err)
	}
}

func TestRunDirectRecordingFFprobeRejectsIncompleteInputsWithoutStartingProcess(t *testing.T) {
	called := false
	command := func(context.Context, string, ...string) *exec.Cmd {
		called = true
		return nil
	}
	for _, test := range []struct {
		name       string
		executable string
		path       string
	}{
		{name: "missing executable", path: "recording.mp4"},
		{name: "missing path", executable: "ffprobe.exe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runDirectRecordingFFprobeWithCommand(t.Context(), test.executable, test.path, command); err == nil {
				t.Fatal("incomplete ffprobe input was accepted")
			}
		})
	}
	if called {
		t.Fatal("incomplete input started a child process")
	}
}

func TestConfigureDirectRecordingProbeForSessionCapturesOutputAndMissingTool(t *testing.T) {
	observer, err := newDirectPublisherObserver(t.TempDir(), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	toolErr := errors.New("ffprobe is not installed")
	output := DirectOutputSettings{Width: 1920, Height: 1080}
	if err := configureDirectRecordingProbeForSession(observer, output, "", toolErr); err != nil {
		t.Fatal(err)
	}
	observer.mu.Lock()
	probe := observer.recordingProbe
	timeout := observer.recordingProbeTimeout
	width := observer.recordingProbeWidth
	height := observer.recordingProbeHeight
	observer.mu.Unlock()
	if probe == nil || timeout != directRecordingProbeTimeout || width != 1920 || height != 1080 {
		t.Fatalf("probe configuration: probe=%v timeout=%s width=%d height=%d", probe != nil, timeout, width, height)
	}
	if _, err := probe(t.Context(), "recording.mp4"); !errors.Is(err, errDirectRecordingProbeUnavailable) || !strings.Contains(err.Error(), toolErr.Error()) {
		t.Fatalf("missing-tool probe error=%v", err)
	}
}

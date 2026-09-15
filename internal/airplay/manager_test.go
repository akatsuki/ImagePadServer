package airplay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

func TestProcessExitDetailReportsExitCode(t *testing.T) {
	if os.Getenv("IMAGEPAD_TEST_EXIT_CODE_HELPER") == "1" {
		os.Exit(37)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestProcessExitDetailReportsExitCode$")
	cmd.Env = append(os.Environ(), "IMAGEPAD_TEST_EXIT_CODE_HELPER=1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("helper unexpectedly exited successfully")
	}
	if got := processExitDetail(err); got != "exit_code=37" {
		t.Fatalf("processExitDetail() = %q, want exit_code=37", got)
	}
}

func TestExplicitStopInitiatorKeepsFirstCaller(t *testing.T) {
	done := make(chan struct{})
	close(done)
	manager := New(nil)
	manager.running = true
	manager.cancel = func() {}
	manager.done = done

	if !manager.StopForUser(time.Second) {
		t.Fatal("first explicit stop did not finish")
	}
	if !manager.StopForServerShutdown(time.Second) {
		t.Fatal("second explicit stop did not finish")
	}
	if got := manager.sourceClockStopInitiator(); got != airplaycontract.TerminationReasonUserStop {
		t.Fatalf("stop initiator=%q, want first caller %q", got, airplaycontract.TerminationReasonUserStop)
	}
}

func TestSourceClockTerminationInitiatorPrefersExplicitStopOverFallback(t *testing.T) {
	manager := New(nil)
	manager.stopInitiator = airplaycontract.TerminationReasonUserStop
	if got := manager.sourceClockTerminationInitiator(airplaycontract.TerminationReasonNoSignalTimeout); got != airplaycontract.TerminationReasonUserStop {
		t.Fatalf("resolved initiator=%q, want explicit user stop", got)
	}
	manager.stopInitiator = ""
	if got := manager.sourceClockTerminationInitiator(airplaycontract.TerminationReasonNoSignalTimeout); got != airplaycontract.TerminationReasonNoSignalTimeout {
		t.Fatalf("resolved initiator=%q, want no-signal fallback", got)
	}
	if got := manager.sourceClockTerminationInitiator(""); got != "" {
		t.Fatalf("unclassified initiator=%q, want empty", got)
	}
}

func TestDirectNoSignalExpiresOnlyAfterThreeMinutesWithoutEitherStream(t *testing.T) {
	started := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	video := started.Add(30 * time.Second)
	audio := started.Add(90 * time.Second)
	if directNoSignalExpired(started, video, audio, started.Add(2*time.Minute+59*time.Second)) {
		t.Fatal("timeout fired before three minutes from the latest media activity")
	}
	if !directNoSignalExpired(started, video, audio, audio.Add(3*time.Minute)) {
		t.Fatal("timeout did not fire three minutes after the latest media activity")
	}
}

func TestRTPRelayLastActivityUsesStoredPacketTime(t *testing.T) {
	video := &h264RTPRelay{}
	audio := &l16RTPRelay{}
	if !video.LastActivity().IsZero() || !audio.LastActivity().IsZero() {
		t.Fatal("a relay without packets must report no activity")
	}
	want := time.Now().Add(-time.Second)
	video.lastActivity.Store(want.UnixNano())
	audio.lastActivity.Store(want.Add(250 * time.Millisecond).UnixNano())
	if got := video.LastActivity(); got.IsZero() || got.Before(want.Add(-time.Millisecond)) {
		t.Fatalf("video last activity = %v; want near %v", got, want)
	}
	if got := audio.LastActivity(); got.IsZero() || got.Before(want.Add(200*time.Millisecond)) {
		t.Fatalf("audio last activity = %v; want near %v", got, want.Add(250*time.Millisecond))
	}
}

func TestDirectPublisherRetryPolicy(t *testing.T) {
	if !shouldRetryDirectPublisher(directPublisherMaxRetries) {
		t.Fatalf("retry should still be allowed at the final retry")
	}
	if shouldRetryDirectPublisher(directPublisherMaxRetries + 1) {
		t.Fatalf("retry should stop after the final retry")
	}
	if directPublisherRetryDelay(2) <= directPublisherRetryDelay(1) {
		t.Fatalf("retry delay must back off: first=%s second=%s", directPublisherRetryDelay(1), directPublisherRetryDelay(2))
	}
	if got := directPublisherRetryDelay(directPublisherMaxRetries); got > 4*time.Second {
		t.Fatalf("retry delay grew beyond the cap: %s", got)
	}
}

func TestDirectMonitorKeepsPublisherAliveOnVideoFormatChange(t *testing.T) {
	source, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("read manager source: %v", err)
	}
	text := string(source)
	start := strings.Index(text, "func (m *Manager) monitorDirect(")
	if start < 0 {
		t.Fatal("monitorDirect implementation was not found")
	}
	directMonitor := text[start:]
	for _, forbidden := range []string{
		"formatChanges := relay.FormatChanges()",
		"case <-formatChanges:",
		"stopForRestart()",
		"restarting direct GStreamer publisher",
	} {
		if strings.Contains(directMonitor, forbidden) {
			t.Errorf("direct monitor must keep the publisher alive across format changes; found %q", forbidden)
		}
	}
}

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
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		if !FeatureEnabled() {
			t.Fatal("FeatureEnabled() = false without explicit opt-in on supported Windows")
		}
	} else if FeatureEnabled() {
		t.Fatal("FeatureEnabled() = true on unsupported platform")
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
	for _, flag := range []string{"-vrtp", "-artp"} {
		index := indexOfArg(args, flag)
		if index < 0 || index+1 >= len(args) || !strings.Contains(args[index+1], "	!	udpsink	") {
			t.Errorf("%s RTP pipeline must use tab separators: %q", flag, args)
		}
	}
	if !containsArgPair(args, "-fps", "60") {
		t.Errorf("receiver args must request 60 fps: %q", args)
	}
	if !containsArgPair(args, "-vs", "0") {
		t.Errorf("receiver args must use the working headless UxPlay video setting: %q", args)
	}
}

func TestBuildReceiverArgsEnablesBoundedDiagnostics(t *testing.T) {
	t.Setenv("IMAGEPAD_AIRPLAY_RTP_DEBUG", "1")
	args := BuildReceiverArgs(41001, 41002, "Test Receiver")
	if !containsArgPair(args, "-d", "1") {
		t.Fatalf("diagnostic receiver args must suppress packet-level debug noise: %q", args)
	}
	if !containsArgValue(args, "-FPSdata") {
		t.Fatalf("diagnostic receiver args must request client FPS reports: %q", args)
	}
}

func containsArgValue(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func TestBuildGStreamerBridgeArgsUsesDedicatedRTPPorts(t *testing.T) {
	args := BuildGStreamerBridgeArgs(42001, 42003)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--video-port 42001",
		"--audio-port 42003",
		"--stdout",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in %q", expected, joined)
		}
	}
	if strings.Contains(joined, "-vrtp") || strings.Contains(joined, "-artp") {
		t.Fatalf("bridge helper args must not contain UxPlay receiver args: %q", joined)
	}
}

func TestBuildGStreamerFFmpegArgsReadsOneMuxedInput(t *testing.T) {
	encoder := video.CPUVideoEncoder(video.EncoderLowLatency)
	preset := video.ResolveQualityForUpload("1080", 20, 0)
	args := BuildGStreamerFFmpegArgs("rtmp://127.0.0.1:1935/live/test", encoder, preset)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"-f matroska",
		"-i pipe:0",
		"-map 0:v:0",
		"-map 0:a:0",
		"-c:a aac",
		"-ar 48000",
		"-f flv",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in %q", expected, joined)
		}
	}
	if countArg(args, "-i") != 1 {
		t.Fatalf("expected one muxed input, got %d in %q", countArg(args, "-i"), joined)
	}
	if strings.Contains(joined, "session.sdp") || strings.Contains(joined, "-protocol_whitelist") {
		t.Fatalf("GStreamer bridge FFmpeg args must not use the old SDP/RTP input: %q", joined)
	}
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func countArg(args []string, want string) int {
	count := 0
	for _, arg := range args {
		if arg == want {
			count++
		}
	}
	return count
}

func indexOfArg(args []string, want string) int {
	for index, arg := range args {
		if arg == want {
			return index
		}
	}
	return -1
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
		"-reorder_queue_size 0",
		"-analyzeduration 2000000",
		"-probesize 5000000",
		"-fflags +genpts+discardcorrupt",
		"-i session.sdp",
		"-map 0:v:0",
		"-map 0:a:0",
		"-vf scale=1920:1080:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1",
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
const helperProcessArgsEnv = "IMAGEPAD_AIRPLAY_TEST_HELPER_ARGS"
const helperSourceClockReadyMarkerEnv = "IMAGEPAD_AIRPLAY_TEST_SOURCE_CLOCK_READY_MARKER"
const helperSourceClockReadyAckEnv = "IMAGEPAD_AIRPLAY_TEST_SOURCE_CLOCK_READY_ACK"

func init() {
	if os.Getenv(helperProcessEnv) == "1" {
		args := os.Args[1:]
		if path := os.Getenv(helperProcessArgsEnv); path != "" {
			data, err := json.Marshal(args)
			if err != nil {
				os.Exit(95)
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(96)
			}
			_, writeErr := file.Write(append(data, '\n'))
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				os.Exit(97)
			}
		}
		if markerPath := os.Getenv(helperSourceClockReadyMarkerEnv); markerPath != "" {
			if readyPath := commandArgValue(args, "--ready-file"); readyPath != "" {
				if err := os.WriteFile(markerPath, []byte("publisher-started"), 0600); err != nil {
					os.Exit(98)
				}
				ackPath := os.Getenv(helperSourceClockReadyAckEnv)
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(ackPath); err == nil {
						break
					}
					if time.Now().After(deadline) {
						os.Exit(99)
					}
					time.Sleep(5 * time.Millisecond)
				}
				generation, err := strconv.ParseUint(commandArgValue(args, "--publisher-generation"), 10, 64)
				if err != nil {
					os.Exit(100)
				}
				ready := airplaycontract.Event{
					Schema: 2, SessionID: commandArgValue(args, "--session-id"), PublisherGeneration: generation,
					Event: "publisher-ready", At: time.Now().UTC(), ProtocolVersion: 1,
					VideoListenPort: 41001, AudioListenPort: 41002, PipelineStartAccepted: true,
				}
				data, err := json.Marshal(ready)
				if err != nil || os.WriteFile(readyPath, data, 0600) != nil {
					os.Exit(101)
				}
				stopPath := commandArgValue(args, "--stop-file")
				deadline = time.Now().Add(30 * time.Second)
				for {
					if _, err := os.Stat(stopPath); err == nil {
						os.Exit(0)
					}
					if time.Now().After(deadline) {
						os.Exit(102)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}
		time.Sleep(30 * time.Second)
	}
}

func TestDiagnosticReceiverNameOverridesUxPlayArgumentAndStatus(t *testing.T) {
	t.Setenv("IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME", "  Candidate   8123  ")
	name, statusName := startManagerAndCaptureReceiverName(t)
	if name != "Candidate-8123" {
		t.Fatalf("UxPlay -n value = %q, want %q", name, "Candidate-8123")
	}
	if statusName != name {
		t.Fatalf("status receiverName = %q, want launched name %q", statusName, name)
	}
}

func TestDefaultReceiverNameRemainsProductionIdentity(t *testing.T) {
	unsetEnvironmentForTest(t, "IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME")
	name, statusName := startManagerAndCaptureReceiverName(t)
	if name != "ImagePadServer-AirPlay" {
		t.Fatalf("default UxPlay -n value = %q, want exact production identity", name)
	}
	if statusName != name {
		t.Fatalf("default status receiverName = %q, want launched name %q", statusName, name)
	}
}

func TestLegacyStartDirectPropagatesDiagnosticReceiverNameToUxPlayAndStatus(t *testing.T) {
	t.Setenv(envDiagnosticName, "  Legacy   Direct   Candidate  ")
	t.Setenv(envFeatureFlag, "1")
	t.Setenv(envUxPlayPath, os.Args[0])
	t.Setenv(envAirPlayPipeline, "gstreamer-direct")
	t.Setenv(envAirPlayGStreamerBridgePath, os.Args[0])
	t.Setenv(helperProcessEnv, "1")
	argsPath := filepath.Join(t.TempDir(), "direct-child-args.jsonl")
	t.Setenv(helperProcessArgsEnv, argsPath)

	m := New(nil)
	if err := m.StartDirect(t.Context(), "legacy-direct-name", "rtsp://127.0.0.1:8554/test", filepath.Join(t.TempDir(), "recording.mp4"), DirectOutputConfig{}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !m.Stop(5 * time.Second) {
			t.Error("legacy direct manager did not stop child processes before timeout")
		}
	})

	name := waitForReceiverNameArgument(t, argsPath)
	statusName := managerStatusReceiverName(t, m)
	if name != "Legacy-Direct-Candidate" {
		t.Fatalf("legacy StartDirect UxPlay -n value = %q, want %q", name, "Legacy-Direct-Candidate")
	}
	if statusName != name {
		t.Fatalf("legacy StartDirect status receiverName = %q, want launched name %q", statusName, name)
	}
}

func TestInvalidExplicitDiagnosticReceiverNamesFailClosed(t *testing.T) {
	for _, value := range []string{"", "-candidate", "candidate/name", "candidaté", strings.Repeat("a", 64)} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(envFeatureFlag, "1")
			t.Setenv(envUxPlayPath, os.Args[0])
			t.Setenv("IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME", value)
			err := New(nil).Start(t.Context(), os.Args[0], "invalid-publish-url")
			if err == nil || !strings.Contains(err.Error(), "IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME") {
				t.Fatalf("Start error = %v, want diagnostic receiver-name validation error", err)
			}
		})
	}
}

func startManagerAndCaptureReceiverName(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv(envFeatureFlag, "1")
	t.Setenv(envUxPlayPath, os.Args[0])
	t.Setenv(helperProcessEnv, "1")
	argsPath := filepath.Join(t.TempDir(), "child-args.jsonl")
	t.Setenv(helperProcessArgsEnv, argsPath)

	m := New(nil)
	if err := m.Start(t.Context(), os.Args[0], "rtmp://127.0.0.1:1935/live/test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !m.Stop(5 * time.Second) {
			t.Error("manager did not stop child processes before timeout")
		}
	})

	name := waitForReceiverNameArgument(t, argsPath)
	return name, managerStatusReceiverName(t, m)
}

func waitForReceiverNameArgument(t *testing.T, argsPath string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(argsPath)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var args []string
				if json.Unmarshal([]byte(line), &args) == nil {
					if name := receiverNameArgument(args); name != "" {
						return name
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("UxPlay child arguments were not captured from %s", argsPath)
	return ""
}

func managerStatusReceiverName(t *testing.T, m *Manager) string {
	t.Helper()
	statusData, err := json.Marshal(m.Status())
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatal(err)
	}
	statusName, _ := status["receiverName"].(string)
	return statusName
}

func receiverNameArgument(args []string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-n" {
			return args[index+1]
		}
	}
	return ""
}

func unsetEnvironmentForTest(t *testing.T, key string) {
	t.Helper()
	value, present := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	})
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

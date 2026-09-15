package airplay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

const sourceClockStartupExitHelperEnv = "IMAGEPAD_TEST_SOURCE_CLOCK_STARTUP_EXIT"
const sourceClockStartupReceiverMarkerEnv = "IMAGEPAD_TEST_SOURCE_CLOCK_STARTUP_RECEIVER_MARKER"

func init() {
	if os.Getenv(sourceClockStartupExitHelperEnv) != "1" {
		return
	}
	args := os.Args[1:]
	if commandArgValue(args, "--ready-file") != "" {
		token := commandArgValue(args, "--session-token")
		publishURL := commandArgValue(args, "--publish-url")
		_, _ = os.Stderr.WriteString("pipeline construction failed\r\ntoken=" + token + "\tpublish-url=" + publishURL + "\n")
		os.Exit(23)
	}
	if commandArgValue(args, "-ipscv") != "" {
		if marker := os.Getenv(sourceClockStartupReceiverMarkerEnv); marker != "" {
			_ = os.WriteFile(marker, []byte("receiver-started"), 0600)
		}
		os.Exit(24)
	}
}

func TestBuildSourceClockReceiverArgsAvoidsRTPRelays(t *testing.T) {
	args := BuildSourceClockReceiverArgs("127.0.0.1:41001", "127.0.0.1:41002", sourceClockToken(strings.Repeat("ab", 16)), "receiver-test-1", "ImagePadServer AirPlay")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-ipscv 127.0.0.1:41001", "-ipsca 127.0.0.1:41002", "-ipsct " + strings.Repeat("ab", 16), "-ipscid receiver-test-1", "-vs 0", "-as 0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args=%q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "-vrtp") || strings.Contains(joined, "-artp") {
		t.Fatalf("source-clock args unexpectedly contain RTP relay options: %q", joined)
	}
	if countArg(args, "-ipscid") != 1 || commandArgValue(args, "-ipscid") != "receiver-test-1" {
		t.Fatalf("source-clock args must contain one receiver ID: %q", args)
	}
	if commandArgValue(args, "-ipscid") == commandArgValue(args, "-ipsct") {
		t.Fatalf("receiver ID reused the source-clock token: %q", args)
	}
}

func TestBuildSourceClockReceiverArgsEnablesRequestDiagnostics(t *testing.T) {
	args := BuildSourceClockReceiverArgs("127.0.0.1:41001", "127.0.0.1:41002", sourceClockToken(strings.Repeat("ab", 16)), "receiver-test-1", "ImagePadServer AirPlay")
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-d" && args[i+1] == "1" {
			return
		}
	}
	t.Fatalf("source-clock args must enable UxPlay request diagnostics: %q", args)
}

func TestNewSourceClockReceiverIDFromUsesIndependentBoundedEncoding(t *testing.T) {
	seed := make([]byte, 16)
	for index := range seed {
		seed[index] = byte(index)
	}
	receiverID, err := newSourceClockReceiverIDFrom(bytes.NewReader(seed))
	if err != nil {
		t.Fatal(err)
	}
	if receiverID != "receiver-000102030405060708090a0b0c0d0e0f" || !validSourceClockReceiverID(string(receiverID)) {
		t.Fatalf("unexpected receiver ID: %q", receiverID)
	}
	if _, err := newSourceClockReceiverIDFrom(bytes.NewReader(seed[:15])); err == nil {
		t.Fatal("short receiver ID entropy was accepted")
	}
	if _, err := newSourceClockReceiverIDFrom(nil); err == nil {
		t.Fatal("nil receiver ID entropy reader was accepted")
	}
}

func TestWithSourceClockReceiverIDGeneratesOnceBeforeStartingChildren(t *testing.T) {
	t.Setenv("IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME", "  Source   Clock   Candidate  ")
	resolvedName, resolveErr := resolveReceiverTitle(defaultReceiverTitle)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	const token = "abababababababababababababababab"
	const receiverID = "receiver-000102030405060708090a0b0c0d0e0f"
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER_ARGUMENT_CAPTURE", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER_EMIT_METRICS", "1")
	markerPath := filepath.Join(t.TempDir(), "receiver-started.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER_ARGUMENT_CAPTURE_MARKER", markerPath)
	generateCalls := 0
	startCalls := 0
	var capturedArgs []string
	err := withSourceClockReceiverID(
		func() (sourceClockReceiverID, error) {
			generateCalls++
			return receiverID, nil
		},
		func(gotReceiverID sourceClockReceiverID) error {
			startCalls++
			factory := func(ctx context.Context, path string, args ...string) *exec.Cmd {
				capturedArgs = append([]string(nil), args...)
				return exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverArgumentCaptureProcess$")
			}
			receiver, receiverOutput, metricsGate, _, err := startSourceClockReceiverChild(context.Background(), os.Args[0], "127.0.0.1:41001", "127.0.0.1:41002", token, gotReceiverID, resolvedName, t.TempDir(), "", factory)
			if err != nil {
				return err
			}
			if err := receiver.Wait(); err != nil {
				_ = receiverOutput.Close()
				return err
			}
			if err := receiverOutput.Close(); err != nil {
				return err
			}
			if stats := metricsGate.snapshot(); !stats.HasMetrics || stats.Latest.ReceiverID != string(receiverID) {
				t.Fatalf("live child metrics were not accepted: %#v", stats)
			}
			if logText := receiverOutput.log.String(); !strings.Contains(logText, "ordinary child stdout log") || !strings.Contains(logText, "ordinary child stderr log") || !strings.Contains(logText, sourceClockMetricsPrefix) {
				t.Fatalf("live child output was not preserved in the bounded log: %q", logText)
			}
			before := metricsGate.snapshot()
			savedOnly := strings.Replace(canonicalSourceClockMetricsLine, `"receiverId":"receiver-1"`, `"receiverId":"receiver-000102030405060708090a0b0c0d0e0f"`, 1)
			savedOnly = strings.Replace(savedOnly, `"sequence":7`, `"sequence":8`, 1)
			if _, err := receiverOutput.log.Write([]byte(savedOnly + "\n")); err != nil {
				t.Fatal(err)
			}
			after := metricsGate.snapshot()
			if after.Latest.Sequence != before.Latest.Sequence || !after.LastAcceptedAt.Equal(before.LastAcceptedAt) {
				t.Fatalf("saved bounded log was parsed as live activity: before=%#v after=%#v", before, after)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if generateCalls != 1 || startCalls != 1 {
		t.Fatalf("generate calls=%d start calls=%d, want 1 and 1", generateCalls, startCalls)
	}
	if countArg(capturedArgs, "-ipscid") != 1 || commandArgValue(capturedArgs, "-ipscid") != string(receiverID) {
		t.Fatalf("source-clock receiver ID was not wired to one -ipscid argument at child start: %q", capturedArgs)
	}
	if commandArgValue(capturedArgs, "-ipscid") == commandArgValue(capturedArgs, "-ipsct") {
		t.Fatalf("source-clock receiver ID reused the source-clock token at child start: %q", capturedArgs)
	}
	if got := commandArgValue(capturedArgs, "-n"); got != "Source-Clock-Candidate" {
		t.Fatalf("source-clock UxPlay -n value = %q, want diagnostic identity", got)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("receiver child did not start: %v", err)
	}
}

func TestStartSourceClockReceiverChildUsesAlreadyResolvedReceiverName(t *testing.T) {
	t.Setenv(envDiagnosticName, "  Outer   Candidate  ")
	resolvedName, err := resolveReceiverTitle(defaultReceiverTitle)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(envDiagnosticName, "Mutated-Candidate")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER_ARGUMENT_CAPTURE", "1")
	markerPath := filepath.Join(t.TempDir(), "receiver-started.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER_ARGUMENT_CAPTURE_MARKER", markerPath)
	var capturedArgs []string
	factory := func(ctx context.Context, path string, args ...string) *exec.Cmd {
		capturedArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverArgumentCaptureProcess$")
	}
	receiver, receiverOutput, _, _, err := startSourceClockReceiverChild(
		t.Context(), os.Args[0], "127.0.0.1:41001", "127.0.0.1:41002",
		sourceClockToken(strings.Repeat("ab", 16)), "receiver-resolved-name", resolvedName,
		t.TempDir(), "", factory,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Wait(); err != nil {
		_ = receiverOutput.Close()
		t.Fatal(err)
	}
	if err := receiverOutput.Close(); err != nil {
		t.Fatal(err)
	}
	if got := commandArgValue(capturedArgs, "-n"); got != "Outer-Candidate" {
		t.Fatalf("source-clock child re-resolved receiver name: got %q, want %q", got, "Outer-Candidate")
	}
}

func TestWithSourceClockReceiverIDFailsBeforeStartingChildren(t *testing.T) {
	wantErr := errors.New("entropy unavailable")
	startCalls := 0
	err := withSourceClockReceiverID(
		func() (sourceClockReceiverID, error) { return "", wantErr },
		func(sourceClockReceiverID) error {
			startCalls++
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if startCalls != 0 {
		t.Fatalf("child starter was called %d times after receiver ID generation failed", startCalls)
	}
}

func TestWithSourceClockReceiverIDRejectsInvalidIDBeforeStartingChildren(t *testing.T) {
	startCalls := 0
	err := withSourceClockReceiverID(
		func() (sourceClockReceiverID, error) { return "receiver invalid", nil },
		func(sourceClockReceiverID) error {
			startCalls++
			return nil
		},
	)
	if err == nil {
		t.Fatal("invalid receiver ID was accepted")
	}
	if startCalls != 0 {
		t.Fatalf("child starter was called %d times for an invalid receiver ID", startCalls)
	}
}

func TestStartSourceClockReceiverChildClosesOutputAfterStartFailure(t *testing.T) {
	oldOpen := openSourceClockReceiverOutputFile
	file := &sourceClockReceiverChildCleanupTestFile{}
	openSourceClockReceiverOutputFile = func(string) (sourceClockReceiverOutputFile, error) { return file, nil }
	t.Cleanup(func() { openSourceClockReceiverOutputFile = oldOpen })

	factory := func(ctx context.Context, path string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-source-clock-receiver.exe"))
	}
	_, _, _, _, err := startSourceClockReceiverChild(
		context.Background(), os.Args[0], "127.0.0.1:41001", "127.0.0.1:41002",
		"abababababababababababababababab", "receiver-start-failure", "ImagePadServer AirPlay",
		t.TempDir(), "injected", factory,
	)
	if err == nil {
		t.Fatal("missing receiver executable unexpectedly started")
	}
	if !file.isClosed() {
		t.Fatal("receiver output did not finish framer shutdown and close its diagnostic file after Start failed")
	}
}

func callWaitSourceClockReadyForTest(t *testing.T, path, sessionID string, generation uint64) (airplaycontract.Event, error) {
	t.Helper()

	wait := reflect.ValueOf(waitSourceClockReady)
	var inputs []reflect.Value
	switch wait.Type().NumIn() {
	case 2: // Pre-migration signature, retained only so this test reaches the legacy reader in RED.
		inputs = []reflect.Value{reflect.ValueOf(context.Background()), reflect.ValueOf(path)}
	case 4:
		inputs = []reflect.Value{
			reflect.ValueOf(context.Background()),
			reflect.ValueOf(path),
			reflect.ValueOf(sessionID),
			reflect.ValueOf(generation),
		}
	default:
		t.Fatalf("waitSourceClockReady has %d inputs, want the legacy 2 or schema-2 4", wait.Type().NumIn())
	}

	outputs := wait.Call(inputs)
	if len(outputs) != 2 {
		t.Fatalf("waitSourceClockReady returned %d values, want 2", len(outputs))
	}
	var event airplaycontract.Event
	if candidate, ok := outputs[0].Interface().(airplaycontract.Event); ok {
		event = candidate
	}
	if outputs[1].IsNil() {
		return event, nil
	}
	err, ok := outputs[1].Interface().(error)
	if !ok {
		t.Fatalf("waitSourceClockReady second result is %T, want error", outputs[1].Interface())
	}
	return event, err
}

func writeReadyEvent(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "publisher-ready.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWaitSourceClockReadyAcceptsMatchingSchema2Event(t *testing.T) {
	path := writeReadyEvent(t, `{"schema":2,"sessionId":"obs-session","publisherGeneration":3,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":41001,"audioListenPort":41002,"pipelineStartAccepted":true}`)

	ready, err := callWaitSourceClockReadyForTest(t, path, "obs-session", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !ready.ReadyFor("obs-session", 3) {
		t.Fatalf("ready=%+v does not match the expected schema-2 publisher", ready)
	}
}

func TestWaitSourceClockReadyPrioritizesBufferedPublisherExitOverValidReady(t *testing.T) {
	path := writeReadyEvent(t, `{"schema":2,"sessionId":"obs-session","publisherGeneration":3,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":41001,"audioListenPort":41002,"pipelineStartAccepted":true}`)
	publisherErr := errors.New("buffered publisher exit")
	publisherDone := make(chan error, 1)
	publisherDone <- publisherErr
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := waitSourceClockReadyWithPublisher(ctx, path, "obs-session", 3, publisherDone, time.Hour)
	if err == nil {
		t.Fatal("valid ready won over a buffered publisher exit")
	}
	var exited *sourceClockPublisherExitedBeforeReadyError
	if !errors.As(err, &exited) {
		t.Fatalf("wait error = %T %v, want publisher-exited-before-ready", err, err)
	}
	if !errors.Is(err, publisherErr) {
		t.Fatalf("wait error = %v, want buffered publisher cause %v", err, publisherErr)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation won over buffered publisher exit: %v", err)
	}
}

func TestWaitSourceClockReadyRejectsWrongIdentityAndSchema1(t *testing.T) {
	tests := []struct {
		name       string
		contents   string
		sessionID  string
		generation uint64
	}{
		{
			name:       "schema 1",
			contents:   `{"schema":1,"protocolVersion":1,"videoListenPort":41001,"audioListenPort":41002,"pipelinePlaying":true}`,
			sessionID:  "obs-session",
			generation: 3,
		},
		{
			name:       "another session",
			contents:   `{"schema":2,"sessionId":"other-session","publisherGeneration":3,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":41001,"audioListenPort":41002,"pipelineStartAccepted":true}`,
			sessionID:  "obs-session",
			generation: 3,
		},
		{
			name:       "old generation",
			contents:   `{"schema":2,"sessionId":"obs-session","publisherGeneration":2,"event":"publisher-ready","at":"2026-09-05T00:00:00Z","protocolVersion":1,"videoListenPort":41001,"audioListenPort":41002,"pipelineStartAccepted":true}`,
			sessionID:  "obs-session",
			generation: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeReadyEvent(t, tt.contents)
			if _, err := callWaitSourceClockReadyForTest(t, path, tt.sessionID, tt.generation); err == nil {
				t.Fatal("incompatible publisher-ready event was accepted")
			}
		})
	}
}

func TestStartSourceClockDirectReportsPublisherExitBeforeReadyWithoutStartingReceiver(t *testing.T) {
	t.Setenv(envFeatureFlag, "1")
	t.Setenv(envAirPlayPipeline, "source-clock")
	t.Setenv(sourceClockStartupExitHelperEnv, "1")
	receiverPath := sourceClockReceiverExecutableForLaunchTest(t)
	t.Setenv(envUxPlayPath, receiverPath)
	t.Setenv(envReceiverPath, "")
	t.Setenv(envAirPlayGStreamerBridgePath, receiverPath)
	receiverMarker := filepath.Join(t.TempDir(), "receiver-started")
	t.Setenv(sourceClockStartupReceiverMarkerEnv, receiverMarker)

	const publishURL = "rtsp://source-user:source-pass@127.0.0.1:8554/sensitive-path"
	manager := New(nil)
	started := time.Now()
	err := manager.StartDirect(
		t.Context(), "startup-early-exit", publishURL,
		filepath.Join(t.TempDir(), "recording.mp4"), DirectOutputConfig{}, nil,
	)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("publisher exit before ready unexpectedly started source-clock")
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("publisher exit was not detected promptly: elapsed=%s err=%v", elapsed, err)
	}
	message := err.Error()
	for _, want := range []string{"exit_code=23", "pipeline construction failed", "token=[REDACTED]", "publish-url=[REDACTED]"} {
		if !strings.Contains(message, want) {
			t.Fatalf("startup error %q does not contain %q", message, want)
		}
	}
	for _, secret := range []string{"source-user", "source-pass", "sensitive-path"} {
		if strings.Contains(message, secret) {
			t.Fatalf("startup error exposed %q: %q", secret, message)
		}
	}
	if _, statErr := os.Stat(receiverMarker); !os.IsNotExist(statErr) {
		t.Fatalf("UxPlay started before publisher readiness: marker error=%v", statErr)
	}
	if status := manager.Status(); status.Running {
		t.Fatalf("failed source-clock startup reported running: %+v", status)
	}
}

func TestNewSourceClockTokenIsExactlySixteenBytesHex(t *testing.T) {
	token, err := newSourceClockToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 32 {
		t.Fatalf("token length=%d", len(token))
	}
	if strings.Trim(string(token), "0123456789abcdef") != "" {
		t.Fatalf("token is not lowercase hex: %q", token)
	}
}

func TestValidateSourceClockReceiverCapabilitiesChecksBinaryHashAndFeatures(t *testing.T) {
	dir := t.TempDir()
	receiver := filepath.Join(dir, "uxplay-source-clock.exe")
	if err := os.WriteFile(receiver, []byte("patched receiver"), 0600); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(receiver)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	manifest := sourceClockCapabilities{
		Schema: 1, ProtocolVersion: 1, Binary: filepath.Base(receiver),
		BinarySHA256: hex.EncodeToString(sum[:]),
		Features:     []string{"video-au", "audio-frame", "remote-ntp", "bounded-writer", "idle-wait", "audio-format-lock", "egress-metrics-v2"},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "imagepad-source-clock-capabilities.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceClockReceiverCapabilities(receiver); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"idle-wait", "audio-format-lock", "egress-metrics-v2"} {
		original := append([]string(nil), manifest.Features...)
		manifest.Features = nil
		for _, feature := range original {
			if feature != required {
				manifest.Features = append(manifest.Features, feature)
			}
		}
		data, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "imagepad-source-clock-capabilities.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateSourceClockReceiverCapabilities(receiver); err == nil {
			t.Fatalf("receiver without %s unexpectedly passed capability validation", required)
		}
		manifest.Features = original
	}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "imagepad-source-clock-capabilities.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiver, []byte("tampered receiver"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceClockReceiverCapabilities(receiver); err == nil {
		t.Fatal("tampered receiver unexpectedly passed capability validation")
	}
}

func TestSourceClockReceiverProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

func TestSourceClockReceiverArgumentCaptureProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER_ARGUMENT_CAPTURE") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER_ARGUMENT_CAPTURE_MARKER"), []byte("started"), 0600); err != nil {
		os.Exit(94)
	}
	if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER_EMIT_METRICS") == "1" {
		_, _ = os.Stdout.WriteString("ordinary child stdout log\n")
		_, _ = os.Stderr.WriteString("ordinary child stderr log\n")
		line := strings.Replace(canonicalSourceClockMetricsLine, `"receiverId":"receiver-1"`, `"receiverId":"receiver-000102030405060708090a0b0c0d0e0f"`, 1)
		middle := len(line) / 2
		_, _ = os.Stdout.WriteString(line[:middle])
		time.Sleep(5 * time.Millisecond)
		_, _ = os.Stdout.WriteString(line[middle:] + "\n")
	}
}

type sourceClockReceiverChildCleanupTestFile struct {
	mu     sync.Mutex
	closed bool
}

func (f *sourceClockReceiverChildCleanupTestFile) Write(p []byte) (int, error) { return len(p), nil }

func (f *sourceClockReceiverChildCleanupTestFile) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *sourceClockReceiverChildCleanupTestFile) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func TestSourceClockPublisherProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER") != "1" {
		return
	}
	counterPath := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER")
	count := 0
	if data, err := os.ReadFile(counterPath); err == nil {
		count, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	count++
	if err := os.WriteFile(counterPath, []byte(strconv.Itoa(count)), 0600); err != nil {
		os.Exit(91)
	}
	if argsPath := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_ARGS"); argsPath != "" {
		file, err := os.OpenFile(argsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(92)
		}
		_, writeErr := file.WriteString(strings.Join(os.Args[1:], "\x1f") + "\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			os.Exit(93)
		}
	}
	if rawExitCode := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_CODE"); rawExitCode != "" {
		if generation := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_GENERATION"); generation != "" && generation != commandArgValue(os.Args[1:], "--publisher-generation") {
			// This fixture is intentionally generation-selective so the planned
			// executor can keep the old publisher alive while making a candidate
			// fail immediately.
		} else {
			exitCode, err := strconv.ParseUint(rawExitCode, 0, 32)
			if err != nil {
				os.Exit(94)
			}
			if rawDelay := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_DELAY_MS"); rawDelay != "" {
				delayMS, err := strconv.Atoi(rawDelay)
				if err != nil || delayMS < 0 {
					os.Exit(95)
				}
				time.Sleep(time.Duration(delayMS) * time.Millisecond)
			}
			os.Exit(int(exitCode))
		}
	}
	if count == 1 {
		os.Exit(22)
	}
	stopPath := commandArgValue(os.Args[1:], "--stop-file")
	proofGeneration, _ := strconv.ParseUint(commandArgValue(os.Args[1:], "--publisher-generation"), 10, 64)
	if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_CANDIDATE_PROOF") == "1" && proofGeneration >= 2 {
		session := commandArgValue(os.Args[1:], "--session-id")
		path := commandArgValue(os.Args[1:], "--event-log")
		appendCandidateEventForTest(t, path, sourceClockPublisherReadyForWatermarkTest(session, proofGeneration))
		for _, name := range []string{"video-input-idr", "video-decoded", "video-encoded-idr"} {
			appendCandidateEventForTest(t, path, sourceClockEventForWatermarkTest(name, session, proofGeneration, 7, 99+proofGeneration))
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if crashPath := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_CRASH_REQUEST"); crashPath != "" {
			if data, err := os.ReadFile(crashPath); err == nil && strings.TrimSpace(string(data)) == commandArgValue(os.Args[1:], "--publisher-generation") {
				if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_WATERMARK_ON_CRASH") == "1" {
					appendCandidateEventForTest(t, commandArgValue(os.Args[1:], "--event-log"), sourceClockEventForWatermarkTest("video-watermark-final", commandArgValue(os.Args[1:], "--session-id"), proofGeneration, 7, 99+proofGeneration))
				}
				if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_CRASH_EXIT20") == "1" {
					os.Exit(20)
				}
				os.Exit(22)
			}
		}
		if stopPath != "" {
			if _, err := os.Stat(stopPath); err == nil {
				if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_WRITE_FINALIZED_ON_STOP") == "1" {
					generation, parseErr := strconv.ParseUint(commandArgValue(os.Args[1:], "--publisher-generation"), 10, 64)
					event := airplaycontract.Event{
						Schema: 2, SessionID: commandArgValue(os.Args[1:], "--session-id"), PublisherGeneration: generation,
						Event: "recording-finalized", At: time.Now().UTC(),
						RecordingPath: commandArgValue(os.Args[1:], "--recording"), RecordingClosed: true,
					}
					data, marshalErr := json.Marshal(event)
					if parseErr != nil || marshalErr != nil || os.WriteFile(commandArgValue(os.Args[1:], "--event-log"), append(data, '\n'), 0600) != nil {
						os.Exit(96)
					}
				}
				if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_WATERMARK_ON_STOP") == "1" {
					generation, err := strconv.ParseUint(commandArgValue(os.Args[1:], "--publisher-generation"), 10, 64)
					if err != nil {
						os.Exit(99)
					}
					sequence := uint64(100)
					if generation >= 2 && os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_CANDIDATE_PROOF") == "1" {
						sequence = 99 + generation
					}
					appendCandidateEventForTest(t, commandArgValue(os.Args[1:], "--event-log"),
						sourceClockEventForWatermarkTest("video-watermark-final", commandArgValue(os.Args[1:], "--session-id"), generation, 7, sequence))
				}
				if ackPath := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_STOP_EVENT_ACK"); ackPath != "" {
					ackDeadline := time.Now().Add(5 * time.Second)
					for {
						if _, ackErr := os.Stat(ackPath); ackErr == nil {
							break
						}
						if time.Now().After(ackDeadline) {
							os.Exit(98)
						}
						time.Sleep(5 * time.Millisecond)
					}
				}
				if rawDelay := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_STOP_EXIT_DELAY_MS"); rawDelay != "" {
					delayMS, parseErr := strconv.Atoi(rawDelay)
					if parseErr != nil || delayMS < 0 {
						os.Exit(97)
					}
					time.Sleep(time.Duration(delayMS) * time.Millisecond)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type lockedLogBuffer struct {
	mu      sync.Mutex
	builder strings.Builder
}

type sourceClockArtifactObserverForTest struct {
	managed          bool
	recoveryOwner    airplaycontract.PublisherRecoveryOwner
	mu               sync.Mutex
	root             string
	session          string
	prepared         []airplaycontract.PublisherArtifacts
	events           []airplaycontract.Event
	completions      []airplaycontract.PublisherCompletion
	initiators       []sourceClockTerminationInitiatorForTest
	failPrepareAfter uint64
	sealed           bool
	calls            []string
}

func (o *sourceClockArtifactObserverForTest) ManagedRecoveryOwner() (airplaycontract.PublisherRecoveryOwner, bool) {
	return o.recoveryOwner, o.managed
}

type sourceClockRecoveryOwnerForTest struct {
	recover func(context.Context, airplaycontract.PublisherArtifacts, airplaycontract.PublisherRecoveryExecutor) error
}

func (o sourceClockRecoveryOwnerForTest) RecoverPublisher(ctx context.Context, old airplaycontract.PublisherArtifacts, e airplaycontract.PublisherRecoveryExecutor) error {
	return o.recover(ctx, old, e)
}
func (o sourceClockRecoveryOwnerForTest) PublisherHealthy(airplaycontract.PublisherArtifacts) {}

type sourceClockTerminationInitiatorForTest struct {
	sessionID  string
	generation uint64
	reason     string
}

func (o *sourceClockArtifactObserverForTest) PreparePublisher(ctx context.Context, artifacts airplaycontract.PublisherArtifacts) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	expected := airplaycontract.NewPublisherArtifacts(o.root, o.session, artifacts.Generation)
	if artifacts != expected {
		return errors.New("non-canonical test artifacts")
	}
	if o.failPrepareAfter != 0 && artifacts.Generation >= o.failPrepareAfter {
		return errors.New("injected publisher preparation failure")
	}
	if err := os.MkdirAll(filepath.Dir(artifacts.Recording), 0700); err != nil {
		return err
	}
	o.mu.Lock()
	o.prepared = append(o.prepared, artifacts)
	o.mu.Unlock()
	return nil
}

func (o *sourceClockArtifactObserverForTest) ObservePublisher(event airplaycontract.Event) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.calls = append(o.calls, "observe")
	o.mu.Unlock()
}
func (o *sourceClockArtifactObserverForTest) CompletePublisher(completion airplaycontract.PublisherCompletion) {
	o.mu.Lock()
	o.completions = append(o.completions, completion)
	o.calls = append(o.calls, "complete")
	o.mu.Unlock()
}
func (o *sourceClockArtifactObserverForTest) ObserveTerminationInitiator(sessionID string, generation uint64, reason string) {
	o.mu.Lock()
	registered := false
	for _, artifacts := range o.prepared {
		if artifacts.SessionID == sessionID && artifacts.Generation == generation {
			registered = true
			break
		}
	}
	if !registered {
		o.mu.Unlock()
		return
	}
	o.initiators = append(o.initiators, sourceClockTerminationInitiatorForTest{sessionID: sessionID, generation: generation, reason: reason})
	o.calls = append(o.calls, "initiator")
	o.mu.Unlock()
}
func (o *sourceClockArtifactObserverForTest) SealPublishers() {
	o.mu.Lock()
	o.sealed = true
	o.calls = append(o.calls, "seal")
	o.mu.Unlock()
}
func (*sourceClockArtifactObserverForTest) FinishRecording(airplaycontract.RecordingOutcome) {
}

func (o *sourceClockArtifactObserverForTest) snapshot() []airplaycontract.PublisherArtifacts {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]airplaycontract.PublisherArtifacts(nil), o.prepared...)
}

func (o *sourceClockArtifactObserverForTest) eventSnapshot() []airplaycontract.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]airplaycontract.Event(nil), o.events...)
}

func (o *sourceClockArtifactObserverForTest) completionSnapshot() ([]airplaycontract.PublisherCompletion, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]airplaycontract.PublisherCompletion(nil), o.completions...), o.sealed
}

func (o *sourceClockArtifactObserverForTest) callSnapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.calls...)
}

func (o *sourceClockArtifactObserverForTest) initiatorSnapshot() []sourceClockTerminationInitiatorForTest {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]sourceClockTerminationInitiatorForTest(nil), o.initiators...)
}

func TestMonitorPlannedCandidateImmediateExitDoesNotWaitForReadinessDeadline(t *testing.T) {
	testMonitorPlannedCandidateWatermark(t, "exit")
}

func TestMonitorPlannedCandidateMissingWatermarkNeverStarts(t *testing.T) {
	testMonitorPlannedCandidateWatermark(t, "missing")
}

func TestMonitorPlannedCandidateNativeProofStillRequiresPrivateOutput(t *testing.T) {
	testMonitorPlannedCandidateWatermark(t, "proof")
}

func TestMonitorPlannedCandidateCommitsOwnerValidatedOutputAndKeepsReceiver(t *testing.T) {
	testMonitorPlannedCandidateWatermark(t, "commit")
}

func TestMonitorPlannedCandidateAllowsSecondOwnerIssuedGeneration(t *testing.T) {
	testMonitorPlannedCandidateWatermark(t, "repeat")
}

func TestMonitorPlannedCandidateCrashRecordsExitWithoutLegacyRetry(t *testing.T) {
	testMonitorPlannedCandidateWatermark(t, "commit-crash")
}

func TestMonitorPlannedCandidateFailureRecoversWithFreshOwnedPublisher(t *testing.T) {
	testMonitorPlannedCandidateWatermark(t, "compensate")
}

func TestMonitorManagedPublisherRecoveryKeepsReceiverAndOwnsGenerations(t *testing.T) {
	for _, mode := range []string{"managed-initial", "managed-recover", "managed-missing-watermark", "managed-exit20"} {
		t.Run(mode, func(t *testing.T) { testMonitorPlannedCandidateWatermark(t, mode) })
	}
}

func testMonitorPlannedCandidateWatermark(t *testing.T, mode string) {
	withWatermark := mode != "missing"
	managed := strings.HasPrefix(mode, "managed-")
	committing := mode == "commit" || mode == "repeat" || mode == "commit-crash" || managed
	outputFailure := errors.New("private output validation failed")
	crashPath := filepath.Join(t.TempDir(), "crash.request")
	if mode == "commit-crash" || managed {
		t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_CRASH_REQUEST", crashPath)
	}
	if managed && mode != "managed-missing-watermark" {
		t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_WATERMARK_ON_CRASH", "1")
	}
	if mode == "managed-exit20" {
		t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_CRASH_EXIT20", "1")
	}
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	if mode != "proof" && !committing && mode != "compensate" {
		t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_CODE", "22")
	} else {
		t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_CANDIDATE_PROOF", "1")
	}
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_GENERATION", "2")
	if withWatermark {
		t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_WATERMARK_ON_STOP", "1")
	}
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_ARGS", argsPath)
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	if err := os.WriteFile(counterPath, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	token := strings.Repeat("ab", 16)
	old := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	candidate := airplaycontract.NewPublisherArtifacts(root, sessionID, 2)
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
	recoveryDone := make(chan error, 1)
	if managed {
		observer.managed = true
		observer.recoveryOwner = sourceClockRecoveryOwnerForTest{recover: func(ownerCtx context.Context, old airplaycontract.PublisherArtifacts, e airplaycontract.PublisherRecoveryExecutor) error {
			expected := uint64(2)
			output := DirectOutputConfig{Width: 1920, Height: 1080, SourceFPS: 60, OutputFPS: 30, GOPFrames: 60}
			if mode == "managed-initial" {
				expected = 1
				output.Width = 1280
				output.Height = 720
				output.GOPFrames = 30
			}
			if old.Generation != expected {
				return errors.New("recovery received stale publisher")
			}
			recovery := SourceClockDeliveryRequest{RequestID: "owner-crash-recovery", ExpectedActiveGeneration: expected, CandidateGeneration: expected + 1, PublishURL: "rtsp://127.0.0.1/recovered", Output: output, Artifacts: airplaycontract.NewPublisherArtifacts(root, sessionID, expected+1)}
			err := observer.PreparePublisher(ownerCtx, recovery.Artifacts)
			if err == nil {
				err = e.RecoverSourceClockDelivery(ownerCtx, bindSourceClockCandidateForTest(recovery))
			}
			recoveryDone <- err
			return err
		}}
	}
	if err := observer.PreparePublisher(ctx, old); err != nil {
		t.Fatal(err)
	}
	if mode != "managed-initial" {
		if err := observer.PreparePublisher(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}
	oldArgs := buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: 43101, AudioListenPort: 43102,
		PublishURL: "rtsp://127.0.0.1/old", SessionToken: token,
		Artifacts: old,
		Output:    DirectOutputConfig{Width: 1280, Height: 720, SourceFPS: 60, OutputFPS: 30, GOPFrames: 30},
	})
	oldArgs = append([]string{"-test.run=TestSourceClockPublisherProcess$", "fixture"}, oldArgs...)
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], oldArgs, old.StopRequest)
	if err != nil {
		_ = receiver.Process.Kill()
		_ = receiver.Wait()
		t.Fatal(err)
	}

	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.deliveryRetry = make(chan struct{}, 1)
	manager.deliveryReconfigure = make(chan sourceClockDeliveryCommand, 1)
	manager.deliverySessionID = sessionID
	manager.deliveryActiveGeneration = 1
	manager.deliveryActivePublishURL = "rtsp://127.0.0.1/old"
	manager.deliveryActiveOutput = DirectOutputConfig{Width: 1280, Height: 720, SourceFPS: 60, OutputFPS: 30, GOPFrames: 30}
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true, MediaReady: true, MediaReadyKnown: true}
	gate, err := newSourceClockMetricsGate("planned-candidate-exit")
	if err != nil {
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		t.Fatal(err)
	}
	go manager.monitorSourceClockWithPublisherDoneAt(
		ctx, cancel, done, pipeline, nil, receiver, receiverOutput, gate,
		old.Ready, old.MediaReady, sessionID, 1, root, observer,
		3*time.Minute, time.Now(), nil, nil, root, nil,
	)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if receiver.Process != nil {
				_ = receiver.Process.Kill()
			}
		}
	}()

	if mode == "managed-initial" {
		if err := os.WriteFile(crashPath, []byte("1"), 0600); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-recoveryDone:
			if err != nil {
				t.Fatalf("initial managed recovery: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("initial publisher used no managed recovery")
		}
		manager.mu.Lock()
		generation, height := manager.deliveryActiveGeneration, manager.deliveryActiveOutput.Height
		manager.mu.Unlock()
		if generation != 2 || height != 720 || !manager.Status().ReceiverRunning || !manager.Status().MediaReady {
			t.Fatal("initial owned recovery lost session/output")
		}
		data, err := os.ReadFile(counterPath)
		if err != nil || strings.TrimSpace(string(data)) != "3" {
			t.Fatalf("initial recovery publisher starts=%s %v", data, err)
		}
		return
	}
	request := SourceClockDeliveryRequest{
		RequestID:                        "candidate-exit",
		ExpectedActiveGeneration:         1,
		CandidateGeneration:              2,
		PublishURL:                       "rtsp://127.0.0.1/new",
		Output:                           DirectOutputConfig{Width: 1920, Height: 1080, SourceFPS: 60, OutputFPS: 30, GOPFrames: 60},
		Artifacts:                        candidate,
		allowEvidenceInsufficientForTest: true,
	}
	if committing || mode == "compensate" {
		request.allowEvidenceInsufficientForTest = false
		owner := bindSourceClockCandidateForTest(request)
		if mode == "compensate" {
			owner.validate = func(context.Context, []airplaycontract.Event, []airplaycontract.Event) error { return outputFailure }
		}
		request.CandidateDelivery = owner
	}
	command := sourceClockDeliveryCommand{request: request, reply: make(chan error, 1), identity: &struct{}{}}
	manager.mu.Lock()
	manager.deliveryReconfigurePending = true
	manager.deliveryReconfigureRequestID = request.RequestID
	manager.deliveryReconfigureReply = command.reply
	manager.mu.Unlock()
	manager.deliveryReconfigure <- command
	started := time.Now()
	err = <-command.reply
	if time.Since(started) >= sourceClockReadyTimeout {
		t.Fatalf("candidate exit waited for readiness deadline: %s", time.Since(started))
	}
	var exited *sourceClockPublisherExitedBeforeReadyError
	if mode == "exit" && !errors.As(err, &exited) {
		t.Fatalf("planned candidate error=%v, want candidate exit", err)
	}
	if !withWatermark && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing watermark error=%v", err)
	}
	if mode == "proof" && !errors.Is(err, ErrDeliveryReconfigureEvidenceInsufficient) {
		t.Fatalf("complete native proof must reach private-output gate: %v", err)
	}
	if mode == "compensate" && !errors.Is(err, outputFailure) {
		t.Fatalf("candidate failure=%v", err)
	}
	if committing {
		if err != nil {
			t.Fatalf("owner-validated candidate failed to become active: %v", err)
		}
		manager.mu.Lock()
		generation, activeURL, output := manager.deliveryActiveGeneration, manager.deliveryActivePublishURL, manager.deliveryActiveOutput
		manager.mu.Unlock()
		if generation != 2 || activeURL != "rtsp://127.0.0.1/new" || output.Height != 1080 {
			t.Fatal("candidate output was not adopted")
		}
		status := manager.Status()
		if !status.Running || !status.ReceiverRunning || !status.BridgeRunning || !status.MediaReady || status.DeliveryPhase != "active" {
			t.Fatalf("adopted status=%+v", status)
		}
		select {
		case <-done:
			t.Fatal("candidate commit stopped the receiver session")
		default:
		}
	}
	data, readErr := os.ReadFile(counterPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	wantStarts, wantCompletions := "2", 1
	if withWatermark {
		wantStarts, wantCompletions = "3", 2
	}
	if committing {
		wantCompletions = 1
	}
	if got := strings.TrimSpace(string(data)); got != wantStarts {
		t.Fatalf("publisher starts=%s, want old plus one candidate", got)
	}
	if withWatermark {
		captured, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(captured)), "\n")
		if len(lines) != 2 {
			t.Fatalf("process argument lines=%d", len(lines))
		}
		candidateArgs := strings.Split(lines[1], "\x1f")
		if commandArgValue(candidateArgs, "--proof-source-generation") != "7" || commandArgValue(candidateArgs, "--proof-video-watermark") != "100" {
			t.Fatal("real candidate process lacks final watermark CLI arguments")
		}
	}
	completions, _ := observer.completionSnapshot()
	if len(completions) != wantCompletions {
		t.Fatalf("completion count=%d, want old and candidate exactly once", len(completions))
	}
	if mode == "compensate" {
		recovery := request
		recovery.RequestID = "restore-old-output"
		recovery.CandidateGeneration = 3
		recovery.Output = DirectOutputConfig{Width: 1280, Height: 720, SourceFPS: 60, OutputFPS: 30, GOPFrames: 30}
		recovery.PublishURL = "rtsp://127.0.0.1/recovery-backend"
		recovery.Artifacts = airplaycontract.NewPublisherArtifacts(root, sessionID, 3)
		if err := observer.PreparePublisher(ctx, recovery.Artifacts); err != nil {
			t.Fatal(err)
		}
		owner := bindSourceClockCandidateForTest(recovery)
		waitCtx, stopWait := context.WithTimeout(ctx, 5*time.Second)
		defer stopWait()
		if err := manager.RecoverSourceClockDelivery(waitCtx, owner); err != nil {
			t.Fatalf("fresh owner recovery rejected: %v", err)
		}
		manager.mu.Lock()
		generation, height := manager.deliveryActiveGeneration, manager.deliveryActiveOutput.Height
		manager.mu.Unlock()
		if generation != 3 || height != 720 {
			t.Fatal("recovery did not restore old output using a new generation")
		}
		status := manager.Status()
		if !status.Running || !status.ReceiverRunning || !status.BridgeRunning || !status.MediaReady {
			t.Fatalf("recovery status=%+v", status)
		}
		data, err := os.ReadFile(counterPath)
		if err != nil || strings.TrimSpace(string(data)) != "4" {
			t.Fatalf("recovery starts=%s err=%v", data, err)
		}
		captured, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(captured)), "\n")
		if len(lines) != 3 {
			t.Fatalf("recovery commands=%d", len(lines))
		}
		args := strings.Split(lines[2], "\x1f")
		if commandArgValue(args, "--proof-video-watermark") != "100" || commandArgValue(args, "--publish-url") != recovery.PublishURL || commandArgValue(args, "--recording") != recovery.Artifacts.Recording || commandArgValue(args, "--video-listen-port") != "43101" {
			t.Fatal("recovery reused failed output/artifacts or changed receiver ingress")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("recovery cleanup stalled")
		}
		completions, _ := observer.completionSnapshot()
		if len(completions) != 3 {
			t.Fatalf("recovery completions=%d", len(completions))
		}
		return
	}
	if committing {
		wantFinalCompletions := 2
		if managed {
			if err := os.WriteFile(crashPath, []byte("2"), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "managed-recover" {
				select {
				case err := <-recoveryDone:
					if err != nil {
						t.Fatalf("managed recovery: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("managed crash was not delegated to its owner")
				}
				manager.mu.Lock()
				generation := manager.deliveryActiveGeneration
				manager.mu.Unlock()
				if generation != 3 || !manager.Status().MediaReady || !manager.Status().ReceiverRunning {
					t.Fatal("owned recovery did not restore generation3 on original receiver")
				}
				wantFinalCompletions = 3
			} else {
				deadline := time.Now().Add(3 * time.Second)
				for manager.Status().BridgeRunning && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if manager.Status().BridgeRunning || !manager.Status().ReceiverRunning {
					t.Fatal("nonrecoverable publisher changed receiver lifetime")
				}
				select {
				case <-recoveryDone:
					t.Fatal("invalid final evidence or exit20 authorized recovery")
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
		if mode == "commit-crash" {
			if err := os.WriteFile(crashPath, []byte("2"), 0600); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				completions, sealed := observer.completionSnapshot()
				if len(completions) == 2 && !manager.Status().BridgeRunning {
					if sealed {
						t.Fatal("publisher crash sealed the receiver session")
					}
					break
				}
				if !time.Now().Before(deadline) {
					t.Fatalf("committed publisher exit was not recorded: completions=%d", len(completions))
				}
				time.Sleep(10 * time.Millisecond)
			}
			status := manager.Status()
			if !status.Running || !status.ReceiverRunning || status.BridgeRunning || status.MediaReady {
				t.Fatalf("post-switch crash status=%+v", status)
			}
			data, err := os.ReadFile(counterPath)
			if err != nil || strings.TrimSpace(string(data)) != "3" {
				t.Fatalf("unexpected legacy retry after managed crash: %s %v", data, err)
			}
		}
		if mode == "repeat" {
			next := request
			next.RequestID = "second-switch"
			next.ExpectedActiveGeneration, next.CandidateGeneration = 2, 3
			next.Output.Width, next.Output.Height, next.Output.GOPFrames = 1280, 720, 30
			next.Artifacts = airplaycontract.NewPublisherArtifacts(root, sessionID, 3)
			if err := observer.PreparePublisher(ctx, next.Artifacts); err != nil {
				t.Fatal(err)
			}
			next.CandidateDelivery = bindSourceClockCandidateForTest(next)
			waitCtx, stopWait := context.WithTimeout(ctx, 5*time.Second)
			defer stopWait()
			if err := manager.ReconfigureSourceClockDelivery(waitCtx, next); err != nil {
				t.Fatalf("second owner-issued switch rejected: %v", err)
			}
			manager.mu.Lock()
			generation, height := manager.deliveryActiveGeneration, manager.deliveryActiveOutput.Height
			manager.mu.Unlock()
			if generation != 3 || height != 720 || !manager.Status().ReceiverRunning {
				t.Fatal("second switch did not adopt generation 3 on the same receiver")
			}
			data, err := os.ReadFile(counterPath)
			if err != nil || strings.TrimSpace(string(data)) != "4" {
				t.Fatalf("repeat publisher starts=%s err=%v", data, err)
			}
			captured, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(captured)), "\n")
			if len(lines) != 3 {
				t.Fatalf("repeat command count=%d", len(lines))
			}
			args := strings.Split(lines[2], "\x1f")
			if commandArgValue(args, "--proof-video-watermark") != "101" || commandArgValue(args, "--video-listen-port") != "43101" || commandArgValue(args, "--audio-listen-port") != "43102" {
				t.Fatal("repeat used stale watermark or changed receiver ingress")
			}
			wantFinalCompletions = 3
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("adopted candidate cleanup stalled")
		}
		completions, _ = observer.completionSnapshot()
		if len(completions) != wantFinalCompletions {
			t.Fatalf("final completions=%d, want exactly one per generation", len(completions))
		}
		return
	}
	if err := manager.RetrySourceClockDelivery(); err != nil {
		t.Fatalf("manual retry after planned failure = %v, want queued for monitor rejection", err)
	}
	time.Sleep(250 * time.Millisecond)
	data, readErr = os.ReadFile(counterPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.TrimSpace(string(data)); got != wantStarts {
		t.Fatalf("publisher starts after recovery latch=%s, want no retry", got)
	}
}

func TestSourceClockDeliveryRecoveryLatchRejectsRetriesAndReservesCandidateGeneration(t *testing.T) {
	var recovery sourceClockDeliveryRecoveryLatch
	if !recovery.beginPlanned(5) {
		t.Fatal("first planned delivery was not accepted")
	}
	if recovery.retryAllowed() {
		t.Fatal("retry remained allowed after planned publisher stop began")
	}
	if !recovery.candidateReserved(5) {
		t.Fatal("planned candidate generation was not reserved")
	}
	if recovery.beginPlanned(5) {
		t.Fatal("a reserved candidate generation was accepted again")
	}
	if recovery.beginPlanned(6) {
		t.Fatal("planned recovery latch was cleared after the first request")
	}

	var nextSession sourceClockDeliveryRecoveryLatch
	if !nextSession.beginPlanned(5) {
		t.Fatal("delivery recovery latch leaked into a new session")
	}
}

func TestSourceClockPublisherDoneOwnershipMovesLateExitToRetiringOnly(t *testing.T) {
	done := make(chan error, 1)
	activeDone := (<-chan error)(done)
	var retiringDone <-chan error
	transferSourceClockPublisherDoneToRetiring(&activeDone, &retiringDone, done)
	if activeDone != nil {
		t.Fatal("active publisher retained ownership of its Wait result")
	}
	if retiringDone == nil {
		t.Fatal("retiring publisher did not receive Wait ownership")
	}
	done <- errors.New("late publisher exit")
	if got := <-retiringDone; got == nil || got.Error() != "late publisher exit" {
		t.Fatalf("retiring Wait result=%v, want late publisher exit", got)
	}
	select {
	case <-activeDone:
		t.Fatal("active publisher consumed the retiring Wait result")
	default:
	}
}

func TestStartSourceClockDirectSealsPublisherObserverOnValidationFailure(t *testing.T) {
	t.Setenv("IMAGEPAD_AIRPLAY", "1")
	t.Setenv(envAirPlayPipeline, "source-clock")
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}

	err := New(nil).StartSourceClockDirect(
		t.Context(),
		sessionID,
		"http://127.0.0.1/not-rtsp",
		root,
		observer,
		DirectOutputConfig{},
		nil,
	)
	if err == nil {
		t.Fatal("invalid source-clock publish URL was accepted")
	}
	prepared := observer.snapshot()
	_, sealed := observer.completionSnapshot()
	if len(prepared) != 0 {
		t.Fatalf("validation failure prepared publisher artifacts: %+v", prepared)
	}
	if !sealed {
		t.Fatal("validation failure did not seal publisher observer")
	}
}

func TestStartSourceClockDirectValidatesDiagnosticNameBeforePublisherConfiguration(t *testing.T) {
	t.Setenv("IMAGEPAD_AIRPLAY", "1")
	t.Setenv(envAirPlayPipeline, "source-clock")
	t.Setenv("IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME", "candidate/name")
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}

	err := New(nil).StartSourceClockDirect(
		t.Context(), sessionID, "http://127.0.0.1/not-rtsp", root, observer, DirectOutputConfig{}, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME") {
		t.Fatalf("StartSourceClockDirect error = %v, want diagnostic receiver-name validation error", err)
	}
	if prepared := observer.snapshot(); len(prepared) != 0 {
		t.Fatalf("invalid receiver name prepared publisher artifacts: %+v", prepared)
	}
	if _, sealed := observer.completionSnapshot(); !sealed {
		t.Fatal("invalid receiver name did not seal publisher observer")
	}
}

func TestStartSourceClockDirectKeepsResolvedReceiverNameInLaunchAndStatus(t *testing.T) {
	t.Setenv(envFeatureFlag, "1")
	t.Setenv(envAirPlayPipeline, "source-clock")
	t.Setenv(envDiagnosticName, "  Outer   Source   Clock  ")
	receiverPath := sourceClockReceiverExecutableForLaunchTest(t)
	t.Setenv(envUxPlayPath, receiverPath)
	t.Setenv(envReceiverPath, "")
	t.Setenv(envAirPlayGStreamerBridgePath, receiverPath)
	t.Setenv(helperProcessEnv, "1")
	argsPath := filepath.Join(t.TempDir(), "source-clock-child-args.jsonl")
	t.Setenv(helperProcessArgsEnv, argsPath)
	markerPath := filepath.Join(t.TempDir(), "publisher-started")
	ackPath := filepath.Join(t.TempDir(), "receiver-name-mutated")
	t.Setenv(helperSourceClockReadyMarkerEnv, markerPath)
	t.Setenv(helperSourceClockReadyAckEnv, ackPath)

	mutationResult := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(markerPath); err == nil {
				break
			}
			if time.Now().After(deadline) {
				mutationResult <- errors.New("publisher helper did not reach the receiver-name mutation point")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err := os.Setenv(envDiagnosticName, "Mutated-Source-Clock"); err != nil {
			mutationResult <- err
			return
		}
		mutationResult <- os.WriteFile(ackPath, []byte("continue"), 0600)
	}()

	m := New(nil)
	err := m.StartDirect(
		t.Context(), "resolved-name-session", "rtsp://127.0.0.1:8554/resolved-name",
		filepath.Join(t.TempDir(), "recording.mp4"), DirectOutputConfig{}, nil,
	)
	if mutationErr := <-mutationResult; mutationErr != nil {
		t.Fatal(mutationErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !m.Stop(5 * time.Second) {
			t.Error("source-clock manager did not stop child processes before timeout")
		}
	})

	launchedName := waitForReceiverNameArgument(t, argsPath)
	statusName := managerStatusReceiverName(t, m)
	if launchedName != "Outer-Source-Clock" {
		t.Fatalf("source-clock UxPlay -n value = %q, want outer resolved name", launchedName)
	}
	if statusName != launchedName {
		t.Fatalf("source-clock status receiverName = %q, want launched name %q", statusName, launchedName)
	}
}

func sourceClockReceiverExecutableForLaunchTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	receiverPath := filepath.Join(dir, "uxplay-source-clock.exe")
	if err := os.Link(os.Args[0], receiverPath); err != nil {
		contents, readErr := os.ReadFile(os.Args[0])
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(receiverPath, contents, 0700); writeErr != nil {
			t.Fatalf("link test receiver: %v; copy fallback: %v", err, writeErr)
		}
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for {
			err := os.Remove(receiverPath)
			if err == nil || os.IsNotExist(err) {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("remove source-clock test receiver: %v", err)
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	contents, err := os.ReadFile(receiverPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	manifest := sourceClockCapabilities{
		Schema: 1, ProtocolVersion: 1, Binary: filepath.Base(receiverPath),
		BinarySHA256: hex.EncodeToString(sum[:]),
		Features:     []string{"video-au", "audio-frame", "remote-ntp", "bounded-writer", "idle-wait", "audio-format-lock", "egress-metrics-v2"},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "imagepad-source-clock-capabilities.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return receiverPath
}

func (b *lockedLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder.Write(data)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder.String()
}

func commandArgValue(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func TestSourceClockPublisherStatusRequiresCurrentGenerationMediaReady(t *testing.T) {
	manager := New(nil)
	manager.running = true
	manager.status = Status{
		Running:         true,
		ReceiverRunning: true,
		BridgeRunning:   true,
		MediaReadyKnown: true,
		MediaReady:      true,
	}

	manager.setSourceClockPublisherStatus(false, "GStreamerを再接続しています。AirPlay受信は維持されています。")
	status := manager.Status()
	if status.BridgeRunning || status.MediaReady {
		t.Fatalf("publisher exit kept current-generation media ready: %+v", status)
	}

	manager.setSourceClockPublisherStatus(true, "GStreamer publisherを再接続しました。")
	status = manager.Status()
	if !status.BridgeRunning || status.MediaReady {
		t.Fatalf("publisher process restart bypassed current-generation media validation: %+v", status)
	}

	manager.setSourceClockMediaReady()
	status = manager.Status()
	if !status.MediaReady || !status.MediaReadyKnown {
		t.Fatalf("validated current generation did not become media ready: %+v", status)
	}
}

func TestRetrySourceClockDeliveryQueuesOneRequestWithoutStoppingReceiver(t *testing.T) {
	manager := New(nil)
	retry := make(chan struct{}, 1)
	manager.running = true
	manager.deliveryRetry = retry
	manager.status = Status{Running: true, ReceiverRunning: true, MediaReadyKnown: true}

	if err := manager.RetrySourceClockDelivery(); err != nil {
		t.Fatalf("first delivery retry: %v", err)
	}
	select {
	case <-retry:
	default:
		t.Fatal("delivery retry signal was not queued")
	}
	if err := manager.RetrySourceClockDelivery(); !errors.Is(err, ErrDeliveryRetryPending) {
		t.Fatalf("duplicate delivery retry error = %v, want %v", err, ErrDeliveryRetryPending)
	}
	status := manager.Status()
	if !status.Running || !status.ReceiverRunning || status.BridgeRunning {
		t.Fatalf("delivery retry changed receiver ownership: %+v", status)
	}
	manager.finishSourceClockDeliveryRetry()
	if err := manager.RetrySourceClockDelivery(); err != nil {
		t.Fatalf("delivery retry after completion: %v", err)
	}
}

func TestRetrySourceClockDeliveryRejectsUnavailableOrHealthyPublisher(t *testing.T) {
	manager := New(nil)
	if err := manager.RetrySourceClockDelivery(); !errors.Is(err, ErrDeliveryRetryUnavailable) {
		t.Fatalf("stopped delivery retry error = %v, want %v", err, ErrDeliveryRetryUnavailable)
	}
	manager.running = true
	manager.deliveryRetry = make(chan struct{}, 1)
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true, MediaReadyKnown: true}
	if err := manager.RetrySourceClockDelivery(); !errors.Is(err, ErrDeliveryRetryUnavailable) {
		t.Fatalf("healthy delivery retry error = %v, want %v", err, ErrDeliveryRetryUnavailable)
	}
}

func TestSourceClockPublisherRestartSuccessAtomicallyClosesRetryWindow(t *testing.T) {
	manager := New(nil)
	manager.running = true
	manager.deliveryRetry = make(chan struct{}, 1)
	manager.deliveryRetryPending = true
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: false}

	manager.setSourceClockPublisherRestarted("restarted")

	status := manager.Status()
	if !status.Running || !status.ReceiverRunning || !status.BridgeRunning || status.Message != "restarted" {
		t.Fatalf("restart success status was not committed atomically: %+v", status)
	}
	manager.mu.Lock()
	pending := manager.deliveryRetryPending
	manager.mu.Unlock()
	if pending {
		t.Fatal("restart success left delivery retry pending")
	}
	if err := manager.RetrySourceClockDelivery(); !errors.Is(err, ErrDeliveryRetryUnavailable) {
		t.Fatalf("retry request entered the restarted publisher window: %v", err)
	}

	for attempt := 0; attempt < 1000; attempt++ {
		manager.mu.Lock()
		manager.running = true
		manager.deliveryRetryPending = true
		manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: false}
		manager.mu.Unlock()
		start := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			<-start
			result <- manager.RetrySourceClockDelivery()
		}()
		close(start)
		manager.setSourceClockPublisherRestarted("restarted")
		err := <-result
		if !errors.Is(err, ErrDeliveryRetryPending) && !errors.Is(err, ErrDeliveryRetryUnavailable) {
			t.Fatalf("attempt %d entered an intermediate retry window: %v", attempt, err)
		}
	}
}

func TestMonitorSourceClockKeepsReceiverAndRestartsPublisher(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	argsPath := filepath.Join(t.TempDir(), "publisher-args.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_ARGS", argsPath)
	logOutput := &lockedLogBuffer{}
	previousLogOutput := log.Writer()
	log.SetOutput(logOutput)
	defer log.SetOutput(previousLogOutput)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatalf("start receiver helper: %v", err)
	}
	pipeline, err := startSourceClockGStreamerProcess(
		ctx,
		os.Args[0],
		[]string{
			"-test.run=TestSourceClockPublisherProcess$",
			"fixture",
			"--publisher-generation", "1",
			"--video-listen-port", "41001",
			"--audio-listen-port", "41002",
		},
		"",
	)
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatalf("start publisher helper: %v", err)
	}

	done := make(chan struct{})
	tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-monitor-")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	delayedPublisherPath := filepath.Join(t.TempDir(), "publisher-after-failed-start.exe")
	pipeline.restartPath = delayedPublisherPath
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{
		Enabled:         true,
		Available:       true,
		Running:         true,
		ReceiverRunning: true,
		BridgeRunning:   true,
	}
	metricsGate, err := newSourceClockMetricsGate("receiver-monitor-test")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", metricsGate, nil)
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, metricsGate, filepath.Join(tempDir, "missing.publisher-ready"), filepath.Join(tempDir, "missing.media-ready"), "monitor-session", 1, "", nil, sourceClockNoSignalTimeout, nil, nil, tempDir, nil)

	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if receiver.Process != nil {
				_ = receiver.Process.Kill()
			}
		}
		removeDeadline := time.Now().Add(2 * time.Second)
		for {
			err := os.Remove(delayedPublisherPath)
			if err == nil || os.IsNotExist(err) {
				break
			}
			if time.Now().After(removeDeadline) {
				t.Errorf("remove delayed publisher helper: %v", err)
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	failedStartDeadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(failedStartDeadline) {
		if strings.Contains(logOutput.String(), "publisher restart failed: attempt=1") {
			if err := os.Link(os.Args[0], delayedPublisherPath); err != nil {
				t.Fatalf("make publisher available after failed start: %v", err)
			}
			if err := os.Chmod(delayedPublisherPath, 0700); err != nil {
				t.Fatalf("mark delayed publisher executable: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(delayedPublisherPath); err != nil {
		t.Fatalf("publisher start failure was not observed: %v; log=%q", err, logOutput.String())
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatal("GStreamer publisher failure stopped the AirPlay receiver session")
		default:
		}
		data, readErr := os.ReadFile(counterPath)
		if readErr == nil && strings.TrimSpace(string(data)) == "2" {
			argsData, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(argsData)), "\n")
			if len(lines) != 2 {
				t.Fatalf("publisher args lines=%d, want 2: %q", len(lines), argsData)
			}
			firstArgs := strings.Split(lines[0], "\x1f")
			secondArgs := strings.Split(lines[1], "\x1f")
			if got := commandArgValue(firstArgs, "--publisher-generation"); got != "1" {
				t.Fatalf("initial publisher generation=%q, want 1", got)
			}
			if got := commandArgValue(secondArgs, "--publisher-generation"); got != "3" {
				t.Fatalf("publisher generation after one failed restart=%q, want 3", got)
			}
			for _, flag := range []string{"--video-listen-port", "--audio-listen-port"} {
				if first, second := commandArgValue(firstArgs, flag), commandArgValue(secondArgs, flag); first == "" || second != first {
					t.Fatalf("%s changed across publisher attempts: first=%q second=%q", flag, first, second)
				}
			}
			status := manager.Status()
			if !status.Running || !status.ReceiverRunning || !status.BridgeRunning {
				t.Fatalf("AirPlay receiver was not preserved after publisher restart: %+v", status)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("GStreamer publisher was not restarted while the AirPlay receiver remained active")
}

func TestMonitorSourceClockRestartUsesFreshPublisherArtifacts(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	argsPath := filepath.Join(t.TempDir(), "publisher-args.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_ARGS", argsPath)
	logOutput := &lockedLogBuffer{}
	previousLogOutput := log.Writer()
	log.SetOutput(logOutput)
	defer log.SetOutput(previousLogOutput)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	root := t.TempDir()
	const sessionID = "0123456789abcdef"
	observer := &sourceClockArtifactObserverForTest{root: root, session: sessionID}
	first := airplaycontract.NewPublisherArtifacts(root, sessionID, 1)
	if err := observer.PreparePublisher(ctx, first); err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	writeSourceClockEventForTest(t, first.EventLog, airplaycontract.Event{
		Schema: 2, SessionID: sessionID, PublisherGeneration: 1,
		Event: "recording-finalized", At: time.Unix(90, 0).UTC(),
		RecordingPath: first.Recording, RecordingClosed: true,
	})
	initialArgs := buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: 49001,
		AudioListenPort: 49002,
		PublishURL:      "rtsp://127.0.0.1:8554/artifact-restart",
		SessionToken:    strings.Repeat("ab", 16),
		Artifacts:       first,
		Output: DirectOutputConfig{
			Width: 1280, Height: 720, SourceFPS: 60, OutputFPS: 30,
			VideoBitrateKbps: 2500, MaxRateKbps: 3000, BufferSizeKbps: 5000,
			AudioBitrateBps: 128000, GOPFrames: 30,
		},
	})
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], append([]string{"-test.run=TestSourceClockPublisherProcess$", "fixture"}, initialArgs...), first.StopRequest)
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	delayedPublisherPath := filepath.Join(t.TempDir(), "publisher-after-failed-artifact-start.exe")
	pipeline.restartPath = delayedPublisherPath
	tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-artifact-restart-")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	gate, err := newSourceClockMetricsGate("receiver-artifact-restart-test")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, gate, first.Ready, first.MediaReady, sessionID, 1, root, observer, sourceClockNoSignalTimeout, nil, nil, tempDir, nil)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if receiver.Process != nil {
				_ = receiver.Process.Kill()
			}
		}
		removeDeadline := time.Now().Add(2 * time.Second)
		for {
			err := os.Remove(delayedPublisherPath)
			if err == nil || os.IsNotExist(err) {
				break
			}
			if time.Now().After(removeDeadline) {
				t.Errorf("remove delayed artifact publisher helper: %v", err)
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	failedStartDeadline := time.Now().Add(4 * time.Second)
	for !strings.Contains(logOutput.String(), "publisher restart failed: attempt=1") {
		if time.Now().After(failedStartDeadline) {
			t.Fatalf("first generation-scoped restart did not fail: log=%q", logOutput.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Link(os.Args[0], delayedPublisherPath); err != nil {
		t.Fatalf("make delayed artifact publisher available: %v", err)
	}
	if err := os.Chmod(delayedPublisherPath, 0700); err != nil {
		t.Fatalf("mark delayed artifact publisher executable: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for {
		data, readErr := os.ReadFile(counterPath)
		if readErr == nil && strings.TrimSpace(string(data)) == "2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("artifact publisher did not restart: readErr=%v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	prepared := observer.snapshot()
	if len(prepared) != 3 || prepared[0].Generation != 1 || prepared[1].Generation != 2 || prepared[2].Generation != 3 {
		t.Fatalf("prepared generations=%+v", prepared)
	}
	observed := observer.eventSnapshot()
	if len(observed) != 1 || !observed[0].RecordingFinalizedFor(sessionID, 1, first.Recording) {
		t.Fatalf("finished generation events=%+v", observed)
	}
	completions, sealed := observer.completionSnapshot()
	if sealed || len(completions) != 2 || completions[0].Artifacts != first || !completions[0].Started || !completions[0].ExitConfirmed || completions[1].Artifacts.Generation != 2 || completions[1].Started || completions[1].ExitConfirmed {
		t.Fatalf("restart completions=%+v sealed=%t", completions, sealed)
	}
	second := prepared[2]
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("publisher args lines=%d, want 2: %q", len(lines), data)
	}
	firstArgs := strings.Split(lines[0], "\x1f")
	secondArgs := strings.Split(lines[1], "\x1f")
	for _, expected := range []struct {
		flag   string
		first  string
		second string
	}{
		{flag: "--publisher-generation", first: "1", second: "3"},
		{flag: "--recording", first: first.Recording, second: second.Recording},
		{flag: "--ready-file", first: first.Ready, second: second.Ready},
		{flag: "--media-ready-file", first: first.MediaReady, second: second.MediaReady},
		{flag: "--event-log", first: first.EventLog, second: second.EventLog},
		{flag: "--stop-file", first: first.StopRequest, second: second.StopRequest},
	} {
		requireFlagValueExactlyOnce(t, firstArgs, expected.flag, expected.first)
		requireFlagValueExactlyOnce(t, secondArgs, expected.flag, expected.second)
	}
	for _, flag := range []string{"--video-listen-port", "--audio-listen-port", "--session-token", "--publish-url", "--width", "--height", "--fps", "--gop"} {
		if firstValue, secondValue := commandArgValue(firstArgs, flag), commandArgValue(secondArgs, flag); firstValue == "" || firstValue != secondValue {
			t.Fatalf("stable restart flag %s changed: first=%q second=%q", flag, firstValue, secondValue)
		}
	}
}

func TestMonitorSourceClockKeepsReceiverWithoutRestartingTerminalExit(t *testing.T) {
	tests := []struct {
		code   int
		reason string
	}{
		{code: 0, reason: "stopped"},
		{code: 2, reason: "configuration-error"},
		{code: 21, reason: "protocol-error"},
		{code: 37, reason: "unclassified-exit"},
	}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.code), func(t *testing.T) {
			t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
			t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
			t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_CODE", strconv.Itoa(test.code))
			counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
			t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)

			ctx, cancel := context.WithCancel(context.Background())
			receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
			if err := receiver.Start(); err != nil {
				cancel()
				t.Fatal(err)
			}
			pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{
				"-test.run=TestSourceClockPublisherProcess$", "fixture",
				"--publisher-generation", "1",
				"--video-listen-port", "43001",
				"--audio-listen-port", "43002",
			}, "")
			if err != nil {
				cancel()
				_ = receiver.Wait()
				t.Fatal(err)
			}
			tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-terminal-")
			if err != nil {
				cancel()
				_ = receiver.Wait()
				_ = pipeline.wait()
				t.Fatal(err)
			}
			done := make(chan struct{})
			manager := New(nil)
			manager.running = true
			manager.cancel = cancel
			manager.done = done
			manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
			gate, err := newSourceClockMetricsGate("receiver-terminal-" + strconv.Itoa(test.code))
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, gate, filepath.Join(tempDir, "missing.publisher-ready"), filepath.Join(tempDir, "missing.media-ready"), "terminal-session", 1, "", nil, sourceClockNoSignalTimeout, nil, nil, tempDir, nil)
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					if receiver.Process != nil {
						_ = receiver.Process.Kill()
					}
				}
			}()

			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case <-done:
					t.Fatal("terminal publisher exit stopped the receiver session")
				default:
				}
				manager.mu.Lock()
				status := manager.status
				manager.mu.Unlock()
				if status.Running && status.ReceiverRunning && !status.BridgeRunning && strings.Contains(status.Message, "reason="+test.reason) {
					time.Sleep(300 * time.Millisecond)
					data, err := os.ReadFile(counterPath)
					if err != nil {
						t.Fatal(err)
					}
					if got := strings.TrimSpace(string(data)); got != "1" {
						t.Fatalf("publisher restarted after terminal exit: starts=%s status=%+v", got, status)
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("terminal publisher exit did not settle as delivery-failed")
		})
	}
}

func TestMonitorSourceClockExit20BeforeDeadlineKeepsReceiverWithoutRestarting(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_CODE", "20")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{
		"-test.run=TestSourceClockPublisherProcess$", "fixture",
		"--publisher-generation", "1",
		"--video-listen-port", "43601",
		"--audio-listen-port", "43602",
	}, "")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	tempDir := t.TempDir()
	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	gate, err := newSourceClockMetricsGate("receiver-exit-20")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go manager.monitorSourceClockWithPublisherDoneAt(
		ctx, cancel, done, pipeline, nil, receiver, receiverOutput, nil,
		filepath.Join(tempDir, "missing.publisher-ready"), filepath.Join(tempDir, "missing.media-ready"),
		"exit-20-session", 1, "", nil, 3*time.Minute, time.Now(),
		nil, nil, tempDir, nil,
	)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if receiver.Process != nil {
				_ = receiver.Process.Kill()
			}
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatal("publisher exit 20 stopped the receiver before the Go deadline")
		default:
		}
		status := manager.Status()
		if status.Running && status.ReceiverRunning && !status.BridgeRunning && strings.Contains(status.Message, "reason=no-signal") {
			data, readErr := os.ReadFile(counterPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got := strings.TrimSpace(string(data)); got != "1" {
				t.Fatalf("publisher exit 20 triggered a restart: starts=%s", got)
			}
			if receiver.ProcessState != nil && receiver.ProcessState.Exited() {
				t.Fatal("receiver exited after publisher exit 20")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("publisher exit 20 did not settle as delivery-failed")
}

func TestMonitorSourceClockManualDeliveryRetryRestartsPublisherWithoutReplacingReceiver(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_CODE", "21")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverPID := receiver.Process.Pid
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{
		"-test.run=TestSourceClockPublisherProcess$", "fixture",
		"--publisher-generation", "1",
		"--video-listen-port", "43501",
		"--audio-listen-port", "43502",
	}, "")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-manual-retry-")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.deliveryRetry = make(chan struct{}, 1)
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	gate, err := newSourceClockMetricsGate("receiver-manual-retry-test")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, gate, filepath.Join(tempDir, "missing.publisher-ready"), filepath.Join(tempDir, "missing.media-ready"), "manual-retry-session", 1, "", nil, sourceClockNoSignalTimeout, nil, nil, tempDir, nil)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if receiver.Process != nil {
				_ = receiver.Process.Kill()
			}
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		status := manager.Status()
		if status.Running && status.ReceiverRunning && !status.BridgeRunning && strings.Contains(status.Message, "reason=protocol-error") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal publisher exit did not settle as delivery-failed: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Unsetenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_CODE"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RetrySourceClockDelivery(); err != nil {
		t.Fatalf("queue manual delivery retry: %v", err)
	}

	deadline = time.Now().Add(4 * time.Second)
	for {
		status := manager.Status()
		if status.Running && status.ReceiverRunning && status.BridgeRunning {
			data, readErr := os.ReadFile(counterPath)
			if readErr == nil && strings.TrimSpace(string(data)) == "2" {
				if receiver.Process == nil || receiver.Process.Pid != receiverPID {
					t.Fatalf("manual delivery retry replaced receiver: before=%d after=%v", receiverPID, receiver.Process)
				}
				manager.mu.Lock()
				pending := manager.deliveryRetryPending
				manager.mu.Unlock()
				if pending {
					t.Fatal("manual delivery retry remained pending after publisher restart")
				}
				return
			}
		}
		select {
		case <-done:
			t.Fatal("manual delivery retry stopped the AirPlay receiver session")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("manual delivery retry did not restart publisher: status=%+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMonitorSourceClockStopsRetryingAfterBudgetExhaustion(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)
	logOutput := &lockedLogBuffer{}
	previousLogOutput := log.Writer()
	log.SetOutput(logOutput)
	defer log.SetOutput(previousLogOutput)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{
		"-test.run=TestSourceClockPublisherProcess$", "fixture",
		"--publisher-generation", "1",
		"--video-listen-port", "44001",
		"--audio-listen-port", "44002",
	}, "")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	pipeline.restartPath = filepath.Join(t.TempDir(), "missing-publisher.exe")
	tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-budget-")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	gate, err := newSourceClockMetricsGate("receiver-budget-test")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, gate, filepath.Join(tempDir, "missing.publisher-ready"), filepath.Join(tempDir, "missing.media-ready"), "budget-session", 1, "", nil, sourceClockNoSignalTimeout, nil, nil, tempDir, nil)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if receiver.Process != nil {
				_ = receiver.Process.Kill()
			}
		}
	}()

	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatal("retry exhaustion stopped the receiver session")
		default:
		}
		manager.mu.Lock()
		status := manager.status
		manager.mu.Unlock()
		if status.Running && status.ReceiverRunning && !status.BridgeRunning && strings.Contains(status.Message, "retry-attempts-exhausted") {
			time.Sleep(500 * time.Millisecond)
			if got := strings.Count(logOutput.String(), "publisher restart failed: attempt="); got != sourceClockRetryMaxAttempts {
				t.Fatalf("restart failures=%d, want %d; log=%q", got, sourceClockRetryMaxAttempts, logOutput.String())
			}
			data, err := os.ReadFile(counterPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(data)); got != "1" {
				t.Fatalf("missing restart executable unexpectedly launched: starts=%s", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("retry budget did not exhaust; log=%q", logOutput.String())
}

func TestMonitorSourceClockStopsOnlyAfterDecodedMediaDeadline(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	if err := os.WriteFile(counterPath, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{
		"-test.run=TestSourceClockPublisherProcess$", "fixture",
		"--publisher-generation", "1",
		"--video-listen-port", "41001",
		"--audio-listen-port", "41002",
	}, "")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-timeout-")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	gate, err := newSourceClockMetricsGate("receiver-timeout-test")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	mediaReadyPath := filepath.Join(tempDir, "media-ready.json")
	ticks := make(chan time.Time)
	tickHandled := make(chan struct{})
	observer := &sourceClockArtifactObserverForTest{root: tempDir, session: "timeout-session"}
	if err := observer.PreparePublisher(t.Context(), airplaycontract.NewPublisherArtifacts(tempDir, "timeout-session", 1)); err != nil {
		t.Fatal(err)
	}
	go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, gate, filepath.Join(tempDir, "missing.publisher-ready"), mediaReadyPath, "timeout-session", 1, tempDir, observer, 10*time.Second, ticks, tickHandled, tempDir, nil)
	sendTick := func(now time.Time) {
		t.Helper()
		select {
		case ticks <- now:
		case <-time.After(2 * time.Second):
			t.Fatal("monitor did not receive lifecycle tick")
		}
		select {
		case <-tickHandled:
		case <-time.After(2 * time.Second):
			t.Fatal("monitor did not finish lifecycle tick")
		}
	}
	base := time.Unix(600, 0)
	sendTick(base)
	sendTick(base.Add(time.Hour))
	select {
	case <-done:
		t.Fatal("session stopped before decoded media-ready")
	default:
	}
	running := uint64(0)
	writeSourceClockEventForTest(t, mediaReadyPath, airplaycontract.Event{
		Schema: 2, SessionID: "timeout-session", PublisherGeneration: 1,
		Event: "video-decoded", At: time.Unix(500, 0).UTC(), VideoDecoded: true, RunningTimeNS: &running,
	})
	decodedObservedAt := base.Add(time.Hour + time.Second)
	sendTick(decodedObservedAt)
	select {
	case <-done:
		t.Fatal("session stopped at first decoded observation")
	default:
	}
	sendTick(decodedObservedAt.Add(10 * time.Second))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("session did not stop at decoded-media deadline")
	}
	manager.mu.Lock()
	status := manager.status
	manager.mu.Unlock()
	if status.Running || status.ReceiverRunning || status.BridgeRunning {
		t.Fatalf("manager stayed running after no-signal deadline: %+v", status)
	}
	if !strings.Contains(status.Message, "無信号3分") {
		t.Fatalf("no-signal reason was not saved: %q", status.Message)
	}
	if got := observer.initiatorSnapshot(); len(got) != 1 || got[0].sessionID != "timeout-session" || got[0].generation != 1 || got[0].reason != airplaycontract.TerminationReasonNoSignalTimeout {
		t.Fatalf("no-signal initiators=%+v", got)
	}
}

func TestMonitorSourceClockStopsDuringPublisherRestartFailure(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_CODE", "22")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_EXIT_DELAY_MS", "1000")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	if err := os.WriteFile(counterPath, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		t.Fatal(err)
	}
	const sessionID = "retry-timeout-session"
	artifactRoot := t.TempDir()
	artifacts := airplaycontract.NewPublisherArtifacts(artifactRoot, sessionID, 1)
	publisherArgs := append([]string{"-test.run=TestSourceClockPublisherProcess$", "fixture"}, buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: 42001,
		AudioListenPort: 42002,
		PublishURL:      "rtsp://127.0.0.1:8554/retry-timeout",
		SessionToken:    "fixture-token",
		Artifacts:       artifacts,
	})...)
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], publisherArgs, artifacts.StopRequest)
	if err != nil {
		_ = receiver.Wait()
		t.Fatal(err)
	}
	pipeline.restartPath = filepath.Join(t.TempDir(), "missing-publisher.exe")
	tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-retry-timeout-")
	if err != nil {
		_ = receiver.Process.Kill()
		_ = receiver.Wait()
		pipeline.stop()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	gate, err := newSourceClockMetricsGate("receiver-retry-timeout-test")
	if err != nil {
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		t.Fatal(err)
	}
	mediaReadyPath := filepath.Join(tempDir, "media-ready.json")
	running := uint64(0)
	writeSourceClockEventForTest(t, mediaReadyPath, airplaycontract.Event{
		Schema: 2, SessionID: "retry-timeout-session", PublisherGeneration: 1,
		Event: "video-decoded", At: time.Unix(700, 0).UTC(), VideoDecoded: true, RunningTimeNS: &running,
	})
	ticks := make(chan time.Time)
	tickHandled := make(chan struct{})
	observer := &sourceClockArtifactObserverForTest{root: artifactRoot, session: sessionID, failPrepareAfter: 2}
	if err := observer.PreparePublisher(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, gate, artifacts.Ready, mediaReadyPath, sessionID, 1, artifactRoot, observer, 10*time.Second, ticks, tickHandled, tempDir, nil)
	base := time.Unix(800, 0)
	ticks <- base
	<-tickHandled
	deadline := time.Now().Add(3 * time.Second)
	for {
		manager.mu.Lock()
		status := manager.status
		manager.mu.Unlock()
		if !status.BridgeRunning && strings.Contains(status.Message, "1回目") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("publisher did not enter failed retry state: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	ticks <- base.Add(10 * time.Second)
	<-tickHandled
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop while publisher restart kept failing")
	}
	manager.mu.Lock()
	status := manager.status
	manager.mu.Unlock()
	if status.Running || status.ReceiverRunning || status.BridgeRunning {
		t.Fatalf("manager stayed running after retry-loop no-signal deadline: %+v", status)
	}
	if !strings.Contains(status.Message, "無信号3分") {
		t.Fatalf("retry-loop no-signal reason was not saved: %q", status.Message)
	}
	if got := observer.initiatorSnapshot(); len(got) != 1 || got[0].sessionID != sessionID || got[0].generation != 1 || got[0].reason != airplaycontract.TerminationReasonNoSignalTimeout {
		t.Fatalf("retry-loop no-signal initiators=%+v", got)
	}
}

func TestSourceClockPublisherRetryDelayCapsAtFourSeconds(t *testing.T) {
	if first, second := sourceClockPublisherRetryDelay(1), sourceClockPublisherRetryDelay(2); second <= first {
		t.Fatalf("source-clock retry delay must back off: first=%s second=%s", first, second)
	}
	if got := sourceClockPublisherRetryDelay(100); got != 4*time.Second {
		t.Fatalf("source-clock retry delay=%s, want 4s cap", got)
	}
}

func TestSourceClockPublisherHealthTickResetsConsumedRetryBudget(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "publisher-ready.json")
	writeSourceClockEventForTest(t, readyPath, airplaycontract.Event{
		Schema: 2, SessionID: "health-session", PublisherGeneration: 2,
		Event: "publisher-ready", At: time.Unix(900, 0).UTC(), ProtocolVersion: 1,
		VideoListenPort: 41001, AudioListenPort: 41002, PipelineStartAccepted: true,
	})
	base := time.Unix(1000, 0)
	var budget sourceClockRetryBudget
	if first := budget.Next(base); !first.Allowed || first.Attempt != 1 {
		t.Fatalf("first retry permission = %+v", first)
	}
	if second := budget.Next(base.Add(time.Second)); !second.Allowed || second.Attempt != 2 {
		t.Fatalf("second retry permission = %+v", second)
	}
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	stats := sourceClockMetricsGateStats{}
	for second := 0; second < 30; second++ {
		reset, err := sourceClockPublisherHealthTick(tracker, &budget, readyPath, "health-session", 2, false, stats, base.Add(time.Duration(second)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if reset {
			t.Fatalf("retry budget reset early at second %d", second)
		}
	}
	reset, err := sourceClockPublisherHealthTick(tracker, &budget, readyPath, "health-session", 2, false, stats, base.Add(30*time.Second))
	if err != nil || !reset {
		t.Fatalf("healthy reset = %v, %v", reset, err)
	}
	if next := budget.Next(base.Add(31 * time.Second)); !next.Allowed || next.Attempt != 1 {
		t.Fatalf("retry budget after healthy reset = %+v", next)
	}
}

func TestMonitorSourceClockResetsConsumedRetryBudgetAfterHealthyRestart(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)
	logOutput := &lockedLogBuffer{}
	previousLogOutput := log.Writer()
	log.SetOutput(logOutput)
	defer log.SetOutput(previousLogOutput)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{
		"-test.run=TestSourceClockPublisherProcess$", "fixture",
		"--publisher-generation", "1",
		"--video-listen-port", "45001",
		"--audio-listen-port", "45002",
	}, "")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}
	tempDir, err := os.MkdirTemp(t.TempDir(), "source-clock-health-monitor-")
	if err != nil {
		cancel()
		_ = receiver.Wait()
		_ = pipeline.wait()
		t.Fatal(err)
	}
	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	gate, err := newSourceClockMetricsGate("receiver-health-monitor-test")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	readyPath := filepath.Join(tempDir, "publisher-ready.json")
	ticks := make(chan time.Time)
	tickHandled := make(chan struct{})
	go manager.monitorSourceClock(ctx, cancel, done, pipeline, receiver, receiverOutput, gate, readyPath, filepath.Join(tempDir, "missing.media-ready"), "health-monitor-session", 1, "", nil, sourceClockNoSignalTimeout, ticks, tickHandled, tempDir, nil)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if receiver.Process != nil {
				_ = receiver.Process.Kill()
			}
		}
	}()

	restartDeadline := time.Now().Add(4 * time.Second)
	for {
		data, readErr := os.ReadFile(counterPath)
		if readErr == nil && strings.TrimSpace(string(data)) == "2" {
			break
		}
		if time.Now().After(restartDeadline) {
			t.Fatalf("publisher did not reach restarted generation: readErr=%v log=%q", readErr, logOutput.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	writeSourceClockEventForTest(t, readyPath, airplaycontract.Event{
		Schema: 2, SessionID: "health-monitor-session", PublisherGeneration: 2,
		Event: "publisher-ready", At: time.Unix(100, 0).UTC(), ProtocolVersion: 1,
		VideoListenPort: 45001, AudioListenPort: 45002, PipelineStartAccepted: true,
	})
	base := time.Unix(3000, 0)
	for second := 0; second <= 30; second++ {
		select {
		case ticks <- base.Add(time.Duration(second) * time.Second):
		case <-time.After(2 * time.Second):
			t.Fatalf("monitor did not receive health tick %d", second)
		}
		select {
		case <-tickHandled:
		case <-time.After(2 * time.Second):
			t.Fatalf("monitor did not finish lifecycle tick %d", second)
		}
	}
	resetDeadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logOutput.String(), "retry budget reset after healthy generation=2") {
		select {
		case <-done:
			t.Fatalf("monitor stopped before healthy reset: log=%q", logOutput.String())
		default:
		}
		if time.Now().After(resetDeadline) {
			t.Fatalf("monitor did not wire healthy reset after restart: log=%q", logOutput.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := manager.Status()
	if !status.Running || !status.ReceiverRunning || !status.BridgeRunning {
		t.Fatalf("healthy reset disturbed running session: %+v", status)
	}
}

func TestBuildSourceClockDirectArgsReusesStableIngressPorts(t *testing.T) {
	paths, err := airplaycontract.FixedPathsForRecording("recording.mp4")
	if err != nil {
		t.Fatal(err)
	}
	args := buildSourceClockFixedPortArgsForTest(
		t,
		41001,
		41002,
		"rtsp://127.0.0.1:8554/airplay",
		"recording.mp4",
		"stop.request",
		strings.Repeat("ab", 16),
		"obs-session",
		3,
		paths,
		DirectOutputConfig{Width: 1920, Height: 1080, SourceFPS: 60, OutputFPS: 30},
	)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--video-listen-port 41001") {
		t.Fatalf("source-clock args do not preserve the video ingress port: %q", joined)
	}
	if !strings.Contains(joined, "--audio-listen-port 41002") {
		t.Fatalf("source-clock args do not preserve the audio ingress port: %q", joined)
	}
}

func TestNextSourceClockRestartArgsCopiesAndIncrements(t *testing.T) {
	original := []string{"--session-id", "obs-session", "--publisher-generation", "41", "--video-listen-port", "41001"}
	next, generation, err := nextSourceClockRestartArgs(original)
	if err != nil {
		t.Fatal(err)
	}
	if generation != 42 || commandArgValue(next, "--publisher-generation") != "42" {
		t.Fatalf("next generation=%d args=%q", generation, next)
	}
	if commandArgValue(original, "--publisher-generation") != "41" {
		t.Fatalf("input args were mutated: %q", original)
	}
}

func TestNextSourceClockRestartArgsRejectsInvalidGeneration(t *testing.T) {
	for name, args := range map[string][]string{
		"missing flag":  {"--session-id", "obs-session"},
		"missing value": {"--publisher-generation"},
		"duplicate":     {"--publisher-generation", "1", "--publisher-generation", "2"},
		"not decimal":   {"--publisher-generation", "1x"},
		"zero":          {"--publisher-generation", "0"},
		"overflow":      {"--publisher-generation", "18446744073709551615"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := nextSourceClockRestartArgs(args); err == nil {
				t.Fatalf("invalid args were accepted: %q", args)
			}
		})
	}
}

func TestNextSourceClockPublisherArgsReplacesWholeArtifactGeneration(t *testing.T) {
	root := t.TempDir()
	firstArtifacts := airplaycontract.NewPublisherArtifacts(root, "0123456789abcdef", 1)
	secondArtifacts := airplaycontract.NewPublisherArtifacts(root, "0123456789abcdef", 2)
	original := buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: 48001,
		AudioListenPort: 48002,
		PublishURL:      "rtsp://127.0.0.1:8554/artifacts",
		SessionToken:    strings.Repeat("ab", 16),
		Artifacts:       firstArtifacts,
		Output: DirectOutputConfig{
			Width: 1920, Height: 1080, SourceFPS: 60, OutputFPS: 30,
			VideoBitrateKbps: 4500, MaxRateKbps: 5200, BufferSizeKbps: 9000,
			AudioBitrateBps: 160000, GOPFrames: 30,
		},
	})
	next, err := nextSourceClockPublisherArgs(original, secondArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--video-listen-port", "--audio-listen-port", "--session-token", "--publish-url", "--width", "--height", "--source-fps", "--fps", "--bitrate", "--maxrate", "--buffer-size", "--audio-bitrate", "--gop", "--no-signal-seconds"} {
		if first, second := commandArgValue(original, flag), commandArgValue(next, flag); first == "" || first != second {
			t.Fatalf("stable flag %s changed: first=%q second=%q", flag, first, second)
		}
	}
	for _, expected := range []struct {
		flag   string
		first  string
		second string
	}{
		{flag: "--session-id", first: firstArtifacts.SessionID, second: secondArtifacts.SessionID},
		{flag: "--publisher-generation", first: "1", second: "2"},
		{flag: "--recording", first: firstArtifacts.Recording, second: secondArtifacts.Recording},
		{flag: "--ready-file", first: firstArtifacts.Ready, second: secondArtifacts.Ready},
		{flag: "--media-ready-file", first: firstArtifacts.MediaReady, second: secondArtifacts.MediaReady},
		{flag: "--event-log", first: firstArtifacts.EventLog, second: secondArtifacts.EventLog},
		{flag: "--stop-file", first: firstArtifacts.StopRequest, second: secondArtifacts.StopRequest},
	} {
		requireFlagValueExactlyOnce(t, original, expected.flag, expected.first)
		requireFlagValueExactlyOnce(t, next, expected.flag, expected.second)
	}
	if commandArgValue(original, "--publisher-generation") != "1" {
		t.Fatalf("input args were mutated: %q", original)
	}
}

func TestNextSourceClockPublisherArgsRejectsIncompleteOrDuplicatedContract(t *testing.T) {
	artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), "0123456789abcdef", 2)
	base := buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: 48001,
		AudioListenPort: 48002,
		PublishURL:      "rtsp://127.0.0.1:8554/artifacts",
		SessionToken:    strings.Repeat("ab", 16),
		Artifacts:       airplaycontract.NewPublisherArtifacts(t.TempDir(), "0123456789abcdef", 1),
		Output:          DirectOutputConfig{Width: 1280, Height: 720, SourceFPS: 60, OutputFPS: 30},
	})
	missingRecording := make([]string, 0, len(base)-2)
	for index := 0; index < len(base); index++ {
		if base[index] == "--recording" && index+1 < len(base) {
			index++
			continue
		}
		missingRecording = append(missingRecording, base[index])
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing recording", args: missingRecording},
		{name: "duplicate generation", args: append(append([]string(nil), base...), "--publisher-generation", "9")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := nextSourceClockPublisherArgs(test.args, artifacts); err == nil {
				t.Fatalf("invalid source-clock args were accepted: %q", test.args)
			}
		})
	}
	invalidArtifacts := artifacts
	invalidArtifacts.StopRequest = ""
	if _, err := nextSourceClockPublisherArgs(base, invalidArtifacts); err == nil {
		t.Fatal("incomplete publisher artifacts were accepted")
	}
}

func TestConfiguredSourceClockNoSignalTimeoutIsolatedOverride(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_ISOLATED_LIFECYCLE", "")
	t.Setenv("IMAGEPAD_TEST_AIRPLAY_NO_SIGNAL_SECONDS", "4")
	if got := configuredSourceClockNoSignalTimeout(); got != sourceClockNoSignalTimeout {
		t.Fatalf("non-isolated timeout=%s, want %s", got, sourceClockNoSignalTimeout)
	}

	t.Setenv("IMAGEPAD_TEST_ISOLATED_LIFECYCLE", "1")
	if got := configuredSourceClockNoSignalTimeout(); got != 4*time.Second {
		t.Fatalf("isolated timeout=%s, want 4s", got)
	}

	for _, invalid := range []string{"", "0", "-1", "31", "not-a-number"} {
		t.Setenv("IMAGEPAD_TEST_AIRPLAY_NO_SIGNAL_SECONDS", invalid)
		if got := configuredSourceClockNoSignalTimeout(); got != sourceClockNoSignalTimeout {
			t.Fatalf("invalid %q timeout=%s, want %s", invalid, got, sourceClockNoSignalTimeout)
		}
	}
}

func TestSourceClockMonitorDecisionKeepsDeadlineWhenObservationFails(t *testing.T) {
	startedAt := time.Unix(2200, 0)
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)
	gate, err := newSourceClockMetricsGate("receiver-monitor-decision")
	if err != nil {
		t.Fatal(err)
	}
	missingMediaReady := filepath.Join(t.TempDir(), "missing-media-ready.json")

	decision, err := sourceClockMonitorLifecycleDecision(
		tracker, gate, missingMediaReady, "monitor-session", 1,
		startedAt.Add(179*time.Second),
	)
	if err != nil {
		t.Fatalf("missing media-ready observation returned an error: %v", err)
	}
	if decision != sourceClockTimeoutNone {
		t.Fatalf("decision at 179 seconds = %q, want %q", decision, sourceClockTimeoutNone)
	}

	malformed := filepath.Join(t.TempDir(), "malformed-media-ready.json")
	if err := os.WriteFile(malformed, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	decision, err = sourceClockMonitorLifecycleDecision(
		tracker, gate, malformed, "monitor-session", 1,
		startedAt.Add(180*time.Second),
	)
	if err == nil {
		t.Fatal("malformed media-ready observation was accepted")
	}
	if decision != sourceClockTimeoutNoInitialMedia {
		t.Fatalf("decision at 180 seconds after observation error = %q, want %q", decision, sourceClockTimeoutNoInitialMedia)
	}

	decision, err = sourceClockMonitorLifecycleDecision(
		tracker, nil, missingMediaReady, "monitor-session", 1,
		startedAt.Add(180*time.Second),
	)
	if err == nil {
		t.Fatal("nil metrics gate was accepted")
	}
	if decision != sourceClockTimeoutNoInitialMedia {
		t.Fatalf("decision at 180 seconds with nil metrics gate = %q, want %q", decision, sourceClockTimeoutNoInitialMedia)
	}
}

func TestSourceClockPublisherExit20CannotAuthorizeSessionStop(t *testing.T) {
	for _, generation := range []uint64{1, 2, 3} {
		if sourceClockPublisherExitAuthorizesSessionStop(20, false, generation, 3, sourceClockTimeoutNone) {
			t.Fatalf("exit code 20 authorized session stop for generation %d before Go timeout", generation)
		}
	}
	if sourceClockPublisherExitAuthorizesSessionStop(20, false, 3, 3, sourceClockTimeoutNoSignal) {
		t.Fatal("publisher exit 20 acquired session-stop authority from a Go timeout")
	}
	if sourceClockPublisherExitAuthorizesSessionStop(20, false, 2, 3, sourceClockTimeoutNoSignal) {
		t.Fatal("old publisher generation authorized session stop")
	}
}

func TestSourceClockMonitorDecisionKeepsInitialDeadlineWhenMetricsGateIsUnavailable(t *testing.T) {
	startedAt := time.Unix(2300, 0)
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)

	decision, err := sourceClockMonitorLifecycleDecision(
		tracker, nil, filepath.Join(t.TempDir(), "missing-media-ready.json"),
		"monitor-session", 1, startedAt.Add(3*time.Minute),
	)
	if err == nil {
		t.Fatal("nil metrics gate was accepted")
	}
	if decision != sourceClockTimeoutNoInitialMedia {
		t.Fatalf("decision with unavailable metrics at deadline = %q, want %q", decision, sourceClockTimeoutNoInitialMedia)
	}
}

func TestMonitorSourceClockInitialDeadlineWaitsForCleanupBeforePublishingStopped(t *testing.T) {
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_RECEIVER", "1")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER", "1")
	counterPath := filepath.Join(t.TempDir(), "publisher-starts.txt")
	t.Setenv("IMAGEPAD_TEST_SOURCE_CLOCK_PUBLISHER_COUNTER", counterPath)

	ctx, cancel := context.WithCancel(context.Background())
	receiver := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSourceClockReceiverProcess$")
	if err := receiver.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	tempDir := t.TempDir()
	stopFile := filepath.Join(tempDir, "publisher.stop")
	pipeline, err := startSourceClockGStreamerProcess(ctx, os.Args[0], []string{
		"-test.run=TestSourceClockPublisherProcess$", "fixture",
		"--publisher-generation", "1",
		"--video-listen-port", "44001",
		"--audio-listen-port", "44002",
		"--stop-file", stopFile,
	}, stopFile)
	if err != nil {
		cancel()
		_ = receiver.Wait()
		t.Fatal(err)
	}

	done := make(chan struct{})
	manager := New(nil)
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true}
	gate, err := newSourceClockMetricsGate("receiver-initial-timeout-test")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(&limitedBuffer{max: 8192}, "", gate, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	const sessionID = "initial-timeout-session"
	malformedMediaReady := filepath.Join(tempDir, "malformed.media-ready")
	if err := os.WriteFile(malformedMediaReady, []byte("{"), 0600); err != nil {
		cancel()
		t.Fatal(err)
	}
	observer := &sourceClockArtifactObserverForTest{root: tempDir, session: sessionID}
	if err := observer.PreparePublisher(t.Context(), airplaycontract.NewPublisherArtifacts(tempDir, sessionID, 1)); err != nil {
		cancel()
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	tickHandled := make(chan struct{})
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var callbackMu sync.Mutex
	callbackCalls := 0
	onStopped := func() {
		callbackMu.Lock()
		callbackCalls++
		if callbackCalls == 1 {
			close(cleanupStarted)
		}
		callbackMu.Unlock()
		<-releaseCleanup
	}
	startedAt := time.Unix(2400, 0)
	go manager.monitorSourceClockWithPublisherDoneAt(
		ctx, cancel, done, pipeline, nil, receiver, receiverOutput, nil,
		filepath.Join(tempDir, "missing.publisher-ready"), malformedMediaReady,
		sessionID, 1, tempDir, observer, 3*time.Minute, startedAt,
		ticks, tickHandled, tempDir, onStopped,
	)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if receiver.Process != nil {
				_ = receiver.Process.Kill()
			}
		}
	}()

	sendTick := func(now time.Time) {
		t.Helper()
		select {
		case ticks <- now:
		case <-time.After(2 * time.Second):
			t.Fatal("monitor did not receive lifecycle tick")
		}
		select {
		case <-tickHandled:
		case <-time.After(2 * time.Second):
			t.Fatal("monitor did not finish lifecycle observation")
		}
	}
	sendTick(startedAt.Add(179*time.Second + 999*time.Millisecond))
	select {
	case <-done:
		t.Fatal("session stopped before initial-media deadline")
	default:
	}
	sendTick(startedAt.Add(3 * time.Minute))
	select {
	case <-cleanupStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout cleanup did not start")
	}
	if status := manager.Status(); !status.Running || !status.ReceiverRunning {
		t.Fatalf("manager published stopped before owned cleanup completed: %+v", status)
	}
	close(releaseCleanup)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not finish after cleanup")
	}
	status := manager.Status()
	if status.Running || status.ReceiverRunning || status.BridgeRunning || status.MediaReady {
		t.Fatalf("manager did not publish a fully stopped state: %+v", status)
	}
	callbackMu.Lock()
	gotCallbackCalls := callbackCalls
	callbackMu.Unlock()
	if gotCallbackCalls != 1 {
		t.Fatalf("onStopped calls = %d, want 1", gotCallbackCalls)
	}
	if data, err := os.ReadFile(counterPath); err != nil || strings.TrimSpace(string(data)) != "1" {
		t.Fatalf("publisher starts = %q err=%v, want exactly one generation", strings.TrimSpace(string(data)), err)
	}
	initiators := observer.initiatorSnapshot()
	if len(initiators) != 1 || initiators[0].reason != airplaycontract.TerminationReasonNoInitialMedia {
		t.Fatalf("termination initiators = %+v, want no-initial-media", initiators)
	}
}

func TestSourceClockTerminationPriorityPrefersUserReceiverTimeoutPublisher(t *testing.T) {
	tests := []struct {
		name            string
		userStop        bool
		receiverExit    bool
		timeoutDecision sourceClockTimeoutDecision
		publisherExit   bool
		want            sourceClockTerminationCause
	}{
		{name: "user stop", userStop: true, receiverExit: true, timeoutDecision: sourceClockTimeoutNoSignal, publisherExit: true, want: sourceClockTerminationUserStop},
		{name: "receiver exit", receiverExit: true, timeoutDecision: sourceClockTimeoutNoInitialMedia, publisherExit: true, want: sourceClockTerminationReceiverExit},
		{name: "timeout", timeoutDecision: sourceClockTimeoutNoInitialMedia, publisherExit: true, want: sourceClockTerminationTimeout},
		{name: "publisher failure", publisherExit: true, want: sourceClockTerminationPublisherFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceClockTerminationCauseFor(tt.userStop, tt.receiverExit, tt.timeoutDecision, tt.publisherExit); got != tt.want {
				t.Fatalf("sourceClockTerminationCauseFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

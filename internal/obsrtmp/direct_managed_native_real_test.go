package obsrtmp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"imagepadserver/internal/airplay"
	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

// This integration catches a handoff that passes invented native events but
// cannot bootstrap an actual decoder, validate RTSP, or restore a live stream.
// Only the iPhone/UxPlay sender is a fixture. No fake ready/proof events are used.
func TestManagedDirectNativeRealExchange(t *testing.T) {
	if os.Getenv("IMAGEPAD_DIRECT_NATIVE_REAL") != "1" {
		t.Skip("isolated native bridge integration opt-in required")
	}
	for _, name := range []string{"apply-360", "restore-720-after-validation-failure", "backend-eof-recovery"} {
		t.Run(name, func(t *testing.T) { testManagedDirectNativeExchange(t, name) })
	}
}

type rejectValidatedNativeCandidate struct {
	airplaycontract.CandidateDelivery
	rejection error
}

func (d rejectValidatedNativeCandidate) Validate(ctx context.Context, oldEvents, candidateEvents []airplaycontract.Event) error {
	if err := d.CandidateDelivery.Validate(ctx, oldEvents, candidateEvents); err != nil {
		return err
	}
	return d.rejection
}

func testManagedDirectNativeExchange(t *testing.T, mode string) {
	restore := mode == "restore-720-after-validation-failure"
	backendEOF := mode == "backend-eof-recovery"
	bridge, err := filepath.Abs(os.Getenv("IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE"))
	if err != nil || os.Getenv("IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE") == "" {
		t.Fatal("explicit bridge path required")
	}
	if _, err := os.Stat(bridge); err != nil {
		t.Fatal(err)
	}
	gstRoot := os.Getenv("IMAGEPAD_TEST_GSTREAMER_ROOT")
	if gstRoot == "" {
		t.Fatal("explicit GStreamer runtime required")
	}
	root := t.TempDir()
	if evidenceRoot := os.Getenv("IMAGEPAD_DIRECT_NATIVE_EVIDENCE"); evidenceRoot != "" {
		if err := os.MkdirAll(evidenceRoot, 0700); err != nil {
			t.Fatal(err)
		}
		root, err = os.MkdirTemp(evidenceRoot, "native-exchange-")
		if err != nil {
			t.Fatal(err)
		}
		root, err = filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("evidence: %s", root)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	run := func(name string, args ...string) {
		cmd := exec.CommandContext(ctx, name, args...)
		hideWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
	}
	fixture := filepath.Join(root, "uxplay-source-clock.exe")
	run("go", "build", "-o", fixture, "../../cmd/airplay-source-clock-fixture")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	manifest, err := json.Marshal(map[string]any{
		"schema": 1, "protocolVersion": 1, "binary": filepath.Base(fixture), "binarySha256": hex.EncodeToString(sum[:]),
		"features": []string{"video-au", "audio-frame", "remote-ntp", "bounded-writer", "idle-wait", "audio-format-lock", "egress-metrics-v2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "imagepad-source-clock-capabilities.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "input.h264")
	run("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=60", "-t", "2", "-an", "-c:v", "libx264", "-threads", "2", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "60", "-bf", "0", "-pix_fmt", "yuv420p", "-x264-params", "aud=1:repeat-headers=1", "-f", "h264", input)
	mtx, err := ResolveMediaMTX()
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"IMAGEPAD_MEDIAMTX": mtx, "IMAGEPAD_MEDIAMTX_DEBUG_LOG": "", "IMAGEPAD_FFPROBE": ffprobe,
		"IMAGEPAD_DATA_DIR": filepath.Join(root, "data"), "IMAGEPAD_AIRPLAY": "1", "IMAGEPAD_AIRPLAY_PIPELINE": "source-clock",
		"IMAGEPAD_AIRPLAY_RECEIVER": fixture, "IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE": bridge,
		"IMAGEPAD_AIRPLAY_RECEIVER_LOG":    filepath.Join(root, "receiver.log"),
		"IMAGEPAD_AIRPLAY_FIXTURE_ADAPTER": "1", "IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE": input,
		"IMAGEPAD_AIRPLAY_FIXTURE_SCENARIO": "publisher-restart-initial-config-only", "IMAGEPAD_AIRPLAY_FIXTURE_DURATION": "95s",
		"IMAGEPAD_AIRPLAY_FIXTURE_NO_AUDIO": "1", "IMAGEPAD_AIRPLAY_FIXTURE_INITIAL_CONFIG_ONLY": "1",
		"IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_TRACE": filepath.Join(root, "video-trace.jsonl"),
		"IMAGEPAD_SOURCE_CLOCK_DEBUG":          "1", "GST_DEBUG_NO_COLOR": "1",
		"GST_PLUGIN_PATH_1_0": filepath.Join(gstRoot, "lib", "gstreamer-1.0"),
		"PATH":                filepath.Join(gstRoot, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
	} {
		t.Setenv(key, value)
	}
	publications := make(chan Session, 4)
	m := &Manager{outDir: filepath.Join(root, "out"), host: "127.0.0.1"}
	m.cb.OnStart = func(s Session) { publications <- s }
	plan, err := newDirectDeliveryPlan("0123456789abcdef", "native-initial", NormalizeLatencyProfile("rtsp-ultra"), "720", video.ResolveQuality("720", 0))
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.StartManagedDirectPublishingWithPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.StopDirect(info.Handle, 7*time.Second); info.PublisherObserver.SealPublishers() })
	receiver := airplay.New(nil)
	backendStopped := make(chan bool, 1)
	t.Cleanup(func() {
		if !receiver.Stop(10 * time.Second) {
			t.Error("receiver/publisher cleanup unconfirmed")
		}
	})
	if err := receiver.StartSourceClockDirect(ctx, plan.SessionID, info.PublishURL, info.PublisherArtifactRoot, info.PublisherObserver, airplay.DirectOutputConfig(info.Output), func() { backendStopped <- m.StopDirect(info.Handle, 7*time.Second) }); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-publications:
		if s.ID != plan.SessionID {
			t.Fatal("wrong public session")
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("native initial media did not publish: %+v", receiver.Status())
	}
	publicURL := m.rtspGate.backendRouter.snapshot().runtime.rtspURL()
	if _, err := runDirectBackendFFprobe(ctx, ffprobe, publicURL, 1280, 720, exec.CommandContext); err != nil {
		t.Fatalf("initial RTSP: %v", err)
	}
	next, err := newDirectDeliveryPlan(plan.SessionID, "native-360", NormalizeLatencyProfile("rtsp-ultra"), "360", video.ResolveQuality("360", 0))
	if err != nil {
		t.Fatal(err)
	}
	var executor DirectDeliveryExecutor = receiver
	rejection := errors.New("injected post-native-and-RTSP-validation failure")
	if restore {
		executor = directDeliveryExecutorFixture{
			apply: func(ctx context.Context, candidate airplaycontract.CandidateDelivery) error {
				return receiver.ApplySourceClockDelivery(ctx, rejectValidatedNativeCandidate{candidate, rejection})
			}, recover: receiver.RecoverSourceClockDelivery,
		}
	}
	var result DirectDeliveryChangeResult
	if backendEOF {
		// Stop only the exact isolated backend owned by this test. A real RTSP
		// EOF must drive native exit/completion and the managed recovery owner.
		oldRuntime := m.rtspGate.backendRouter.snapshot().runtime
		// Initial runtime ownership is the session supervisor, not a candidate
		// reservation. Invoke that exact runtime's bounded stop for this fault.
		if err := oldRuntime.stop(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		oldRuntime.mu.Lock()
		backendExited := oldRuntime.stopped
		oldRuntime.mu.Unlock()
		if !backendExited {
			t.Fatal("backend EOF injection did not stop its target")
		}
		deadline := time.NewTimer(30 * time.Second)
		defer deadline.Stop()
		for m.directDelivery.ledger.Snapshot().ActiveGeneration != 2 {
			select {
			case <-deadline.C:
				t.Fatalf("native backend EOF did not recover: %+v", receiver.Status())
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
		result.Generation = 2
		result.Applied = true
	} else {
		result, err = m.ReconfigureManagedDirectDelivery(ctx, info.Handle, next, ffprobe, executor)
	}
	wantGeneration, wantWidth, wantHeight := uint64(2), 640, 360
	if backendEOF {
		wantWidth, wantHeight = 1280, 720
	}
	if restore {
		wantGeneration, wantWidth, wantHeight = 3, 1280, 720
		if !errors.Is(err, rejection) || !result.Restored || result.Applied {
			t.Fatalf("native compensation: result=%+v err=%v status=%+v", result, err, receiver.Status())
		}
	} else if err != nil || !result.Applied || result.Restored {
		t.Fatalf("native apply: result=%+v err=%v status=%+v", result, err, receiver.Status())
	}
	if result.Generation != wantGeneration {
		t.Fatalf("committed generation=%d want=%d", result.Generation, wantGeneration)
	}
	c := m.directDelivery
	snapshot := c.ledger.Snapshot()
	if snapshot.ActiveGeneration != wantGeneration || snapshot.LastAllocatedGeneration != wantGeneration || snapshot.PreparedGeneration != 0 {
		t.Fatalf("ledger=%+v", snapshot)
	}
	if !receiver.Status().ReceiverRunning || !receiver.Status().BridgeRunning {
		t.Fatalf("receiver/publisher not alive: %+v", receiver.Status())
	}
	if c.gate.backendRouter.snapshot().runtime.rtspURL() != publicURL {
		t.Fatal("public RTSP URL changed")
	}
	var publicProbe *exec.Cmd
	output, err := runDirectBackendFFprobe(ctx, ffprobe, publicURL, wantWidth, wantHeight, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// Preserve per-frame decoder logs from this very probe, not a later join.
		// Extra JSON fields do not relax the normal stderr/decoded-frame gate.
		args = append([]string{"-show_log", "24"}, args...)
		publicProbe = exec.CommandContext(ctx, name, args...)
		return publicProbe
	})
	if publicProbe != nil && publicProbe.ProcessState != nil {
		for name, stream := range map[string]any{"public-probe.json": publicProbe.Stdout, "public-probe.stderr": publicProbe.Stderr} {
			if buffer, ok := stream.(*directRecordingProbeBuffer); ok {
				if writeErr := os.WriteFile(filepath.Join(root, name), buffer.Bytes(), 0600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
		}
	}
	if err != nil {
		t.Fatalf("committed RTSP: %v", err)
	}
	first := airplaycontract.NewPublisherArtifacts(info.PublisherArtifactRoot, plan.SessionID, 1)
	last := airplaycontract.NewPublisherArtifacts(info.PublisherArtifactRoot, plan.SessionID, wantGeneration)
	readReady := func(path string) airplaycontract.Event {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var event airplaycontract.Event
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		return event
	}
	a, b := readReady(first.Ready), readReady(last.Ready)
	if a.VideoListenPort == 0 || a.VideoListenPort != b.VideoListenPort || a.AudioListenPort != b.AudioListenPort {
		t.Fatal("receiver ingress changed")
	}
	select {
	case <-publications:
		t.Fatal("duplicate history publication")
	default:
	}
	if !receiver.Stop(10 * time.Second) {
		t.Fatal("native session did not stop")
	}
	select {
	case stopped := <-backendStopped:
		if !stopped {
			t.Fatal("session callback could not confirm backend stop")
		}
	default:
		t.Fatal("receiver stop did not execute session cleanup")
	}
	m.mu.Lock()
	clean := !m.running && len(m.directBackendReservations) == 0 && len(m.directRetiringBackends) == 0 && m.directBackendMonitorCount == 0
	m.mu.Unlock()
	if !clean {
		t.Fatal("native session retained backend ownership")
	}
	if pids, err := readMediaMTXProcessIDs(); err != nil || len(pids) != 0 {
		t.Fatalf("isolated owners=%v err=%v", pids, err)
	}
	t.Logf("REAL native+MediaMTX: case=%s generation=%d output=%dx%d decoded=%d same public URL and ingress, history1; sender=FIXTURE; iPhone/VRChat=NOT_RUN", mode, wantGeneration, wantWidth, wantHeight, output.Frames)
}

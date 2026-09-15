package obsrtmp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

func TestDirectManagedStartupRealInitialOwnership(t *testing.T) {
	if os.Getenv("IMAGEPAD_DIRECT_BACKEND_OUTPUT_REAL") != "1" {
		t.Skip("isolated real backend opt-in required")
	}
	exe, err := ResolveMediaMTX()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_MEDIAMTX", exe)
	t.Setenv("IMAGEPAD_MEDIAMTX_DEBUG_LOG", "")
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	publications := make(chan Session, 2)
	m := &Manager{outDir: t.TempDir(), host: "127.0.0.1"}
	m.cb.OnStart = func(session Session) { publications <- session }
	plan, err := newDirectDeliveryPlan("0123456789abcdef", "managed-initial", NormalizeLatencyProfile(LatencyModeRTSPUltra), "720", video.ResolveQuality("720", 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	info, err := m.StartManagedDirectPublishingWithPlan(ctx, plan)
	if err != nil {
		t.Fatalf("managed normal startup: %v", err)
	}
	t.Cleanup(func() { m.StopDirect(info.Handle, 7*time.Second); info.PublisherObserver.SealPublishers() })
	observer := info.PublisherObserver.(*directPublisherObserver)
	if observer.deliveryLedger == nil {
		t.Fatal("managed startup returned legacy observer")
	}
	route := m.rtspGate.backendRouter.snapshot()
	if route.sessionID != plan.SessionID || route.sessionEpoch != info.Handle.Generation || route.generation != 1 || route.requestID != "managed-initial" || route.runtime != m.mtx {
		t.Fatal("initial gate does not belong to the managed session")
	}
	initial := airplaycontract.NewPublisherArtifacts(info.PublisherArtifactRoot, plan.SessionID, 1)
	if err := observer.PreparePublisher(ctx, initial); err != nil {
		t.Fatalf("AirPlay initial launch claim: %v", err)
	}
	if err := observer.PreparePublisher(ctx, initial); err == nil {
		t.Fatal("initial publisher could claim artifacts twice")
	}
	if err := observer.PreparePublisher(ctx, airplaycontract.NewPublisherArtifacts(info.PublisherArtifactRoot, plan.SessionID, 2)); err == nil {
		t.Fatal("legacy retry allocated generation 2 outside the coordinator")
	}
	if snapshot := observer.deliveryLedger.Snapshot(); snapshot.ActiveGeneration != 1 || snapshot.LastAllocatedGeneration != 1 {
		t.Fatalf("initial ledger=%+v", snapshot)
	}
	if _, ready := observer.currentArtifacts(); ready {
		t.Fatal("initial launch claim became media-ready without media")
	}
	// Actual video/RTSP plus the normal startup supervisor. Only native ready
	// notifications below are fixtures; this is not an iPhone/GStreamer test.
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	pubCtx, stopPublisher := context.WithCancel(ctx)
	cmd := exec.CommandContext(pubCtx, ffmpeg, "-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30", "-an", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "30", "-bf", "0", "-pix_fmt", "yuv420p", "-f", "rtsp", "-rtsp_transport", "tcp", info.PublishURL)
	hideWindow(cmd)
	cmd.WaitDelay = 250 * time.Millisecond
	if err := cmd.Start(); err != nil {
		stopPublisher()
		t.Fatal(err)
	}
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- cmd.Wait() }()
	defer func() {
		stopPublisher()
		select {
		case <-publisherDone:
		case <-time.After(3 * time.Second):
			t.Error("isolated sender cleanup unconfirmed")
		}
	}()
	readyCtx, cancelReady := context.WithTimeout(ctx, 5*time.Second)
	defer cancelReady()
	for !route.runtime.pathReady(readyCtx) {
		select {
		case <-readyCtx.Done():
			t.Fatal("isolated sender did not reach MediaMTX")
		case <-time.After(25 * time.Millisecond):
		}
	}
	select {
	case <-publications:
		t.Fatal("RTSP fallback alone published without native media readiness")
	default:
	}
	now, running := time.Now().UTC(), uint64(1)
	for path, event := range map[string]airplaycontract.Event{
		initial.Ready:      {Schema: 2, SessionID: plan.SessionID, PublisherGeneration: 1, Event: "publisher-ready", At: now, ProtocolVersion: 1, VideoListenPort: 5000, AudioListenPort: 5001, PipelineStartAccepted: true},
		initial.MediaReady: {Schema: 2, SessionID: plan.SessionID, PublisherGeneration: 1, Event: "video-decoded", At: now, RunningTimeNS: &running, VideoDecoded: true},
	} {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		observer.ObservePublisher(event)
	}
	select {
	case published := <-publications:
		if published.ID != plan.SessionID || published.Recording != initial.Recording {
			t.Fatal("initial public session mismatch")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("managed startup did not publish validated initial media")
	}
	output, err := runDirectBackendFFprobe(ctx, ffprobe, route.runtime.rtspURL(), 1280, 720, exec.CommandContext)
	if err != nil {
		t.Fatalf("managed initial public RTSP: %v", err)
	}
	select {
	case <-publications:
		t.Fatal("initial public session was registered twice")
	default:
	}
	t.Logf("REAL managed startup: normal owner/gate/supervisor; 1280x720, decoded=%d; initial native notifications=FIXTURE, device=NOT_RUN", output.Frames)
	if !m.StopDirect(info.Handle, 7*time.Second) {
		t.Fatal("managed startup did not stop")
	}
	if snapshot := observer.deliveryLedger.Snapshot(); snapshot.TerminalEpoch != info.Handle.Generation {
		t.Fatalf("stop did not revoke managed epoch: %+v", snapshot)
	}
	if _, err := observer.deliveryLedger.Allocate(plan.SessionID, info.Handle.Generation, 1, "late", testContractDeliveryProfile(), testContractDeliveryOutput(640, 360)); err == nil {
		t.Fatal("stopped session allocated a late publisher")
	}
	m.mu.Lock()
	clean := len(m.directBackendReservations) == 0 && len(m.directRetiringBackends) == 0 && m.directBackendMonitorCount == 0
	m.mu.Unlock()
	if !clean {
		t.Fatal("managed startup retained owned backend")
	}
	if pids, err := readMediaMTXProcessIDs(); err != nil || len(pids) != 0 {
		t.Fatalf("isolated registry=%v err=%v", pids, err)
	}
}

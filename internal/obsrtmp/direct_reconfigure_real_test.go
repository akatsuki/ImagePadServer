package obsrtmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

// Real MediaMTX/FFmpeg/ffprobe, public TCP gate and session supervisor; fixture
// native proof/initial strict binding. NOT an iPhone or GStreamer qualification.
func TestDirectReconfigureRealBackendCommitKeepsPublicURL(t *testing.T) {
	testDirectReconfigureRealBackendCommit(t, "direct")
}

func TestDirectCandidateDeliveryRealBackendValidateThenCommit(t *testing.T) {
	testDirectReconfigureRealBackendCommit(t, "capability")
}

func TestManagedDirectChangeRealCommitKeepsPublicURL(t *testing.T) {
	testDirectReconfigureRealBackendCommit(t, "transaction")
}

func testDirectReconfigureRealBackendCommit(t *testing.T, mode string) {
	t.Helper()
	capability, transaction := mode != "direct", mode == "transaction"
	if os.Getenv("IMAGEPAD_DIRECT_BACKEND_OUTPUT_REAL") != "1" {
		t.Skip("opt-in isolated real RTSP test")
	}
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Cleanup(func() {
		pids, err := readMediaMTXProcessIDs()
		if err != nil || len(pids) != 0 {
			t.Errorf("private process registry not empty: %v %v", pids, err)
		}
	})
	mtx, err := ResolveMediaMTX()
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	c, plan, _ := directReconfigureFixture(t)
	c.ctx = ctx
	ports, err := allocMediaMTXPorts()
	if err != nil {
		t.Fatal(err)
	}
	user, pass, err := mediaMTXCredential()
	if err != nil {
		t.Fatal(err)
	}
	oldRuntime := newMediaMTXRuntime(mtx, directMediaMTXConfig(mediaMTXPathName(c.active.SessionID), user, pass, ports, "127.0.0.1", "", NormalizeLatencyProfile("rtsp-ultra")))
	t.Cleanup(func() {
		if err := oldRuntime.stop(3 * time.Second); err != nil {
			t.Error(err)
		}
	})
	if err := c.manager.startOwnedDirectBackend(ctx, oldRuntime); err != nil {
		t.Fatal(err)
	}
	oldRoute := directBackendRoute{sessionID: c.active.SessionID, sessionEpoch: 4, generation: 1, requestID: "initial", privateRTSPPort: ports.BackendRTSP, runtime: oldRuntime}
	gate := newRTSPGate(rtspGateConfig{PublicRTSPPort: ports.RTSP, PublicRTPPort: ports.RTP, PublicRTCPPort: ports.RTCP, BackendRTSPPort: ports.BackendRTSP, Path: oldRuntime.cfg.Path})
	gate.backendRouter = mustNewDirectBackendRouter(t, oldRoute)
	if err := gate.start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.stop() })
	c.gate, c.manager.rtspGate, c.manager.mtx = gate, gate, oldRuntime
	if transaction {
		c.manager.directDelivery = c
	}
	startVideo := func(runtime *mediaMTXRuntime, size string) func() {
		pubCtx, stopPub := context.WithCancel(ctx)
		cmd := exec.CommandContext(pubCtx, ffmpeg, "-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "testsrc2=size="+size+":rate=30", "-an", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "30", "-bf", "0", "-pix_fmt", "yuv420p", "-f", "rtsp", "-rtsp_transport", "tcp", runtime.directPublishURL())
		hideWindow(cmd)
		cmd.WaitDelay = 250 * time.Millisecond
		if err := cmd.Start(); err != nil {
			stopPub()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				stopPub()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("test video process did not exit")
				}
			})
		}
		t.Cleanup(stop)
		readyCtx, stopReady := context.WithTimeout(ctx, 5*time.Second)
		defer stopReady()
		for !runtime.pathReady(readyCtx) {
			select {
			case <-readyCtx.Done():
				t.Fatal("test video not ready")
			case <-time.After(25 * time.Millisecond):
			}
		}
		return stop
	}
	stopOld := startVideo(oldRuntime, "1280x720")
	// Only these native notifications are fixture data. Publication itself and
	// generation monitor ownership run through the real supervisor below.
	firstTime, firstRunning := time.Now().UTC(), uint64(1)
	firstReady := airplaycontract.Event{Schema: 2, SessionID: c.active.SessionID, PublisherGeneration: 1, Event: "publisher-ready", At: firstTime, ProtocolVersion: 1, VideoListenPort: 5000, AudioListenPort: 5001, PipelineStartAccepted: true}
	firstMedia := airplaycontract.Event{Schema: 2, SessionID: c.active.SessionID, PublisherGeneration: 1, Event: "video-decoded", At: firstTime, RunningTimeNS: &firstRunning, VideoDecoded: true}
	for path, event := range map[string]airplaycontract.Event{c.active.ArtifactPaths.Ready: firstReady, c.active.ArtifactPaths.MediaReady: firstMedia} {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		c.observer.ObservePublisher(event)
	}
	var publications atomic.Int32
	started, finalized := make(chan struct{}, 1), make(chan Session, 1)
	c.manager.cb.OnStart = func(Session) {
		publications.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
	}
	c.manager.cb.OnDone = func(s Session) { finalized <- s }
	c.manager.status.Publishing = true
	session := Session{ID: c.active.SessionID, Recording: c.active.ArtifactPaths.Recording, RecordingVerificationRequired: true,
		ActiveContract: &OBSActiveSessionContract{SessionID: c.active.SessionID, LatencyProfile: NormalizeLatencyProfile("rtsp-ultra")}}
	supervisorDone := make(chan struct{})
	c.manager.stop, c.manager.done = cancel, supervisorDone
	go c.manager.monitorDirectPublishing(ctx, cancel, 4, session, oldRuntime, gate, c.observer.root, supervisorDone, c.observer)
	defer func() {
		cancel()
		select {
		case <-supervisorDone:
		case <-time.After(7 * time.Second):
			t.Error("real supervisor did not finish")
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not publish initial real output")
	}
	publicURL := oldRuntime.rtspURL()
	if _, err := runDirectBackendFFprobe(ctx, ffprobe, publicURL, 1280, 720, exec.CommandContext); err != nil {
		t.Fatalf("old public output: %v", err)
	}
	var a *directReconfigureAttempt
	var issued airplaycontract.CandidateDelivery
	type transactionResult struct {
		result DirectDeliveryChangeResult
		err    error
	}
	transactionDone := make(chan transactionResult, 1)
	executorDone := make(chan error, 1)
	if transaction {
		issuedChannel := make(chan airplaycontract.CandidateDelivery, 1)
		executor := directDeliveryExecutorFixture{
			apply: func(ownerCtx context.Context, d airplaycontract.CandidateDelivery) error {
				issuedChannel <- d
				select {
				case err := <-executorDone:
					return err
				case <-ownerCtx.Done():
					return ownerCtx.Err()
				}
			},
			recover: func(context.Context, airplaycontract.CandidateDelivery) error {
				return errors.New("successful change unexpectedly requested restoration")
			},
		}
		go func() {
			result, err := c.manager.ReconfigureManagedDirectDelivery(ctx, c.manager.directHandle, plan, ffprobe, executor)
			transactionDone <- transactionResult{result, err}
		}()
		select {
		case issued = <-issuedChannel:
			a = issued.(*directCandidateDelivery).attempt
		case result := <-transactionDone:
			t.Fatalf("transaction never issued a publisher: %v", result.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	} else if capability {
		issued, err = c.PrepareCandidateDelivery(1, plan, ffprobe)
		if err != nil {
			t.Fatalf("candidate issuance: %v", err)
		}
		a = issued.(*directCandidateDelivery).attempt
	} else {
		a, err = c.PrepareDirectReconfigure(1, plan)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := c.manager.stopOwnedDirectBackend(a.backend.runtime, 3*time.Second); err != nil {
			t.Error(err)
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", a.backend.route.privateRTSPPort), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Error("candidate listener survived cleanup")
		}
	})
	if !capability {
		if _, err := c.StartDirectReconfigureBackend(a); err != nil {
			t.Fatal(err)
		}
	}
	if gate.backendRouter.snapshot() != oldRoute {
		t.Fatal("prepare/start switched public gate")
	}
	stopOld() // Wait is consumed before recording the fixture completion.
	if capability {
		if err := issued.ClaimPublisher(); err != nil {
			t.Fatal(err)
		}
	}
	startVideo(a.backend.runtime, "640x360")
	now, watermark, sequence, running := time.Now().UTC(), uint64(100), uint64(101), uint64(1)
	old := []airplaycontract.Event{{Schema: 2, SessionID: c.active.SessionID, PublisherGeneration: 1, Event: "video-watermark-final", At: now, SourceSessionGeneration: 4, SourceVideoSequence: &watermark}}
	candidate := []airplaycontract.Event{{Schema: 2, SessionID: c.active.SessionID, PublisherGeneration: 2, Event: "publisher-ready", At: now, ProtocolVersion: 1, VideoListenPort: 5000, AudioListenPort: 5001, PipelineStartAccepted: true}}
	for _, name := range []string{"video-input-idr", "video-decoded", "video-encoded-idr"} {
		candidate = append(candidate, airplaycontract.Event{Schema: 2, SessionID: c.active.SessionID, PublisherGeneration: 2, Event: name, At: now, SourceSessionGeneration: 4, SourceVideoSequence: &sequence, RunningTimeNS: &running, VideoDecoded: name == "video-decoded"})
	}
	for _, e := range candidate {
		c.observer.ObservePublisher(e)
	}
	c.observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: c.active.ArtifactPaths, Started: true, ExitConfirmed: true, CompletedAt: now})
	if capability {
		delivery := issued
		if err := delivery.Validate(ctx, old, candidate); err != nil {
			t.Fatalf("candidate capability validation: %v", err)
		}
		if c.ledger.Snapshot().ActiveGeneration != 1 || gate.backendRouter.snapshot() != oldRoute || gate.backendRouter.canAccept() {
			t.Fatal("private validation exposed candidate before final commit")
		}
		if err := delivery.Commit(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("candidate capability commit: %v", err)
		}
	} else {
		output, err := probeDirectBackendGeneration(ctx, ffprobe, a.descriptor, a.backend.route, exec.CommandContext)
		if err != nil {
			t.Fatalf("candidate decode: %v", err)
		}
		if err := c.CommitDirectReconfigure(a, old, candidate, output, time.Now().Add(10*time.Second)); err != nil {
			t.Fatalf("backend commit: %v", err)
		}
	}
	if transaction {
		executorDone <- nil
		select {
		case result := <-transactionDone:
			if result.err != nil || !result.result.Applied || result.result.Restored || result.result.Generation != 2 || result.result.Plan.Output.Height != 360 {
				t.Fatalf("transaction result: %+v %v", result.result, result.err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var publicCommand *exec.Cmd
	public, err := runDirectBackendFFprobe(ctx, ffprobe, publicURL, 640, 360, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		publicCommand = exec.CommandContext(ctx, name, args...)
		return publicCommand
	})
	if err != nil {
		if publicCommand != nil {
			t.Logf("public probe stderr: %s", publicCommand.Stderr.(*directRecordingProbeBuffer).String())
			t.Logf("public probe stdout: %s", publicCommand.Stdout.(*directRecordingProbeBuffer).String())
		}
		t.Fatalf("committed public output: %v", err)
	}
	if public.Frames < 2 || c.manager.rtspGate != gate || a.backend.runtime.rtspURL() != publicURL {
		t.Fatal("public identity or new decoded output lost")
	}
	oldRuntime.mu.Lock()
	oldStopped := oldRuntime.stopped
	oldRuntime.mu.Unlock()
	if !oldStopped || publications.Load() != 1 {
		t.Fatal("old backend survived or history was published twice")
	}
	cancel()
	select {
	case <-supervisorDone:
	case <-time.After(7 * time.Second):
		t.Fatal("committed supervisor cleanup timed out")
	}
	select {
	case last := <-finalized:
		if last.Recording != a.descriptor.ArtifactPaths.Recording || last.ActiveContract.QualityPreset.Height != 360 {
			t.Fatal("final callback used old generation")
		}
	default:
		t.Fatal("committed session final callback missing")
	}
	c.manager.mu.Lock()
	remaining := c.manager.directBackendMonitorCount + len(c.manager.directBackendReservations) + len(c.manager.directRetiringBackends)
	c.manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("session left %d backend owners", remaining)
	}
	t.Logf("REAL backend+gate+supervisor: URL unchanged; 1280x720 -> generation2=640x360, decoded=%d frames; history=1, old backend retired, final recording=gen2, owners=0; native proof=FIXTURE, device=NOT_RUN", public.Frames)
}

package obsrtmp

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func directReconfigureReadyFixture(t *testing.T) (*directDeliveryCoordinator, *directReconfigureAttempt, []airplaycontract.Event, []airplaycontract.Event, directBackendOutputProof, context.CancelFunc) {
	t.Helper()
	c, plan, cancel := directReconfigureFixture(t)
	c.manager.current = &Session{ID: c.active.SessionID, Recording: c.active.ArtifactPaths.Recording, ActiveContract: &OBSActiveSessionContract{SessionID: c.active.SessionID, LatencyProfile: NormalizeLatencyProfile("rtsp-ultra")}}
	a, err := c.PrepareDirectReconfigure(1, plan)
	if err != nil {
		t.Fatal(err)
	}
	proc := newFakeProcess()
	proc.exitOnStop = true
	a.backend.runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	a.backend.runtime.checkHealth = func(context.Context, string) error { return nil }
	t.Cleanup(func() { proc.finish(nil); _ = c.manager.stopOwnedDirectBackend(a.backend.runtime, time.Second) })
	if _, err := c.StartDirectReconfigureBackend(a); err != nil {
		t.Fatal(err)
	}
	a.backend.runtime.httpClient = &http.Client{Transport: directRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != a.backend.runtime.apiBaseURL()+"/v3/paths/get/"+a.backend.runtime.cfg.Path {
			t.Errorf("unexpected candidate path check: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ready":true}`)), Header: make(http.Header)}, nil
	})}
	now := time.Now().UTC()
	event := func(name string, generation, sequence uint64) airplaycontract.Event {
		running := uint64(1)
		return airplaycontract.Event{Schema: 2, SessionID: c.active.SessionID, PublisherGeneration: generation, Event: name, At: now,
			SourceSessionGeneration: 4, SourceVideoSequence: &sequence, RunningTimeNS: &running, VideoDecoded: name == "video-decoded",
			ProtocolVersion: 1, VideoListenPort: 5000, AudioListenPort: 5001, PipelineStartAccepted: true}
	}
	old := []airplaycontract.Event{event("video-watermark-final", 1, 100)}
	candidate := []airplaycontract.Event{event("publisher-ready", 2, 0), event("video-input-idr", 2, 101), event("video-decoded", 2, 101), event("video-encoded-idr", 2, 101)}
	for _, e := range candidate {
		c.observer.ObservePublisher(e)
	}
	c.observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: c.active.ArtifactPaths, Started: true, ExitConfirmed: true, CompletedAt: now})
	decoded, err := decodeDirectBackendOutputJSON([]byte(backendOutputFixture), 640, 360)
	if err != nil {
		t.Fatal(err)
	}
	output := directBackendOutputProof{descriptor: a.descriptor, route: a.backend.route, decoded: decoded, observedAt: now}
	return c, a, old, candidate, output, cancel
}

func TestDirectReconfigureCommitPromotesMatchingReadyGenerationTogether(t *testing.T) {
	c, a, old, candidate, output, _ := directReconfigureReadyFixture(t)
	gate := c.manager.rtspGate
	oldURL := c.manager.mtx.rtspURL()
	if err := c.CommitDirectReconfigure(a, old, candidate, output, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("commit ready candidate: %v", err)
	}
	if c.gate.backendRouter.snapshot() != a.backend.route || c.ledger.Snapshot().ActiveGeneration != 2 || c.pending != nil || c.active != a.descriptor {
		t.Fatal("route, ledger and coordinator were not promoted together")
	}
	if c.manager.rtspGate != gate || a.backend.runtime.rtspURL() != oldURL || c.manager.current.ID != a.descriptor.SessionID || c.manager.current.Recording != a.descriptor.ArtifactPaths.Recording {
		t.Fatal("commit replaced public session or retained old recording")
	}
	if c.manager.current.ActiveContract.QualityPreset.Height != 360 || c.manager.status.Latency.Mode != "rtsp-ultra" {
		t.Fatal("active output contract not updated")
	}
	if err := c.AbortDirectReconfigure(a); err == nil {
		t.Fatal("stale abort stopped committed generation")
	}
}

func TestDirectReconfigureCommitRefusesPartialStaleAndTerminalEvidence(t *testing.T) {
	for _, cause := range []string{"missing-native-stage", "wrong-sequence", "old-output", "deadline", "cancel", "terminal", "old-still-running", "candidate-exited", "backend-stopping", "ledger-aborted", "wrong-session"} {
		t.Run(cause, func(t *testing.T) {
			c, a, old, candidate, output, cancel := directReconfigureReadyFixture(t)
			route := c.gate.backendRouter.snapshot()
			deadline := time.Now().Add(time.Second)
			switch cause {
			case "missing-native-stage":
				candidate = candidate[:3]
			case "wrong-sequence":
				n := uint64(102)
				candidate[2].SourceVideoSequence = &n
			case "old-output":
				output.route = route
			case "deadline":
				deadline = time.Now().Add(-time.Second)
			case "cancel":
				cancel()
			case "terminal":
				c.gate.backendRouter.markTerminal()
			case "old-still-running":
				delete(c.observer.completions, 1)
			case "candidate-exited":
				c.observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: a.descriptor.ArtifactPaths, Started: true, ExitConfirmed: true})
			case "backend-stopping":
				c.manager.directBackendReservations[a.backend.runtime].stopping = true
			case "ledger-aborted":
				if err := c.ledger.Abort(a.descriptor); err != nil {
					t.Fatal(err)
				}
			case "wrong-session":
				c.manager.directHandle.ID = "9999999999999999"
			}
			if err := c.CommitDirectReconfigure(a, old, candidate, output, deadline); err == nil {
				t.Fatal("ineligible candidate committed")
			}
			if c.gate.backendRouter.snapshot() != route || c.ledger.Snapshot().ActiveGeneration != 1 || c.manager.current.Recording != c.active.ArtifactPaths.Recording {
				t.Fatal("failed commit partially changed delivery")
			}
		})
	}
}

func TestDirectReconfigureStopReportsCommittedRecordingAndProfile(t *testing.T) {
	c, a, old, candidate, output, _ := directReconfigureReadyFixture(t)
	initial := *c.manager.current
	if err := c.CommitDirectReconfigure(a, old, candidate, output, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var finished Session
	c.manager.cb.OnDone = func(s Session) { finished = s }
	c.manager.finishDirectPublishing(4, initial, true, "test stop")
	if finished.Recording != a.descriptor.ArtifactPaths.Recording || finished.ActiveContract.QualityPreset.Height != 360 {
		t.Fatal("session finalization reported the pre-switch recording or output contract")
	}
}

func TestDirectReconfigureCommitRechecksCancellationAfterConnectionDrain(t *testing.T) {
	c, a, old, candidate, output, cancel := directReconfigureReadyFixture(t)
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	connection, ok := c.gate.registerConnection(client)
	if !ok {
		t.Fatal("initial gate did not accept connection")
	}
	t.Cleanup(func() { c.gate.closeConnection(connection); c.gate.finishConnection(connection) })
	finished := make(chan error, 1)
	go func() {
		finished <- c.CommitDirectReconfigure(a, old, candidate, output, time.Now().Add(2*time.Second))
	}()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("old connection was not closed for drain")
	}
	cancel()
	c.gate.finishConnection(connection)
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("commit ignored cancellation during drain")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("commit did not return after drain")
	}
	if c.ledger.Snapshot().ActiveGeneration != 1 || c.gate.backendRouter.snapshot() != a.expected || c.pending != a {
		t.Fatal("late commit changed active generation")
	}
}

func TestDirectReconfigureCommittedArtifactsFollowLedgerNotLegacyCounter(t *testing.T) {
	c, a, old, candidate, output, _ := directReconfigureReadyFixture(t)
	if _, ok := c.observer.currentArtifacts(); ok {
		t.Fatal("prepared candidate leaked into active artifacts")
	}
	if err := c.CommitDirectReconfigure(a, old, candidate, output, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	current, ok := c.observer.currentArtifacts()
	if !ok || current != a.descriptor.ArtifactPaths {
		t.Fatal("committed strict generation is invisible to session supervisor")
	}
	if err := c.ledger.Terminate(c.active.SessionID, c.active.SessionEpoch); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.observer.currentArtifacts(); ok {
		t.Fatal("terminal ledger still exposes active artifacts")
	}
}

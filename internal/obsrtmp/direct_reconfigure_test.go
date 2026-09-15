package obsrtmp

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

func directReconfigureFixture(t *testing.T) (*directDeliveryCoordinator, DirectDeliveryPlan, context.CancelFunc) {
	t.Helper()
	root := t.TempDir()
	const id = "0123456789abcdef"
	ledger, err := airplaycontract.NewDeliveryLedger(root, id, 4)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := newDirectPublisherObserverWithLedger(root, ledger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	first, err := ledger.Allocate(id, 4, 0, "initial", testContractDeliveryProfile(), testContractDeliveryOutput(1280, 720))
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.PrepareDeliveryGeneration(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Commit(0, first); err != nil {
		t.Fatal(err)
	}
	cfg := defaultTestConfig()
	cfg.Path = mediaMTXPathName(id)
	cfg.Ports = mediaMTXPorts{RTSP: 49001, RTP: 49002, RTCP: 49003, API: 49004, HLS: 49005, BackendRTSP: 49000, BackendRTP: 49006, BackendRTCP: 49007}
	runtime := testRuntime(cfg)
	route := directBackendRoute{sessionID: id, sessionEpoch: 4, generation: 1, requestID: "initial", privateRTSPPort: cfg.Ports.mediaMTXRTSPPort(), runtime: runtime}
	router, err := newDirectBackendRouter(route)
	if err != nil {
		t.Fatal(err)
	}
	gate := newRTSPGate(rtspGateConfig{PublicRTSPPort: cfg.Ports.RTSP, PublicRTPPort: cfg.Ports.RTP, PublicRTCPPort: cfg.Ports.RTCP, BackendRTSPPort: route.privateRTSPPort, Path: cfg.Path})
	gate.backendRouter = router
	m := &Manager{running: true, directPublishing: true, listenerGeneration: 4, directHandle: DirectSessionHandle{ID: id, Generation: 4}, rtspGate: gate, mtx: runtime}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	c := &directDeliveryCoordinator{ctx: ctx, manager: m, gate: gate, ledger: ledger, observer: observer, active: first}
	plan, err := newDirectDeliveryPlan(id, "change-360", NormalizeLatencyProfile("rtsp-ultra"), "360", video.QualityPreset{Mode: "360", Height: 360, VideoBitrate: "1000k", MaxRate: "1200k", BufferSize: "2000k", AudioBitrate: "128k"})
	if err != nil {
		t.Fatal(err)
	}
	return c, plan, cancel
}

func TestDirectReconfigurePrepareKeepsActiveRouteAndBurnsAbortedGeneration(t *testing.T) {
	c, plan, _ := directReconfigureFixture(t)
	old := c.gate.backendRouter.snapshot()
	attempt, err := c.PrepareDirectReconfigure(1, plan)
	if err != nil {
		t.Fatalf("prepare candidate: %v", err)
	}
	if attempt.descriptor.Generation != 2 || attempt.descriptor.Output.Width != 640 || attempt.descriptor.Output.Height != 360 || attempt.backend == nil {
		t.Fatalf("candidate contract not captured: %+v", attempt.descriptor)
	}
	if c.gate.backendRouter.snapshot() != old || c.ledger.Snapshot().ActiveGeneration != 1 || attempt.backend.runtime.proc != nil {
		t.Fatal("prepare published candidate or started a process")
	}
	if _, err := c.PrepareDirectReconfigure(1, plan); !errors.Is(err, airplaycontract.ErrDeliveryGenerationPending) {
		t.Fatalf("second prepare: %v", err)
	}
	if err := c.AbortDirectReconfigure(attempt); err != nil {
		t.Fatal(err)
	}
	plan.RequestID = "retry-360"
	next, err := c.PrepareDirectReconfigure(1, plan)
	if err != nil {
		t.Fatal(err)
	}
	if next.descriptor.Generation != 3 || next.descriptor.ArtifactPaths.Recording == attempt.descriptor.ArtifactPaths.Recording {
		t.Fatal("aborted generation or recording was reused")
	}
	if err := c.AbortDirectReconfigure(attempt); err == nil {
		t.Fatal("stale abort accepted")
	}
	if c.ledger.Snapshot().PreparedGeneration != 3 || c.pending != next || c.gate.backendRouter.snapshot() != old {
		t.Fatal("stale abort changed current candidate or public route")
	}
	if err := c.AbortDirectReconfigure(next); err != nil {
		t.Fatal(err)
	}
}

func TestDirectReconfigurePrepareRejectsRevokedSessionBeforeAllocation(t *testing.T) {
	for _, cause := range []string{"canceled", "wrong-epoch", "terminal", "stale-active", "foreign-plan"} {
		t.Run(cause, func(t *testing.T) {
			c, plan, cancel := directReconfigureFixture(t)
			expected := uint64(1)
			switch cause {
			case "canceled":
				cancel()
			case "wrong-epoch":
				c.manager.listenerGeneration++
			case "terminal":
				c.gate.backendRouter.markTerminal()
			case "stale-active":
				expected = 9
			case "foreign-plan":
				plan.SessionID = "9999999999999999"
			}
			if _, err := c.PrepareDirectReconfigure(expected, plan); err == nil {
				t.Fatal("revoked session prepared candidate")
			}
			if c.ledger.Snapshot().LastAllocatedGeneration != 1 || c.pending != nil {
				t.Fatal("rejection allocated artifacts")
			}
		})
	}
}

func TestDirectReconfigureBackendStartsPrivatelyAndAbortConfirmsExit(t *testing.T) {
	c, plan, _ := directReconfigureFixture(t)
	old := c.gate.backendRouter.snapshot()
	a, err := c.PrepareDirectReconfigure(1, plan)
	if err != nil {
		t.Fatal(err)
	}
	proc := newFakeProcess()
	proc.exitOnStop = true
	a.backend.runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	a.backend.runtime.checkHealth = func(context.Context, string) error { return nil }
	t.Cleanup(func() { proc.finish(nil); _ = c.AbortDirectReconfigure(a) })
	endpoint, err := c.StartDirectReconfigureBackend(a)
	if err != nil {
		t.Fatalf("start candidate: %v", err)
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() != "127.0.0.1" || u.User == nil || endpoint == old.runtime.directPublishURL() {
		t.Fatal("candidate did not get a fresh private publish endpoint")
	}
	if c.gate.backendRouter.snapshot() != old || c.ledger.Snapshot().ActiveGeneration != 1 || len(c.manager.directBackendReservations) != 1 {
		t.Fatal("startup changed route or lost unpublished owner")
	}
	if _, err := c.StartDirectReconfigureBackend(a); err == nil {
		t.Fatal("same candidate started twice")
	}
	if err := c.AbortDirectReconfigure(a); err != nil {
		t.Fatal(err)
	}
	if proc.stopCalls.Load() != 1 || !a.backend.runtime.stopped || a.backend.runtime.dir != "" || len(c.manager.directBackendReservations) != 0 || c.ledger.Snapshot().PreparedGeneration != 0 {
		t.Fatal("abort failed to confirm cleanup")
	}
}

func TestDirectReconfigureBackendRevokedDuringStartupCannotReturnPublishURL(t *testing.T) {
	c, plan, cancel := directReconfigureFixture(t)
	a, err := c.PrepareDirectReconfigure(1, plan)
	if err != nil {
		t.Fatal(err)
	}
	proc := newFakeProcess()
	proc.exitOnStop = true
	a.backend.runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	a.backend.runtime.checkHealth = func(context.Context, string) error { cancel(); return nil }
	t.Cleanup(func() { proc.finish(nil); _ = c.manager.stopOwnedDirectBackend(a.backend.runtime, time.Second) })
	endpoint, err := c.StartDirectReconfigureBackend(a)
	if !errors.Is(err, context.Canceled) || endpoint != "" {
		t.Fatalf("revoked startup: endpoint-present=%t err=%v", endpoint != "", err)
	}
	if c.pending != nil || len(c.manager.directBackendReservations) != 0 || c.ledger.Snapshot().PreparedGeneration != 0 {
		t.Fatal("confirmed canceled candidate retains pending ownership")
	}
}

func TestDirectReconfigureAbortUnconfirmedExitPreventsNextAttempt(t *testing.T) {
	c, plan, _ := directReconfigureFixture(t)
	a, err := c.PrepareDirectReconfigure(1, plan)
	if err != nil {
		t.Fatal(err)
	}
	proc := newFakeProcess()
	proc.exitOnKill = false
	a.backend.runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	a.backend.runtime.checkHealth = func(context.Context, string) error { return nil }
	t.Cleanup(func() { proc.finish(nil); _ = c.manager.stopOwnedDirectBackend(a.backend.runtime, time.Second) })
	if _, err := c.StartDirectReconfigureBackend(a); err != nil {
		t.Fatal(err)
	}
	if err := c.AbortDirectReconfigure(a); !errors.Is(err, errMediaMTXProcessExitUnconfirmed) {
		t.Fatalf("abort lost unconfirmed exit: %v", err)
	}
	if c.pending != a || len(c.manager.directBackendReservations) != 1 {
		t.Fatal("unconfirmed candidate was forgotten")
	}
	if _, err := c.PrepareDirectReconfigure(1, plan); !errors.Is(err, airplaycontract.ErrDeliveryGenerationPending) {
		t.Fatalf("new attempt allowed before exit: %v", err)
	}
	if c.ledger.Snapshot().LastAllocatedGeneration != 2 {
		t.Fatal("uncertain cleanup burned a compensation generation")
	}
	proc.finish(nil)
	if err := c.AbortDirectReconfigure(a); err != nil {
		t.Fatal(err)
	}
	plan.RequestID = "confirmed-recovery"
	next, err := c.PrepareDirectReconfigure(1, plan)
	if err != nil || next.descriptor.Generation != 3 {
		t.Fatalf("confirmed abort did not release allocation: %v", err)
	}
	if err := c.AbortDirectReconfigure(next); err != nil {
		t.Fatal(err)
	}
}

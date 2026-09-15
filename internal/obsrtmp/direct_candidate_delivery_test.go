package obsrtmp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func TestDirectCandidateWaitsForPrivatePathBeforeDecoding(t *testing.T) {
	for _, neverReady := range []bool{false, true} {
		name := "delayed-path"
		if neverReady {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			c, a, old, next, _, _ := directReconfigureReadyFixture(t)
			checks, probes := 0, 0
			a.backend.runtime.httpClient.Transport = directRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				checks++
				body := `{"ready":false}`
				if checks >= 2 && !neverReady {
					body = `{"ready":true}`
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			d := &directCandidateDelivery{owner: c, attempt: a, ffprobe: "probe", command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				probes++
				if checks < 2 || neverReady {
					t.Error("decoder probe started before the private path existed")
				}
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDirectBackendOutputHelperProcess$")
				cmd.Env = append(os.Environ(), "IMAGEPAD_TEST_BACKEND_OUTPUT=good")
				return cmd
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			if neverReady {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 80*time.Millisecond)
			}
			defer cancel()
			err := d.Validate(ctx, old, next)
			if neverReady {
				if !errors.Is(err, context.DeadlineExceeded) || probes != 0 {
					t.Fatalf("unready path err=%v probes=%d", err, probes)
				}
			} else if err != nil || probes != 1 || checks < 2 {
				t.Fatalf("delayed path err=%v checks=%d probes=%d", err, checks, probes)
			}
			if c.ledger.Snapshot().ActiveGeneration != 1 {
				t.Fatal("path readiness promoted without commit")
			}
		})
	}
}

func TestDirectCandidateDeliveryAbortWaitsForClaimedPublisherExit(t *testing.T) {
	c, a, _, _, _, _ := directReconfigureReadyFixture(t)
	d := &directCandidateDelivery{owner: c, attempt: a}
	if err := d.ClaimPublisher(); err != nil {
		t.Fatal(err)
	}
	if err := d.ClaimPublisher(); err == nil {
		t.Fatal("candidate publisher was claimed twice")
	}
	if err := d.Abort(); err == nil {
		t.Fatal("abort stopped backend while its claimed publisher exit was unknown")
	}
	if c.pending != a || c.ledger.Snapshot().PreparedGeneration != 2 || len(c.manager.directBackendReservations) != 1 {
		t.Fatal("unsafe abort altered candidate ownership")
	}
	c.observer.CompletePublisher(airplaycontract.PublisherCompletion{Artifacts: a.descriptor.ArtifactPaths, Started: true, ExitConfirmed: true, CompletedAt: time.Now()})
	if err := d.Abort(); err != nil {
		t.Fatalf("confirmed publisher could not release backend: %v", err)
	}
	if c.pending != nil || len(c.manager.directBackendReservations) != 0 {
		t.Fatal("confirmed abort retained backend owner")
	}
}

func TestDirectCandidateDeliveryAbortRevokesValidationBeforeProbe(t *testing.T) {
	c, a, old, next, _, _ := directReconfigureReadyFixture(t)
	called := false
	d := &directCandidateDelivery{owner: c, attempt: a, command: func(context.Context, string, ...string) *exec.Cmd {
		called = true
		return exec.Command(os.Args[0], "-test.run=^$")
	}}
	if err := d.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(t.Context(), old, next); err == nil {
		t.Fatal("aborted candidate validated")
	}
	if called {
		t.Fatal("aborted candidate launched an output probe")
	}
}

func TestDirectCandidateDeliveryIssuerFailureBurnsGeneration(t *testing.T) {
	c, plan, _ := directReconfigureFixture(t)
	old := c.gate.backendRouter.snapshot()
	old.runtime.exe = filepath.Join(t.TempDir(), "missing-mediamtx.exe")
	delivery, err := c.PrepareCandidateDelivery(1, plan, os.Args[0])
	if err == nil || delivery != nil {
		t.Fatal("missing backend produced a usable capability")
	}
	snapshot := c.ledger.Snapshot()
	if snapshot.LastAllocatedGeneration != 2 || snapshot.ActiveGeneration != 1 || snapshot.PreparedGeneration != 0 {
		t.Fatalf("failed candidate did not burn its generation: %+v", snapshot)
	}
	if c.pending != nil || len(c.manager.directBackendReservations) != 0 || c.gate.backendRouter.snapshot() != old {
		t.Fatal("failed issuer leaked ownership or moved old route")
	}
}

func TestDirectCandidateDeliveryValidationCannotPromoteWithoutFinalCommit(t *testing.T) {
	c, a, old, candidate, _, _ := directReconfigureReadyFixture(t)
	d := &directCandidateDelivery{owner: c, attempt: a, ffprobe: "probe", command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "probe" || args[len(args)-1] != a.backend.runtime.backendRTSPURL() {
			t.Fatal("validation read a different backend")
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDirectBackendOutputHelperProcess$")
		cmd.Env = append(os.Environ(), "IMAGEPAD_TEST_BACKEND_OUTPUT=good")
		return cmd
	}}
	if err := d.Commit(time.Now().Add(time.Second)); err == nil {
		t.Fatal("candidate committed without output validation")
	}
	if err := d.Validate(t.Context(), old, candidate); err != nil {
		t.Fatalf("candidate validation: %v", err)
	}
	if c.ledger.Snapshot().ActiveGeneration != 1 || c.gate.backendRouter.snapshot() != a.expected || c.gate.backendRouter.canAccept() {
		t.Fatal("validation must drain old gate but cannot publish candidate")
	}
	if err := d.Commit(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if c.ledger.Snapshot().ActiveGeneration != 2 || c.gate.backendRouter.snapshot() != a.backend.route {
		t.Fatal("final commit did not move both routes")
	}
	if err := d.Commit(time.Now().Add(time.Second)); err == nil {
		t.Fatal("candidate committed twice")
	}
}

func TestDirectCandidateDeliveryRevocationAfterValidationNeverPromotes(t *testing.T) {
	for _, cause := range []string{"abort", "deadline", "session-stop"} {
		t.Run(cause, func(t *testing.T) {
			c, a, old, candidate, _, _ := directReconfigureReadyFixture(t)
			ctx, cancel := context.WithCancel(c.ctx)
			defer cancel()
			c.ctx = ctx
			d := &directCandidateDelivery{owner: c, attempt: a, ffprobe: "probe", command: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDirectBackendOutputHelperProcess$")
				cmd.Env = append(os.Environ(), "IMAGEPAD_TEST_BACKEND_OUTPUT=good")
				return cmd
			}}
			if err := d.Validate(t.Context(), old, candidate); err != nil {
				t.Fatal(err)
			}
			if cause != "abort" {
				deadline := time.Now().Add(time.Second)
				if cause == "session-stop" {
					cancel()
				} else {
					deadline = time.Now().Add(-time.Second)
				}
				if err := d.Commit(deadline); err == nil {
					t.Fatal("revoked delivery became active")
				}
			}
			if err := d.Abort(); err != nil {
				t.Fatal(err)
			}
			if c.ledger.Snapshot().ActiveGeneration != 1 || c.ledger.Snapshot().PreparedGeneration != 0 || c.gate.backendRouter.snapshot() != a.expected {
				t.Fatal("aborted candidate changed old route or retained a prepared generation")
			}
			if !c.gate.backendRouter.canAccept() {
				t.Fatal("candidate left old route indefinitely draining")
			}
			if err := d.Commit(time.Now().Add(time.Second)); err == nil {
				t.Fatal("aborted validation could be replayed")
			}
		})
	}
}

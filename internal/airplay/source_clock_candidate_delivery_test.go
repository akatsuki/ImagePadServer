package airplay

import (
	"context"
	"errors"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

// This substitutes only the downstream process owner; request admission and
// session state changes are exercised through the real AirPlay manager.
type sourceClockCandidateDeliveryForTest struct {
	binding  airplaycontract.CandidateDeliveryBinding
	validate func(context.Context, []airplaycontract.Event, []airplaycontract.Event) error
	commit   func(time.Time) error
}

func (c *sourceClockCandidateDeliveryForTest) Binding() airplaycontract.CandidateDeliveryBinding {
	return c.binding
}

func (c *sourceClockCandidateDeliveryForTest) ClaimPublisher() error { return nil }
func (c *sourceClockCandidateDeliveryForTest) Validate(ctx context.Context, old, candidate []airplaycontract.Event) error {
	if c.validate != nil {
		return c.validate(ctx, old, candidate)
	}
	return nil
}
func (c *sourceClockCandidateDeliveryForTest) Commit(deadline time.Time) error {
	if c.commit != nil {
		return c.commit(deadline)
	}
	return nil
}
func (c *sourceClockCandidateDeliveryForTest) Abort() error { return nil }

func bindSourceClockCandidateForTest(request SourceClockDeliveryRequest) *sourceClockCandidateDeliveryForTest {
	return &sourceClockCandidateDeliveryForTest{binding: airplaycontract.CandidateDeliveryBinding{
		ExpectedActiveGeneration: request.ExpectedActiveGeneration,
		PublishURL:               request.PublishURL,
		Descriptor: airplaycontract.DeliveryGeneration{
			SessionID: request.Artifacts.SessionID, SessionEpoch: 9,
			RequestID: request.RequestID, Generation: request.CandidateGeneration,
			Output: airplaycontract.DeliveryOutput(request.Output), ArtifactPaths: request.Artifacts,
			ExpectedRecordingWidth: request.Output.Width, ExpectedRecordingHeight: request.Output.Height,
		},
	}}
}

func TestReconfigureSourceClockDeliveryAcceptsOwnerBoundCandidateWithoutTestBypass(t *testing.T) {
	manager := plannedDeliveryTestManager()
	request := plannedDeliveryRequest()
	request.allowEvidenceInsufficientForTest = false
	request.CandidateDelivery = bindSourceClockCandidateForTest(request)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- manager.ReconfigureSourceClockDelivery(ctx, request) }()
	select {
	case command := <-manager.deliveryReconfigure:
		if command.request.CandidateDelivery != request.CandidateDelivery {
			t.Fatal("owner capability was lost")
		}
		manager.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureNotImplemented)
		if err := <-result; !errors.Is(err, ErrDeliveryReconfigureNotImplemented) {
			t.Fatalf("result=%v", err)
		}
	case err := <-result:
		t.Fatalf("matching owner-bound request was rejected before queue: %v", err)
	case <-ctx.Done():
		t.Fatal("request admission stalled")
	}
	if manager.deliveryActiveGeneration != 4 {
		t.Fatal("queueing promoted an unvalidated generation")
	}
}

func TestReconfigureSourceClockDeliveryRejectsMismatchedOwnerBindingBeforeQueue(t *testing.T) {
	for name, mutate := range map[string]func(*airplaycontract.CandidateDeliveryBinding){
		"old generation":       func(b *airplaycontract.CandidateDeliveryBinding) { b.ExpectedActiveGeneration++ },
		"candidate generation": func(b *airplaycontract.CandidateDeliveryBinding) { b.Descriptor.Generation++ },
		"session":              func(b *airplaycontract.CandidateDeliveryBinding) { b.Descriptor.SessionID = "other" },
		"missing epoch":        func(b *airplaycontract.CandidateDeliveryBinding) { b.Descriptor.SessionEpoch = 0 },
		"request":              func(b *airplaycontract.CandidateDeliveryBinding) { b.Descriptor.RequestID = "other" },
		"private URL":          func(b *airplaycontract.CandidateDeliveryBinding) { b.PublishURL = "rtsp://127.0.0.1/wrong" },
		"output":               func(b *airplaycontract.CandidateDeliveryBinding) { b.Descriptor.Output.GOPFrames++ },
		"artifact":             func(b *airplaycontract.CandidateDeliveryBinding) { b.Descriptor.ArtifactPaths.Ready = "old.ready" },
		"recording size":       func(b *airplaycontract.CandidateDeliveryBinding) { b.Descriptor.ExpectedRecordingWidth++ },
	} {
		t.Run(name, func(t *testing.T) {
			manager := plannedDeliveryTestManager()
			request := plannedDeliveryRequest()
			candidate := bindSourceClockCandidateForTest(request)
			mutate(&candidate.binding)
			request.CandidateDelivery = candidate
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if err := manager.ReconfigureSourceClockDelivery(ctx, request); !errors.Is(err, ErrDeliveryReconfigureStale) {
				t.Fatalf("mismatched owner admission=%v", err)
			}
			if len(manager.deliveryReconfigure) != 0 || manager.deliveryReconfigurePending {
				t.Fatal("mismatched owner was queued")
			}
		})
	}
}

func TestSourceClockCandidateValidationCancelsAndJoinsOnReceiverPriority(t *testing.T) {
	started, joined := make(chan struct{}), make(chan struct{})
	candidate := &sourceClockCandidateDeliveryForTest{validate: func(ctx context.Context, _, _ []airplaycontract.Event) error {
		close(started)
		<-ctx.Done()
		close(joined)
		return ctx.Err()
	}}
	err, consumed, finished := validateSourceClockCandidateOutput(t.Context(), candidate, nil, nil, nil, func() error {
		select {
		case <-started:
			return ErrDeliveryReconfigureUnavailable
		default:
			return nil
		}
	})
	if !errors.Is(err, ErrDeliveryReconfigureUnavailable) || consumed || !finished {
		t.Fatalf("priority result=%v, consumed=%v, joined=%v", err, consumed, finished)
	}
	select {
	case <-joined:
	default:
		t.Fatal("validation returned before canceled downstream probe joined")
	}
}

func TestSourceClockCandidateValidationRejectsExitBeforeSuccessfulProbeReturns(t *testing.T) {
	done := make(chan error, 1)
	exitErr := errors.New("candidate exited")
	candidate := &sourceClockCandidateDeliveryForTest{validate: func(context.Context, []airplaycontract.Event, []airplaycontract.Event) error {
		done <- exitErr
		return nil
	}}
	err, consumed, joined := validateSourceClockCandidateOutput(t.Context(), candidate, nil, nil, done, nil)
	if !errors.Is(err, exitErr) || !consumed || !joined {
		t.Fatalf("exit lost to probe success: %v, consumed=%v, joined=%v", err, consumed, joined)
	}
	if len(done) != 0 {
		t.Fatal("candidate Wait was not consumed exactly once")
	}
}

func TestSourceClockCandidateCommitRejectsStopStaleAndExpiredBeforeDownstreamMutation(t *testing.T) {
	for _, kind := range []string{"user-stop", "cancel", "deadline", "stale-generation", "stale-reply", "missing-request", "backend-failure"} {
		t.Run(kind, func(t *testing.T) {
			manager := plannedDeliveryTestManager()
			request := plannedDeliveryRequest()
			candidate := bindSourceClockCandidateForTest(request)
			backendFailure := errors.New("downstream rejected commit")
			candidate.commit = func(time.Time) error {
				if kind != "backend-failure" {
					t.Fatal("rejected session reached downstream mutation")
				}
				return backendFailure
			}
			request.CandidateDelivery = candidate
			command := sourceClockDeliveryCommand{request: request, reply: make(chan error, 1), identity: &struct{}{}}
			manager.deliveryReconfigurePending = true
			manager.deliveryReconfigureRequestID = request.RequestID
			manager.deliveryReconfigureReply = command.reply
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			deadline := time.Now().Add(time.Minute)
			switch kind {
			case "user-stop":
				manager.stopInitiator = airplaycontract.TerminationReasonUserStop
			case "cancel":
				cancel()
			case "deadline":
				deadline = time.Now().Add(-time.Second)
			case "stale-generation":
				manager.deliveryActiveGeneration = 3
			case "stale-reply":
				manager.deliveryReconfigureReply = make(chan error, 1)
			case "missing-request":
				manager.deliveryReconfigurePending = false
			}
			oldGeneration, oldURL, oldOutput := manager.deliveryActiveGeneration, manager.deliveryActivePublishURL, manager.deliveryActiveOutput
			err := manager.commitSourceClockCandidate(ctx, command, deadline)
			if err == nil {
				t.Fatal("invalid commit succeeded")
			}
			if kind == "backend-failure" && !errors.Is(err, backendFailure) {
				t.Fatalf("backend failure lost: %v", err)
			}
			if manager.deliveryActiveGeneration != oldGeneration || manager.deliveryActivePublishURL != oldURL || manager.deliveryActiveOutput != oldOutput {
				t.Fatal("failed commit changed active publisher snapshot")
			}
		})
	}
}

func TestSourceClockCandidateCommitDeadlinePreservesReceiverTimeline(t *testing.T) {
	start := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		lifecycle SessionLifecycle
		seconds   int
	}{
		{"initial", SessionLifecycle{StartedAt: start, Timeout: 3 * time.Minute}, 180},
		{"video-only", SessionLifecycle{StartedAt: start, FirstDecodedAt: start.Add(30 * time.Second), LastVideoAt: start.Add(time.Minute), Timeout: 3 * time.Minute}, 240},
		{"audio-continues", SessionLifecycle{StartedAt: start, FirstDecodedAt: start.Add(30 * time.Second), LastVideoAt: start.Add(time.Minute), LastAudioAt: start.Add(90 * time.Second), Timeout: 3 * time.Minute}, 270},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.lifecycle
			deadline := sourceClockCandidateCommitDeadline(tt.lifecycle)
			if !deadline.Equal(start.Add(time.Duration(tt.seconds) * time.Second)) {
				t.Fatalf("deadline=%v", deadline)
			}
			if tt.lifecycle.NoSignalDecision(deadline.Add(-time.Nanosecond)) != sourceClockTimeoutNone || tt.lifecycle.NoSignalDecision(deadline) == sourceClockTimeoutNone {
				t.Fatal("commit deadline disagrees with receiver timeout boundary")
			}
			if tt.lifecycle != before {
				t.Fatal("delivery changed receiver timeline")
			}
		})
	}
}

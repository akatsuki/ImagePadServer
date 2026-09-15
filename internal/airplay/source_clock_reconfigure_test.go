package airplay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func plannedDeliveryTestManager() *Manager {
	manager := New(nil)
	manager.running = true
	manager.status = Status{Running: true, ReceiverRunning: true, BridgeRunning: true, MediaReady: true, MediaReadyKnown: true}
	manager.deliveryRetry = make(chan struct{}, 1)
	manager.deliveryReconfigure = make(chan sourceClockDeliveryCommand, 1)
	manager.deliverySessionID = "0123456789abcdef"
	manager.deliveryActiveGeneration = 4
	manager.deliveryActivePublishURL = "rtsp://127.0.0.1/current"
	manager.deliveryActiveOutput = DirectOutputConfig{Width: 1280, Height: 720, SourceFPS: 60, OutputFPS: 30, VideoBitrateKbps: 2500, MaxRateKbps: 3000, BufferSizeKbps: 5000, AudioBitrateBps: 128000, GOPFrames: 30}
	manager.status.DeliveryPhase = string(SourceClockDeliveryActive)
	return manager
}

func plannedDeliveryRequest() SourceClockDeliveryRequest {
	return SourceClockDeliveryRequest{
		RequestID:                        "request-5",
		ExpectedActiveGeneration:         4,
		CandidateGeneration:              5,
		PublishURL:                       "rtsp://127.0.0.1/next",
		Output:                           DirectOutputConfig{Width: 1920, Height: 1080, SourceFPS: 60, OutputFPS: 30, VideoBitrateKbps: 4500, MaxRateKbps: 5200, BufferSizeKbps: 9000, AudioBitrateBps: 160000, GOPFrames: 60},
		Artifacts:                        airplaycontract.PublisherArtifacts{SessionID: "0123456789abcdef", Generation: 5, Recording: "generation-5.mp4", Ready: "generation-5.ready", MediaReady: "generation-5.media-ready", EventLog: "generation-5.events", ProcessLog: "generation-5.log", StopRequest: "generation-5.stop"},
		allowEvidenceInsufficientForTest: true,
	}
}

func TestReconfigureSourceClockDeliveryRejectsWithoutReadinessEvidenceBeforeQueue(t *testing.T) {
	manager := plannedDeliveryTestManager()
	request := plannedDeliveryRequest()
	request.allowEvidenceInsufficientForTest = false
	if err := manager.ReconfigureSourceClockDelivery(t.Context(), request); !errors.Is(err, ErrDeliveryReconfigureEvidenceInsufficient) {
		t.Fatalf("error=%v, want evidence-insufficient", err)
	}
	select {
	case command := <-manager.deliveryReconfigure:
		t.Fatalf("evidence-insufficient request was queued: %+v", command.request)
	default:
	}
}

func TestPlannedSourceClockPublisherArgsUseRequestWithoutRewritingStableIdentity(t *testing.T) {
	request := plannedDeliveryRequest()
	request.Artifacts = airplaycontract.PublisherArtifacts{
		SessionID:   request.Artifacts.SessionID,
		Generation:  request.CandidateGeneration,
		Recording:   "candidate-recording.mp4",
		Ready:       "candidate.ready.json",
		MediaReady:  "candidate.media-ready.json",
		EventLog:    "candidate.events.jsonl",
		ProcessLog:  "candidate.log",
		StopRequest: "candidate.stop",
	}
	old := buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: 48001,
		AudioListenPort: 48002,
		PublishURL:      "rtsp://127.0.0.1/old",
		SessionToken:    strings.Repeat("ab", 16),
		Artifacts:       airplaycontract.NewPublisherArtifacts(t.TempDir(), request.Artifacts.SessionID, 4),
		Output:          plannedDeliveryTestManager().deliveryActiveOutput,
	})

	candidate, err := buildPlannedSourceClockPublisherArgs(old, request)
	if err != nil {
		t.Fatal(err)
	}
	if got := commandArgValue(candidate, "--publish-url"); got != request.PublishURL {
		t.Fatalf("publish URL=%q, want %q", got, request.PublishURL)
	}
	if got := commandArgValue(candidate, "--publisher-generation"); got != "5" {
		t.Fatalf("candidate generation=%q, want 5", got)
	}
	if got := commandArgValue(candidate, "--session-token"); got != strings.Repeat("ab", 16) {
		t.Fatalf("session token changed: %q", got)
	}
	if got := commandArgValue(candidate, "--video-listen-port"); got != "48001" {
		t.Fatalf("video listen port=%q, want 48001", got)
	}
	if got := commandArgValue(candidate, "--audio-listen-port"); got != "48002" {
		t.Fatalf("audio listen port=%q, want 48002", got)
	}
	for _, pair := range []struct {
		flag string
		want string
	}{
		{"--recording", request.Artifacts.Recording},
		{"--ready-file", request.Artifacts.Ready},
		{"--media-ready-file", request.Artifacts.MediaReady},
		{"--event-log", request.Artifacts.EventLog},
		{"--stop-file", request.Artifacts.StopRequest},
		{"--width", "1920"},
		{"--height", "1080"},
	} {
		if got := commandArgValue(candidate, pair.flag); got != pair.want {
			t.Fatalf("%s=%q, want %q", pair.flag, got, pair.want)
		}
	}
	if got := commandArgValue(old, "--publish-url"); got != "rtsp://127.0.0.1/old" {
		t.Fatalf("old args mutated: %q", got)
	}
}

func TestPlannedSourceClockArgsRejectProcessLogNotDerivedFromStopRequest(t *testing.T) {
	request := plannedDeliveryRequest()
	request.Artifacts.ProcessLog = "wrong.log"
	old := buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: 48001,
		AudioListenPort: 48002,
		PublishURL:      "rtsp://127.0.0.1/old",
		SessionToken:    strings.Repeat("ab", 16),
		Artifacts:       airplaycontract.NewPublisherArtifacts(t.TempDir(), request.Artifacts.SessionID, 4),
		Output:          plannedDeliveryTestManager().deliveryActiveOutput,
	})
	if _, err := buildPlannedSourceClockPublisherArgs(old, request); err == nil {
		t.Fatal("mismatched process log was accepted")
	}
}

func TestPlannedSourceClockArtifactsRejectEmptyAndOverlappingPaths(t *testing.T) {
	request := plannedDeliveryRequest()
	current := airplaycontract.NewPublisherArtifacts(t.TempDir(), request.Artifacts.SessionID, request.ExpectedActiveGeneration)
	if err := validatePublisherArtifactsForPlannedExchange(request.Artifacts, current); err != nil {
		t.Fatalf("valid candidate artifacts rejected: %v", err)
	}
	empty := request.Artifacts
	empty.EventLog = ""
	if err := validatePublisherArtifactsForPlannedExchange(empty, current); err == nil {
		t.Fatal("empty candidate event log was accepted")
	}
	overlap := request.Artifacts
	overlap.Ready = current.Ready
	if err := validatePublisherArtifactsForPlannedExchange(overlap, current); err == nil {
		t.Fatal("candidate artifact overlapping current generation was accepted")
	}
}

func TestPlannedSourceClockReadinessCannotPromoteWithoutOutputEvidence(t *testing.T) {
	if !errors.Is(ErrDeliveryReconfigureEvidenceInsufficient, ErrDeliveryReconfigureEvidenceInsufficient) {
		t.Fatal("evidence boundary error is not available")
	}
}

func TestPlannedSourceClockReadinessRequiresFreshRealVideoChain(t *testing.T) {
	startedAt := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	base := []airplaycontract.Event{
		{Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 5, Event: "publisher-ready", At: startedAt.Add(time.Millisecond), ProtocolVersion: 1, VideoListenPort: 48001, AudioListenPort: 48002, PipelineStartAccepted: true},
		{Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 5, Event: "video-input-idr", At: startedAt.Add(2 * time.Millisecond), RunningTimeNS: ptrUint64(1)},
		{Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 5, Event: "video-decoded", At: startedAt.Add(3 * time.Millisecond), RunningTimeNS: ptrUint64(2), VideoDecoded: true},
		{Schema: 2, SessionID: "0123456789abcdef", PublisherGeneration: 5, Event: "video-encoded", At: startedAt.Add(4 * time.Millisecond), RunningTimeNS: ptrUint64(3)},
	}
	if !sourceClockPublisherReadinessComplete(base, "0123456789abcdef", 5, startedAt) {
		t.Fatal("complete fresh real-video chain was rejected")
	}
	for name, mutate := range map[string]func([]airplaycontract.Event) []airplaycontract.Event{
		"ready only": func(events []airplaycontract.Event) []airplaycontract.Event { return events[:1] },
		"black encoded only": func(events []airplaycontract.Event) []airplaycontract.Event {
			return []airplaycontract.Event{events[0], events[3]}
		},
		"old generation": func(events []airplaycontract.Event) []airplaycontract.Event {
			events[1].PublisherGeneration = 4
			return events
		},
		"stale idr": func(events []airplaycontract.Event) []airplaycontract.Event {
			events[1].At = startedAt.Add(-time.Second)
			return events
		},
		"audio only": func(events []airplaycontract.Event) []airplaycontract.Event {
			events[1] = airplaycontract.Event{Schema: 2, SessionID: events[1].SessionID, PublisherGeneration: 5, Event: "audio-frame", At: startedAt.Add(2 * time.Millisecond), RunningTimeNS: ptrUint64(1)}
			return events
		},
	} {
		t.Run(name, func(t *testing.T) {
			events := append([]airplaycontract.Event(nil), base...)
			events = mutate(events)
			if sourceClockPublisherReadinessComplete(events, "0123456789abcdef", 5, startedAt) {
				t.Fatal("incomplete readiness chain was accepted")
			}
		})
	}
}

func ptrUint64(value uint64) *uint64 { return &value }

func TestReconfigureSourceClockDeliverySameValueIsNoOp(t *testing.T) {
	manager := plannedDeliveryTestManager()
	request := plannedDeliveryRequest()
	request.PublishURL = manager.deliveryActivePublishURL
	request.Output = manager.deliveryActiveOutput
	request.allowEvidenceInsufficientForTest = false
	if err := manager.ReconfigureSourceClockDelivery(t.Context(), request); err != nil {
		t.Fatalf("same-value request: %v", err)
	}
	select {
	case command := <-manager.deliveryReconfigure:
		t.Fatalf("same-value request was queued: %+v", command.request)
	default:
	}
}

func TestReconfigureSourceClockDeliveryQueuesSpecifiedGenerationOnce(t *testing.T) {
	manager := plannedDeliveryTestManager()
	request := plannedDeliveryRequest()
	result := make(chan error, 1)
	go func() { result <- manager.ReconfigureSourceClockDelivery(context.Background(), request) }()
	command := <-manager.deliveryReconfigure
	if command.request.RequestID != request.RequestID || command.request.CandidateGeneration != 5 || command.request.ExpectedActiveGeneration != 4 {
		t.Fatalf("queued request changed identity/generation: %+v", command.request)
	}
	if err := manager.ReconfigureSourceClockDelivery(t.Context(), request); !errors.Is(err, ErrDeliveryReconfigurePending) {
		t.Fatalf("second request error=%v, want pending", err)
	}
	manager.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureNotImplemented)
	if err := <-result; !errors.Is(err, ErrDeliveryReconfigureNotImplemented) {
		t.Fatalf("first request result=%v", err)
	}
	select {
	case extra := <-manager.deliveryReconfigure:
		t.Fatalf("monitor allocated a second candidate: %+v", extra.request)
	default:
	}
}

func TestReconfigureSourceClockDeliveryRequestCancelDoesNotStopReceiver(t *testing.T) {
	manager := plannedDeliveryTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- manager.ReconfigureSourceClockDelivery(ctx, plannedDeliveryRequest()) }()
	command := <-manager.deliveryReconfigure
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation result=%v", err)
	}
	status := manager.Status()
	if !status.Running || !status.ReceiverRunning || !status.BridgeRunning {
		t.Fatalf("request cancellation stopped session ownership: %+v", status)
	}
	manager.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureNotImplemented)
}

func TestReconfigureSourceClockDeliveryPreCanceledRequestIsNotQueued(t *testing.T) {
	manager := plannedDeliveryTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.ReconfigureSourceClockDelivery(ctx, plannedDeliveryRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled request=%v", err)
	}
	select {
	case command := <-manager.deliveryReconfigure:
		t.Fatalf("pre-canceled request was queued: %+v", command.request)
	default:
	}
}

func TestPlannedDeliveryAndRetryAreMutuallyExclusive(t *testing.T) {
	manager := plannedDeliveryTestManager()
	result := make(chan error, 1)
	go func() {
		result <- manager.ReconfigureSourceClockDelivery(context.Background(), plannedDeliveryRequest())
	}()
	command := <-manager.deliveryReconfigure
	if err := manager.RetrySourceClockDelivery(); !errors.Is(err, ErrDeliveryRetryPending) {
		t.Fatalf("manual retry during planned request=%v, want retry pending", err)
	}
	if manager.beginSourceClockDeliveryRetry() {
		t.Fatal("automatic retry entered during planned request")
	}
	if started, deferred := manager.beginSourceClockCrashRetryOrDefer(); started || !deferred {
		t.Fatalf("crash retry decision during planned request started=%t deferred=%t", started, deferred)
	}
	manager.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureNotImplemented)
	<-result
	if !manager.beginSourceClockDeliveryRetry() {
		t.Fatal("automatic retry did not resume after planned request completed")
	}
	manager.finishSourceClockDeliveryRetry()

	manager.deliveryRetryPending = true
	if err := manager.ReconfigureSourceClockDelivery(t.Context(), plannedDeliveryRequest()); !errors.Is(err, ErrDeliveryRetryPending) {
		t.Fatalf("planned request during retry=%v, want retry pending", err)
	}
}

func TestPlannedDeliveryGenerationAllocationTracksSuccessfulCrashRetry(t *testing.T) {
	manager := plannedDeliveryTestManager()
	manager.setSourceClockDeliveryActiveGeneration(5)
	request := plannedDeliveryRequest()
	request.RequestID = "request-6"
	request.ExpectedActiveGeneration = 5
	request.CandidateGeneration = 6
	request.Artifacts.Generation = 6
	result := make(chan error, 1)
	go func() { result <- manager.ReconfigureSourceClockDelivery(context.Background(), request) }()
	command := <-manager.deliveryReconfigure
	if command.request.ExpectedActiveGeneration != 5 || command.request.CandidateGeneration != 6 {
		t.Fatalf("post-retry generation request changed: %+v", command.request)
	}
	manager.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureNotImplemented)
	<-result
}

func TestPlannedDeliveryStaleCompletionDoesNotReplaceNewRequest(t *testing.T) {
	manager := plannedDeliveryTestManager()
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- manager.ReconfigureSourceClockDelivery(context.Background(), plannedDeliveryRequest())
	}()
	first := <-manager.deliveryReconfigure
	manager.mu.Lock()
	manager.deliveryReconfigurePending = true
	manager.deliveryReconfigureRequestID = "newer-request"
	manager.status.DeliveryRequestID = "newer-request"
	manager.status.DeliveryPhase = string(SourceClockDeliveryRequested)
	manager.mu.Unlock()
	manager.completeSourceClockDelivery(first, SourceClockDeliveryActive, nil)
	if err := <-firstResult; !errors.Is(err, ErrDeliveryReconfigureStale) {
		t.Fatalf("stale completion result=%v", err)
	}
	status := manager.Status()
	if status.DeliveryRequestID != "newer-request" || status.DeliveryPhase != string(SourceClockDeliveryRequested) {
		t.Fatalf("stale completion replaced newer state: %+v", status)
	}
}

func TestPlannedDeliveryCompletionRejectsSameRequestIDWithDifferentCommandIdentity(t *testing.T) {
	manager := plannedDeliveryTestManager()
	result := make(chan error, 1)
	go func() {
		result <- manager.ReconfigureSourceClockDelivery(context.Background(), plannedDeliveryRequest())
	}()
	command := <-manager.deliveryReconfigure
	duplicate := command
	duplicate.reply = make(chan error, 1)
	manager.completeSourceClockDelivery(duplicate, SourceClockDeliveryActive, nil)
	if err := <-duplicate.reply; !errors.Is(err, ErrDeliveryReconfigureStale) {
		t.Fatalf("duplicate command result=%v, want stale", err)
	}
	if err := manager.Status().DeliveryPhase; err != string(SourceClockDeliveryRequested) {
		t.Fatalf("duplicate completion changed phase=%q", err)
	}
	manager.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureEvidenceInsufficient)
	if err := <-result; !errors.Is(err, ErrDeliveryReconfigureEvidenceInsufficient) {
		t.Fatalf("original command result=%v", err)
	}
}

func TestPlannedDeliveryFailureKeepsReceiverAndActiveGeneration(t *testing.T) {
	manager := plannedDeliveryTestManager()
	result := make(chan error, 1)
	go func() {
		result <- manager.ReconfigureSourceClockDelivery(context.Background(), plannedDeliveryRequest())
	}()
	command := <-manager.deliveryReconfigure
	manager.completeSourceClockDelivery(command, SourceClockDeliveryFailed, errors.New("candidate failed"))
	if err := <-result; err == nil {
		t.Fatal("planned failure returned nil")
	}
	if manager.deliveryActiveGeneration != 4 {
		t.Fatalf("planned failure changed active generation to %d", manager.deliveryActiveGeneration)
	}
	status := manager.Status()
	if !status.Running || !status.ReceiverRunning || !status.BridgeRunning || status.DeliveryPhase != string(SourceClockDeliveryFailed) {
		t.Fatalf("planned failure changed receiver/publisher ownership: %+v", status)
	}
}

func TestPlannedDeliveryFailureAfterOldPublisherStopDoesNotClaimDeliveryIsMaintained(t *testing.T) {
	manager := plannedDeliveryTestManager()
	result := make(chan error, 1)
	go func() {
		result <- manager.ReconfigureSourceClockDelivery(context.Background(), plannedDeliveryRequest())
	}()
	command := <-manager.deliveryReconfigure
	manager.status.BridgeRunning = false
	manager.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureEvidenceInsufficient)
	if err := <-result; !errors.Is(err, ErrDeliveryReconfigureEvidenceInsufficient) {
		t.Fatalf("planned failure result=%v", err)
	}
	if message := manager.Status().Message; !strings.Contains(message, "配信は停止しています") {
		t.Fatalf("failure message=%q, want stopped-delivery wording", message)
	}
}

func TestPlannedDeliveryUserStopCompletesWaiter(t *testing.T) {
	manager := plannedDeliveryTestManager()
	result := make(chan error, 1)
	go func() {
		result <- manager.ReconfigureSourceClockDelivery(context.Background(), plannedDeliveryRequest())
	}()
	<-manager.deliveryReconfigure
	done := make(chan struct{})
	manager.finishMonitorWithCallback(done, t.TempDir(), true, "", nil, nil, nil, nil, nil, nil)
	if err := <-result; !errors.Is(err, ErrDeliveryReconfigureUnavailable) {
		t.Fatalf("user stop result=%v, want unavailable", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("user stop did not finish the monitor")
	}
}

func TestPlannedDeliveryPriority(t *testing.T) {
	for _, test := range []struct {
		name                           string
		userStop, timeout, plan, retry bool
		want                           sourceClockDeliveryAction
	}{
		{"user stop", true, true, true, true, sourceClockDeliveryActionStop},
		{"deadline", false, true, true, true, sourceClockDeliveryActionTimeout},
		{"planned", false, false, true, true, sourceClockDeliveryActionPlanned},
		{"retry", false, false, false, true, sourceClockDeliveryActionRetry},
		{"idle", false, false, false, false, sourceClockDeliveryActionNone},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := chooseSourceClockDeliveryAction(test.userStop, test.timeout, test.plan, test.retry); got != test.want {
				t.Fatalf("action=%v, want %v", got, test.want)
			}
		})
	}
}

func TestReconfigureSourceClockDeliveryContextTimeoutLeavesQueuedRequestOwnedBySession(t *testing.T) {
	manager := plannedDeliveryTestManager()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := manager.ReconfigureSourceClockDelivery(ctx, plannedDeliveryRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait timeout=%v", err)
	}
	if !manager.deliveryReconfigurePending {
		t.Fatal("request wait timeout incorrectly released session-owned request")
	}
}

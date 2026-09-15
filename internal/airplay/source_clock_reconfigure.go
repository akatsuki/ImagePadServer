package airplay

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"imagepadserver/internal/airplaycontract"
)

var (
	ErrDeliveryReconfigureUnavailable          = errors.New("airplay planned delivery reconfigure is unavailable")
	ErrDeliveryReconfigurePending              = errors.New("airplay planned delivery reconfigure is already pending")
	ErrDeliveryReconfigureStale                = errors.New("airplay planned delivery reconfigure is stale")
	ErrDeliveryReconfigureNotImplemented       = errors.New("airplay planned delivery executor is not implemented")
	ErrDeliveryReconfigureEvidenceInsufficient = errors.New("airplay planned delivery readiness evidence is insufficient for active promotion")
)

type SourceClockDeliveryPhase string

const (
	SourceClockDeliveryRequested  SourceClockDeliveryPhase = "requested"
	SourceClockDeliverySwitching  SourceClockDeliveryPhase = "switching"
	SourceClockDeliveryValidating SourceClockDeliveryPhase = "validating"
	SourceClockDeliveryActive     SourceClockDeliveryPhase = "active"
	SourceClockDeliveryFailed     SourceClockDeliveryPhase = "failed"
)

type SourceClockDeliveryRequest struct {
	RequestID                string
	ExpectedActiveGeneration uint64
	CandidateGeneration      uint64
	PublishURL               string
	Output                   DirectOutputConfig
	Artifacts                airplaycontract.PublisherArtifacts
	// CandidateDelivery is issued by the downstream session owner, never by
	// an HTTP body. Its immutable binding must match every publisher input.
	CandidateDelivery airplaycontract.CandidateDelivery `json:"-"`
	recoverPublisher  bool
	// allowEvidenceInsufficientForTest is deliberately unexported. Fixtures may
	// exercise the native exchange without a downstream owner, but cannot commit
	// that candidate. Normal admission requires the owner binding above.
	allowEvidenceInsufficientForTest bool
}

// RecoverSourceClockDelivery accepts only a freshly allocated downstream-owned
// candidate. It does not stop/restart the receiver and cannot allocate a retry
// generation independently of that owner.
func (m *Manager) RecoverSourceClockDelivery(waitContext context.Context, candidate airplaycontract.CandidateDelivery) error {
	return m.applyOwnedSourceClockDelivery(waitContext, candidate, true)
}

func (m *Manager) ApplySourceClockDelivery(waitContext context.Context, candidate airplaycontract.CandidateDelivery) error {
	return m.applyOwnedSourceClockDelivery(waitContext, candidate, false)
}

func (m *Manager) applyOwnedSourceClockDelivery(waitContext context.Context, candidate airplaycontract.CandidateDelivery, recovery bool) error {
	if candidate == nil {
		return ErrDeliveryReconfigureEvidenceInsufficient
	}
	binding := candidate.Binding()
	return m.ReconfigureSourceClockDelivery(waitContext, SourceClockDeliveryRequest{
		RequestID:                binding.Descriptor.RequestID,
		ExpectedActiveGeneration: binding.ExpectedActiveGeneration,
		CandidateGeneration:      binding.Descriptor.Generation,
		PublishURL:               binding.PublishURL,
		Output:                   DirectOutputConfig(binding.Descriptor.Output),
		Artifacts:                binding.Descriptor.ArtifactPaths,
		CandidateDelivery:        candidate,
		recoverPublisher:         recovery,
	})
}

type sourceClockDeliveryCommand struct {
	request  SourceClockDeliveryRequest
	reply    chan error
	identity *struct{}
}

type sourceClockDeliveryAction uint8

const (
	sourceClockDeliveryActionNone sourceClockDeliveryAction = iota
	sourceClockDeliveryActionStop
	sourceClockDeliveryActionTimeout
	sourceClockDeliveryActionPlanned
	sourceClockDeliveryActionRetry
)

func chooseSourceClockDeliveryAction(userStop, timeout, planned, retry bool) sourceClockDeliveryAction {
	switch {
	case userStop:
		return sourceClockDeliveryActionStop
	case timeout:
		return sourceClockDeliveryActionTimeout
	case planned:
		return sourceClockDeliveryActionPlanned
	case retry:
		return sourceClockDeliveryActionRetry
	default:
		return sourceClockDeliveryActionNone
	}
}

func validateSourceClockDeliveryRequest(request SourceClockDeliveryRequest) error {
	if strings.TrimSpace(request.RequestID) == "" {
		return fmt.Errorf("planned AirPlay delivery request ID is empty")
	}
	if request.ExpectedActiveGeneration == 0 || request.CandidateGeneration <= request.ExpectedActiveGeneration {
		return fmt.Errorf("planned AirPlay delivery generation is invalid")
	}
	if strings.TrimSpace(request.PublishURL) == "" {
		return fmt.Errorf("planned AirPlay delivery publish URL is empty")
	}
	if request.Output.Width < 2 || request.Output.Height < 2 || request.Output.SourceFPS < 1 || request.Output.OutputFPS < 1 || request.Output.GOPFrames < 1 {
		return fmt.Errorf("planned AirPlay delivery output is invalid")
	}
	if strings.TrimSpace(request.Artifacts.SessionID) == "" || request.Artifacts.Generation != request.CandidateGeneration {
		return fmt.Errorf("planned AirPlay delivery artifacts do not match candidate generation")
	}
	for name, value := range map[string]string{
		"recording":    request.Artifacts.Recording,
		"ready":        request.Artifacts.Ready,
		"media-ready":  request.Artifacts.MediaReady,
		"event-log":    request.Artifacts.EventLog,
		"process-log":  request.Artifacts.ProcessLog,
		"stop-request": request.Artifacts.StopRequest,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("planned AirPlay delivery artifact %s is empty", name)
		}
	}
	if request.CandidateDelivery != nil {
		binding := request.CandidateDelivery.Binding()
		descriptor := binding.Descriptor
		if binding.ExpectedActiveGeneration != request.ExpectedActiveGeneration || binding.PublishURL != request.PublishURL ||
			descriptor.SessionEpoch == 0 || descriptor.SessionID != request.Artifacts.SessionID ||
			descriptor.RequestID != request.RequestID || descriptor.Generation != request.CandidateGeneration ||
			descriptor.ArtifactPaths != request.Artifacts || descriptor.Output != airplaycontract.DeliveryOutput(request.Output) ||
			descriptor.ExpectedRecordingWidth != request.Output.Width || descriptor.ExpectedRecordingHeight != request.Output.Height {
			return ErrDeliveryReconfigureStale
		}
	}
	return nil
}

func validatePublisherArtifactsForPlannedExchange(candidate, current airplaycontract.PublisherArtifacts) error {
	if err := validateSourceClockDeliveryRequest(SourceClockDeliveryRequest{
		RequestID:                "artifact-validation",
		ExpectedActiveGeneration: current.Generation,
		CandidateGeneration:      candidate.Generation,
		PublishURL:               "rtsp://artifact-validation",
		Output:                   DirectOutputConfig{Width: 2, Height: 2, SourceFPS: 1, OutputFPS: 1, GOPFrames: 1},
		Artifacts:                candidate,
	}); err != nil {
		return err
	}
	if candidate.ProcessLog != directPublisherLogPath(candidate.StopRequest) {
		return fmt.Errorf("source-clock publisher process log does not match stop request: want %q", directPublisherLogPath(candidate.StopRequest))
	}
	values := []string{candidate.Recording, candidate.Ready, candidate.MediaReady, candidate.EventLog, candidate.ProcessLog, candidate.StopRequest}
	currentValues := map[string]struct{}{
		current.Recording: {}, current.Ready: {}, current.MediaReady: {},
		current.EventLog: {}, current.ProcessLog: {}, current.StopRequest: {},
	}
	for _, value := range values {
		if _, exists := currentValues[value]; exists {
			return fmt.Errorf("planned AirPlay delivery artifacts overlap current generation: %s", value)
		}
	}
	return nil
}

// buildPlannedSourceClockPublisherArgs reconstructs the candidate command
// from the request and only the immutable session identity carried by the
// current publisher. It intentionally does not use nextSourceClockPublisherArgs:
// a planned change is allowed to change the URL and output profile, while the
// receiver-owned token and ingress ports remain fixed.
func buildPlannedSourceClockPublisherArgs(current []string, request SourceClockDeliveryRequest) ([]string, error) {
	if err := validateSourceClockDeliveryRequest(request); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Artifacts.ProcessLog) == "" || request.Artifacts.ProcessLog != directPublisherLogPath(request.Artifacts.StopRequest) {
		return nil, fmt.Errorf("source-clock publisher process log does not match stop request: want %q", directPublisherLogPath(request.Artifacts.StopRequest))
	}
	videoPort, err := sourceClockPublisherArgInt(current, "--video-listen-port")
	if err != nil {
		return nil, err
	}
	audioPort, err := sourceClockPublisherArgInt(current, "--audio-listen-port")
	if err != nil {
		return nil, err
	}
	token, err := sourceClockPublisherArg(current, "--session-token")
	if err != nil {
		return nil, err
	}
	candidate := buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
		VideoListenPort: videoPort,
		AudioListenPort: audioPort,
		PublishURL:      request.PublishURL,
		SessionToken:    token,
		Artifacts:       request.Artifacts,
		Output:          request.Output,
	})
	// Preserve only the test runner prefix used by the in-process executor
	// fixtures. The real bridge has no such arguments; keeping arbitrary old
	// flags here would violate the request-owned candidate configuration.
	prefix := make([]string, 0, 2)
	for index, arg := range current {
		if strings.HasPrefix(arg, "-test.run=") {
			prefix = append(prefix, arg)
			if index+1 < len(current) && current[index+1] == "fixture" {
				prefix = append(prefix, current[index+1])
			}
			break
		}
	}
	return append(prefix, candidate...), nil
}

func sourceClockPublisherArg(args []string, flag string) (string, error) {
	value := ""
	count := 0
	for index, arg := range args {
		if arg != flag {
			continue
		}
		count++
		if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
			return "", fmt.Errorf("source-clock publisher argument %s has no value", flag)
		}
		value = args[index+1]
	}
	if count != 1 {
		return "", fmt.Errorf("source-clock publisher argument %s occurs %d times", flag, count)
	}
	return value, nil
}

func sourceClockPublisherArgInt(args []string, flag string) (int, error) {
	value, err := sourceClockPublisherArg(args, flag)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 || parsed > 65534 {
		return 0, fmt.Errorf("source-clock publisher argument %s is invalid", flag)
	}
	return parsed, nil
}

// ReconfigureSourceClockDelivery submits a planned delivery request to the
// session monitor. waitContext controls only how long the caller waits; it is
// never used as the receiver or publisher ownership context.
func (m *Manager) ReconfigureSourceClockDelivery(waitContext context.Context, request SourceClockDeliveryRequest) error {
	if waitContext == nil {
		waitContext = context.Background()
	}
	if err := waitContext.Err(); err != nil {
		return err
	}
	if err := validateSourceClockDeliveryRequest(request); err != nil {
		return err
	}
	m.mu.Lock()
	if !m.running || !m.status.ReceiverRunning || m.deliveryReconfigure == nil ||
		(!request.recoverPublisher && (!m.status.BridgeRunning || !m.status.MediaReady)) ||
		(request.recoverPublisher && (m.status.BridgeRunning || request.CandidateDelivery == nil)) {
		m.mu.Unlock()
		return ErrDeliveryReconfigureUnavailable
	}
	if request.ExpectedActiveGeneration != m.deliveryActiveGeneration || request.Artifacts.SessionID != m.deliverySessionID {
		m.mu.Unlock()
		return ErrDeliveryReconfigureStale
	}
	if !request.recoverPublisher && request.PublishURL == m.deliveryActivePublishURL && request.Output == m.deliveryActiveOutput {
		m.mu.Unlock()
		return nil
	}
	if request.CandidateDelivery == nil && !request.allowEvidenceInsufficientForTest {
		m.mu.Unlock()
		return ErrDeliveryReconfigureEvidenceInsufficient
	}
	if m.deliveryRetryPending {
		m.mu.Unlock()
		return ErrDeliveryRetryPending
	}
	if m.deliveryReconfigurePending {
		m.mu.Unlock()
		return ErrDeliveryReconfigurePending
	}
	command := sourceClockDeliveryCommand{request: request, reply: make(chan error, 1), identity: &struct{}{}}
	m.deliveryReconfigurePending = true
	m.deliveryReconfigureRequestID = request.RequestID
	m.deliveryReconfigureReply = command.reply
	m.status.DeliveryPhase = string(SourceClockDeliveryRequested)
	m.status.DeliveryRequestID = request.RequestID
	select {
	case m.deliveryReconfigure <- command:
		m.mu.Unlock()
		m.notify()
	case <-waitContext.Done():
		m.deliveryReconfigurePending = false
		m.deliveryReconfigureRequestID = ""
		m.deliveryReconfigureReply = nil
		m.status.DeliveryPhase = string(SourceClockDeliveryFailed)
		m.status.DeliveryRequestID = request.RequestID
		m.mu.Unlock()
		m.notify()
		return waitContext.Err()
	default:
		m.deliveryReconfigurePending = false
		m.deliveryReconfigureRequestID = ""
		m.deliveryReconfigureReply = nil
		m.mu.Unlock()
		return ErrDeliveryReconfigurePending
	}
	select {
	case err := <-command.reply:
		return err
	case <-waitContext.Done():
		return waitContext.Err()
	}
}

func (m *Manager) completeSourceClockDelivery(command sourceClockDeliveryCommand, phase SourceClockDeliveryPhase, result error) {
	m.mu.Lock()
	if !m.deliveryReconfigurePending || m.deliveryReconfigureRequestID != command.request.RequestID || command.identity == nil || m.deliveryReconfigureReply != command.reply {
		m.mu.Unlock()
		replySourceClockDelivery(command.reply, ErrDeliveryReconfigureStale)
		return
	}
	m.deliveryReconfigurePending = false
	m.deliveryReconfigureRequestID = ""
	m.deliveryReconfigureReply = nil
	m.status.DeliveryPhase = string(phase)
	m.status.DeliveryRequestID = command.request.RequestID
	if result != nil {
		if m.status.BridgeRunning {
			m.status.Message = "AirPlay配信設定の切替に失敗しました。現在の受信と配信を維持しています。"
		} else {
			m.status.Message = "AirPlay配信設定の切替に失敗しました。受信は維持されていますが、配信は停止しています。"
		}
	}
	m.mu.Unlock()
	m.notify()
	replySourceClockDelivery(command.reply, result)
}

func replySourceClockDelivery(reply chan error, result error) {
	if reply == nil {
		return
	}
	select {
	case reply <- result:
	default:
	}
}

func (m *Manager) beginSourceClockCrashRetryOrDefer() (started, deferred bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running || m.deliveryRetryPending {
		return false, false
	}
	if m.deliveryReconfigurePending {
		return false, true
	}
	m.deliveryRetryPending = true
	return true, false
}

func (m *Manager) setSourceClockDeliveryActiveGeneration(generation uint64) {
	if generation == 0 {
		return
	}
	m.mu.Lock()
	if m.running {
		m.deliveryActiveGeneration = generation
	}
	m.mu.Unlock()
}

func (m *Manager) activateSourceClockDelivery(request SourceClockDeliveryRequest) {
	m.mu.Lock()
	if m.running && m.deliverySessionID == request.Artifacts.SessionID {
		m.deliveryActiveGeneration = request.CandidateGeneration
		m.deliveryActivePublishURL = request.PublishURL
		m.deliveryActiveOutput = request.Output
	}
	m.mu.Unlock()
}

func (m *Manager) setSourceClockDeliveryPhase(phase SourceClockDeliveryPhase) {
	m.mu.Lock()
	if m.running {
		m.status.DeliveryPhase = string(phase)
	}
	m.mu.Unlock()
	m.notify()
}

package airplay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/airplaycontract"
)

type sourceClockCapabilities struct {
	Schema          int      `json:"schema"`
	ProtocolVersion int      `json:"protocolVersion"`
	Binary          string   `json:"binary"`
	BinarySHA256    string   `json:"binarySha256"`
	Features        []string `json:"features"`
}

const sourceClockReceiverDiagnosticLogEnv = "IMAGEPAD_AIRPLAY_RECEIVER_LOG"
const sourceClockNoSignalTimeout = 3 * time.Minute
const sourceClockPublisherStopTimeout = 10 * time.Second
const sourceClockReadyTimeout = 10 * time.Second

func configuredSourceClockNoSignalTimeout() time.Duration {
	if strings.TrimSpace(os.Getenv("IMAGEPAD_TEST_ISOLATED_LIFECYCLE")) != "1" {
		return sourceClockNoSignalTimeout
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(os.Getenv("IMAGEPAD_TEST_AIRPLAY_NO_SIGNAL_SECONDS")))
	if err != nil || seconds < 1 || seconds > 30 {
		return sourceClockNoSignalTimeout
	}
	return time.Duration(seconds) * time.Second
}

type sourceClockToken string
type sourceClockReceiverID string

type sourceClockDeliveryRecoveryLatch struct {
	required                 bool
	usedCandidateGenerations map[uint64]struct{}
}

func (l *sourceClockDeliveryRecoveryLatch) beginPlanned(candidateGeneration uint64) bool {
	if l == nil || l.required {
		return false
	}
	return l.reserveOwnedCandidate(candidateGeneration)
}

// A new coordinator-issued generation may follow a committed planned change.
// This never re-enables legacy crash retry or reuses a consumed candidate.
func (l *sourceClockDeliveryRecoveryLatch) reserveOwnedCandidate(candidateGeneration uint64) bool {
	if l == nil || candidateGeneration == 0 {
		return false
	}
	if l.usedCandidateGenerations == nil {
		l.usedCandidateGenerations = make(map[uint64]struct{})
	}
	if _, used := l.usedCandidateGenerations[candidateGeneration]; used {
		return false
	}
	l.usedCandidateGenerations[candidateGeneration] = struct{}{}
	l.required = true
	return true
}

func (l *sourceClockDeliveryRecoveryLatch) retryAllowed() bool {
	return l != nil && !l.required
}

func (l *sourceClockDeliveryRecoveryLatch) candidateReserved(candidateGeneration uint64) bool {
	if l == nil {
		return false
	}
	_, reserved := l.usedCandidateGenerations[candidateGeneration]
	return reserved
}

func transferSourceClockPublisherDoneToRetiring(activeDone, retiringDone *<-chan error, done <-chan error) {
	if activeDone != nil {
		*activeDone = nil
	}
	if retiringDone != nil {
		*retiringDone = done
	}
}

type sourceClockReceiverCommandFactory func(context.Context, string, ...string) *exec.Cmd

func startSourceClockReceiverChild(ctx context.Context, receiverPath, videoEndpoint, audioEndpoint string, token sourceClockToken, receiverID sourceClockReceiverID, receiverName, tempDir, diagnosticLogPath string, commandFactory sourceClockReceiverCommandFactory) (*exec.Cmd, *sourceClockReceiverOutput, *sourceClockMetricsGate, time.Time, error) {
	if commandFactory == nil {
		return nil, nil, nil, time.Time{}, errors.New("source-clock receiver command factory is nil")
	}
	receiverLog := &limitedBuffer{max: 8192}
	receiver := commandFactory(ctx, receiverPath, BuildSourceClockReceiverArgs(videoEndpoint, audioEndpoint, token, receiverID, receiverName)...)
	restoreReceiverConfig, err := configureReceiverProcess(receiver, receiverLog, receiverPath, tempDir)
	if err != nil {
		return nil, nil, nil, time.Time{}, fmt.Errorf("configure source-clock receiver: %w", err)
	}
	metricsGate, err := newSourceClockMetricsGate(string(receiverID))
	if err != nil {
		if restoreReceiverConfig != nil {
			_ = restoreReceiverConfig()
		}
		return nil, nil, nil, time.Time{}, fmt.Errorf("create source-clock receiver metrics gate: %w", err)
	}
	receiverOutput, err := newSourceClockReceiverOutput(receiverLog, diagnosticLogPath, metricsGate, nil)
	if err != nil {
		if restoreReceiverConfig != nil {
			_ = restoreReceiverConfig()
		}
		return nil, nil, nil, time.Time{}, err
	}
	receiver.Stdout = receiverOutput
	receiver.Stderr = receiverOutput
	if err := receiver.Start(); err != nil {
		_ = receiverOutput.Close()
		if restoreReceiverConfig != nil {
			_ = restoreReceiverConfig()
		}
		return nil, nil, nil, time.Time{}, fmt.Errorf("start source-clock receiver: %w", err)
	}
	startedAt := time.Now()
	if err := restoreReceiverConfigurationAfterStartup(receiverLog, restoreReceiverConfig); err != nil {
		_ = receiver.Process.Kill()
		_ = receiver.Wait()
		_ = receiverOutput.Close()
		return nil, nil, nil, time.Time{}, fmt.Errorf("restore source-clock receiver configuration: %w", err)
	}
	return receiver, receiverOutput, metricsGate, startedAt, nil
}

func validateSourceClockReceiverCapabilities(receiverPath string) error {
	if strings.TrimSpace(receiverPath) == "" {
		return errors.New("source-clock receiver path is empty")
	}
	manifestPath := filepath.Join(filepath.Dir(receiverPath), "imagepad-source-clock-capabilities.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("source-clock receiver capability manifest is missing: %w", err)
	}
	var capabilities sourceClockCapabilities
	if err := json.Unmarshal(data, &capabilities); err != nil {
		return fmt.Errorf("parse source-clock receiver capability manifest: %w", err)
	}
	if capabilities.Schema != 1 || capabilities.ProtocolVersion != 1 ||
		capabilities.Binary != filepath.Base(receiverPath) || len(capabilities.BinarySHA256) != sha256.Size*2 {
		return errors.New("source-clock receiver capability manifest is incompatible")
	}
	wantFeatures := []string{"video-au", "audio-frame", "remote-ntp", "bounded-writer", "idle-wait", "audio-format-lock", "egress-metrics-v2"}
	featureSet := make(map[string]bool, len(capabilities.Features))
	for _, feature := range capabilities.Features {
		featureSet[feature] = true
	}
	for _, feature := range wantFeatures {
		if !featureSet[feature] {
			return fmt.Errorf("source-clock receiver capability %q is missing", feature)
		}
	}
	file, err := os.Open(receiverPath)
	if err != nil {
		return fmt.Errorf("open source-clock receiver for capability validation: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("hash source-clock receiver: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close source-clock receiver: %w", closeErr)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), capabilities.BinarySHA256) {
		return errors.New("source-clock receiver binary hash does not match capability manifest")
	}
	return nil
}

func BuildSourceClockReceiverArgs(videoEndpoint, audioEndpoint string, token sourceClockToken, receiverID sourceClockReceiverID, title string) []string {
	// The source-clock callbacks run before UxPlay's use_video/use_audio
	// renderer branches, so -vs/-as 0 disables only the legacy local sinks.
	// Keeping those sinks out of the headless child avoids starting a second
	// GStreamer decode/render pipeline, which previously corrupted its heap.
	return []string{"-n", normalizeReceiverTitle(title), "-vs", "0", "-as", "0", "-fps", "60", "-d", "1", "-ipscv", videoEndpoint, "-ipsca", audioEndpoint, "-ipsct", string(token), "-ipscid", string(receiverID)}
}

func newSourceClockToken() (sourceClockToken, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate AirPlay source-clock token: %w", err)
	}
	return sourceClockToken(hex.EncodeToString(token[:])), nil
}

func newSourceClockReceiverIDFrom(reader io.Reader) (sourceClockReceiverID, error) {
	if reader == nil {
		return "", errors.New("source-clock receiver ID entropy reader is nil")
	}
	var entropy [16]byte
	if _, err := io.ReadFull(reader, entropy[:]); err != nil {
		return "", fmt.Errorf("generate AirPlay source-clock receiver ID: %w", err)
	}
	return sourceClockReceiverID("receiver-" + hex.EncodeToString(entropy[:])), nil
}

func newSourceClockReceiverID() (sourceClockReceiverID, error) {
	return newSourceClockReceiverIDFrom(rand.Reader)
}

func withSourceClockReceiverID(generate func() (sourceClockReceiverID, error), startChildren func(sourceClockReceiverID) error) error {
	if generate == nil {
		return errors.New("source-clock receiver ID generator is nil")
	}
	if startChildren == nil {
		return errors.New("source-clock child starter is nil")
	}
	receiverID, err := generate()
	if err != nil {
		return err
	}
	if !validSourceClockReceiverID(string(receiverID)) {
		return errors.New("source-clock receiver ID generator returned an invalid ID")
	}
	return startChildren(receiverID)
}

type sourceClockPublisherExitedBeforeReadyError struct {
	waitErr error
}

func (e *sourceClockPublisherExitedBeforeReadyError) Error() string {
	if e.waitErr == nil {
		return "source-clock publisher exited before readiness"
	}
	return fmt.Sprintf("source-clock publisher exited before readiness: %v", e.waitErr)
}

func (e *sourceClockPublisherExitedBeforeReadyError) Unwrap() error {
	return e.waitErr
}

func waitSourceClockReady(ctx context.Context, path, sessionID string, publisherGeneration uint64) (airplaycontract.Event, error) {
	return waitSourceClockReadyWithPublisher(ctx, path, sessionID, publisherGeneration, nil, sourceClockReadyTimeout)
}

func waitSourceClockReadyWithPublisher(ctx context.Context, path, sessionID string, publisherGeneration uint64, publisherDone <-chan error, timeout time.Duration) (airplaycontract.Event, error) {
	var ready airplaycontract.Event
	if strings.TrimSpace(path) == "" {
		return ready, errors.New("source-clock ready file path is empty")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &ready); err != nil {
				return ready, fmt.Errorf("parse source-clock ready file: %w", err)
			}
			if !ready.ReadyFor(sessionID, publisherGeneration) {
				return ready, errors.New("source-clock ready file is incomplete")
			}
			select {
			case waitErr := <-publisherDone:
				return ready, &sourceClockPublisherExitedBeforeReadyError{waitErr: waitErr}
			default:
			}
			if err := ctx.Err(); err != nil {
				return ready, err
			}
			return ready, nil
		}
		select {
		case <-ctx.Done():
			return ready, ctx.Err()
		case waitErr := <-publisherDone:
			return ready, &sourceClockPublisherExitedBeforeReadyError{waitErr: waitErr}
		case <-deadline.C:
			return ready, errors.New("source-clock ready timeout")
		case <-ticker.C:
		}
	}
}

func sanitizeSourceClockPublisherLog(output string, args []string) string {
	for index := 0; index+1 < len(args); index++ {
		switch args[index] {
		case "--session-token", "--publish-url", "--recording", "--ready-file", "--media-ready-file", "--event-log", "--stop-file":
			if value := args[index+1]; value != "" {
				output = strings.ReplaceAll(output, value, "[REDACTED]")
			}
		}
	}
	return strings.Join(strings.Fields(output), " ")
}

func sourceClockPublisherStartupError(waitErr error, output string, args []string) error {
	exit := sourceClockProcessExitFromError(waitErr)
	exitCode := "unknown"
	if exit.Known {
		exitCode = strconv.Itoa(exit.Code)
	}
	detail := sanitizeSourceClockPublisherLog(output, args)
	if detail == "" {
		detail = "(no publisher output)"
	}
	if waitErr == nil {
		return fmt.Errorf("source-clock publisher exited before readiness: exit_code=%s; publisher_log=%q", exitCode, detail)
	}
	return fmt.Errorf("source-clock publisher exited before readiness: exit_code=%s: %w; publisher_log=%q", exitCode, waitErr, detail)
}

func (m *Manager) startSourceClock(parent context.Context, sessionID, publishURL, recording string, output DirectOutputConfig, onStopped func()) error {
	return m.startSourceClockConfigured(parent, sessionID, publishURL, recording, "", nil, output, onStopped)
}

// StartSourceClockDirect starts the source-clock path with generation-scoped
// artifacts owned by the OBS session. It intentionally remains separate from
// StartDirect so the legacy direct pipeline keeps its existing contract.
func (m *Manager) StartSourceClockDirect(parent context.Context, sessionID, publishURL, artifactRoot string, observer airplaycontract.PublisherObserver, output DirectOutputConfig, onStopped func()) (err error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if observer != nil {
		defer func() {
			if err != nil {
				observer.SealPublishers()
			}
		}()
	}
	if !FeatureEnabled() {
		return ErrDisabled
	}
	if parent == nil {
		parent = context.Background()
	}
	if !SourceClockPipelineEnabled() {
		return errors.New("AirPlay source-clock pipeline is not selected")
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("AirPlay source-clock session ID is empty")
	}
	if strings.TrimSpace(artifactRoot) == "" || observer == nil {
		return errors.New("AirPlay source-clock publisher artifact owner is incomplete")
	}
	return m.startSourceClockConfigured(parent, sessionID, publishURL, "", artifactRoot, observer, output, onStopped)
}

func (m *Manager) startSourceClockConfigured(parent context.Context, sessionID, publishURL, recording, artifactRoot string, observer airplaycontract.PublisherObserver, output DirectOutputConfig, onStopped func()) error {
	receiverName, err := resolveReceiverTitle(defaultReceiverTitle)
	if err != nil {
		return err
	}
	if err := validatePublishURL(publishURL, "rtsp"); err != nil {
		return err
	}
	var initialArtifacts airplaycontract.PublisherArtifacts
	var paths airplaycontract.FixedPaths
	if observer != nil {
		initialArtifacts = airplaycontract.NewPublisherArtifacts(artifactRoot, sessionID, 1)
		recording = initialArtifacts.Recording
		paths = airplaycontract.FixedPaths{Ready: initialArtifacts.Ready, MediaReady: initialArtifacts.MediaReady, EventLog: initialArtifacts.EventLog}
	} else {
		if strings.TrimSpace(recording) == "" {
			return errors.New("AirPlay direct recording path is empty")
		}
		var err error
		paths, err = airplaycontract.FixedPathsForRecording(recording)
		if err != nil {
			return err
		}
	}
	receiverPath, err := ResolveReceiverPath()
	if err != nil {
		return err
	}
	if err := validateSourceClockReceiverCapabilities(receiverPath); err != nil {
		return err
	}
	bridgePath, err := ResolveGStreamerBridgePath()
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	m.mu.Unlock()
	tempDir, err := os.MkdirTemp("", "imagepad-airplay-source-clock-")
	if err != nil {
		return fmt.Errorf("create source-clock session directory: %w", err)
	}
	stopFile := filepath.Join(tempDir, "stop.request")
	if observer != nil {
		stopFile = initialArtifacts.StopRequest
	}
	token, err := newSourceClockToken()
	if err != nil {
		os.RemoveAll(tempDir)
		return err
	}
	err = withSourceClockReceiverID(newSourceClockReceiverID, func(receiverID sourceClockReceiverID) error {
		return m.startSourceClockChildren(parent, sessionID, publishURL, recording, artifactRoot, observer, initialArtifacts, output, onStopped, paths, receiverPath, receiverName, bridgePath, tempDir, stopFile, token, receiverID)
	})
	if err != nil {
		os.RemoveAll(tempDir)
	}
	return err
}

func (m *Manager) startSourceClockChildren(parent context.Context, sessionID, publishURL, recording, artifactRoot string, observer airplaycontract.PublisherObserver, initialArtifacts airplaycontract.PublisherArtifacts, output DirectOutputConfig, onStopped func(), paths airplaycontract.FixedPaths, receiverPath, receiverName, bridgePath, tempDir, stopFile string, token sourceClockToken, receiverID sourceClockReceiverID) error {
	ctx, cancel := context.WithCancel(parent)
	monitorOwnsPublisherObserver := false
	if observer != nil {
		defer func() {
			if !monitorOwnsPublisherObserver {
				observer.SealPublishers()
			}
		}()
	}
	publisherGeneration := uint64(1)
	if observer != nil {
		if err := observer.PreparePublisher(ctx, initialArtifacts); err != nil {
			cancel()
			os.RemoveAll(tempDir)
			return fmt.Errorf("prepare source-clock publisher generation 1: %w", err)
		}
	}
	pipelineArgs := BuildSourceClockDirectArgs(publishURL, recording, stopFile, string(token), sessionID, publisherGeneration, paths, output)
	if observer != nil {
		pipelineArgs = buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{PublishURL: publishURL, SessionToken: string(token), Artifacts: initialArtifacts, Output: output})
	}
	pipeline, err := startSourceClockGStreamerProcess(ctx, bridgePath, pipelineArgs, stopFile)
	if err != nil {
		_ = completeSourceClockPublisher(observer, initialArtifacts, false, nil)
		cancel()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start source-clock GStreamer publisher: %w", err)
	}
	pipelineDone := make(chan error, 1)
	go func() { pipelineDone <- pipeline.wait() }()
	observeInitialPublisherAfterExit := func(waitErr error) {
		if err := completeSourceClockPublisherProcess(observer, initialArtifacts, true, pipeline.processID(), time.Now(), waitErr); err != nil {
			log.Printf("AirPlay source-clock initial publisher event log was not accepted: generation=%d err=%v", initialArtifacts.Generation, err)
		}
	}
	ready, err := waitSourceClockReadyWithPublisher(ctx, paths.Ready, sessionID, publisherGeneration, pipelineDone, sourceClockReadyTimeout)
	if err != nil {
		cancel()
		var publisherExit *sourceClockPublisherExitedBeforeReadyError
		if errors.As(err, &publisherExit) {
			observeInitialPublisherAfterExit(publisherExit.waitErr)
			os.RemoveAll(tempDir)
			return sourceClockPublisherStartupError(publisherExit.waitErr, pipeline.log.String(), pipelineArgs)
		}
		pipelineErr := pipeline.stopAndWaitDone(pipelineDone, directPublisherStopNormal, 2*time.Second)
		observeInitialPublisherAfterExit(pipelineErr)
		os.RemoveAll(tempDir)
		return fmt.Errorf("wait source-clock publisher readiness: %w", err)
	}
	videoEndpoint := fmt.Sprintf("127.0.0.1:%d", ready.VideoListenPort)
	audioEndpoint := fmt.Sprintf("127.0.0.1:%d", ready.AudioListenPort)
	if observer != nil {
		pipeline.restartArgs = buildSourceClockPublisherArgs(sourceClockPublisherArgsConfig{
			VideoListenPort: ready.VideoListenPort, AudioListenPort: ready.AudioListenPort,
			PublishURL: publishURL, SessionToken: string(token), Artifacts: initialArtifacts, Output: output,
		})
	} else {
		pipeline.restartArgs = buildSourceClockDirectArgs(
			ready.VideoListenPort,
			ready.AudioListenPort,
			publishURL,
			recording,
			stopFile,
			string(token),
			sessionID,
			publisherGeneration,
			paths,
			output,
		)
	}
	receiver, receiverOutput, metricsGate, receiverStartedAt, err := startSourceClockReceiverChild(ctx, receiverPath, videoEndpoint, audioEndpoint, token, receiverID, receiverName, tempDir, os.Getenv(sourceClockReceiverDiagnosticLogEnv), exec.CommandContext)
	if err != nil {
		cancel()
		pipelineErr := pipeline.stopAndWaitDone(pipelineDone, directPublisherStopNormal, 2*time.Second)
		observeInitialPublisherAfterExit(pipelineErr)
		os.RemoveAll(tempDir)
		return err
	}
	done := make(chan struct{})
	deliveryRetry := make(chan struct{}, 1)
	deliveryReconfigure := make(chan sourceClockDeliveryCommand, 1)
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		cancel()
		_ = receiver.Process.Kill()
		_, _ = receiver.Process, receiver.Wait()
		_ = receiverOutput.Close()
		pipelineErr := pipeline.stopAndWaitDone(pipelineDone, directPublisherStopNormal, sourceClockPublisherStopTimeout)
		observeInitialPublisherAfterExit(pipelineErr)
		os.RemoveAll(tempDir)
		return ErrAlreadyRunning
	}
	m.running = true
	m.cancel = cancel
	m.done = done
	m.audioRelay = nil
	m.deliveryRetry = deliveryRetry
	m.deliveryRetryPending = false
	m.deliveryReconfigure = deliveryReconfigure
	m.deliveryReconfigurePending = false
	m.deliveryReconfigureRequestID = ""
	m.deliveryReconfigureReply = nil
	m.deliverySessionID = sessionID
	m.deliveryActiveGeneration = publisherGeneration
	m.deliveryActivePublishURL = publishURL
	m.deliveryActiveOutput = output
	m.stopInitiator = ""
	m.status = Status{Enabled: true, Available: true, Running: true, ReceiverRunning: true, BridgeRunning: true, MediaReadyKnown: true, DeliveryPhase: string(SourceClockDeliveryActive), ReceiverPath: receiverPath, ReceiverName: receiverName, Message: "AirPlay受信待ちです。同一LANのiOSから画面ミラーリングを開始してください。"}
	m.mu.Unlock()
	m.notify()
	monitorOwnsPublisherObserver = true
	go m.monitorSourceClockWithPublisherDoneAt(ctx, cancel, done, pipeline, pipelineDone, receiver, receiverOutput, metricsGate, paths.Ready, paths.MediaReady, sessionID, publisherGeneration, artifactRoot, observer, configuredSourceClockNoSignalTimeout(), receiverStartedAt, nil, nil, tempDir, onStopped)
	return nil
}

func (m *Manager) RetrySourceClockDelivery() error {
	m.mu.Lock()
	if m.deliveryReconfigurePending {
		m.mu.Unlock()
		return ErrDeliveryRetryPending
	}
	if !m.running || !m.status.ReceiverRunning || m.status.BridgeRunning || m.deliveryRetry == nil {
		m.mu.Unlock()
		return ErrDeliveryRetryUnavailable
	}
	if m.deliveryRetryPending {
		m.mu.Unlock()
		return ErrDeliveryRetryPending
	}
	m.deliveryRetryPending = true
	select {
	case m.deliveryRetry <- struct{}{}:
		m.status.Message = "GStreamerを手動で再接続しています。AirPlay受信は維持されています。"
		m.mu.Unlock()
		m.notify()
		return nil
	default:
		m.deliveryRetryPending = false
		m.mu.Unlock()
		return ErrDeliveryRetryPending
	}
}

func (m *Manager) finishSourceClockDeliveryRetry() {
	m.mu.Lock()
	m.deliveryRetryPending = false
	m.mu.Unlock()
}

func (m *Manager) beginSourceClockDeliveryRetry() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running || m.deliveryRetryPending || m.deliveryReconfigurePending {
		return false
	}
	m.deliveryRetryPending = true
	return true
}

func (m *Manager) monitorSourceClock(ctx context.Context, cancel context.CancelFunc, done chan struct{}, pipeline *gstreamerDirectProcess, receiver *exec.Cmd, receiverOutput *sourceClockReceiverOutput, metricsGate *sourceClockMetricsGate, readyPath, mediaReadyPath, sessionID string, publisherGeneration uint64, artifactRoot string, observer airplaycontract.PublisherObserver, noSignalTimeout time.Duration, lifecycleTicks <-chan time.Time, lifecycleTickHandled chan<- struct{}, tempDir string, onStopped func()) {
	m.monitorSourceClockWithPublisherDone(ctx, cancel, done, pipeline, nil, receiver, receiverOutput, metricsGate, readyPath, mediaReadyPath, sessionID, publisherGeneration, artifactRoot, observer, noSignalTimeout, lifecycleTicks, lifecycleTickHandled, tempDir, onStopped)
}

func (m *Manager) monitorSourceClockWithPublisherDone(ctx context.Context, cancel context.CancelFunc, done chan struct{}, pipeline *gstreamerDirectProcess, pipelineDone <-chan error, receiver *exec.Cmd, receiverOutput *sourceClockReceiverOutput, metricsGate *sourceClockMetricsGate, readyPath, mediaReadyPath, sessionID string, publisherGeneration uint64, artifactRoot string, observer airplaycontract.PublisherObserver, noSignalTimeout time.Duration, lifecycleTicks <-chan time.Time, lifecycleTickHandled chan<- struct{}, tempDir string, onStopped func()) {
	m.monitorSourceClockWithPublisherDoneAt(ctx, cancel, done, pipeline, pipelineDone, receiver, receiverOutput, metricsGate, readyPath, mediaReadyPath, sessionID, publisherGeneration, artifactRoot, observer, noSignalTimeout, time.Now(), lifecycleTicks, lifecycleTickHandled, tempDir, onStopped)
}

func (m *Manager) monitorSourceClockWithPublisherDoneAt(ctx context.Context, cancel context.CancelFunc, done chan struct{}, pipeline *gstreamerDirectProcess, pipelineDone <-chan error, receiver *exec.Cmd, receiverOutput *sourceClockReceiverOutput, metricsGate *sourceClockMetricsGate, readyPath, mediaReadyPath, sessionID string, publisherGeneration uint64, artifactRoot string, observer airplaycontract.PublisherObserver, noSignalTimeout time.Duration, startedAt time.Time, lifecycleTicks <-chan time.Time, lifecycleTickHandled chan<- struct{}, tempDir string, onStopped func()) {
	m.mu.Lock()
	deliveryRetry := m.deliveryRetry
	deliveryReconfigure := m.deliveryReconfigure
	m.mu.Unlock()
	var recoveryOwner airplaycontract.PublisherRecoveryOwner
	managedDelivery := false
	if managed, ok := observer.(airplaycontract.ManagedRecoveryObserver); ok {
		recoveryOwner, managedDelivery = managed.ManagedRecoveryOwner()
	}
	type managedRecoveryResult struct {
		generation uint64
		err        error
	}
	managedRecoveryDone := make(chan managedRecoveryResult, 1)
	managedRecoveryActive := false
	receiverDone := make(chan error, 1)
	go func() { receiverDone <- errors.Join(receiver.Wait(), receiverOutput.Close()) }()
	var receiverDoneErr error
	receiverDoneConsumed := false
	var cleanupRetiring func() error
	waitReceiverDone := func() error {
		if !receiverDoneConsumed {
			receiverDoneErr = <-receiverDone
			receiverDoneConsumed = true
		}
		return receiverDoneErr
	}
	takeReceiverDoneIfReady := func() (error, bool) {
		if receiverDoneConsumed {
			return receiverDoneErr, true
		}
		select {
		case receiverDoneErr = <-receiverDone:
			receiverDoneConsumed = true
			return receiverDoneErr, true
		default:
			return nil, false
		}
	}
	terminationInitiatorObserved := false
	markTerminationInitiator := func(fallback string) {
		if terminationInitiatorObserved {
			return
		}
		reason := m.sourceClockTerminationInitiator(fallback)
		if reason == "" {
			return
		}
		if target, ok := observer.(airplaycontract.TerminationInitiatorObserver); ok {
			target.ObserveTerminationInitiator(sessionID, publisherGeneration, reason)
			terminationInitiatorObserved = true
		}
	}
	var cleanupOnce sync.Once
	finishMonitor := func(stoppedByRequest bool, firstName string, firstErr, bridgeErr, receiverErr error) {
		markTerminationInitiator("")
		if cleanupRetiring != nil {
			bridgeErr = errors.Join(bridgeErr, cleanupRetiring())
		}
		if observer != nil {
			observer.SealPublishers()
		}
		cleanupOnce.Do(func() {
			if onStopped != nil {
				onStopped()
			}
		})
		m.finishMonitorWithCallback(done, tempDir, stoppedByRequest, firstName, firstErr, bridgeErr, receiverErr, pipeline.log, receiverOutput.log, nil)
	}
	waitPipeline := func(current *gstreamerDirectProcess) {
		result := make(chan error, 1)
		pipelineDone = result
		go func() { result <- current.wait() }()
	}
	if pipelineDone == nil {
		waitPipeline(pipeline)
	}
	var lastPipelineErr error
	completeFinishedPublisher := func(generation uint64, processID int, waitErr error) {
		if observer == nil || generation == 0 {
			return
		}
		artifacts := airplaycontract.NewPublisherArtifacts(artifactRoot, sessionID, generation)
		if err := completeSourceClockPublisherProcess(observer, artifacts, true, processID, time.Now(), waitErr); err != nil {
			log.Printf("AirPlay source-clock publisher event log was not fully accepted: generation=%d err=%v", generation, err)
		}
	}
	stopPipeline := func(reason directPublisherStopReason) error {
		if pipelineDone == nil {
			return lastPipelineErr
		}
		lastPipelineErr = pipeline.stopAndWaitDone(
			pipelineDone, reason, sourceClockPublisherStopTimeout)
		completeFinishedPublisher(publisherGeneration, pipeline.processID(), lastPipelineErr)
		pipelineDone = nil
		return lastPipelineErr
	}
	var retryBudget sourceClockRetryBudget
	lifecycle := newSourceClockLifecycleTrackerAt(noSignalTimeout, startedAt)
	health := newSourceClockPublisherHealthTracker(sourceClockPublisherHealthyDuration)
	var lifecycleTicker *time.Ticker
	if lifecycleTicks == nil {
		lifecycleTicker = time.NewTicker(time.Second)
		lifecycleTicks = lifecycleTicker.C
		defer lifecycleTicker.Stop()
	}
	lastLifecycleError := ""
	lastGenerationMediaReadyError := ""
	generationMediaReady := false
	publisherRunning := true
	deliveryRecovery := sourceClockDeliveryRecoveryLatch{}
	lastHealthError := ""
	observeLifecycle := func(now time.Time) sourceClockTimeoutDecision {
		decision, lifecycleErr := sourceClockMonitorLifecycleDecision(lifecycle, metricsGate, mediaReadyPath, sessionID, publisherGeneration, now)
		if lifecycleTickHandled != nil {
			lifecycleTickHandled <- struct{}{}
		}
		if lifecycleErr != nil {
			if message := lifecycleErr.Error(); message != lastLifecycleError {
				log.Printf("AirPlay source-clock lifecycle observation failed: %v", lifecycleErr)
				lastLifecycleError = message
			}
			return decision
		}
		lastLifecycleError = ""
		if !generationMediaReady {
			ready, readyErr := m.observeSourceClockGenerationMediaReadyIfRunning(publisherRunning, mediaReadyPath, sessionID, publisherGeneration)
			if ready && readyErr == nil && managedDelivery {
				ready, readyErr = observeSourceClockPublisherReadiness(observer, readyPath, mediaReadyPath, sessionID, publisherGeneration)
			}
			if readyErr != nil {
				if message := readyErr.Error(); message != lastGenerationMediaReadyError {
					log.Printf("AirPlay source-clock current publisher media-ready observation failed: %v", readyErr)
					lastGenerationMediaReadyError = message
				}
			} else {
				lastGenerationMediaReadyError = ""
				if ready {
					generationMediaReady = true
				}
			}
		}
		return decision
	}
	observeHealth := func(now time.Time) (error, bool) {
		if metricsGate == nil {
			return nil, false
		}
		reset, pipelineErr, exited, healthErr := sourceClockPublisherHealthTickIfRunning(pipelineDone, health, &retryBudget, readyPath, sessionID, publisherGeneration, !lifecycle.Snapshot().FirstDecodedAt.IsZero(), metricsGate.snapshot(), now)
		if exited {
			return pipelineErr, true
		}
		if healthErr != nil {
			if message := healthErr.Error(); message != lastHealthError {
				log.Printf("AirPlay source-clock publisher health observation failed: %v", healthErr)
				lastHealthError = message
			}
			return nil, false
		}
		lastHealthError = ""
		if reset {
			if managedDelivery && recoveryOwner != nil {
				artifacts := airplaycontract.NewPublisherArtifacts(artifactRoot, sessionID, publisherGeneration)
				go recoveryOwner.PublisherHealthy(artifacts)
			}
			log.Printf("AirPlay source-clock publisher retry budget reset after healthy generation=%d", publisherGeneration)
		}
		return nil, false
	}
	finishHigherPriorityBeforePublisherFailure := func(pipelineErr error) bool {
		finishCause := func(timeoutDecision sourceClockTimeoutDecision) bool {
			receiverErr, receiverExited := takeReceiverDoneIfReady()
			switch sourceClockTerminationCauseFor(ctx.Err() != nil, receiverExited, timeoutDecision, false) {
			case sourceClockTerminationUserStop:
				cancel()
				receiverErr = waitReceiverDone()
				finishMonitor(true, "", nil, pipelineErr, receiverErr)
				return true
			case sourceClockTerminationReceiverExit:
				cancel()
				finishMonitor(false, "UxPlay source-clock receiver", receiverErr, pipelineErr, receiverErr)
				return true
			case sourceClockTerminationTimeout:
				markTerminationInitiator(sourceClockTerminationReason(timeoutDecision))
				m.setStatusMessage("AirPlay受信を無信号3分で終了します。")
				cancel()
				receiverErr = waitReceiverDone()
				finishMonitor(false, "AirPlay no signal timeout", nil, pipelineErr, receiverErr)
				return true
			default:
				return false
			}
		}
		if finishCause(sourceClockTimeoutNone) {
			return true
		}
		select {
		case now := <-lifecycleTicks:
			if finishCause(observeLifecycle(now)) {
				return true
			}
		default:
		}
		// Recheck the two higher-priority asynchronous conditions after the
		// non-blocking tick probe closes the select race window.
		return finishCause(sourceClockTimeoutNone)
	}
	var deferredPublisherRetry bool
	var deferredPublisherErr error
	type retiringPublisher struct {
		pipeline     *gstreamerDirectProcess
		done         <-chan error
		artifacts    airplaycontract.PublisherArtifacts
		generation   uint64
		processID    int
		doneConsumed bool
		completed    bool
	}
	var retiring *retiringPublisher
	var retiringDone <-chan error
	completePublisher := func(artifacts airplaycontract.PublisherArtifacts, processID int, waitErr error) error {
		if observer == nil {
			return nil
		}
		return completeSourceClockPublisherProcess(observer, artifacts, true, processID, time.Now(), waitErr)
	}
	retirePublisher := func(current *gstreamerDirectProcess, done <-chan error, artifacts airplaycontract.PublisherArtifacts, generation uint64, processID int) {
		transferSourceClockPublisherDoneToRetiring(&pipelineDone, &retiringDone, done)
		retiring = &retiringPublisher{pipeline: current, done: done, artifacts: artifacts, generation: generation, processID: processID}
	}
	completeRetiringPublisher := func(waitErr error) error {
		if retiring == nil || retiring.doneConsumed || retiring.completed {
			return nil
		}
		retiring.doneConsumed = true
		retiring.completed = true
		err := completePublisher(retiring.artifacts, retiring.processID, waitErr)
		retiring = nil
		retiringDone = nil
		return err
	}
	cleanupRetiring = func() error {
		if retiring == nil || retiring.doneConsumed || retiring.completed {
			return nil
		}
		stopErr := retiring.pipeline.stopAndWaitDoneWithin(retiring.done, directPublisherStopNormal, sourceClockPublisherStopTimeout)
		// Complete even when exit confirmation timed out so the sealed observer
		// retains exactly one fail-closed completion for this generation.
		return completeRetiringPublisher(stopErr)
	}
	handlePipelineExit := func(pipelineErr error, manualRetry, publisherExitAlreadyObserved bool) bool {
		// A retry policy must never suppress ownership/completion bookkeeping.
		// In particular, a committed managed publisher still has one Wait and
		// one recording completion even while legacy restart is forbidden.
		if !manualRetry && !publisherExitAlreadyObserved {
			lastPipelineErr = pipelineErr
			publisherRunning = false
			generationMediaReady = false
			completeFinishedPublisher(publisherGeneration, pipeline.processID(), pipelineErr)
		}
		if managedDelivery {
			if finishHigherPriorityBeforePublisherFailure(pipelineErr) {
				return true
			}
			_, decision := sourceClockPublisherExitDecision(pipelineErr)
			if !decision.Restart {
				m.setSourceClockPublisherStatus(false, fmt.Sprintf("GStreamer publisherは停止しています（%s）。AirPlay受信は維持されています。", decision.Reason))
				return false
			}
			if managedRecoveryActive {
				return false
			}
			if recoveryOwner == nil || retiring != nil || publisherRunning || pipelineDone != nil {
				m.setSourceClockPublisherStatus(false, "GStreamerの復旧所有権を確認できません。AirPlay受信は維持されています。")
				return false
			}
			old := airplaycontract.NewPublisherArtifacts(artifactRoot, sessionID, publisherGeneration)
			events, err := readSourceClockPublisherEvents(old.EventLog)
			if err == nil {
				var final airplaycontract.Event
				final, err = sourceClockVideoWatermarkFinalEvent(events, sessionID, publisherGeneration)
				if err == nil && (final.SourceSessionGeneration > uint64(^uint32(0)) || *final.SourceVideoSequence > uint64(^uint32(0))) {
					err = errSourceClockPostWatermarkEvidence
				}
			}
			if err != nil {
				log.Printf("AirPlay managed recovery rejected: generation=%d final-watermark=%v", publisherGeneration, err)
				m.setSourceClockPublisherStatus(false, "GStreamerの最終映像情報が不足しているため復旧を停止しました。AirPlay受信は維持されています。")
				return false
			}
			managedRecoveryActive = true
			m.setSourceClockPublisherStatus(false, "GStreamerを世代管理付きで再接続しています。AirPlay受信は維持されています。")
			go func() {
				managedRecoveryDone <- managedRecoveryResult{old.Generation, recoveryOwner.RecoverPublisher(ctx, old, m)}
			}()
			return false
		}
		if deliveryRecovery.required {
			if finishHigherPriorityBeforePublisherFailure(pipelineErr) {
				return true
			}
			m.setSourceClockPublisherStatus(false, "AirPlay配信切替に失敗したため、自動・手動のGStreamer再接続を停止しています。受信は維持されています。")
			return false
		}
		if !manualRetry && !publisherExitAlreadyObserved {
			receiverErr, receiverExited := takeReceiverDoneIfReady()
			// Publisher exit is only a delivery event. Timeout authority belongs
			// to lifecycle ticks so a child exit cannot invent or mix clocks.
			switch sourceClockTerminationCauseFor(ctx.Err() != nil, receiverExited, sourceClockTimeoutNone, true) {
			case sourceClockTerminationUserStop:
				cancel()
				receiverErr = waitReceiverDone()
				finishMonitor(true, "", nil, pipelineErr, receiverErr)
				return true
			case sourceClockTerminationReceiverExit:
				cancel()
				finishMonitor(false, "UxPlay source-clock receiver", receiverErr, pipelineErr, receiverErr)
				return true
			}
			exit, decision := sourceClockPublisherExitDecision(pipelineErr)
			log.Printf("AirPlay source-clock GStreamer publisher exited without stopping UxPlay: code=%d known=%t crashed=%t decision=%s restart=%t err=%v output=%q", exit.Code, exit.Known, exit.Crashed, decision.Reason, decision.Restart, pipelineErr, pipeline.log.String())
			if !decision.Restart {
				exitCode := "unknown"
				if exit.Known {
					exitCode = strconv.Itoa(exit.Code)
				}
				m.setSourceClockPublisherStatus(false, fmt.Sprintf("GStreamer publisherが停止しました（reason=%s, exit_code=%s）。AirPlay受信は維持されています。", decision.Reason, exitCode))
				return false
			}
			started, deferred := m.beginSourceClockCrashRetryOrDefer()
			if deferred {
				deferredPublisherRetry = true
				deferredPublisherErr = pipelineErr
				m.setSourceClockPublisherStatus(false, "AirPlay配信設定の切替完了後にGStreamerを再接続します。AirPlay受信は維持されています。")
				return false
			}
			if !started {
				m.setSourceClockPublisherStatus(false, "GStreamerの再接続要求が競合しました。AirPlay受信は維持されています。")
				return false
			}
		} else if !manualRetry && !m.beginSourceClockDeliveryRetry() {
			m.setSourceClockPublisherStatus(false, "GStreamerの再接続要求が競合しました。AirPlay受信は維持されています。")
			return false
		}
		retryActive := true
		finishRetry := func() {
			if retryActive {
				m.finishSourceClockDeliveryRetry()
				retryActive = false
			}
		}
		defer finishRetry()
		if manualRetry {
			m.setSourceClockPublisherStatus(false, "GStreamerを手動で再接続しています。AirPlay受信は維持されています。")
		} else {
			m.setSourceClockPublisherStatus(false, "GStreamerを再接続しています。AirPlay受信は維持されています。")
		}
		for {
			if finishHigherPriorityBeforePublisherFailure(pipelineErr) {
				return true
			}
			permission := retryBudget.Next(time.Now())
			if !permission.Allowed {
				m.setSourceClockPublisherStatus(false, fmt.Sprintf("GStreamerの自動再接続を停止しました（reason=%s）。AirPlay受信は維持されています。", permission.Reason))
				return false
			}
			timer := time.NewTimer(permission.Delay)
		waitForRetry:
			for {
				select {
				case now := <-lifecycleTicks:
					timeoutDecision := observeLifecycle(now)
					_, _ = observeHealth(now)
					receiverErr, receiverExited := takeReceiverDoneIfReady()
					cause := sourceClockTerminationCauseFor(ctx.Err() != nil, receiverExited, timeoutDecision, false)
					if cause == sourceClockTerminationNone {
						continue
					}
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					switch cause {
					case sourceClockTerminationUserStop:
						cancel()
						receiverErr = waitReceiverDone()
						finishMonitor(true, "", nil, pipelineErr, receiverErr)
					case sourceClockTerminationReceiverExit:
						cancel()
						finishMonitor(false, "UxPlay source-clock receiver", receiverErr, pipelineErr, receiverErr)
					case sourceClockTerminationTimeout:
						markTerminationInitiator(sourceClockTerminationReason(timeoutDecision))
						m.setStatusMessage("AirPlay受信を無信号3分で終了します。")
						cancel()
						receiverErr = waitReceiverDone()
						finishMonitor(false, "AirPlay no signal timeout", nil, pipelineErr, receiverErr)
					}
					return true
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					cancel()
					receiverErr := waitReceiverDone()
					finishMonitor(true, "", nil, pipelineErr, receiverErr)
					return true
				case receiverErr := <-receiverDone:
					receiverDoneErr = receiverErr
					receiverDoneConsumed = true
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					if ctx.Err() != nil {
						cancel()
						finishMonitor(true, "", nil, pipelineErr, receiverErr)
					} else {
						cancel()
						finishMonitor(false, "UxPlay source-clock receiver", receiverErr, pipelineErr, receiverErr)
					}
					return true
				case <-timer.C:
					break waitForRetry
				}
			}
			var nextArgs []string
			var nextGeneration uint64
			var generationErr error
			var nextArtifacts airplaycontract.PublisherArtifacts
			if observer == nil {
				nextArgs, nextGeneration, generationErr = nextSourceClockRestartArgs(pipeline.restartArgs)
			} else if publisherGeneration == ^uint64(0) {
				generationErr = errors.New("source-clock publisher generation overflow")
			} else {
				nextGeneration = publisherGeneration + 1
				nextArtifacts = airplaycontract.NewPublisherArtifacts(artifactRoot, sessionID, nextGeneration)
				if prepareErr := observer.PreparePublisher(ctx, nextArtifacts); prepareErr != nil {
					log.Printf("AirPlay source-clock publisher generation preparation failed: attempt=%d generation=%d err=%v", permission.Attempt, nextGeneration, prepareErr)
					m.setSourceClockPublisherStatus(false, fmt.Sprintf("GStreamerを再接続しています（%d回目）。AirPlay受信は維持されています。", permission.Attempt))
					continue
				}
				publisherGeneration = nextGeneration
				nextArgs, generationErr = nextSourceClockPublisherArgs(pipeline.restartArgs, nextArtifacts)
				readyPath = nextArtifacts.Ready
				mediaReadyPath = nextArtifacts.MediaReady
				pipeline.stopFile = nextArtifacts.StopRequest
			}
			if generationErr != nil {
				_ = completeSourceClockPublisher(observer, nextArtifacts, false, nil)
				log.Printf("AirPlay source-clock publisher restart configuration is invalid: %v", generationErr)
				if finishHigherPriorityBeforePublisherFailure(pipelineErr) {
					return true
				}
				cancel()
				receiverErr := waitReceiverDone()
				finishMonitor(false, "GStreamer source-clock publisher", generationErr, pipelineErr, receiverErr)
				return true
			}
			pipeline.restartArgs = nextArgs
			publisherGeneration = nextGeneration
			next, restartErr := pipeline.restart(ctx)
			if restartErr != nil {
				_ = completeSourceClockPublisher(observer, nextArtifacts, false, nil)
				log.Printf("AirPlay source-clock GStreamer publisher restart failed: attempt=%d err=%v", permission.Attempt, restartErr)
				m.setSourceClockPublisherStatus(false, fmt.Sprintf("GStreamerを再接続しています（%d回目）。AirPlay受信は維持されています。", permission.Attempt))
				continue
			}
			pipeline = next
			waitPipeline(pipeline)
			publisherRunning = true
			generationMediaReady = false
			retryActive = false
			m.setSourceClockDeliveryActiveGeneration(publisherGeneration)
			m.setSourceClockPublisherRestarted("AirPlay受信中です。GStreamer publisherを再接続しました。")
			return false
		}
	}
	executePlannedDelivery := func(command sourceClockDeliveryCommand) {
		request := command.request
		if err := validateSourceClockDeliveryRequest(request); err != nil {
			m.completeSourceClockDelivery(command, SourceClockDeliveryFailed, err)
			return
		}
		candidateCleanupKnown, validationFinished := true, true
		finishFailed := func(err error) {
			if request.CandidateDelivery != nil && candidateCleanupKnown && validationFinished {
				err = errors.Join(err, request.CandidateDelivery.Abort())
			}
			m.completeSourceClockDelivery(command, SourceClockDeliveryFailed, err)
		}
		m.setSourceClockDeliveryPhase(SourceClockDeliverySwitching)
		if ctx.Err() != nil || lifecycle.NoSignalDecision(time.Now()) != sourceClockTimeoutNone {
			finishFailed(ErrDeliveryReconfigureUnavailable)
			return
		}
		oldPipeline := pipeline
		oldDone := pipelineDone
		oldGeneration := publisherGeneration
		oldArtifacts := airplaycontract.NewPublisherArtifacts(artifactRoot, sessionID, oldGeneration)
		if request.ExpectedActiveGeneration != oldGeneration || request.Artifacts.SessionID != sessionID || request.CandidateGeneration <= oldGeneration {
			finishFailed(ErrDeliveryReconfigureStale)
			return
		}
		if err := validatePublisherArtifactsForPlannedExchange(request.Artifacts, oldArtifacts); err != nil {
			finishFailed(err)
			return
		}
		reserved := false
		if request.CandidateDelivery != nil {
			reserved = deliveryRecovery.reserveOwnedCandidate(request.CandidateGeneration)
		} else {
			reserved = deliveryRecovery.beginPlanned(request.CandidateGeneration)
		}
		if !reserved {
			finishFailed(ErrDeliveryReconfigureStale)
			return
		}
		// A crash retry deferred behind this command belongs to the old
		// delivery. The recovery latch makes it permanently ineligible for
		// this session, so do not leave a stale deferred intent behind.
		deferredPublisherRetry = false
		deferredPublisherErr = nil
		stopErr := lastPipelineErr
		if request.recoverPublisher {
			// Recovery must use the already-observed old completion. Do not
			// wait twice or overwrite the failed candidate's recording files.
			if oldDone != nil || publisherRunning || retiring != nil {
				finishFailed(ErrDeliveryReconfigureUnavailable)
				return
			}
		} else {
			stopErr = oldPipeline.stopAndWaitDoneWithin(oldDone, directPublisherStopNormal, sourceClockPublisherStopTimeout)
		}
		if errors.Is(stopErr, errDirectGStreamerProcessExitUnconfirmed) {
			publisherRunning = false
			generationMediaReady = false
			retirePublisher(oldPipeline, oldDone, oldArtifacts, oldGeneration, oldPipeline.processID())
			m.setSourceClockPublisherStatus(false, "AirPlay配信切替前のGStreamer終了確認に失敗しました。再接続は行いません。")
			finishFailed(stopErr)
			return
		}
		pipelineDone = nil
		publisherRunning = false
		generationMediaReady = false
		if !request.recoverPublisher {
			if completionErr := completePublisher(oldArtifacts, oldPipeline.processID(), stopErr); completionErr != nil {
				stopErr = errors.Join(stopErr, completionErr)
			}
		}
		lastPipelineErr = stopErr
		failAfterOldStop := func(err error) {
			m.setSourceClockPublisherStatus(false, "AirPlay配信設定の切替に失敗しました。受信は維持されていますが、配信は停止しています。")
			finishFailed(err)
		}
		if ctx.Err() != nil || lifecycle.NoSignalDecision(time.Now()) != sourceClockTimeoutNone {
			failAfterOldStop(ErrDeliveryReconfigureUnavailable)
			return
		}
		candidateArgs, oldEvents, err := preparePlannedSourceClockCandidate(oldPipeline.restartArgs, oldArtifacts, request, stopErr)
		if err != nil {
			failAfterOldStop(err)
			return
		}
		m.setSourceClockDeliveryPhase(SourceClockDeliveryValidating)
		if request.CandidateDelivery != nil {
			if err := request.CandidateDelivery.ClaimPublisher(); err != nil {
				failAfterOldStop(err)
				return
			}
		}
		candidate, err := startSourceClockGStreamerProcess(ctx, oldPipeline.restartPath, candidateArgs, request.Artifacts.StopRequest)
		if err != nil {
			_ = completeSourceClockPublisher(observer, request.Artifacts, false, nil)
			failAfterOldStop(err)
			return
		}
		candidateDone := make(chan error, 1)
		go func() { candidateDone <- candidate.wait() }()
		candidateDoneConsumed := false
		cleanupCandidate := func() error {
			if candidateDoneConsumed {
				return nil
			}
			stopKind := directPublisherStopNormal
			if ctx.Err() == nil && lifecycle.NoSignalDecision(time.Now()) != sourceClockTimeoutNone {
				stopKind = directPublisherStopNoSignal
			}
			cleanupErr := candidate.stopAndWaitDoneWithin(candidateDone, stopKind, sourceClockPublisherStopTimeout)
			if errors.Is(cleanupErr, errDirectGStreamerProcessExitUnconfirmed) {
				candidateCleanupKnown = false
				retirePublisher(candidate, candidateDone, request.Artifacts, request.CandidateGeneration, candidate.processID())
				return cleanupErr
			}
			candidateDoneConsumed = true
			return completePublisher(request.Artifacts, candidate.processID(), cleanupErr)
		}
		failCandidate := func(err error) {
			cleanupErr := cleanupCandidate()
			err = errors.Join(err, cleanupErr)
			failAfterOldStop(err)
		}
		proof, readinessErr, doneConsumed := waitSourceClockCandidateProof(ctx,
			request.Artifacts.EventLog, oldEvents, sessionID, oldGeneration,
			request.CandidateGeneration, candidateDone, sourceClockReadyTimeout, func() error {
				if _, exited := takeReceiverDoneIfReady(); exited {
					return ErrDeliveryReconfigureUnavailable
				}
				if observeLifecycle(time.Now()) != sourceClockTimeoutNone {
					return ErrDeliveryReconfigureUnavailable
				}
				return nil
			})
		if readinessErr != nil {
			if doneConsumed {
				candidateDoneConsumed = true
				var exited *sourceClockPublisherExitedBeforeReadyError
				waitErr := readinessErr
				if errors.As(readinessErr, &exited) {
					waitErr = exited.waitErr
				}
				_ = completePublisher(request.Artifacts, candidate.processID(), waitErr)
				failAfterOldStop(readinessErr)
				return
			}
			failCandidate(readinessErr)
			return
		}
		select {
		case candidateErr := <-candidateDone:
			candidateDoneConsumed = true
			_ = completePublisher(request.Artifacts, candidate.processID(), candidateErr)
			failAfterOldStop(&sourceClockPublisherExitedBeforeReadyError{waitErr: candidateErr})
			return
		default:
		}
		if ctx.Err() != nil || lifecycle.NoSignalDecision(time.Now()) != sourceClockTimeoutNone {
			failCandidate(ErrDeliveryReconfigureUnavailable)
			return
		}
		log.Printf("AirPlay candidate native video proof generation=%d source-generation=%d watermark=%d sequence=%d; private RTSP output still required",
			proof.PublisherGeneration, proof.SourceSessionGeneration, proof.WatermarkSequence, proof.Sequence)
		candidateEvents, err := readSourceClockPublisherEvents(request.Artifacts.EventLog)
		if err != nil {
			failCandidate(err)
			return
		}
		priority := func() error {
			if _, exited := takeReceiverDoneIfReady(); exited {
				return ErrDeliveryReconfigureUnavailable
			}
			if observeLifecycle(time.Now()) != sourceClockTimeoutNone {
				return ErrDeliveryReconfigureUnavailable
			}
			return nil
		}
		validationErr, consumed, joined := validateSourceClockCandidateOutput(ctx, request.CandidateDelivery, oldEvents, candidateEvents, candidateDone, priority)
		validationFinished = joined
		if consumed {
			candidateDoneConsumed = true
			var exited *sourceClockPublisherExitedBeforeReadyError
			if errors.As(validationErr, &exited) {
				_ = completePublisher(request.Artifacts, candidate.processID(), exited.waitErr)
			}
		}
		if validationErr != nil {
			failCandidate(validationErr)
			return
		}
		// Recheck process and receiver immediately before the small atomic commit.
		if err := priority(); err != nil {
			failCandidate(err)
			return
		}
		select {
		case waitErr := <-candidateDone:
			candidateDoneConsumed = true
			_ = completePublisher(request.Artifacts, candidate.processID(), waitErr)
			failAfterOldStop(&sourceClockPublisherExitedBeforeReadyError{waitErr: waitErr})
			return
		default:
		}
		if err := m.commitSourceClockCandidate(ctx, command, sourceClockCandidateCommitDeadline(lifecycle.Snapshot())); err != nil {
			failCandidate(err)
			return
		}
		pipeline, pipelineDone = candidate, candidateDone
		publisherGeneration = request.CandidateGeneration
		readyPath, mediaReadyPath = request.Artifacts.Ready, request.Artifacts.MediaReady
		publisherRunning, generationMediaReady = true, true
		lastPipelineErr = nil
		m.completeSourceClockDelivery(command, SourceClockDeliveryActive, nil)
	}
	for {
		select {
		case now := <-lifecycleTicks:
			timeoutDecision := observeLifecycle(now)
			receiverErr, receiverExited := takeReceiverDoneIfReady()
			switch sourceClockTerminationCauseFor(ctx.Err() != nil, receiverExited, timeoutDecision, false) {
			case sourceClockTerminationUserStop:
				cancel()
				pipelineErr := stopPipeline(directPublisherStopNormal)
				receiverErr = waitReceiverDone()
				finishMonitor(true, "", nil, pipelineErr, receiverErr)
				return
			case sourceClockTerminationReceiverExit:
				cancel()
				pipelineErr := stopPipeline(directPublisherStopNormal)
				finishMonitor(false, "UxPlay source-clock receiver", receiverErr, pipelineErr, receiverErr)
				return
			case sourceClockTerminationTimeout:
				markTerminationInitiator(sourceClockTerminationReason(timeoutDecision))
				m.setStatusMessage("AirPlay受信を無信号3分で終了します。")
				cancel()
				pipelineErr := stopPipeline(directPublisherStopNoSignal)
				receiverErr = waitReceiverDone()
				finishMonitor(false, "AirPlay no signal timeout", nil, pipelineErr, receiverErr)
				return
			}
			pipelineErr, exited := observeHealth(now)
			if exited {
				pipelineDone = nil
				if handlePipelineExit(pipelineErr, false, false) {
					return
				}
			}
		case <-ctx.Done():
			cancel()
			pipelineErr := stopPipeline(directPublisherStopNormal)
			receiverErr := waitReceiverDone()
			finishMonitor(true, "", nil, pipelineErr, receiverErr)
			return
		case receiverErr := <-receiverDone:
			receiverDoneErr = receiverErr
			receiverDoneConsumed = true
			if ctx.Err() != nil {
				cancel()
				pipelineErr := stopPipeline(directPublisherStopNormal)
				finishMonitor(true, "", nil, pipelineErr, receiverErr)
			} else {
				cancel()
				pipelineErr := stopPipeline(directPublisherStopNormal)
				finishMonitor(false, "UxPlay source-clock receiver", receiverErr, pipelineErr, receiverErr)
			}
			return
		case retiringErr := <-retiringDone:
			if completionErr := completeRetiringPublisher(retiringErr); completionErr != nil {
				log.Printf("AirPlay source-clock retiring publisher completion failed: %v", completionErr)
			}
		case pipelineErr := <-pipelineDone:
			pipelineDone = nil
			if handlePipelineExit(pipelineErr, false, false) {
				return
			}
		case <-deliveryRetry:
			if managedDelivery {
				m.finishSourceClockDeliveryRetry()
				if !publisherRunning && pipelineDone == nil && retiring == nil {
					if handlePipelineExit(lastPipelineErr, true, false) {
						return
					}
				}
				continue
			}
			if !deliveryRecovery.retryAllowed() {
				m.finishSourceClockDeliveryRetry()
				m.setSourceClockPublisherStatus(false, "AirPlay配信切替に失敗したため、GStreamer再接続を停止しています。AirPlay受信は維持されています。")
				continue
			}
			if publisherRunning || pipelineDone != nil || retiring != nil {
				m.finishSourceClockDeliveryRetry()
				if retiring != nil {
					m.setSourceClockPublisherStatus(false, "GStreamerの終了確認待ちのため再接続を保留しています。AirPlay受信は維持されています。")
				}
				continue
			}
			retryBudget.ResetAfterHealthy()
			if handlePipelineExit(lastPipelineErr, true, false) {
				return
			}
		case result := <-managedRecoveryDone:
			managedRecoveryActive = false
			m.mu.Lock()
			plannedPending := m.deliveryReconfigurePending
			m.mu.Unlock()
			if ctx.Err() == nil && result.generation == publisherGeneration && !publisherRunning && !plannedPending {
				log.Printf("AirPlay managed recovery ended: generation=%d err=%v", result.generation, result.err)
				m.setSourceClockPublisherStatus(false, "GStreamerの復旧に失敗しました。自動再試行は行わず、AirPlay受信だけを維持しています。")
			}
		case command := <-deliveryReconfigure:
			if command.request.CandidateDelivery == nil && !deliveryRecovery.retryAllowed() {
				m.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureUnavailable)
				continue
			}
			if ctx.Err() != nil {
				m.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureUnavailable)
				continue
			}
			if decision := lifecycle.NoSignalDecision(time.Now()); decision != sourceClockTimeoutNone {
				m.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureUnavailable)
				continue
			}
			if retiring != nil ||
				(!command.request.recoverPublisher && (!publisherRunning || pipelineDone == nil)) ||
				(command.request.recoverPublisher && (publisherRunning || pipelineDone != nil)) {
				m.completeSourceClockDelivery(command, SourceClockDeliveryFailed, ErrDeliveryReconfigureUnavailable)
				if deferredPublisherRetry {
					deferredPublisherRetry = false
					if handlePipelineExit(deferredPublisherErr, false, true) {
						return
					}
				}
				continue
			}
			executePlannedDelivery(command)
		}
	}
}

func sourceClockMonitorLifecycleDecision(tracker *sourceClockLifecycleTracker, gate *sourceClockMetricsGate, mediaReadyPath, sessionID string, publisherGeneration uint64, now time.Time) (sourceClockTimeoutDecision, error) {
	if tracker == nil {
		return sourceClockTimeoutNone, errors.New("source-clock lifecycle tracker is nil")
	}
	if gate == nil {
		return tracker.NoSignalDecision(now), errors.New("source-clock metrics gate is nil")
	}
	_, err := sourceClockLifecycleTick(tracker, gate, mediaReadyPath, sessionID, publisherGeneration, now)
	return tracker.NoSignalDecision(now), err
}

func nextSourceClockRestartArgs(args []string) ([]string, uint64, error) {
	nextArgs := append([]string(nil), args...)
	valueIndex := -1
	for index, arg := range nextArgs {
		if arg != "--publisher-generation" {
			continue
		}
		if valueIndex != -1 {
			return nil, 0, errors.New("source-clock publisher generation flag is duplicated")
		}
		if index+1 >= len(nextArgs) {
			return nil, 0, errors.New("source-clock publisher generation value is missing")
		}
		valueIndex = index + 1
	}
	if valueIndex == -1 {
		return nil, 0, errors.New("source-clock publisher generation flag is missing")
	}
	generation, err := strconv.ParseUint(nextArgs[valueIndex], 10, 64)
	if err != nil || generation == 0 || generation == ^uint64(0) {
		return nil, 0, errors.New("source-clock publisher generation is invalid")
	}
	generation++
	nextArgs[valueIndex] = strconv.FormatUint(generation, 10)
	return nextArgs, generation, nil
}

func sourceClockPublisherRetryDelay(attempt int) time.Duration {
	return airplaycontract.RecoveryDelay(attempt)
}

func (m *Manager) setSourceClockPublisherStatus(running bool, message string) {
	m.mu.Lock()
	if m.running {
		m.status.ReceiverRunning = true
		m.status.BridgeRunning = running
		m.status.MediaReadyKnown = true
		m.status.MediaReady = false
		m.status.Message = message
	}
	m.mu.Unlock()
	m.notify()
}

func (m *Manager) setSourceClockPublisherRestarted(message string) {
	m.mu.Lock()
	if m.running {
		m.status.ReceiverRunning = true
		m.status.BridgeRunning = true
		m.status.MediaReadyKnown = true
		m.status.MediaReady = false
		m.status.Message = message
	}
	m.deliveryRetryPending = false
	m.mu.Unlock()
	m.notify()
}

func (m *Manager) setSourceClockMediaReady() {
	m.mu.Lock()
	if m.running {
		m.status.MediaReadyKnown = true
		m.status.MediaReady = true
		m.status.Message = "iPhoneの画面を受信中です。"
	}
	m.mu.Unlock()
	m.notify()
}

package obsrtmp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

// errPublisherDown marks feeder failures caused by the persistent publisher
// process dying (write into its stdin failed); the session cannot continue.
var errPublisherDown = errors.New("radio publisher exited")

const radioRetimingFrameRate = 30.0

const maxRadioFallbackRetries = 4

var (
	radioURLPattern            = regexp.MustCompile(`(?i)(?:rtsp|rtmp|https?|file)://[^\s]+`)
	radioSensitiveKVPattern    = regexp.MustCompile(`(?i)\b(user|username|pass|password|token|secret|key)=([^\s&]+)`)
	radioAuthorizationPattern  = regexp.MustCompile(`(?i)\bauthorization\s*:\s*bearer\s+[^\s,;]+`)
	radioSensitiveColonPattern = regexp.MustCompile(`(?i)\b(pass(?:word)?|token|secret|key)\s*:\s*[^\s,;]+`)
)

type RadioPhase string

const (
	RadioPhaseStarting RadioPhase = "starting"
	RadioPhaseRunning  RadioPhase = "running"
	RadioPhaseStopped  RadioPhase = "stopped"
	RadioPhaseFailed   RadioPhase = "failed"
)

type RadioErrorStage string

const (
	RadioErrorStagePublisher RadioErrorStage = "publisher"
	RadioErrorStageFallback  RadioErrorStage = "fallback"
	RadioErrorStageTrack     RadioErrorStage = "track"
	RadioErrorStageMediaMTX  RadioErrorStage = "mediamtx"
	RadioErrorStageDecoder   RadioErrorStage = "decoder"
	RadioErrorStageOverlay   RadioErrorStage = "overlay"
	RadioErrorStageEncoder   RadioErrorStage = "encoder"
)

// RadioError is safe to expose to status consumers and callbacks. Message is
// deliberately scrubbed so process errors cannot publish ingest credentials.
type RadioError struct {
	Stage       RadioErrorStage `json:"stage"`
	Message     string          `json:"message"`
	Recoverable bool            `json:"recoverable"`
}

type sanitizedRadioError struct {
	cause   error
	message string
}

func (e sanitizedRadioError) Error() string { return e.message }
func (e sanitizedRadioError) Unwrap() error { return e.cause }

// SanitizeRadioError preserves errors.Is while removing credentials and
// private URLs before an error is sent to callbacks, state, or logs.
func SanitizeRadioError(err error) error {
	if err == nil {
		return nil
	}
	if sanitized, ok := err.(*sanitizedRadioError); ok && sanitized != nil {
		return err
	}
	if _, ok := err.(sanitizedRadioError); ok {
		return err
	}
	return &sanitizedRadioError{cause: err, message: sanitizeRadioErrorMessage(err.Error())}
}

func newRadioError(stage RadioErrorStage, err error, recoverable bool) RadioError {
	message := "radio session failed"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = SanitizeRadioError(err).Error()
	}
	return RadioError{Stage: stage, Message: message, Recoverable: recoverable}
}

func sanitizeRadioErrorMessage(message string) string {
	message = radioURLPattern.ReplaceAllStringFunc(message, func(raw string) string {
		trimmed := strings.TrimRight(raw, ".,;:)")
		return strings.Replace(raw, trimmed, "[redacted-url]", 1)
	})
	message = radioAuthorizationPattern.ReplaceAllString(message, "Authorization: [redacted]")
	message = radioSensitiveKVPattern.ReplaceAllString(message, "$1=[redacted]")
	return radioSensitiveColonPattern.ReplaceAllString(message, "$1: [redacted]")
}

// RadioCallbacks notify the server about playlist-radio lifecycle events.
// Callbacks can run from the radio loop or a child-exit observer; they must
// not block for long.
type RadioCallbacks struct {
	OnTrackStart       func(trackID string)
	OnTrackEnd         func(trackID string, err error)
	OnPublisherSink    func(io.Writer)
	OnPublisherDone    func()
	OnIdle             func()
	OnRTSPReady        func(RTSPEndpoint)
	OnRTSPDone         func(RTSPEndpoint)
	OnReadinessChanged func()
	OnError            func(RadioError)
	OnStopped          func()
}

// RadioPublisherProfile is frozen into one radio session before its
// persistent publisher starts. The zero value is the unchanged CPU
// production publisher; playlist GPU evaluation is an explicit opt-in only.
type RadioPublisherProfile string

const (
	RadioPublisherProfileCPUDefault            RadioPublisherProfile = ""
	RadioPublisherProfilePlaylistGPUEvaluation RadioPublisherProfile = "playlist-gpu-evaluation"
)

func normalizeRadioPublisherProfile(profile RadioPublisherProfile) (RadioPublisherProfile, error) {
	switch profile {
	case RadioPublisherProfileCPUDefault, RadioPublisherProfilePlaylistGPUEvaluation:
		return profile, nil
	default:
		return "", fmt.Errorf("unsupported radio publisher profile %q", profile)
	}
}

// RadioActiveSessionContract freezes the desired settings that define one
// running radio session. Desired callbacks are read only when Start creates it.
type RadioActiveSessionContract struct {
	SessionID          string                `json:"sessionId"`
	DeliveryProfile    string                `json:"deliveryProfile"`
	LatencyProfile     LatencyProfile        `json:"latencyProfile"`
	FallbackPreset     video.QualityPreset   `json:"fallbackPreset"`
	HLSVariant         string                `json:"hlsVariant"`
	HLSSegmentCount    int                   `json:"hlsSegmentCount"`
	HLSSegmentDuration string                `json:"hlsSegmentDuration"`
	OutputMode         RadioOutputMode       `json:"outputMode"`
	PublisherProfile   RadioPublisherProfile `json:"publisherProfile"`
}

// RadioTrackClaimMode identifies who owns the media returned by the next-track
// callback. The zero/CPU mode preserves the normal feeder path; Handled is used
// only after an explicit evaluation route has synchronously completed ownership
// of the track. A handled claim must never fall through to a CPU feeder.
type RadioTrackClaimMode string

const (
	RadioTrackClaimCPU     RadioTrackClaimMode = "cpu"
	RadioTrackClaimHandled RadioTrackClaimMode = "handled"
)

type RadioTrackClaimResolver func(mediaPath, trackID string, startSeconds int) (RadioTrackClaimMode, error)

func normalizeRadioTrackClaimMode(mode RadioTrackClaimMode) (RadioTrackClaimMode, error) {
	switch mode {
	case "", RadioTrackClaimCPU:
		return RadioTrackClaimCPU, nil
	case RadioTrackClaimHandled:
		return RadioTrackClaimHandled, nil
	default:
		return "", fmt.Errorf("unsupported radio track claim mode %q", mode)
	}
}

// RadioStatus is a snapshot of the radio session.
type RadioStatus struct {
	Running        bool       `json:"running"`
	Phase          RadioPhase `json:"phase"`
	LastError      string     `json:"lastError"`
	RetryCount     int        `json:"retryCount"`
	StoppedAt      time.Time  `json:"stoppedAt"`
	CurrentTrackID string     `json:"currentTrackId"`
	TrackStartedAt time.Time  `json:"trackStartedAt"`
	// BaseOffsetSeconds is the in-track position the current feed started
	// from (>0 after resuming a paused track).
	BaseOffsetSeconds int                         `json:"baseOffsetSeconds"`
	RTSPURL           string                      `json:"rtspUrl"`
	RTSPPublic        bool                        `json:"rtspPublic"`
	RTSPReady         bool                        `json:"rtspReady"`
	HLSReady          bool                        `json:"hlsReady"`
	Path              string                      `json:"path"`
	ActiveSession     *RadioActiveSessionContract `json:"activeSession,omitempty"`
	OutputMode        RadioOutputMode             `json:"outputMode"`
	OverlayError      string                      `json:"overlayError,omitempty"`
}

// TrackGeneration identifies one feeder lifetime. Completed closes only when
// that exact feeder returns; SessionDone closes when the whole radio exits.
type TrackGeneration struct {
	TrackID         string
	Generation      uint64
	Completed       <-chan struct{}
	SessionCanceled <-chan struct{}
	SessionDone     <-chan struct{}
}

// radioRuntime is the subset of mediaMTXRuntime the radio needs; split out so
// tests can substitute a fake.
type radioRuntime interface {
	start(ctx context.Context) error
	stop(timeout time.Duration) error
	publishURL() string
	rtmpPublishURL() string
	rtspURL() string
	proxyHLS(w http.ResponseWriter, req *http.Request, name string)
	hlsReady(ctx context.Context, profile LatencyProfile) bool
	pathReady(ctx context.Context) bool
	wait() <-chan error
}

type radioGate interface {
	start(ctx context.Context) error
	stop() error
}

// radioPublisher is the persistent ffmpeg process that owns the mediamtx
// publish connection for the whole session. Feeders write MPEG-TS into sink.
type radioPublisher interface {
	sink() io.Writer
	done() <-chan error
	close()
}

type ownedMediaMTXPIDProvider interface {
	ownedMediaMTXPIDs() []int
}

// RadioManager streams a music playlist as a continuous "radio" broadcast.
// One MediaMTX instance serves RTSP + LL-HLS on fixed URLs; one persistent
// publisher process stays connected for the whole session, and per-track
// feeder processes pipe real-time MPEG-TS into it. Between tracks, while
// paused, and while idle an active standby feeder generates the fallback
// screen, so viewers never see the stream drop. Track selection is delegated
// to next(), so the queue policy (shuffle, loop, interrupts) lives entirely in
// the playlist domain.
type RadioManager struct {
	mu     sync.Mutex
	outDir string
	host   string
	next   func() (mediaPath, trackID string, startSeconds int, ok bool)
	cb     RadioCallbacks

	cancel                   context.CancelFunc
	done                     chan struct{}
	starting                 bool
	startingCancel           context.CancelFunc
	startGeneration          uint64
	startingGeneration       uint64
	activeGeneration         uint64
	runtime                  radioRuntime
	status                   RadioStatus
	skipPush                 context.CancelFunc
	fillerCancel             context.CancelFunc
	wake                     chan struct{}
	skipped                  bool
	pathName                 string
	rtspPublic               RTSPEndpoint
	activeSession            *RadioActiveSessionContract
	fallbackPreset           func() video.QualityPreset
	latencyProfile           func() LatencyProfile
	outputMode               func() RadioOutputMode
	publisherProfile         func() RadioPublisherProfile
	overlaySource            func() OverlaySnapshot
	trackGeneration          uint64
	activeTrack              TrackGeneration
	activeTrackDone          chan struct{}
	claimDone                chan struct{}
	trackClaimResolver       RadioTrackClaimResolver
	activeTrackClaimResolver RadioTrackClaimResolver

	// test seams
	buildRuntime              func(ctx context.Context, contract RadioActiveSessionContract) (radioRuntime, radioGate, RTSPEndpoint, error)
	startPublisher            func(ctx context.Context, publishURL string) (radioPublisher, error)
	startPlaylistGPUPublisher func(ctx context.Context, publishURL string) (radioPublisher, error)
	runFeeder                 func(ctx context.Context, mediaPath string, startSeconds int, loop bool, timestampOffset float64, sink io.Writer) error
	runFallbackFeeder         func(ctx context.Context, contract RadioActiveSessionContract, timestampOffset float64, sink io.Writer) (float64, error)
	startProgramEncoder       func(ctx context.Context, contract RadioActiveSessionContract) (ProgramEncoder, error)
	runProgramFeeder          func(ctx context.Context, mediaPath string, startSeconds, width, height int, frames chan<- ProgramSourceFrame) error
	waitFallbackRetry         func(ctx context.Context, delay time.Duration) bool
	afterSessionPromote       func(uint64)
	playlistAudioTee          func(samples []byte, pts time.Duration) error
	playlistAudioTeeMu        sync.Mutex
	playlistAudioTeeCond      *sync.Cond
	playlistAudioTeeInFlight  int
	playlistGPUOutputActive   bool
	playlistOutputMu          sync.RWMutex
}

// OwnedMediaMTXPIDs reports only the active sidecar process handle owned by
// this manager. It never performs a global process-name or PID scan.
func (m *RadioManager) OwnedMediaMTXPIDs() []int {
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	provider, ok := runtime.(ownedMediaMTXPIDProvider)
	if !ok {
		return nil
	}
	return append([]int(nil), provider.ownedMediaMTXPIDs()...)
}

func NewRadioManager(outDir, host string, next func() (mediaPath, trackID string, startSeconds int, ok bool), cb RadioCallbacks) *RadioManager {
	m := &RadioManager{
		outDir: outDir,
		host:   host,
		next:   next,
		cb:     cb,
		wake:   make(chan struct{}, 1),
		status: RadioStatus{Phase: RadioPhaseStopped, OutputMode: RadioOutputModeCompatibilityCopy},
	}
	m.buildRuntime = m.buildMediaMTX
	m.startPublisher = m.startFFmpegPublisher
	m.startPlaylistGPUPublisher = m.startFFmpegPlaylistGPUPublisher
	m.runFeeder = m.runFFmpegFeeder
	m.runFallbackFeeder = m.runFFmpegFallbackFeeder
	m.startProgramEncoder = m.startFFmpegProgramEncoder
	m.runProgramFeeder = m.runFFmpegProgramFeeder
	m.waitFallbackRetry = waitRadioFallbackRetry
	m.fallbackPreset = func() video.QualityPreset { return video.MusicRadioQualityPreset("auto", 0, 0) }
	m.latencyProfile = func() LatencyProfile { return NormalizeLatencyProfile(LatencyModeRTSPUltra) }
	m.outputMode = func() RadioOutputMode { return RadioOutputModeCompatibilityCopy }
	m.publisherProfile = func() RadioPublisherProfile { return RadioPublisherProfileCPUDefault }
	m.overlaySource = func() OverlaySnapshot { return OverlaySnapshot{Mode: OverlayModeOff} }
	return m
}

// SetOutputMode changes only the desired mode captured by the next Start. A
// running session never swaps packet producers on its existing publisher.
func (m *RadioManager) SetOutputMode(fn func() RadioOutputMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		m.outputMode = func() RadioOutputMode { return RadioOutputModeCompatibilityCopy }
		return
	}
	m.outputMode = fn
}

// SetPublisherProfile selects the persistent publisher only for the next
// session. A running session never swaps publisher processes. Nil restores the
// unchanged CPU production profile.
func (m *RadioManager) SetPublisherProfile(fn func() RadioPublisherProfile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		m.publisherProfile = func() RadioPublisherProfile { return RadioPublisherProfileCPUDefault }
		return
	}
	m.publisherProfile = fn
}

// SetTrackClaimResolver installs the session-scoped ownership handoff used by
// explicit playlist GPU evaluation. A nil resolver preserves the ordinary CPU
// feeder behavior. The resolver is snapshotted when Start creates a session;
// changing it while a session is running cannot change ownership mid-stream.
func (m *RadioManager) SetTrackClaimResolver(fn RadioTrackClaimResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trackClaimResolver = fn
}

func (m *RadioManager) resolveTrackClaim(mediaPath, trackID string, startSeconds int) (RadioTrackClaimMode, error) {
	m.mu.Lock()
	resolver := m.activeTrackClaimResolver
	m.mu.Unlock()
	if resolver == nil {
		return RadioTrackClaimCPU, nil
	}
	mode, err := resolver(mediaPath, trackID, startSeconds)
	if err != nil {
		return "", err
	}
	return normalizeRadioTrackClaimMode(mode)
}

// SetPlaylistAudioTee installs an explicit evaluation-only PCM/PTS tee for
// program sessions. Nil leaves the normal CPU publisher path unchanged.
func (m *RadioManager) SetPlaylistAudioTee(tee func(samples []byte, pts time.Duration) error) {
	m.playlistAudioTeeMu.Lock()
	m.playlistAudioTee = tee
	if m.playlistAudioTeeCond == nil {
		m.playlistAudioTeeCond = sync.NewCond(&m.playlistAudioTeeMu)
	}
	for m.playlistAudioTeeInFlight > 0 {
		m.playlistAudioTeeCond.Wait()
	}
	m.playlistAudioTeeMu.Unlock()
}

// writePlaylistAudioTee acquires an in-flight reference before invoking the
// explicit evaluation tee. Removing or replacing the tee waits for this
// reference, so GPU mux teardown cannot close the bridge under an active PCM
// callback. With no tee installed, the normal CPU program route remains a
// no-op exactly as before.
func (m *RadioManager) writePlaylistAudioTee(samples []byte, pts time.Duration) (err error) {
	m.playlistAudioTeeMu.Lock()
	tee := m.playlistAudioTee
	if tee == nil {
		m.playlistAudioTeeMu.Unlock()
		return nil
	}
	m.playlistAudioTeeInFlight++
	m.playlistAudioTeeMu.Unlock()
	defer func() {
		m.playlistAudioTeeMu.Lock()
		m.playlistAudioTeeInFlight--
		if m.playlistAudioTeeCond != nil {
			m.playlistAudioTeeCond.Broadcast()
		}
		m.playlistAudioTeeMu.Unlock()
	}()
	return tee(samples, pts)
}

// SetPlaylistGPUOutputActive suppresses the normal CPU program bitstream while
// the explicit playlist GPU mux owns the active publisher sink.
func (m *RadioManager) SetPlaylistGPUOutputActive(active bool) {
	m.playlistOutputMu.Lock()
	m.mu.Lock()
	m.playlistGPUOutputActive = active
	m.mu.Unlock()
	m.playlistOutputMu.Unlock()
}

func (m *RadioManager) writeProgramOutput(sink io.Writer, payload []byte) (int, error) {
	m.playlistOutputMu.RLock()
	defer m.playlistOutputMu.RUnlock()
	m.mu.Lock()
	active := m.playlistGPUOutputActive
	m.mu.Unlock()
	if active {
		return len(payload), nil
	}
	return sink.Write(payload)
}

// SetOverlaySource supplies metadata only. Program sessions sample it at track
// boundaries; changing it never restarts the encoder, publisher, or endpoint.
func (m *RadioManager) SetOverlaySource(fn func() OverlaySnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		m.overlaySource = func() OverlaySnapshot { return OverlaySnapshot{Mode: OverlayModeOff} }
		return
	}
	m.overlaySource = fn
}

// SetFillerSource is retained for compatibility with older tests and callers.
// The playlist radio now uses an active generated fallback instead of a file.
func (m *RadioManager) SetFillerSource(fn func() (string, error)) {
	_ = fn
}

func (m *RadioManager) SetFallbackPreset(fn func() video.QualityPreset) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fallbackPreset = fn
}

// SetLatencyProfile selects HLS remuxing only when a later radio session is
// started. A running RTSP session is intentionally left untouched.
func (m *RadioManager) SetLatencyProfile(fn func() LatencyProfile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		m.latencyProfile = func() LatencyProfile { return NormalizeLatencyProfile(LatencyModeRTSPUltra) }
		return
	}
	m.latencyProfile = fn
}

// Start launches the radio session. It is idempotent while running.
func (m *RadioManager) Start() error {
	m.mu.Lock()
	if m.done != nil || m.starting {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.startGeneration++
	startGeneration := m.startGeneration
	m.starting = true
	m.startingCancel = cancel
	m.startingGeneration = startGeneration
	fallbackPreset := m.fallbackPreset
	latencyProfile := m.latencyProfile
	outputMode := m.outputMode
	publisherProfile := m.publisherProfile
	trackClaimResolver := m.trackClaimResolver
	m.mu.Unlock()

	contract := RadioActiveSessionContract{
		SessionID:       sessionID(),
		DeliveryProfile: LatencyModeRTSPUltra,
		LatencyProfile:  NormalizeLatencyProfile(LatencyModeRTSPUltra),
		FallbackPreset:  video.MusicRadioQualityPreset("auto", 0, 0),
	}
	if latencyProfile != nil {
		contract.LatencyProfile = normalizeLatencyProfile(latencyProfile())
		contract.DeliveryProfile = contract.LatencyProfile.Mode
	}
	if fallbackPreset != nil {
		contract.FallbackPreset = fallbackPreset()
	}
	contract.OutputMode = RadioOutputModeCompatibilityCopy
	if outputMode != nil {
		contract.OutputMode = normalizeRadioOutputMode(outputMode())
	}
	contract.PublisherProfile = RadioPublisherProfileCPUDefault
	if publisherProfile != nil {
		profile, profileErr := normalizeRadioPublisherProfile(publisherProfile())
		if profileErr != nil {
			m.mu.Lock()
			if m.startingGeneration == startGeneration {
				m.starting = false
				m.startingCancel = nil
				m.startingGeneration = 0
			}
			m.mu.Unlock()
			cancel()
			return profileErr
		}
		contract.PublisherProfile = profile
	}
	contract.HLSVariant, contract.HLSSegmentCount, contract.HLSSegmentDuration = radioHLSSettings(contract.LatencyProfile)

	m.mu.Lock()
	if !m.starting || m.startingGeneration != startGeneration || ctx.Err() != nil {
		if m.startingGeneration == startGeneration {
			m.starting = false
			m.startingCancel = nil
			m.startingGeneration = 0
		}
		m.mu.Unlock()
		cancel()
		return nil
	}
	done := make(chan struct{})
	m.activeSession = &contract
	m.activeTrackClaimResolver = trackClaimResolver
	m.activeGeneration = startGeneration
	m.cancel = cancel
	m.done = done
	activeCopy := contract
	m.status = RadioStatus{Running: true, Phase: RadioPhaseStarting, ActiveSession: &activeCopy, OutputMode: contract.OutputMode}
	m.starting = false
	m.startingCancel = nil
	m.startingGeneration = 0
	m.mu.Unlock()
	if m.afterSessionPromote != nil {
		m.afterSessionPromote(startGeneration)
	}

	runtime, gate, endpoint, err := m.buildRuntime(ctx, contract)
	if err != nil {
		if !m.recordTerminalErrorForGeneration(startGeneration, newRadioError(RadioErrorStageMediaMTX, err, false)) {
			cancel()
			close(done)
			return nil
		}
		m.mu.Lock()
		if m.ownsActiveGenerationLocked(startGeneration) {
			m.cancel = nil
			m.done = nil
			m.activeSession = nil
			m.activeGeneration = 0
			m.status.ActiveSession = nil
		}
		m.mu.Unlock()
		cancel()
		close(done)
		return err
	}

	m.mu.Lock()
	if !m.ownsActiveGenerationLocked(startGeneration) || ctx.Err() != nil {
		m.mu.Unlock()
		if gate != nil {
			_ = gate.stop()
		}
		_ = runtime.stop(5 * time.Second)
		close(done)
		return nil
	}
	m.runtime = runtime
	endpoint.Generation = startGeneration
	m.pathName = endpoint.Path
	m.rtspPublic = endpoint
	runningCopy := contract
	m.status = RadioStatus{Running: true, Phase: RadioPhaseRunning, Path: endpoint.Path, ActiveSession: &runningCopy, OutputMode: contract.OutputMode}
	m.mu.Unlock()

	go m.run(ctx, done, runtime, gate, endpoint, contract, startGeneration)
	return nil
}

func (m *RadioManager) run(ctx context.Context, done chan struct{}, runtime radioRuntime, gate radioGate, endpoint RTSPEndpoint, contract RadioActiveSessionContract, generation uint64) {
	defer func() {
		if gate != nil {
			_ = gate.stop()
		}
		_ = runtime.stop(5 * time.Second)
		m.mu.Lock()
		owned := m.ownsActiveGenerationLocked(generation)
		if !owned {
			m.mu.Unlock()
			close(done)
			return
		}
		m.runtime = nil
		hadRTSPReady := m.status.RTSPReady
		m.status.RTSPReady = false
		m.status.HLSReady = false
		m.status.RTSPURL = ""
		m.status.RTSPPublic = false
		if m.status.Phase != RadioPhaseFailed {
			m.status.Running = false
			m.status.Phase = RadioPhaseStopped
			m.status.StoppedAt = time.Now()
		}
		if m.activeTrackDone != nil {
			close(m.activeTrackDone)
			m.activeTrack = TrackGeneration{}
			m.activeTrackDone = nil
		}
		m.cancel = nil
		m.done = nil
		m.activeSession = nil
		m.activeTrackClaimResolver = nil
		m.activeGeneration = 0
		m.status.ActiveSession = nil
		m.mu.Unlock()
		close(done)
		if hadRTSPReady && m.cb.OnRTSPDone != nil {
			m.cb.OnRTSPDone(endpoint)
		}
		if m.cb.OnStopped != nil {
			m.cb.OnStopped()
		}
	}()

	var programEncoder ProgramEncoder
	var err error
	if contract.OutputMode == RadioOutputModeProgram {
		programEncoder, err = m.startProgramEncoder(ctx, contract)
		if err != nil {
			m.recordTerminalErrorForGeneration(generation, newRadioError(RadioErrorStageEncoder, err, false))
			return
		}
	}

	var publisherStarter func(context.Context, string) (radioPublisher, error)
	switch contract.PublisherProfile {
	case RadioPublisherProfileCPUDefault:
		publisherStarter = m.startPublisher
	case RadioPublisherProfilePlaylistGPUEvaluation:
		publisherStarter = m.startPlaylistGPUPublisher
	default:
		m.recordTerminalErrorForGeneration(generation, newRadioError(RadioErrorStagePublisher, fmt.Errorf("unsupported radio publisher profile %q", contract.PublisherProfile), false))
		if programEncoder != nil {
			programEncoder.Close()
		}
		return
	}
	if publisherStarter == nil {
		if programEncoder != nil {
			programEncoder.Close()
		}
		m.recordTerminalErrorForGeneration(generation, newRadioError(RadioErrorStagePublisher, errors.New("radio publisher starter is unavailable"), false))
		return
	}
	publisher, err := publisherStarter(ctx, runtime.rtmpPublishURL())
	if err != nil {
		if programEncoder != nil {
			programEncoder.Close()
		}
		m.recordTerminalErrorForGeneration(generation, newRadioError(RadioErrorStagePublisher, err, false))
		return
	}
	defer publisher.close()
	if m.cb.OnPublisherSink != nil {
		m.cb.OnPublisherSink(publisher.sink())
		if contract.PublisherProfile == RadioPublisherProfilePlaylistGPUEvaluation {
			_ = recordPlaylistGPUTrace(playlistGPUTracePathFromEnv(), "gpu_publisher_sink_exposed", nil)
		}
	}
	defer func() {
		if m.cb.OnPublisherDone != nil {
			m.cb.OnPublisherDone()
		}
	}()
	if programEncoder != nil {
		defer programEncoder.Close()
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	watchDone := make(chan struct{})
	defer close(watchDone)
	go m.watchRadioChildrenForGeneration(ctx, runtime, publisher, cancelSession, watchDone, generation)
	go m.monitorReadiness(sessionCtx, runtime, endpoint, contract, generation)
	var fatal *RadioError
	if contract.OutputMode == RadioOutputModeProgram {
		fatal = m.runProgramSession(sessionCtx, ctx, done, publisher, programEncoder, contract, generation)
	} else {
		fatal = m.runSession(sessionCtx, ctx, done, publisher, contract, generation)
	}
	if fatal != nil {
		m.recordTerminalErrorForGeneration(generation, *fatal)
		cancelSession()
	}
}

func (m *RadioManager) watchRadioChildren(parent context.Context, runtime radioRuntime, publisher radioPublisher, cancel context.CancelFunc, watchDone <-chan struct{}) {
	radioErr, ok := waitRadioChildExit(parent, runtime, publisher, watchDone)
	if !ok || parent.Err() != nil {
		return
	}
	m.recordTerminalError(radioErr)
	cancel()
}

func (m *RadioManager) watchRadioChildrenForGeneration(parent context.Context, runtime radioRuntime, publisher radioPublisher, cancel context.CancelFunc, watchDone <-chan struct{}, generation uint64) {
	radioErr, ok := waitRadioChildExit(parent, runtime, publisher, watchDone)
	if !ok {
		return
	}
	if parent.Err() != nil {
		return
	}
	m.recordTerminalErrorForGeneration(generation, radioErr)
	cancel()
}

func waitRadioChildExit(parent context.Context, runtime radioRuntime, publisher radioPublisher, watchDone <-chan struct{}) (RadioError, bool) {
	runtimeDone := runtime.wait()
	publisherDone := publisher.done()
	select {
	case err, open := <-runtimeDone:
		return radioChildError(RadioErrorStageMediaMTX, err, open), true
	default:
	}
	select {
	case err, open := <-runtimeDone:
		return radioChildError(RadioErrorStageMediaMTX, err, open), true
	case err, open := <-publisherDone:
		// If both exits are available, MediaMTX owns the failure episode.
		select {
		case runtimeErr, runtimeOpen := <-runtimeDone:
			return radioChildError(RadioErrorStageMediaMTX, runtimeErr, runtimeOpen), true
		default:
		}
		return radioChildError(RadioErrorStagePublisher, err, open), true
	case <-parent.Done():
		return RadioError{}, false
	case <-watchDone:
		return RadioError{}, false
	}
}

func radioChildError(stage RadioErrorStage, err error, open bool) RadioError {
	if !open || err == nil {
		if stage == RadioErrorStageMediaMTX {
			err = errors.New("MediaMTX exited")
		} else {
			err = errors.New("publisher exited")
		}
	}
	return newRadioError(stage, err, false)
}

func (m *RadioManager) runSession(ctx, sessionParent context.Context, done chan struct{}, publisher radioPublisher, contract RadioActiveSessionContract, sessionGeneration uint64) *RadioError {

	// Feeders must keep their own media timeline local. The persistent publisher
	// owns the continuous RTSP/HLS session; carrying idle/connection/fallback
	// time into -output_ts_offset makes the next track start with an artificial
	// head offset on some receivers.
	const feederTimestampOffset = 0.0
	for {
		if ctx.Err() != nil {
			return nil
		}
		pushCtx, cancelPush := context.WithCancel(ctx)
		m.mu.Lock()
		if !m.ownsActiveGenerationLocked(sessionGeneration) {
			m.mu.Unlock()
			cancelPush()
			return nil
		}
		m.claimDone = make(chan struct{})
		m.mu.Unlock()
		// next is a server callback and may synchronously prepare/submit an
		// explicit playlist GPU transition. Never call it while holding the
		// radio mutex: the callback is allowed to inspect RadioStatus.
		mediaPath, trackID, startSeconds, ok := m.next()
		claimMode, claimErr := m.resolveTrackClaim(mediaPath, trackID, startSeconds)
		m.mu.Lock()
		if !m.ownsActiveGenerationLocked(sessionGeneration) {
			if m.claimDone != nil {
				close(m.claimDone)
				m.claimDone = nil
			}
			m.mu.Unlock()
			cancelPush()
			return nil
		}
		if claimErr != nil {
			if m.claimDone != nil {
				close(m.claimDone)
				m.claimDone = nil
			}
			m.mu.Unlock()
			cancelPush()
			radioErr := newRadioError(RadioErrorStageTrack, fmt.Errorf("resolve radio track ownership: %w", claimErr), false)
			return &radioErr
		}
		generation := uint64(0)
		if ok {
			m.skipPush = cancelPush
			m.skipped = false
			m.trackGeneration++
			m.activeTrackDone = make(chan struct{})
			m.activeTrack = TrackGeneration{
				TrackID:         trackID,
				Generation:      m.trackGeneration,
				Completed:       m.activeTrackDone,
				SessionCanceled: sessionParent.Done(),
				SessionDone:     done,
			}
			m.status.CurrentTrackID = trackID
			m.status.TrackStartedAt = time.Now()
			m.status.BaseOffsetSeconds = startSeconds
			generation = m.trackGeneration
		}
		if m.claimDone != nil {
			close(m.claimDone)
			m.claimDone = nil
		}
		m.mu.Unlock()
		if !ok {
			cancelPush()
			m.setCurrentForGeneration(sessionGeneration, "")
			if m.cb.OnIdle != nil {
				m.cb.OnIdle()
			}
			if _, fatal := m.feedFillerUntilWake(ctx, publisher, contract, feederTimestampOffset, sessionGeneration); fatal != nil {
				return fatal
			}
			continue
		}
		if m.cb.OnTrackStart != nil {
			m.cb.OnTrackStart(trackID)
		}
		if claimMode == RadioTrackClaimHandled {
			cancelPush()
			m.completeTrack(sessionGeneration, generation)
			if m.cb.OnTrackEnd != nil {
				m.cb.OnTrackEnd(trackID, nil)
			}
			continue
		}

		err := m.runFeeder(pushCtx, mediaPath, startSeconds, false, feederTimestampOffset, publisher.sink())
		cancelPush()
		m.mu.Lock()
		if !m.ownsActiveGenerationLocked(sessionGeneration) {
			m.mu.Unlock()
			return nil
		}
		skipped := m.skipped
		m.skipPush = nil
		m.mu.Unlock()
		m.completeTrack(sessionGeneration, generation)

		if errors.Is(err, errPublisherDown) {
			radioErr := newRadioError(RadioErrorStagePublisher, err, false)
			return &radioErr
		}
		if ctx.Err() != nil {
			return nil
		}
		if skipped && !errors.Is(err, errPublisherDown) {
			err = nil
		}
		err = SanitizeRadioError(err)
		if m.cb.OnTrackEnd != nil {
			m.cb.OnTrackEnd(trackID, err)
		}
		if err != nil {
			m.recordRecoverableErrorForGeneration(sessionGeneration, newRadioError(RadioErrorStageTrack, err, true), 0)
		}
	}
}

func (m *RadioManager) recordRecoverableErrorForGeneration(generation uint64, radioErr RadioError, retryCount int) bool {
	m.mu.Lock()
	if !m.ownsActiveGenerationLocked(generation) {
		m.mu.Unlock()
		return false
	}
	m.status.LastError = radioErr.Message
	m.status.RetryCount = retryCount
	m.mu.Unlock()
	if m.cb.OnError != nil {
		m.cb.OnError(radioErr)
	}
	return true
}

func (m *RadioManager) recordTerminalErrorForGeneration(generation uint64, radioErr RadioError) bool {
	m.mu.Lock()
	if !m.ownsActiveGenerationLocked(generation) || m.status.Phase == RadioPhaseFailed {
		m.mu.Unlock()
		return false
	}
	m.status.Running = false
	m.status.Phase = RadioPhaseFailed
	m.status.LastError = radioErr.Message
	m.status.StoppedAt = time.Now()
	m.mu.Unlock()
	if m.cb.OnError != nil {
		m.cb.OnError(radioErr)
	}
	return true
}

// recordTerminalError is retained for isolated child-exit tests that do not
// start a session. Running sessions must use the generation-aware variant.
func (m *RadioManager) recordTerminalError(radioErr RadioError) {
	m.mu.Lock()
	if m.status.Phase == RadioPhaseFailed {
		m.mu.Unlock()
		return
	}
	m.status.Running = false
	m.status.Phase = RadioPhaseFailed
	m.status.LastError = radioErr.Message
	m.status.StoppedAt = time.Now()
	m.mu.Unlock()
	if m.cb.OnError != nil {
		m.cb.OnError(radioErr)
	}
}

func (m *RadioManager) setRetryCountForGeneration(generation uint64, retryCount int) bool {
	m.mu.Lock()
	if !m.ownsActiveGenerationLocked(generation) {
		m.mu.Unlock()
		return false
	}
	m.status.RetryCount = retryCount
	m.mu.Unlock()
	return true
}

func (m *RadioManager) monitorReadiness(ctx context.Context, runtime radioRuntime, endpoint RTSPEndpoint, contract RadioActiveSessionContract, generation uint64) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	const lossConfirmations = 3
	rtspFailures, hlsFailures := 0, 0
	rtspKnownReady, hlsKnownReady := false, false
	for {
		rtspCtx, cancelRTSP := context.WithTimeout(ctx, 2*time.Second)
		rtspReady := runtime.pathReady(rtspCtx)
		cancelRTSP()
		if contract.PublisherProfile == RadioPublisherProfilePlaylistGPUEvaluation {
			_ = recordPlaylistGPUTrace(playlistGPUTracePathFromEnv(), "mediamtx_path_readiness", map[string]any{
				"ready": rtspReady,
			})
		}
		hlsReady := false
		if contract.PublisherProfile != RadioPublisherProfilePlaylistGPUEvaluation {
			hlsCtx, cancelHLS := context.WithTimeout(ctx, 2*time.Second)
			hlsReady = runtime.hlsReady(hlsCtx, contract.LatencyProfile)
			cancelHLS()
		}
		if rtspReady {
			rtspFailures = 0
			rtspKnownReady = true
		} else if rtspKnownReady {
			rtspFailures++
			rtspReady = rtspFailures < lossConfirmations
		}
		if hlsReady {
			hlsFailures = 0
			hlsKnownReady = true
		} else if hlsKnownReady {
			hlsFailures++
			hlsReady = hlsFailures < lossConfirmations
		}
		rtspBecameReady, rtspLost, changed := m.updateReadinessForGeneration(generation, rtspReady, hlsReady)
		if !changed {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		if rtspBecameReady && m.cb.OnRTSPReady != nil {
			m.cb.OnRTSPReady(endpoint)
		}
		if rtspLost && m.cb.OnRTSPDone != nil {
			m.cb.OnRTSPDone(endpoint)
		}
		if m.cb.OnReadinessChanged != nil {
			m.cb.OnReadinessChanged()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *RadioManager) updateReadinessForGeneration(generation uint64, rtspReady, hlsReady bool) (rtspBecameReady, rtspLost, changed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsActiveGenerationLocked(generation) {
		return false, false, false
	}
	previousRTSP := m.status.RTSPReady
	previousHLS := m.status.HLSReady
	m.status.RTSPReady = rtspReady
	m.status.HLSReady = hlsReady
	if !rtspReady {
		m.status.RTSPURL = ""
		m.status.RTSPPublic = false
	}
	return !previousRTSP && rtspReady, previousRTSP && !rtspReady, previousRTSP != rtspReady || previousHLS != hlsReady
}

// feedFillerUntilWake broadcasts the active fallback until Wake/Skip arrives
// (or the session ends). It is called only while no track feeder is active.
func (m *RadioManager) feedFillerUntilWake(ctx context.Context, publisher radioPublisher, contract RadioActiveSessionContract, timestampOffset float64, generation uint64) (float64, *RadioError) {
	// A wake queued during the previous track means new work is already
	// waiting — skip the filler entirely.
	select {
	case <-m.wake:
		return 0, nil
	default:
	}
	for retries := 0; ; {
		fctx, cancel := context.WithCancel(ctx)
		m.mu.Lock()
		if !m.ownsActiveGenerationLocked(generation) {
			m.mu.Unlock()
			cancel()
			return 0, nil
		}
		m.fillerCancel = cancel
		m.mu.Unlock()
		go func() {
			select {
			case <-m.wake:
				cancel()
			case <-fctx.Done():
			}
		}()
		feedStarted := time.Now()
		duration, err := m.runFallbackFeeder(fctx, contract, timestampOffset, publisher.sink())
		cancel()
		m.mu.Lock()
		if m.ownsActiveGenerationLocked(generation) {
			m.fillerCancel = nil
		}
		m.mu.Unlock()
		retimed := retimeRadioFallbackDuration(duration, elapsedFeedSeconds(feedStarted))
		if ctx.Err() != nil || err == nil {
			m.setRetryCountForGeneration(generation, 0)
			return retimed, nil
		}
		if errors.Is(err, errPublisherDown) {
			radioErr := newRadioError(RadioErrorStagePublisher, err, false)
			return retimed, &radioErr
		}
		if retries == maxRadioFallbackRetries {
			radioErr := newRadioError(RadioErrorStageFallback, err, false)
			m.setRetryCountForGeneration(generation, retries)
			return retimed, &radioErr
		}
		retries++
		radioErr := newRadioError(RadioErrorStageFallback, err, true)
		m.recordRecoverableErrorForGeneration(generation, radioErr, retries)
		if !m.waitFallbackRetry(ctx, radioFallbackRetryDelay(retries)) {
			return retimed, nil
		}
	}
}

func radioFallbackRetryDelay(failures int) time.Duration {
	switch failures {
	case 1:
		return 250 * time.Millisecond
	case 2:
		return 500 * time.Millisecond
	case 3:
		return time.Second
	default:
		return 2 * time.Second
	}
}

func waitRadioFallbackRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func elapsedFeedSeconds(start time.Time) float64 {
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0.001
	}
	return elapsed
}

func retimeRadioFallbackDuration(encodedSeconds, wallSeconds float64) float64 {
	seconds := encodedSeconds
	if wallSeconds > seconds {
		seconds = wallSeconds
	}
	if seconds <= 0 {
		return 0
	}
	return math.Ceil(seconds*radioRetimingFrameRate-1e-9) / radioRetimingFrameRate
}

func (m *RadioManager) setCurrentForGeneration(generation uint64, trackID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsActiveGenerationLocked(generation) {
		return
	}
	m.status.CurrentTrackID = trackID
	if trackID == "" {
		m.status.TrackStartedAt = time.Time{}
		m.status.BaseOffsetSeconds = 0
	}
}

func (m *RadioManager) completeTrack(sessionGeneration, generation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsActiveGenerationLocked(sessionGeneration) {
		return
	}
	if m.activeTrack.Generation == generation && m.activeTrackDone != nil {
		close(m.activeTrackDone)
		m.activeTrack = TrackGeneration{}
		m.activeTrackDone = nil
	}
	m.status.CurrentTrackID = ""
	m.status.TrackStartedAt = time.Time{}
	m.status.BaseOffsetSeconds = 0
}

// CurrentTrackGeneration returns the currently active feeder completion.
// Callers must retain this value before asking the manager to skip a track.
func (m *RadioManager) CurrentTrackGeneration() TrackGeneration {
	for {
		m.mu.Lock()
		claimDone := m.claimDone
		generation := m.activeTrack
		m.mu.Unlock()
		if claimDone == nil {
			return generation
		}
		<-claimDone
	}
}

// Wake nudges an idle radio to re-query next() (e.g. after a track was added
// or playback was requested); it interrupts a running filler loop.
func (m *RadioManager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// SkipCurrent aborts the current track feed; the loop immediately asks next()
// for the following track. Used for skip, 割り込み再生, and pause (the server
// flips its own state before calling this).
func (m *RadioManager) SkipCurrent() {
	m.mu.Lock()
	cancel := m.skipPush
	if cancel != nil {
		m.skipped = true
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	} else {
		m.Wake()
	}
}

// CancelGeneration cancels only the exact feeder generation observed by the
// caller. A stale request cannot interrupt a newer track.
func (m *RadioManager) CancelGeneration(generation uint64) bool {
	m.mu.Lock()
	if generation == 0 || m.activeTrack.Generation != generation || m.skipPush == nil {
		m.mu.Unlock()
		return false
	}
	cancel := m.skipPush
	m.skipped = true
	m.mu.Unlock()
	cancel()
	return true
}

// Stop shuts the radio down and waits for the loop to exit.
func (m *RadioManager) Stop(timeout time.Duration) {
	m.mu.Lock()
	if m.starting && m.startingCancel != nil {
		cancel := m.startingCancel
		m.starting = false
		m.startingCancel = nil
		m.startingGeneration = 0
		m.mu.Unlock()
		cancel()
		return
	}
	cancel := m.cancel
	done := m.done
	if cancel == nil {
		m.mu.Unlock()
		return
	}
	m.cancel = nil
	m.done = nil
	m.activeSession = nil
	m.activeGeneration = 0
	m.status.Running = false
	m.status.Phase = RadioPhaseStopped
	m.status.StoppedAt = time.Now()
	m.status.RTSPReady = false
	m.status.HLSReady = false
	m.status.RTSPURL = ""
	m.status.RTSPPublic = false
	m.status.ActiveSession = nil
	endpoint := m.rtspPublic
	m.rtspPublic = RTSPEndpoint{}
	onStopped := m.cb.OnStopped
	onRTSPDone := m.cb.OnRTSPDone
	m.mu.Unlock()
	cancel()
	if endpoint.SessionID != "" && onRTSPDone != nil {
		go onRTSPDone(endpoint)
	}
	if onStopped != nil {
		go onStopped()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(timeout):
		}
	}
}

func (m *RadioManager) ownsActiveGeneration(generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ownsActiveGenerationLocked(generation)
}

func (m *RadioManager) ownsActiveGenerationLocked(generation uint64) bool {
	return generation != 0 && m.done != nil && m.activeGeneration == generation
}

// IsActiveSession reports whether an RTSP notification still belongs to the
// running radio session. It lets asynchronous server publication reject a
// stale endpoint without blocking a replacement Start.
func (m *RadioManager) IsActiveSession(sessionID string, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return generation != 0 && m.activeSession != nil && m.activeSession.SessionID == sessionID && m.activeGeneration == generation && m.done != nil
}

func (m *RadioManager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done != nil
}

func (m *RadioManager) Status() RadioStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status
	status.Running = m.done != nil && status.Phase != RadioPhaseFailed && status.Phase != RadioPhaseStopped
	if m.activeSession != nil {
		activeCopy := *m.activeSession
		status.ActiveSession = &activeCopy
	}
	return status
}

func (m *RadioManager) SetRTSPURL(sessionID, publicURL, message string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done == nil || !m.status.RTSPReady || m.rtspPublic.SessionID != sessionID {
		return false
	}
	m.status.RTSPURL = publicURL
	m.status.RTSPPublic = strings.HasPrefix(publicURL, "rtsp://")
	return true
}

func (m *RadioManager) SetRTSPEndpointURL(endpoint RTSPEndpoint, publicURL, message string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done == nil || !m.status.RTSPReady || m.rtspPublic.SessionID != endpoint.SessionID || m.rtspPublic.Generation != endpoint.Generation {
		return false
	}
	m.status.RTSPURL = publicURL
	m.status.RTSPPublic = strings.HasPrefix(publicURL, "rtsp://")
	return true
}

// ProxyLLHLS forwards an LL-HLS request for the radio path to the loopback
// MediaMTX HLS server. Returns false when the radio is not running.
func (m *RadioManager) ProxyLLHLS(w http.ResponseWriter, r *http.Request, name string) bool {
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	if runtime == nil {
		return false
	}
	if name == "" || name == "." || name == "/" {
		name = "index.m3u8"
	}
	runtime.proxyHLS(w, r, name)
	return true
}

// HLSReady returns the monitor's cached readiness result. It never probes.
func (m *RadioManager) HLSReady(ctx context.Context) bool {
	_ = ctx
	return m.Status().HLSReady
}

// buildMediaMTX provisions the real MediaMTX instance plus the public RTSP
// gate, mirroring the OBS RTSPT sidecar but with the low-latency HLS variant
// and the loopback RTMP ingest enabled so RTSP and LL-HLS are served
// simultaneously from the persistent publisher.
func (m *RadioManager) buildMediaMTX(ctx context.Context, contract RadioActiveSessionContract) (radioRuntime, radioGate, RTSPEndpoint, error) {
	mtxExe, err := EnsureMediaMTX(ctx)
	if err != nil {
		return nil, nil, RTSPEndpoint{}, err
	}
	ports, err := allocMediaMTXPorts()
	if err != nil {
		return nil, nil, RTSPEndpoint{}, err
	}
	user, pass, err := mediaMTXCredential()
	if err != nil {
		return nil, nil, RTSPEndpoint{}, err
	}
	id := contract.SessionID
	path := "radio" + strings.TrimPrefix(mediaMTXPathName(id), "obs")
	runtime := newMediaMTXRuntime(mtxExe, mediaMTXSessionConfig{
		Path:               path,
		PublishUser:        user,
		PublishPass:        pass,
		Ports:              ports,
		AdvertiseHost:      m.host,
		DebugLogPath:       mediaMTXDebugLogPath(),
		HLSVariant:         contract.HLSVariant,
		HLSSegmentCount:    contract.HLSSegmentCount,
		HLSSegmentDuration: contract.HLSSegmentDuration,
		EnableRTMP:         true,
	})
	if err := runtime.start(ctx); err != nil {
		return nil, nil, RTSPEndpoint{}, err
	}
	gate := newRTSPGate(rtspGateConfig{
		PublicRTSPPort:  ports.RTSP,
		PublicRTPPort:   ports.RTP,
		PublicRTCPPort:  ports.RTCP,
		BackendRTSPPort: ports.mediaMTXRTSPPort(),
		Path:            path,
	})
	if err := gate.start(ctx); err != nil {
		_ = runtime.stop(5 * time.Second)
		return nil, nil, RTSPEndpoint{}, err
	}
	endpoint := RTSPEndpoint{
		SessionID:  id,
		Host:       m.host,
		Port:       ports.RTSP,
		RTPPort:    ports.RTP,
		RTCPPort:   ports.RTCP,
		Path:       path,
		LocalURL:   runtime.rtspURL(),
		BackendURL: runtime.backendRTSPURL(),
	}
	return runtime, gate, endpoint, nil
}

func radioHLSSettings(profile LatencyProfile) (variant string, segmentCount int, segmentDuration string) {
	switch NormalizeLatencyMode(profile.Mode) {
	case LatencyModeHLSHigh:
		return "fmp4", 6, "4s"
	case LatencyModeHLS:
		return "fmp4", 8, "1s"
	default:
		return "lowLatency", 0, ""
	}
}

// --- real ffmpeg publisher / feeder -----------------------------------------

type ffmpegPublisher struct {
	cmd       *exec.Cmd
	in        io.WriteCloser
	exit      chan error
	stderr    *bytes.Buffer
	tracePath string
	cancel    context.CancelFunc
}

func (p *ffmpegPublisher) sink() io.Writer    { return p.in }
func (p *ffmpegPublisher) done() <-chan error { return p.exit }
func (p *ffmpegPublisher) close() {
	if p == nil {
		return
	}
	if p.cancel != nil {
		defer p.cancel()
	}
	_ = recordPlaylistGPUTrace(p.tracePath, "gpu_publisher_stdin_close_started", nil)
	_ = p.in.Close()
	_ = recordPlaylistGPUTrace(p.tracePath, "gpu_publisher_stdin_closed", nil)
	select {
	case <-p.exit:
	case <-time.After(3 * time.Second):
		if p.cancel != nil {
			p.cancel()
		} else if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-p.exit
	}
}

func (m *RadioManager) startFFmpegPublisher(ctx context.Context, publishURL string) (radioPublisher, error) {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, ffmpeg, video.RadioPublisherArgs(publishURL)...)
	hideWindow(cmd)
	cmd.Dir = m.outDir
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if path := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_PUBLISHER_DEBUG")); path != "" {
		message := fmt.Sprintf("started pid=%d\npublishURL=%s\nargs=%q\n", cmd.Process.Pid, sanitizeRadioErrorMessage(publishURL), sanitizeRadioArgs(video.RadioPublisherArgs(publishURL)))
		_ = os.WriteFile(path, []byte(message), 0600)
	}
	untrack := video.TrackStartedFFmpeg(cmd)
	exit := make(chan error, 1)
	go func() {
		defer untrack()
		err := cmd.Wait()
		if err != nil {
			detail := strings.TrimSpace(stderr.String())
			if len(detail) > 800 {
				detail = detail[len(detail)-800:]
			}
			if detail != "" {
				err = fmt.Errorf("publisher FFmpeg: %w: %s", err, detail)
			}
		}
		if path := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_PUBLISHER_DEBUG")); path != "" {
			message := fmt.Sprintf("publishURL=%s\nexit=%v\nstderr=%s\n", sanitizeRadioErrorMessage(publishURL), err, stderr.String())
			f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
			if openErr == nil {
				_, _ = f.WriteString(message)
				_ = f.Close()
			}
		}
		exit <- err
		close(exit)
	}()
	return &ffmpegPublisher{cmd: cmd, in: stdin, exit: exit, stderr: stderr}, nil
}

func sanitizeRadioArgs(args []string) []string {
	clean := make([]string, len(args))
	for i, arg := range args {
		clean[i] = sanitizeRadioErrorMessage(arg)
	}
	return clean
}

func (m *RadioManager) runFFmpegFeeder(ctx context.Context, mediaPath string, startSeconds int, loop bool, timestampOffset float64, sink io.Writer) error {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, ffmpeg, video.RadioFeederArgs(mediaPath, startSeconds, loop, timestampOffset)...)
	hideWindow(cmd)
	cmd.Dir = m.outDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	untrack := video.TrackStartedFFmpeg(cmd)
	defer untrack()
	_, copyErr := io.Copy(sink, stdout)
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil // skipped / paused / session shutdown
	}
	if copyErr != nil {
		return fmt.Errorf("%w: %v", errPublisherDown, copyErr)
	}
	if waitErr != nil {
		detail := stderr.String()
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		return fmt.Errorf("feeder %s: %w: %s", mediaPath, waitErr, detail)
	}
	return nil
}

func (m *RadioManager) runFFmpegFallbackFeeder(ctx context.Context, contract RadioActiveSessionContract, timestampOffset float64, sink io.Writer) (float64, error) {
	preset := contract.FallbackPreset
	return NewRadioFallbackFeeder(m.outDir, func() video.QualityPreset { return preset }).Run(ctx, timestampOffset, sink)
}

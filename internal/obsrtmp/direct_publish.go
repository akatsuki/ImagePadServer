package obsrtmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

// DirectPublishInfo is the handoff from the OBS/MediaMTX manager to an
// AirPlay publisher. The URL is a credentialed loopback RTSP publish target;
// readers continue to use the normal public MediaMTX URLs.
type DirectPublishInfo struct {
	PublishURL            string
	Session               Session
	Handle                DirectSessionHandle
	Plan                  DirectDeliveryPlan
	Output                DirectOutputSettings
	PublisherArtifactRoot string
	PublisherObserver     airplaycontract.PublisherObserver
}

// DirectDeliveryPlan is the immutable output contract for one AirPlay direct
// session. Later delivery generations must copy this value or create a new
// plan; they must not re-read mutable application settings.
type DirectDeliveryPlan struct {
	Profile              LatencyProfile
	Output               DirectOutputSettings
	SessionID            string
	RequestID            string
	RequestedQualityMode string
	EffectiveHeight      int

	qualityPreset video.QualityPreset
}

// DirectOutputSettings is the immutable AirPlay encoder snapshot taken when a
// direct session starts. It keeps UxPlay's source clock independent from the
// delivery cadence selected in the ImagePadServer latency profile.
type DirectOutputSettings struct {
	Width            int
	Height           int
	SourceFPS        int
	OutputFPS        int
	VideoBitrateKbps int
	MaxRateKbps      int
	BufferSizeKbps   int
	AudioBitrateBps  int
	GOPFrames        int
}

// DirectSessionHandle prevents a late AirPlay cleanup callback from stopping
// a newer OBS or AirPlay session that reused the same Manager.
type DirectSessionHandle struct {
	ID         string
	Generation uint64
}

type directReadinessProber interface {
	pathReady(context.Context) bool
	hlsReady(context.Context, LatencyProfile) bool
}

const mediaMTXMinimumLowLatencyHLSSegments = 7
const directRecordingOutcomeSessionLimit = 40

// AirPlay screen sharing is a high-motion source and is re-encoded before it
// reaches MediaMTX. The ordinary 720p upload preset (2.5 Mbps) is tuned for
// more compressible camera/video content and produces visible blocks while
// scrolling or animating a phone screen. Keep this constrained-quality ceiling
// local to the AirPlay direct path so ordinary uploads retain their existing
// bandwidth contract.
const (
	airPlay720VideoBitrateKbps = 5000
	airPlay720MaxRateKbps      = 5000
	airPlay720BufferSizeKbps   = 5000
)

// StartDirectPublishing creates the MediaMTX sidecar before the AirPlay
// process starts. This removes the old dependency on an RTMP connection to
// discover and create the session.
func (m *Manager) StartDirectPublishing(parent context.Context) (DirectPublishInfo, error) {
	return m.startDirectPublishing(parent, false, nil)
}

// StartDirectPublishingWithPublisherArtifacts creates the source-clock-only
// generation registry. The ordinary direct path keeps its legacy file layout.
func (m *Manager) StartDirectPublishingWithPublisherArtifacts(parent context.Context) (DirectPublishInfo, error) {
	return m.startDirectPublishing(parent, true, nil)
}

// NewDirectDeliveryPlan captures a caller-resolved latency/quality snapshot
// before any external process starts.
func (m *Manager) NewDirectDeliveryPlan(profile LatencyProfile, requestedQualityMode string, requested video.QualityPreset) (DirectDeliveryPlan, error) {
	return newDirectDeliveryPlan(sessionID(), sessionID(), profile, requestedQualityMode, requested)
}

// StartDirectPublishingWithPlan preserves the legacy direct artifact layout
// while consuming a caller-captured immutable delivery plan.
func (m *Manager) StartDirectPublishingWithPlan(parent context.Context, plan DirectDeliveryPlan) (DirectPublishInfo, error) {
	return m.startDirectPublishing(parent, false, &plan)
}

// StartDirectPublishingWithPublisherArtifactsPlan starts generation 1 with a
// caller-captured immutable delivery plan.
func (m *Manager) StartDirectPublishingWithPublisherArtifactsPlan(parent context.Context, plan DirectDeliveryPlan) (DirectPublishInfo, error) {
	return m.startDirectPublishing(parent, true, &plan)
}

// Explicit managed startup is kept separate until server routing and managed
// recovery are both connected. It must never silently use the legacy owner.
func (m *Manager) StartManagedDirectPublishingWithPlan(parent context.Context, plan DirectDeliveryPlan) (DirectPublishInfo, error) {
	return m.startDirectPublishingMode(parent, true, &plan, true)
}

func (m *Manager) startDirectPublishing(parent context.Context, generationArtifacts bool, requestedPlan *DirectDeliveryPlan) (DirectPublishInfo, error) {
	return m.startDirectPublishingMode(parent, generationArtifacts, requestedPlan, false)
}

func (m *Manager) startDirectPublishingMode(parent context.Context, generationArtifacts bool, requestedPlan *DirectDeliveryPlan, managed bool) (DirectPublishInfo, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.reapDirectBackendCleanup()
	if parent == nil {
		parent = context.Background()
	}
	m.mu.Lock()
	if m.running || m.current != nil {
		m.mu.Unlock()
		return DirectPublishInfo{}, fmt.Errorf("direct OBS publishing is already running")
	}
	if m.directBackendMonitorCount != 0 || len(m.directRetiringBackends) != 0 || len(m.directBackendReservations) != 0 {
		m.mu.Unlock()
		return DirectPublishInfo{}, fmt.Errorf("direct backend cleanup is not confirmed")
	}
	var reservedEpoch uint64
	if managed {
		if !generationArtifacts || m.listenerGeneration == ^uint64(0) {
			m.mu.Unlock()
			return DirectPublishInfo{}, errDirectReconfigureUnavailable
		}
		m.listenerGeneration++ // failed startup also burns its epoch
		reservedEpoch = m.listenerGeneration
	}
	m.mu.Unlock()
	plan := DirectDeliveryPlan{}
	var err error
	if requestedPlan == nil {
		preset := m.currentPreset()
		plan, err = newDirectDeliveryPlan(sessionID(), sessionID(), m.currentLatency(), preset.Mode, preset)
	} else {
		plan = *requestedPlan
		err = validateDirectDeliveryPlan(plan)
	}
	if err != nil {
		return DirectPublishInfo{}, err
	}

	mtxExe, err := EnsureMediaMTX(parent)
	if err != nil {
		return DirectPublishInfo{}, err
	}
	ports, err := allocMediaMTXPorts()
	if err != nil {
		return DirectPublishInfo{}, err
	}
	user, pass, err := mediaMTXCredential()
	if err != nil {
		return DirectPublishInfo{}, err
	}
	baseDir := m.outDir
	if baseDir == "" {
		baseDir = filepath.Join(settings.Dir(), "media")
	}
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return DirectPublishInfo{}, err
	}
	profile := plan.Profile
	preset := plan.qualityPreset
	output := plan.Output
	id := plan.SessionID
	pathName := mediaMTXPathName(id)
	recording := filepath.Join(baseDir, "obs-recording-"+id+".mp4")
	var publisherObserver *directPublisherObserver
	publisherArtifactRoot := ""
	if generationArtifacts {
		publisherObserver, err = newDirectPublisherObserver(baseDir, id)
		if err != nil {
			return DirectPublishInfo{}, err
		}
		ffprobePath, ffprobeErr := video.ExistingFFprobePath()
		if err := configureDirectRecordingProbeForSession(publisherObserver, output, ffprobePath, ffprobeErr); err != nil {
			_ = publisherObserver.Close()
			return DirectPublishInfo{}, err
		}
		if err := publisherObserver.configureRecordingOutcomeHandler(m.notifyDirectRecordingOutcome); err != nil {
			_ = publisherObserver.Close()
			return DirectPublishInfo{}, err
		}
		if err := publisherObserver.configureRecordingOutcomesDoneHandler(m.notifyDirectRecordingOutcomesDone); err != nil {
			_ = publisherObserver.Close()
			return DirectPublishInfo{}, err
		}
		publisherArtifactRoot = publisherObserver.root
		recording = airplaycontract.NewPublisherArtifacts(publisherArtifactRoot, id, 1).Recording
	}
	runtime := newMediaMTXRuntime(mtxExe, directMediaMTXConfig(
		pathName,
		user,
		pass,
		ports,
		m.host,
		mediaMTXDebugLogPath(),
		profile,
	))
	if err := m.startOwnedDirectBackend(parent, runtime); err != nil {
		if publisherObserver != nil {
			_ = publisherObserver.Close()
		}
		return DirectPublishInfo{}, err
	}
	ctx, cancel := context.WithCancel(parent)
	monitorOwnsContext := false
	defer func() {
		if !monitorOwnsContext {
			cancel()
		}
	}()
	gate := newRTSPGate(rtspGateConfig{
		PublicRTSPPort:  ports.RTSP,
		PublicRTPPort:   ports.RTP,
		PublicRTCPPort:  ports.RTCP,
		BackendRTSPPort: ports.mediaMTXRTSPPort(),
		Path:            pathName,
		Diagnostics:     newRTSPDiagnosticsFromEnvironment(),
	})
	var delivery *directDeliveryCoordinator
	if managed {
		delivery, err = newInitialDirectDeliveryCoordinator(ctx, m, gate, runtime, publisherObserver, DirectSessionHandle{ID: id, Generation: reservedEpoch}, plan)
		if err != nil {
			cleanupErr := m.stopOwnedDirectBackend(runtime, 5*time.Second)
			_ = publisherObserver.Close()
			return DirectPublishInfo{}, errors.Join(err, cleanupErr)
		}
	}
	if err := gate.start(ctx); err != nil {
		cleanupErr := m.stopOwnedDirectBackend(runtime, 5*time.Second)
		if publisherObserver != nil {
			_ = publisherObserver.Close()
		}
		return DirectPublishInfo{}, errors.Join(err, cleanupErr)
	}

	contract := OBSActiveSessionContract{
		SessionID: id,
		IngestURL: runtime.directPublishURL(),
		StreamKey: pathName,
		Port:      ports.mediaMTXRTSPPort(),
		// The direct bridge always publishes RTSP/TCP, even when the OBS
		// preference is HLS. Keep the active contract truthful so URL routing,
		// HLS proxying, and RTSP publication all take the direct path.
		LatencyProfile:      profile,
		QualityPreset:       preset,
		VideoEncoderProfile: video.VideoEncoderProfile{Name: "gstreamer-direct", Purpose: video.EncoderLowLatency},
	}
	session := Session{
		ID:                            id,
		Title:                         "AirPlay " + time.Now().Format("2006-01-02 15:04:05"),
		PlaylistName:                  video.PlaylistName(id),
		Recording:                     recording,
		RecordingVerificationRequired: generationArtifacts,
		Published:                     true,
		ActiveContract:                &contract,
	}
	done := make(chan struct{})
	m.mu.Lock()
	if m.running || m.current != nil {
		m.mu.Unlock()
		cancel()
		_ = gate.stop()
		cleanupErr := m.stopOwnedDirectBackend(runtime, 5*time.Second)
		if publisherObserver != nil {
			_ = publisherObserver.Close()
		}
		return DirectPublishInfo{}, errors.Join(fmt.Errorf("direct OBS publishing became busy"), cleanupErr)
	}
	if !managed {
		m.listenerGeneration++
	}
	generation := m.listenerGeneration
	m.running = true
	m.stop = cancel
	m.done = done
	m.directPublishing = true
	m.directHandle = DirectSessionHandle{ID: id, Generation: generation}
	m.directDelivery = delivery
	m.mtx = runtime
	m.rtspGate = gate
	m.status.Enabled = true
	m.status.Listening = true
	m.status.Connected = false
	m.status.MediaID = ""
	m.status.Publishing = true
	m.status.Latency = contract.LatencyProfile
	m.status.Message = "AirPlay direct RTSP publisher is waiting for GStreamer input."
	m.status.EncoderName = contract.VideoEncoderProfile.Name
	m.status.HardwareEncode = false
	// Reserve ownership before releasing Manager.mu or starting the supervisor;
	// Stop() may revoke running immediately, but a new start must still wait.
	m.directBackendMonitorCount++
	delete(m.directBackendReservations, runtime) // transfer the confirmed startup owner to its monitor
	m.mu.Unlock()
	initialRoute := directBackendRoute{sessionID: id, sessionEpoch: generation, generation: 1, requestID: "legacy-initial", runtime: runtime}
	if managed {
		initialRoute = gate.backendRouter.snapshot()
	}
	initialWatch := monitorDirectBackend(ctx, initialRoute, publisherObserver, !managed)
	monitorOwnsContext = true
	go m.monitorDirectPublishing(ctx, cancel, generation, session, runtime, gate, baseDir, done, publisherObserver, initialWatch)
	return DirectPublishInfo{
		PublishURL:            runtime.directPublishURL(),
		Session:               session,
		Handle:                DirectSessionHandle{ID: id, Generation: generation},
		Plan:                  plan,
		Output:                output,
		PublisherArtifactRoot: publisherArtifactRoot,
		PublisherObserver:     publisherObserver,
	}, nil
}

func newDirectDeliveryPlan(sessionIDValue, requestID string, profile LatencyProfile, requestedQualityMode string, requested video.QualityPreset) (DirectDeliveryPlan, error) {
	requestedQualityMode = strings.ToLower(strings.TrimSpace(requestedQualityMode))
	switch requestedQualityMode {
	case "auto", "360", "720", "1080":
	default:
		return DirectDeliveryPlan{}, fmt.Errorf("invalid AirPlay quality mode %q", requestedQualityMode)
	}
	profile = directLatencyProfile(profile)
	effectivePreset, err := directAirPlayQualityPreset(profile, requested)
	if err != nil {
		return DirectDeliveryPlan{}, err
	}
	output := directOutputSettingsFromPreset(profile, effectivePreset)
	plan := DirectDeliveryPlan{
		Profile:              profile,
		Output:               output,
		SessionID:            strings.TrimSpace(sessionIDValue),
		RequestID:            strings.TrimSpace(requestID),
		RequestedQualityMode: requestedQualityMode,
		EffectiveHeight:      output.Height,
		qualityPreset:        effectivePreset,
	}
	if err := validateDirectDeliveryPlan(plan); err != nil {
		return DirectDeliveryPlan{}, err
	}
	return plan, nil
}

// ResolveDirectAirPlayOutput applies the AirPlay-specific profile ceiling and
// bitrate rules without allocating session identity or starting a process.
func ResolveDirectAirPlayOutput(profile LatencyProfile, requested video.QualityPreset) (DirectOutputSettings, error) {
	effectivePreset, err := directAirPlayQualityPreset(profile, requested)
	if err != nil {
		return DirectOutputSettings{}, err
	}
	return directOutputSettingsFromPreset(profile, effectivePreset), nil
}

func validateDirectDeliveryPlan(plan DirectDeliveryPlan) error {
	if !validDirectPublisherSessionID(plan.SessionID) {
		return fmt.Errorf("AirPlay direct delivery plan session ID is invalid")
	}
	if strings.TrimSpace(plan.RequestID) == "" {
		return fmt.Errorf("AirPlay direct delivery plan request ID is empty")
	}
	switch plan.RequestedQualityMode {
	case "auto", "360", "720", "1080":
	default:
		return fmt.Errorf("AirPlay direct delivery plan quality mode is invalid")
	}
	if plan.Profile != directLatencyProfile(plan.Profile) {
		return fmt.Errorf("AirPlay direct delivery plan profile is invalid")
	}
	if plan.Output.Width < 2 || plan.Output.Height < 2 || plan.EffectiveHeight != plan.Output.Height {
		return fmt.Errorf("AirPlay direct delivery plan output is invalid")
	}
	if plan.qualityPreset.Height != plan.Output.Height || plan.Output != directOutputSettingsFromPreset(plan.Profile, plan.qualityPreset) {
		return fmt.Errorf("AirPlay direct delivery plan preset does not match output")
	}
	return nil
}

func directAirPlayQualityPreset(profile LatencyProfile, requested video.QualityPreset) (video.QualityPreset, error) {
	profile = directLatencyProfile(profile)
	if requested.Height != 360 && requested.Height != 720 && requested.Height != 1080 {
		return video.QualityPreset{}, fmt.Errorf("AirPlay quality height %d is unsupported", requested.Height)
	}
	maxHeight := 1080
	if profile.BitrateMultiplier == 0 {
		maxHeight = 720
	}
	effective := requested
	if effective.Height > maxHeight {
		effective = video.ResolveQuality(strconv.Itoa(maxHeight), 0)
		effective.Mode = requested.Mode
		effective.NetworkMbps = requested.NetworkMbps
		effective.UploadMbps = requested.UploadMbps
	}
	if effective.Height == 720 && profile.BitrateMultiplier > 0 && directBitrateKbps(effective.VideoBitrate) < airPlay720VideoBitrateKbps {
		effective.VideoBitrate = strconv.Itoa(airPlay720VideoBitrateKbps) + "k"
		effective.MaxRate = strconv.Itoa(airPlay720MaxRateKbps) + "k"
		effective.BufferSize = strconv.Itoa(airPlay720BufferSizeKbps) + "k"
	}
	if profile.BitrateMultiplier > 1 {
		effective = scaledLatencyPreset(effective, profile.BitrateMultiplier)
	}
	return effective, nil
}

// DirectPublishing reports whether this Manager currently owns a native
// GStreamer-to-MediaMTX session.
func (m *Manager) DirectPublishing() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.directPublishing
}

// StopCurrentDirect stops the direct session currently owned by this manager.
// It is used by cleanup paths that no longer have the original AirPlay
// callback handle, for example after the AirPlay receiver exits first.
func (m *Manager) StopCurrentDirect(timeout time.Duration) bool {
	m.mu.Lock()
	if !m.directPublishing {
		m.mu.Unlock()
		return true
	}
	handle := m.directHandle
	m.mu.Unlock()
	return m.StopDirect(handle, timeout)
}

// StopDirect stops only the exact direct session returned by
// StartDirectPublishing. A stale callback is deliberately a no-op.
func (m *Manager) StopDirect(handle DirectSessionHandle, timeout time.Duration) bool {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	if !m.directPublishing || handle.ID == "" || handle.Generation == 0 ||
		handle != m.directHandle || handle.Generation != m.listenerGeneration {
		m.mu.Unlock()
		return false
	}
	cancel := m.stop
	done := m.done
	m.mu.Unlock()
	if cancel == nil || done == nil {
		return true
	}
	cancel()
	if timeout <= 0 {
		<-done
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (m *Manager) monitorDirectPublishing(ctx context.Context, stop context.CancelFunc, generation uint64, session Session, runtime *mediaMTXRuntime, gate *rtspGate, outDir string, done chan struct{}, publisherObserver *directPublisherObserver, initial ...*directBackendMonitor) {
	defer close(done)
	defer video.EndExternalHLS(outDir, done)
	watchCtx, stopWatches := context.WithCancel(ctx)
	watches := make(map[directBackendRoute]*directBackendMonitor)
	defer func() {
		stopWatches()
		_ = m.cleanupDirectBackendReservations(5 * time.Second)
		m.finishDirectBackendMonitors(watches)
	}()
	defer func() { _ = gate.stop() }()
	router := gate.backendRouter
	strict := router != nil && router.snapshot().sessionID != "legacy-rtsp-gate"
	route := directBackendRoute{sessionID: session.ID, sessionEpoch: generation, generation: 1, requestID: "legacy-initial", runtime: runtime}
	if strict {
		route = router.snapshot()
		runtime = route.runtime
	}
	var watch *directBackendMonitor
	var watchErr error
	if len(initial) == 1 && initial[0] != nil {
		watch = initial[0]
	} else {
		watch, watchErr = m.watchDirectBackend(watchCtx, route, publisherObserver, !strict)
	}
	var runtimeDone <-chan directBackendExit
	if watchErr != nil {
		if strict {
			m.observeDirectBackendExit(router, directBackendExit{route: route, err: watchErr})
		} else {
			m.finishDirectPublishing(generation, session, false, watchErr.Error())
			return
		}
	} else {
		watches[route] = watch
		runtimeDone = watch.exit
	}
	if publisherObserver == nil {
		eventPaths, err := airplaycontract.FixedPathsForRecording(session.Recording)
		if err != nil {
			m.finishDirectPublishing(generation, session, false, fmt.Sprintf("AirPlay direct event paths are invalid: %v", err))
			return
		}
		defer cleanupDirectEventSnapshots(eventPaths)
	}
	connected := false
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if strict {
			next := router.snapshot()
			if next != route && next.sessionID == session.ID && next.sessionEpoch == generation && next.runtime != nil {
				// Commit has removed every public connection to the old route.
				// Its generation monitor can now retire the healthy old runtime;
				// retaining it until session stop leaks a MediaMTX per change.
				if previous := watches[route]; previous != nil {
					previous.cancel()
				}
				route, runtime = next, next.runtime
				watch = watches[route]
				if watch == nil {
					watch, watchErr = m.watchDirectBackend(watchCtx, route, publisherObserver, false)
					if watchErr != nil {
						m.observeDirectBackendExit(router, directBackendExit{route: route, err: watchErr})
						runtimeDone = nil
						continue
					}
					watches[route] = watch
				}
				runtimeDone = watch.exit
			}
		}
		select {
		case <-ctx.Done():
			m.finishDirectPublishing(generation, session, connected, "AirPlay direct RTSP publisher stopped")
			return
		case event := <-runtimeDone:
			runtimeDone = nil
			if strict {
				m.observeDirectBackendExit(router, event)
				continue
			}
			message := "AirPlay direct RTSP publisher stopped"
			if event.err != nil {
				message = fmt.Sprintf("AirPlay direct RTSP publisher exited: %v", event.err)
			}
			m.finishDirectPublishing(generation, session, connected, message)
			return
		case <-ticker.C:
			if strict && (!router.canAccept() || router.snapshot() != route) {
				continue
			}
			mediaReady := publisherObserver == nil
			currentRecording := session.Recording
			var currentArtifacts airplaycontract.PublisherArtifacts
			var hasCurrentArtifacts bool
			if publisherObserver != nil {
				artifacts, ok := publisherObserver.currentArtifacts()
				if !ok {
					continue
				}
				currentArtifacts = artifacts
				if strict && artifacts.Generation != route.generation {
					continue
				}
				hasCurrentArtifacts = true
				mediaReady = directMediaArrivedForArtifacts(artifacts)
				currentRecording = artifacts.Recording
			}
			if connected && mediaReady && hasCurrentArtifacts {
				if strict {
					if m.claimDirectRecordingForBackend(ctx, router, route, publisherObserver, currentArtifacts) {
						session.Recording = currentRecording
					}
				} else if publisherObserver.claimCurrentRecordingCandidate(currentArtifacts) {
					session.Recording = currentRecording
					m.updateDirectRecordingCandidate(generation, session.ID, currentRecording)
				}
			}
			profile := directLatencyProfile(LatencyProfile{})
			if session.ActiveContract != nil {
				profile = session.ActiveContract.LatencyProfile
			}
			if directReadinessProbe(ctx, connected, runtime, profile, mediaReady) {
				if strict {
					session.Recording = currentRecording
					connected = m.publishDirectBackendSession(ctx, route, &session, router, outDir, stop, done, publisherObserver, currentArtifacts)
					continue
				}
				if publisherObserver != nil && !publisherObserver.claimCurrentForPublication(currentArtifacts) {
					continue
				}
				preset := video.QualityPreset{}
				if session.ActiveContract != nil {
					preset = session.ActiveContract.QualityPreset
				}
				session.Recording = currentRecording
				video.BeginExternalHLS(outDir, session.ID, preset, stop, done)
				session.StartedAt = time.Now()
				_, connected = m.acceptSession(&session, generation)
				if connected {
					m.setRTSPEndpoint(directRTSPEndpoint(runtime, session.ID))
				} else {
					video.EndExternalHLS(outDir, done)
				}
			}
		}
	}
}

func (m *Manager) updateDirectRecordingCandidate(listenerGeneration uint64, sessionID, recording string) {
	if strings.TrimSpace(recording) == "" {
		return
	}
	m.mu.Lock()
	if m.directPublishing && m.listenerGeneration == listenerGeneration && m.current != nil && m.current.ID == sessionID {
		m.current.Recording = recording
	}
	m.mu.Unlock()
}

func directReadinessProbe(ctx context.Context, connected bool, prober directReadinessProber, profile LatencyProfile, mediaReady bool) bool {
	if connected || prober == nil || !mediaReady {
		return false
	}
	profile = directLatencyProfile(profile)
	checkCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	pathReady := prober.pathReady(checkCtx)
	cancel()
	if !pathReady {
		return false
	}
	if profile.Transport != LatencyModeHLS {
		return pathReady && mediaReady
	}
	hlsCtx, hlsCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	hlsReady := prober.hlsReady(hlsCtx, profile)
	hlsCancel()
	return directSessionReady(pathReady, hlsReady, mediaReady)
}

func directSessionReady(pathReady, hlsReady, mediaReady bool) bool {
	return pathReady && hlsReady && mediaReady
}

func directMediaArrived(recording, sessionID string) bool {
	paths, err := airplaycontract.FixedPathsForRecording(recording)
	if err != nil {
		return false
	}
	ready, ok := readDirectEventSnapshot(paths.Ready)
	if !ok || !ready.ReadyFor(sessionID, 1) {
		return false
	}
	mediaReady, ok := readDirectEventSnapshot(paths.MediaReady)
	return ok && mediaReady.MediaReadyFor(sessionID, 1)
}

func directMediaArrivedForArtifacts(artifacts airplaycontract.PublisherArtifacts) bool {
	if artifacts.SessionID == "" || artifacts.Generation == 0 {
		return false
	}
	ready, ok := readDirectEventSnapshot(artifacts.Ready)
	if !ok || !ready.ReadyFor(artifacts.SessionID, artifacts.Generation) {
		return false
	}
	mediaReady, ok := readDirectEventSnapshot(artifacts.MediaReady)
	return ok && mediaReady.MediaReadyFor(artifacts.SessionID, artifacts.Generation)
}

func readDirectEventSnapshot(path string) (airplaycontract.Event, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return airplaycontract.Event{}, false
	}
	var event airplaycontract.Event
	if err := json.Unmarshal(data, &event); err != nil {
		return airplaycontract.Event{}, false
	}
	return event, true
}

func cleanupDirectEventSnapshots(paths airplaycontract.FixedPaths) {
	for _, path := range []string{paths.Ready, paths.MediaReady, paths.EventLog} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func cleanupDirectPublisherEventSnapshots(artifacts airplaycontract.PublisherArtifacts) {
	for _, path := range []string{artifacts.Ready, artifacts.MediaReady, artifacts.EventLog} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func directMediaMTXConfig(path, user, pass string, ports mediaMTXPorts, advertiseHost, debugLogPath string, profile LatencyProfile) mediaMTXSessionConfig {
	profile = directLatencyProfile(profile)
	variant := "lowLatency"
	if profile.Transport == LatencyModeHLS {
		variant = "fmp4"
	}
	segmentCount, _ := strconv.Atoi(strings.TrimSpace(profile.ListSize))
	if variant == "lowLatency" && segmentCount < mediaMTXMinimumLowLatencyHLSSegments {
		segmentCount = mediaMTXMinimumLowLatencyHLSSegments
	}
	segmentDuration := strings.TrimSpace(profile.SegmentSeconds)
	if segmentDuration != "" {
		segmentDuration += "s"
	}
	return mediaMTXSessionConfig{
		Path:               path,
		PublishUser:        user,
		PublishPass:        pass,
		Ports:              ports,
		AdvertiseHost:      advertiseHost,
		DebugLogPath:       debugLogPath,
		HLSVariant:         variant,
		HLSAlwaysRemux:     true,
		HLSSegmentCount:    segmentCount,
		HLSSegmentDuration: segmentDuration,
		UDPReadBufferSize:  64 * 1024 * 1024,
		EnableRTMP:         false,
		RTSPTCPOnly:        false,
	}
}

func directLatencyProfile(profile LatencyProfile) LatencyProfile {
	return NormalizeLatencyProfile(profile.Mode)
}

func directOutputSettings(profile LatencyProfile, preset video.QualityPreset) DirectOutputSettings {
	profile = directLatencyProfile(profile)
	preset = scaledLatencyPreset(preset, profile.BitrateMultiplier)
	return directOutputSettingsFromPreset(profile, preset)
}

func directOutputSettingsFromPreset(profile LatencyProfile, preset video.QualityPreset) DirectOutputSettings {
	profile = directLatencyProfile(profile)
	height := preset.Height
	if height < 2 {
		height = 720
	}
	width := (height*16 + 8) / 9
	if width%2 != 0 {
		width++
	}
	if width > 1920 {
		width = 1920
	}
	outputFPS, err := strconv.Atoi(strings.TrimSpace(profile.FrameRate))
	if err != nil || outputFPS < 1 {
		outputFPS = 30
	}
	gop, err := strconv.Atoi(strings.TrimSpace(profile.GOPFrames))
	if err != nil || gop < 1 {
		gop = outputFPS
	}
	return DirectOutputSettings{
		Width:            width,
		Height:           height,
		SourceFPS:        60,
		OutputFPS:        outputFPS,
		VideoBitrateKbps: directBitrateKbps(preset.VideoBitrate),
		MaxRateKbps:      directBitrateKbps(preset.MaxRate),
		BufferSizeKbps:   directBitrateKbps(preset.BufferSize),
		AudioBitrateBps:  directBitrateKbps(preset.AudioBitrate) * 1000,
		GOPFrames:        gop,
	}
}

func directBitrateKbps(value string) int {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return 0
	}
	multiplier := 1.0
	switch {
	case strings.HasSuffix(value, "m"):
		multiplier = 1000
		value = strings.TrimSuffix(value, "m")
	case strings.HasSuffix(value, "k"):
		value = strings.TrimSuffix(value, "k")
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return 0
	}
	return int(number*multiplier + 0.5)
}

func directRTSPEndpoint(runtime *mediaMTXRuntime, sessionID string) RTSPEndpoint {
	if runtime == nil {
		return RTSPEndpoint{SessionID: sessionID}
	}
	return RTSPEndpoint{
		SessionID:  sessionID,
		Host:       runtime.cfg.AdvertiseHost,
		Port:       runtime.cfg.Ports.RTSP,
		RTPPort:    runtime.cfg.Ports.RTP,
		RTCPPort:   runtime.cfg.Ports.RTCP,
		Path:       runtime.cfg.Path,
		LocalURL:   runtime.rtspURL(),
		BackendURL: runtime.backendRTSPURL(),
	}
}

func (m *Manager) finishDirectPublishing(generation uint64, session Session, connected bool, message string) {
	m.mu.Lock()
	if generation != m.listenerGeneration {
		m.mu.Unlock()
		return
	}
	// The supervisor's launch snapshot may predate one or more committed
	// delivery changes. Finalization belongs to the last committed public
	// session, never the initial generation's recording or quality contract.
	if m.rtspGate != nil && m.rtspGate.backendRouter != nil && m.current != nil && m.current.ID == session.ID {
		route := m.rtspGate.backendRouter.snapshot()
		if route.sessionID == session.ID && route.sessionEpoch == generation {
			session = *m.current
		}
	}
	m.running = false
	m.stop = nil
	m.done = nil
	m.directPublishing = false
	m.directHandle = DirectSessionHandle{}
	if owner := m.directDelivery; owner != nil {
		_ = owner.ledger.Terminate(session.ID, generation)
		m.directDelivery = nil
	}
	m.mtx = nil
	m.status.Listening = false
	m.status.Connected = false
	m.status.MediaID = ""
	m.status.RTSPTURL = ""
	m.status.Publishing = false
	m.status.Message = message
	m.current = nil
	m.mu.Unlock()
	if connected {
		session.FinishedAt = time.Now()
	}
	m.notifyDirectDone(session, connected)
}

func (m *Manager) notifyDirectDone(session Session, connected bool) {
	if !connected || m.cb.OnDone == nil {
		return
	}
	m.cb.OnDone(session)
}

func (m *Manager) notifyDirectRecordingOutcome(outcome airplaycontract.RecordingOutcome) {
	if m == nil || outcome.Artifacts.SessionID == "" || outcome.Artifacts.Generation == 0 || outcome.Artifacts.Recording == "" {
		return
	}
	m.mu.Lock()
	if m.directRecordingOutcomes == nil {
		m.directRecordingOutcomes = make(map[string]map[uint64]airplaycontract.RecordingOutcome)
	}
	sessionOutcomes := m.directRecordingOutcomes[outcome.Artifacts.SessionID]
	if sessionOutcomes == nil {
		sessionOutcomes = make(map[uint64]airplaycontract.RecordingOutcome)
		m.directRecordingOutcomes[outcome.Artifacts.SessionID] = sessionOutcomes
		m.directRecordingSessions = append(m.directRecordingSessions, outcome.Artifacts.SessionID)
		for len(m.directRecordingSessions) > directRecordingOutcomeSessionLimit {
			oldest := m.directRecordingSessions[0]
			m.directRecordingSessions = m.directRecordingSessions[1:]
			delete(m.directRecordingOutcomes, oldest)
		}
	}
	if _, exists := sessionOutcomes[outcome.Artifacts.Generation]; exists {
		m.mu.Unlock()
		return
	}
	sessionOutcomes[outcome.Artifacts.Generation] = outcome
	callback := m.cb.OnRecordingDone
	m.mu.Unlock()
	if callback != nil {
		callback(outcome)
	}
}

func (m *Manager) notifyDirectRecordingOutcomesDone(sessionID string) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	m.mu.Lock()
	callback := m.cb.OnRecordingOutcomesDone
	m.mu.Unlock()
	if callback != nil {
		callback(sessionID)
	}
}

// DirectRecordingOutcomes returns an immutable generation-ordered snapshot
// for one AirPlay session. Failed outcomes remain visible for diagnostics.
func (m *Manager) DirectRecordingOutcomes(sessionID string) []airplaycontract.RecordingOutcome {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	byGeneration := m.directRecordingOutcomes[sessionID]
	if len(byGeneration) == 0 {
		return nil
	}
	result := make([]airplaycontract.RecordingOutcome, 0, len(byGeneration))
	for _, outcome := range byGeneration {
		result = append(result, outcome)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Artifacts.Generation < result[right].Artifacts.Generation
	})
	return result
}

// DirectRepresentativeRecording chooses only the latest fully verified
// generation. An existing file or a close event alone is never sufficient.
func (m *Manager) DirectRepresentativeRecording(sessionID string) (string, bool) {
	outcomes := m.DirectRecordingOutcomes(sessionID)
	if len(outcomes) == 0 {
		return "", false
	}
	outcome := outcomes[len(outcomes)-1]
	if outcome.ProbeOK && outcome.Closed && outcome.HasRealVideo && outcome.DurationNS > 0 && outcome.Reason == recordingReasonVerified {
		return outcome.Artifacts.Recording, true
	}
	return "", false
}

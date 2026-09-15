package airplay

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

const (
	envFeatureFlag       = "IMAGEPAD_AIRPLAY"
	envUxPlayPath        = "IMAGEPAD_UXPLAY"
	envReceiverPath      = "IMAGEPAD_AIRPLAY_RECEIVER"
	envDiagnosticName    = "IMAGEPAD_AIRPLAY_DIAGNOSTIC_RECEIVER_NAME"
	defaultReceiverTitle = "ImagePadServer-AirPlay"
)

var (
	ErrDisabled                 = errors.New("airplay feature is disabled")
	ErrAlreadyRunning           = errors.New("airplay is already running")
	ErrDeliveryRetryUnavailable = errors.New("airplay delivery retry is unavailable")
	ErrDeliveryRetryPending     = errors.New("airplay delivery retry is already pending")
)

type Status struct {
	Enabled           bool   `json:"enabled"`
	Available         bool   `json:"available"`
	Running           bool   `json:"running"`
	ReceiverRunning   bool   `json:"receiverRunning"`
	BridgeRunning     bool   `json:"bridgeRunning"`
	MediaReady        bool   `json:"mediaReady"`
	MediaReadyKnown   bool   `json:"-"`
	Phase             string `json:"phase"`
	DeliveryPhase     string `json:"deliveryPhase,omitempty"`
	DeliveryRequestID string `json:"deliveryRequestID,omitempty"`
	ReceiverPath      string `json:"receiverPath,omitempty"`
	ReceiverName      string `json:"receiverName,omitempty"`
	AudioCodec        string `json:"audioCodec,omitempty"`
	RuntimeState      string `json:"runtimeState,omitempty"`
	RuntimeMessage    string `json:"runtimeMessage,omitempty"`
	RuntimeSetID      string `json:"runtimeSetID,omitempty"`
	Message           string `json:"message,omitempty"`
}

type Manager struct {
	// opMu serializes Start and Stop so a concurrent lifecycle request cannot leak a child process.
	opMu                         sync.Mutex
	mu                           sync.Mutex
	running                      bool
	cancel                       context.CancelFunc
	done                         chan struct{}
	status                       Status
	audioRelay                   *l16RTPRelay
	deliveryRetry                chan struct{}
	deliveryRetryPending         bool
	deliveryReconfigure          chan sourceClockDeliveryCommand
	deliveryReconfigurePending   bool
	deliveryReconfigureRequestID string
	deliveryReconfigureReply     chan error
	deliverySessionID            string
	deliveryActiveGeneration     uint64
	deliveryActivePublishURL     string
	deliveryActiveOutput         DirectOutputConfig
	stopInitiator                string
	onChange                     func()
}

func New(onChange func()) *Manager {
	return &Manager{
		onChange: onChange,
		status:   Status{Message: "AirPlay受信は停止中です。UxPlayを設定すると開始できます。"},
	}
}

func FeatureEnabled() bool {
	value, _ := os.LookupEnv(envFeatureFlag)
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	case "0", "false", "no", "off", "disabled":
		return false
	default:
		return runtime.GOOS == "windows" && runtime.GOARCH == "amd64"
	}
}

func bundledAirPlayRoot() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	base, err := filepath.Abs(filepath.Dir(executable))
	if err != nil {
		return ""
	}
	return filepath.Join(base, "airplay")
}

func bundledAirPlayRuntimeAvailable() bool {
	_, err := ResolvePinnedAirPlayRuntime()
	return err == nil
}

func resolveReceiverTitle(fallback string) (string, error) {
	raw, explicit := os.LookupEnv(envDiagnosticName)
	if !explicit {
		return normalizeReceiverTitle(fallback), nil
	}
	title := strings.Join(strings.Fields(raw), "-")
	if title == "" {
		return "", fmt.Errorf("%s is invalid: receiver name is empty", envDiagnosticName)
	}
	if len(title) > 63 {
		return "", fmt.Errorf("%s is invalid: receiver name exceeds 63 ASCII characters", envDiagnosticName)
	}
	for index := 0; index < len(title); index++ {
		character := title[index]
		alphanumeric := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if !alphanumeric && (character != '-' || index == 0) {
			return "", fmt.Errorf("%s is invalid: receiver name must match [A-Za-z0-9][A-Za-z0-9-]*", envDiagnosticName)
		}
	}
	return title, nil
}

func resolveReceiverOnPATH() (string, error) {
	for _, name := range []string{"uxplay", "uxplay.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("UxPlay was not found on PATH")
}

func ResolveReceiverPath() (string, error) {
	for _, key := range []string{envUxPlayPath, envReceiverPath} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			return resolveExecutable(raw)
		}
	}
	if runtimeSet, err := ResolvePinnedAirPlayRuntime(); err == nil {
		return runtimeSet.ReceiverPath, nil
	}
	return "", errors.New("verified AirPlay runtime was not found; install the pinned runtime or set IMAGEPAD_AIRPLAY_RECEIVER explicitly")
}

func resolveExecutable(raw string) (string, error) {
	if filepath.Base(raw) == raw {
		path, err := exec.LookPath(raw)
		if err != nil {
			return "", fmt.Errorf("AirPlay receiver %q was not found: %w", raw, err)
		}
		return path, nil
	}
	info, err := os.Stat(raw)
	if err != nil {
		return "", fmt.Errorf("AirPlay receiver %q cannot be accessed: %w", raw, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("AirPlay receiver %q is a directory", raw)
	}
	path, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("AirPlay receiver %q has no absolute path: %w", raw, err)
	}
	return path, nil
}

// audioCodecLabel reports the negotiated audio codec for status. It stays
// "l16" while the relay sees only L16; a non-L16 payload type is surfaced as
// "unsupported(pt=N)" so a codec mismatch is visible instead of silent.
func audioCodecLabel(relay *l16RTPRelay) string {
	if relay == nil {
		return ""
	}
	if pt, ok := relay.UnsupportedCodecPT(); ok {
		return fmt.Sprintf("unsupported(pt=%d)", pt)
	}
	return "l16"
}

func (m *Manager) Status() Status {
	enabled := FeatureEnabled()
	preparation := RuntimePreparation()
	m.mu.Lock()
	status := m.status
	running := m.running
	audioRelay := m.audioRelay
	m.mu.Unlock()
	status.Enabled = enabled
	status.RuntimeState = preparation.State
	status.RuntimeMessage = preparation.Message
	status.RuntimeSetID = preparation.RuntimeSetID
	status.AudioCodec = audioCodecLabel(audioRelay)
	if running {
		status.Available = true
		return status
	}
	if enabled {
		if preparation.State == runtimePreparationPreparing {
			status.Available = false
			status.Message = "AirPlayランタイムを準備しています。完了後に受信を開始できます。"
			return status
		}
		if preparation.State == runtimePreparationFailed && !hasExplicitReceiverPath() {
			status.Available = false
			if preparation.Message != "" {
				status.Message = preparation.Message
			}
		}
		receiverName, err := resolveReceiverTitle(defaultReceiverTitle)
		if err != nil {
			status.Available = false
			status.ReceiverName = ""
			status.Message = err.Error()
			return status
		}
		status.ReceiverName = receiverName
		if path, err := ResolveReceiverPath(); err == nil {
			status.Available = true
			status.ReceiverPath = path
		} else {
			status.Available = false
			if status.Message == "" || status.Message == "AirPlay受信は停止中です。UxPlayを設定すると開始できます。" {
				status.Message = err.Error()
			}
		}
	} else {
		status.Available = false
		status.Message = "AirPlay機能は無効です。IMAGEPAD_AIRPLAY=1で有効化できます。"
	}
	return status
}

func (m *Manager) Start(parent context.Context, ffmpegPath, publishURL string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if !FeatureEnabled() {
		return ErrDisabled
	}
	if parent == nil {
		parent = context.Background()
	}
	receiverName, err := resolveReceiverTitle(defaultReceiverTitle)
	if err != nil {
		return err
	}
	receiverPath, err := ResolveReceiverPath()
	if err != nil {
		return err
	}
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath, err = exec.LookPath("ffmpeg")
		if err != nil {
			return fmt.Errorf("ffmpeg was not found: %w", err)
		}
	}
	if err := validatePublishURL(publishURL, "rtmp"); err != nil {
		return err
	}
	useGStreamer := gstreamerPipelineEnabled()
	gstreamerBridgePath := ""
	if useGStreamer {
		gstreamerBridgePath, err = ResolveGStreamerBridgePath()
		if err != nil {
			return err
		}
		log.Printf("AirPlay pipeline=gstreamer-bridge bridge=%s", gstreamerBridgePath)
	} else {
		log.Printf("AirPlay pipeline=ffmpeg")
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	m.mu.Unlock()

	videoInputPort, videoPort, audioInputPort, audioPort, err := reserveRTPPorts()
	if err != nil {
		return fmt.Errorf("reserve AirPlay RTP ports: %w", err)
	}
	tempDir, err := os.MkdirTemp("", "imagepad-airplay-")
	if err != nil {
		return fmt.Errorf("create AirPlay session directory: %w", err)
	}
	sessionSDP := ""
	if !useGStreamer {
		sessionSDP = filepath.Join(tempDir, "session.sdp")
		if err := os.WriteFile(sessionSDP, []byte(BuildSessionSDP(videoPort, audioPort)), 0600); err != nil {
			os.RemoveAll(tempDir)
			return fmt.Errorf("write AirPlay session SDP: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(parent)
	audioRelay, err := startL16RTPRelay(ctx, audioInputPort, audioPort)
	if err != nil {
		cancel()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start AirPlay L16 RTP relay: %w", err)
	}
	relay, err := startH264RTPRelay(ctx, videoInputPort, videoPort, audioRelay.NotifyVideoActivity)
	if err != nil {
		cancel()
		os.RemoveAll(tempDir)
		audioRelay.Close()
		return fmt.Errorf("start AirPlay H.264 RTP relay: %w", err)
	}
	if useGStreamer {
		receiverLog := &limitedBuffer{max: 8192}
		encoder := video.SelectVideoEncoder(ctx, ffmpegPath, video.EncoderLowLatency)
		bridge := BuildGStreamerBridgeArgs(videoPort, audioPort)
		encoderArgs := BuildGStreamerFFmpegArgs(publishURL, encoder, video.ResolveQualityForUpload("1080", 20, 0))
		pipeline, err := startGStreamerBridgeProcess(ctx, gstreamerBridgePath, bridge, ffmpegPath, encoderArgs)
		if err != nil {
			cancel()
			relay.Close()
			audioRelay.Close()
			os.RemoveAll(tempDir)
			return fmt.Errorf("start AirPlay GStreamer bridge: %w", err)
		}
		// The audio relay starts its paced output only after a complete video
		// decoder refresh. Requesting the cached SPS/PPS+IDR here keeps the
		// GStreamer branch's audio/video start boundary identical to the legacy
		// FFmpeg path, including when the sender is already mid-session.
		scheduleBridgeDecoderRefresh(ctx, relay)
		receiver := exec.CommandContext(ctx, receiverPath, BuildReceiverArgs(videoInputPort, audioInputPort, receiverName)...)
		restoreReceiverConfig, err := configureReceiverProcess(receiver, receiverLog, receiverPath, tempDir)
		if err != nil {
			cancel()
			pipeline.stop()
			_, _ = pipeline.wait()
			if pipeline.untrack != nil {
				pipeline.untrack()
			}
			relay.Close()
			audioRelay.Close()
			os.RemoveAll(tempDir)
			return fmt.Errorf("configure AirPlay receiver: %w", err)
		}
		if err := receiver.Start(); err != nil {
			var restoreErr error
			if restoreReceiverConfig != nil {
				restoreErr = restoreReceiverConfig()
			}
			cancel()
			pipeline.stop()
			_, _ = pipeline.wait()
			if pipeline.untrack != nil {
				pipeline.untrack()
			}
			relay.Close()
			audioRelay.Close()
			os.RemoveAll(tempDir)
			if restoreErr != nil {
				return fmt.Errorf("start AirPlay receiver: %w; restore receiver configuration: %v", err, restoreErr)
			}
			return fmt.Errorf("start AirPlay receiver: %w", err)
		}
		if err := restoreReceiverConfigurationAfterStartup(receiverLog, restoreReceiverConfig); err != nil {
			cancel()
			_, _ = receiver.Process, receiver.Wait()
			pipeline.stop()
			_, _ = pipeline.wait()
			if pipeline.untrack != nil {
				pipeline.untrack()
			}
			relay.Close()
			audioRelay.Close()
			os.RemoveAll(tempDir)
			return fmt.Errorf("restore AirPlay receiver configuration: %w", err)
		}

		done := make(chan struct{})
		m.mu.Lock()
		m.running = true
		m.cancel = cancel
		m.done = done
		m.audioRelay = audioRelay
		m.status = Status{
			Enabled:         true,
			Available:       true,
			Running:         true,
			ReceiverRunning: true,
			BridgeRunning:   true,
			ReceiverPath:    receiverPath,
			ReceiverName:    receiverName,
			Message:         "AirPlay受信待ちです。同一LANのiOSから画面ミラーリングを開始してください。",
		}
		m.mu.Unlock()
		m.notify()
		go m.monitorGStreamer(ctx, cancel, done, relay, audioRelay, pipeline, receiver, receiverLog, tempDir)
		return nil
	}
	receiverLog := &limitedBuffer{max: 8192}
	// Select the GPU encoder once up front and re-encode the bridge video so a
	// portrait<->landscape rotation (fresh SPS/PPS from UxPlay) cannot crash the
	// FLV muxer. The bridge preserves the incoming resolution, so the OBS stage
	// keeps deciding portrait vs landscape from the actual frame dimensions.
	encoder := video.SelectVideoEncoder(ctx, ffmpegPath, video.EncoderLowLatency)
	bridgePreset := video.ResolveQualityForUpload("1080", 20, 0)
	bridgeArgs := BuildBridgeArgs(sessionSDP, publishURL, encoder, bridgePreset)
	bridge, bridgeLog, untrack, err := startBridgeProcess(ctx, ffmpegPath, bridgeArgs)
	if err != nil {
		cancel()
		relay.Close()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start AirPlay FFmpeg bridge: %w", err)
	}
	scheduleBridgeDecoderRefresh(ctx, relay)
	receiver := exec.CommandContext(ctx, receiverPath, BuildReceiverArgs(videoInputPort, audioInputPort, receiverName)...)
	restoreReceiverConfig, err := configureReceiverProcess(receiver, receiverLog, receiverPath, tempDir)
	if err != nil {
		cancel()
		_, _ = bridge.Process, bridge.Wait()
		untrack()
		os.RemoveAll(tempDir)
		return fmt.Errorf("configure AirPlay receiver: %w", err)
	}
	if err := receiver.Start(); err != nil {
		var restoreErr error
		if restoreReceiverConfig != nil {
			restoreErr = restoreReceiverConfig()
		}
		cancel()
		_, _ = bridge.Process, bridge.Wait()
		untrack()
		os.RemoveAll(tempDir)
		if restoreErr != nil {
			return fmt.Errorf("start AirPlay receiver: %w; restore receiver configuration: %v", err, restoreErr)
		}
		return fmt.Errorf("start AirPlay receiver: %w", err)
	}
	if err := restoreReceiverConfigurationAfterStartup(receiverLog, restoreReceiverConfig); err != nil {
		cancel()
		_, _ = receiver.Process, receiver.Wait()
		_, _ = bridge.Process, bridge.Wait()
		untrack()
		os.RemoveAll(tempDir)
		return fmt.Errorf("restore AirPlay receiver configuration: %w", err)
	}

	done := make(chan struct{})
	m.mu.Lock()
	m.running = true
	m.cancel = cancel
	m.done = done
	m.audioRelay = audioRelay
	m.status = Status{
		Enabled:         true,
		Available:       true,
		Running:         true,
		ReceiverRunning: true,
		BridgeRunning:   true,
		ReceiverPath:    receiverPath,
		ReceiverName:    receiverName,
		Message:         "AirPlay受信待ちです。同一LANのiOSから画面ミラーリングを開始してください。",
	}
	m.mu.Unlock()
	m.notify()
	go m.monitor(ctx, cancel, done, relay, audioRelay, bridge, receiver, ffmpegPath, bridgeArgs, bridgeLog, receiverLog, untrack, tempDir)
	return nil
}

// StartDirect starts UxPlay plus the native GStreamer publisher. Unlike Start,
// this path does not resolve or launch FFmpeg; GStreamer owns decode, timing,
// encode, RTSP/TCP publication, and MP4 recording.
func (m *Manager) StartDirect(parent context.Context, sessionID, publishURL, recording string, output DirectOutputConfig, onStopped func()) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if !FeatureEnabled() {
		return ErrDisabled
	}
	if parent == nil {
		parent = context.Background()
	}
	if SourceClockPipelineEnabled() {
		if strings.TrimSpace(sessionID) == "" {
			return errors.New("AirPlay source-clock session ID is empty")
		}
		return m.startSourceClock(parent, sessionID, publishURL, recording, output, onStopped)
	}
	receiverName, err := resolveReceiverTitle(defaultReceiverTitle)
	if err != nil {
		return err
	}
	receiverPath, err := ResolveReceiverPath()
	if err != nil {
		return err
	}
	if err := validatePublishURL(publishURL, "rtsp"); err != nil {
		return err
	}
	if strings.TrimSpace(recording) == "" {
		return errors.New("AirPlay direct recording path is empty")
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
	videoInputPort, videoPort, audioInputPort, audioPort, err := reserveRTPPorts()
	if err != nil {
		return fmt.Errorf("reserve AirPlay RTP ports: %w", err)
	}
	tempDir, err := os.MkdirTemp("", "imagepad-airplay-")
	if err != nil {
		return fmt.Errorf("create AirPlay session directory: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	audioRelay, err := startL16RTPRelay(ctx, audioInputPort, audioPort)
	if err != nil {
		cancel()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start AirPlay L16 RTP relay: %w", err)
	}
	relay, err := startH264RTPRelay(ctx, videoInputPort, videoPort, audioRelay.NotifyVideoActivity)
	if err != nil {
		cancel()
		audioRelay.Close()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start AirPlay H.264 RTP relay: %w", err)
	}
	stopFile := filepath.Join(tempDir, "stop.request")
	pipelineArgs := BuildGStreamerDirectArgs(videoPort, audioPort, publishURL, recording, stopFile, output)
	pipeline, err := startGStreamerDirectProcess(ctx, bridgePath, pipelineArgs, stopFile)
	if err != nil {
		cancel()
		relay.Close()
		audioRelay.Close()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start direct GStreamer publisher: %w", err)
	}
	receiverLog := &limitedBuffer{max: 8192}
	receiver := exec.CommandContext(ctx, receiverPath, BuildReceiverArgs(videoInputPort, audioInputPort, receiverName)...)
	restoreReceiverConfig, err := configureReceiverProcess(receiver, receiverLog, receiverPath, tempDir)
	if err != nil {
		cancel()
		pipeline.stop()
		_ = pipeline.wait()
		relay.Close()
		audioRelay.Close()
		os.RemoveAll(tempDir)
		return fmt.Errorf("configure AirPlay receiver: %w", err)
	}
	if err := receiver.Start(); err != nil {
		if restoreReceiverConfig != nil {
			_ = restoreReceiverConfig()
		}
		cancel()
		pipeline.stop()
		_ = pipeline.wait()
		relay.Close()
		audioRelay.Close()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start AirPlay receiver: %w", err)
	}
	if err := restoreReceiverConfigurationAfterStartup(receiverLog, restoreReceiverConfig); err != nil {
		cancel()
		_ = receiver.Process.Kill()
		_, _ = receiver.Process, receiver.Wait()
		pipeline.stop()
		_ = pipeline.wait()
		relay.Close()
		audioRelay.Close()
		os.RemoveAll(tempDir)
		return fmt.Errorf("restore AirPlay receiver configuration: %w", err)
	}

	done := make(chan struct{})
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		cancel()
		_ = receiver.Process.Kill()
		_, _ = receiver.Process, receiver.Wait()
		pipeline.stop()
		_ = pipeline.wait()
		relay.Close()
		audioRelay.Close()
		os.RemoveAll(tempDir)
		return ErrAlreadyRunning
	}
	m.running = true
	m.cancel = cancel
	m.done = done
	m.audioRelay = audioRelay
	m.status = Status{
		Enabled:         true,
		Available:       true,
		Running:         true,
		ReceiverRunning: true,
		BridgeRunning:   true,
		ReceiverPath:    receiverPath,
		ReceiverName:    receiverName,
		Message:         "AirPlay受信待ちです。同一LANのiOSから画面ミラーリングを開始してください。",
	}
	m.mu.Unlock()
	m.notify()
	restartPipeline := func(runCtx context.Context) (*gstreamerDirectProcess, error) {
		return startGStreamerDirectProcess(runCtx, bridgePath, pipelineArgs, stopFile)
	}
	go m.monitorDirect(ctx, cancel, done, relay, audioRelay, pipeline, restartPipeline, receiver, receiverLog, tempDir, onStopped)
	return nil
}

func restoreReceiverConfigurationAfterStartup(output *limitedBuffer, restore func() error) error {
	if restore == nil {
		return nil
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), "[arguments] Reading:") {
			// The wrapper logs immediately before opening arguments.txt. Keep the
			// temporary file in place long enough for that synchronous read.
			time.Sleep(100 * time.Millisecond)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return restore()
}

func validatePublishURL(raw string, allowedSchemes ...string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid AirPlay publish URL")
	}
	for _, scheme := range allowedSchemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}
	if len(allowedSchemes) == 0 {
		return fmt.Errorf("invalid AirPlay publish URL")
	}
	return fmt.Errorf("invalid AirPlay publish URL")
}

type rtpPortPair struct {
	rtp  *net.UDPConn
	rtcp *net.UDPConn
	port int
}

func reserveRTPPorts() (videoInputPort, videoPort, audioInputPort, audioPort int, err error) {
	pairs := make([]*rtpPortPair, 0, 4)
	defer func() {
		for _, pair := range pairs {
			pair.close()
		}
	}()
	labels := []string{"video input", "video output", "audio input", "audio output"}
	for _, label := range labels {
		pair, reserveErr := reserveRTPPortPair()
		if reserveErr != nil {
			return 0, 0, 0, 0, fmt.Errorf("%s pair: %w", label, reserveErr)
		}
		pairs = append(pairs, pair)
	}
	return pairs[0].port, pairs[1].port, pairs[2].port, pairs[3].port, nil
}

func reserveRTPPortPair() (*rtpPortPair, error) {
	const maxAttempts = 100
	addr := net.ParseIP("127.0.0.1")
	for attempt := 0; attempt < maxAttempts; attempt++ {
		rtp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: addr, Port: 0})
		if err != nil {
			return nil, err
		}
		port := rtp.LocalAddr().(*net.UDPAddr).Port
		if port >= 65535 {
			_ = rtp.Close()
			continue
		}
		rtcp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: addr, Port: port + 1})
		if err != nil {
			_ = rtp.Close()
			continue
		}
		return &rtpPortPair{rtp: rtp, rtcp: rtcp, port: port}, nil
	}
	return nil, fmt.Errorf("could not reserve consecutive RTP/RTCP ports after %d attempts", maxAttempts)
}

func (p *rtpPortPair) close() {
	_ = p.rtp.Close()
	_ = p.rtcp.Close()
}

func normalizeReceiverTitle(title string) string {
	fields := strings.Fields(title)
	if len(fields) == 0 {
		return defaultReceiverTitle
	}
	return strings.Join(fields, "-")
}

func BuildReceiverArgs(videoPort, audioPort int, title string) []string {
	title = normalizeReceiverTitle(title)
	videoPipeline := fmt.Sprintf("config-interval=-1\t!\tudpsink\thost=127.0.0.1\tport=%d", videoPort)
	audioPipeline := fmt.Sprintf("pt=96\t!\tudpsink\thost=127.0.0.1\tport=%d", audioPort)
	args := []string{
		"-n", title,
		// UxPlay's Windows bundle keeps the AirPlay RTP egress active with
		// the established headless setting used by the raw-capture path.
		"-vs", "0",
		"-fps", "60",
		"-vrtp", videoPipeline,
		"-artp", audioPipeline,
	}
	if os.Getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") == "1" {
		// Keep packet-level debug output bounded while exposing the receiver's
		// negotiation and client FPS reports in the parent application log.
		args = append(args, "-d", "1", "-FPSdata")
	}
	return args
}

func BuildBridgeArgs(sessionSDP, publishURL string, encoder video.VideoEncoderProfile, preset video.QualityPreset) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-protocol_whitelist", "file,udp,rtp",
		"-thread_queue_size", "512",
		"-buffer_size", "4194304",
		// The Go relay already normalizes RTP sequence numbers and drops
		// incomplete access units. Do not make FFmpeg wait on a large packet
		// reorder cache when iPhone briefly pauses or changes applications.
		"-reorder_queue_size", "0",
		"-analyzeduration", "2000000",
		"-probesize", "5000000",
		"-fflags", "+genpts+discardcorrupt",
		"-i", sessionSDP,
		"-map", "0:v:0",
		"-map", "0:a:0",
	}
	// Normalize the output canvas before encoding. UxPlay emits fresh SPS/PPS
	// when the source rotates between portrait and landscape; a fixed canvas
	// lets the decoder accept that input change without forcing the FLV encoder
	// and downstream ingest to change dimensions mid-stream.
	args = append(args, "-vf", "scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=black")
	args = append(args, encoder.FFmpegArgs(preset, "ultrafast")...)
	args = append(args,
		"-c:a", "aac",
		// Derive audio PTS from decoded sample count so source resets cannot
		// produce a negative or backward FLV timestamp.
		"-af", "aresample=async=1:first_pts=0,asetpts=N/SR/TB",
		"-b:a", "160k",
		// Match OBS's native 48 kHz so the ingest does not resample the AAC a
		// second time; the aresample filter above converts the 44.1 kHz L16.
		"-ar", "48000",
		"-ac", "2",
		"-avoid_negative_ts", "make_zero",
		"-f", "flv",
		publishURL,
	)
	return args
}

func BuildSessionSDP(videoPort, audioPort int) string {
	return fmt.Sprintf("v=0\r\n"+
		"o=- 0 0 IN IP4 127.0.0.1\r\n"+
		"s=ImagePadServer AirPlay session\r\n"+
		"c=IN IP4 127.0.0.1\r\n"+
		"t=0 0\r\n"+
		"m=video %d RTP/AVP 96\r\n"+
		"a=rtpmap:96 H264/90000\r\n"+
		"a=fmtp:96 packetization-mode=1\r\n"+
		"a=recvonly\r\n"+
		"m=audio %d RTP/AVP 96\r\n"+
		"a=rtpmap:96 L16/44100/2\r\n"+
		"a=recvonly\r\n", videoPort, audioPort)
}

func (m *Manager) Stop(timeout time.Duration) bool {
	return m.stop(timeout, "")
}

// StopForUser records only an explicit user stop as causal intent. It remains
// separate from internal cleanup so a canceled context is not mislabelled.
func (m *Manager) StopForUser(timeout time.Duration) bool {
	return m.stop(timeout, airplaycontract.TerminationReasonUserStop)
}

// StopForServerShutdown records the application shutdown boundary without
// treating ordinary receiver synchronization as a server shutdown.
func (m *Manager) StopForServerShutdown(timeout time.Duration) bool {
	return m.stop(timeout, airplaycontract.TerminationReasonServerShutdown)
}

func (m *Manager) stop(timeout time.Duration, initiator string) bool {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	running := m.running
	if running && (initiator == airplaycontract.TerminationReasonUserStop ||
		initiator == airplaycontract.TerminationReasonServerShutdown) && m.stopInitiator == "" {
		m.stopInitiator = initiator
	}
	m.mu.Unlock()
	if !running || cancel == nil || done == nil {
		return true
	}
	cancel()
	if timeout <= 0 {
		<-done
		return true
	}
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (m *Manager) sourceClockStopInitiator() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopInitiator
}

func (m *Manager) sourceClockTerminationInitiator(fallback string) string {
	if initiator := m.sourceClockStopInitiator(); initiator != "" {
		return initiator
	}
	return fallback
}

func startBridgeProcess(ctx context.Context, ffmpegPath string, args []string) (*exec.Cmd, *limitedBuffer, func(), error) {
	output := &limitedBuffer{max: 8192}
	bridgeArgs := args
	if os.Getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") == "1" {
		bridgeArgs = append([]string(nil), args...)
		for i := 0; i+1 < len(bridgeArgs); i++ {
			if bridgeArgs[i] == "-loglevel" {
				bridgeArgs[i+1] = "verbose"
				break
			}
		}
		// Packet timestamps distinguish a stalled RTP clock from FLV
		// interleaving blocked by a sparse audio stream.
		last := len(bridgeArgs) - 1
		bridgeArgs = append(bridgeArgs[:last], append([]string{"-debug_ts"}, bridgeArgs[last:]...)...)
	}
	bridge := exec.CommandContext(ctx, ffmpegPath, bridgeArgs...)
	configureProcess(bridge, output)
	if err := bridge.Start(); err != nil {
		return nil, nil, nil, err
	}
	untrack, trackErr := video.TrackStartedFFmpeg(bridge)
	if trackErr != nil {
		waitErr := bridge.Wait()
		return nil, output, nil, errors.Join(trackErr, waitErr)
	}
	return bridge, output, untrack, nil
}

func waitForProcess(cmd *exec.Cmd, result chan<- error) {
	result <- cmd.Wait()
}

var bridgeDecoderRefreshIntervals = []time.Duration{
	0,
	100 * time.Millisecond,
	150 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
}

// scheduleBridgeDecoderRefresh repeats the cached SPS/PPS and complete IDR
// while a newly spawned FFmpeg process is opening its SDP and binding the UDP
// socket. A single replay immediately after cmd.Start races that bind and can
// be silently lost, leaving FFmpeg with only P-frames until the next iOS IDR.
func scheduleBridgeDecoderRefresh(ctx context.Context, relay *h264RTPRelay) {
	go replayDecoderRefreshes(ctx, bridgeDecoderRefreshIntervals, relay.ReplayParameterSets)
}

func replayDecoderRefreshes(ctx context.Context, intervals []time.Duration, replay func()) {
	for _, interval := range intervals {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
			replay()
		}
	}
}

func (m *Manager) monitor(ctx context.Context, cancel context.CancelFunc, done chan struct{}, relay *h264RTPRelay, audioRelay *l16RTPRelay, bridge, receiver *exec.Cmd, ffmpegPath string, bridgeArgs []string, bridgeLog, receiverLog *limitedBuffer, untrack func(), tempDir string) {
	defer relay.Close()
	defer audioRelay.Close()
	receiverDone := make(chan error, 1)
	go func() { receiverDone <- receiver.Wait() }()

	currentBridge := bridge
	currentBridgeLog := bridgeLog
	currentUntrack := untrack
	bridgeDone := make(chan error, 1)
	go waitForProcess(currentBridge, bridgeDone)

	stoppedByRequest := false
	var firstName string
	var firstErr error
	backoff := newBridgeRespawnBackoff(bridgeRespawnInitialDelay, bridgeRespawnMaxDelay, bridgeRespawnMaxRetries)
	lastBridgeStart := time.Now()
	formatChanges := relay.FormatChanges()
	var debugTicker *time.Ticker
	var debugTick <-chan time.Time
	lastDebugLog := ""
	if os.Getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") == "1" {
		debugTicker = time.NewTicker(time.Second)
		debugTick = debugTicker.C
		defer debugTicker.Stop()
	}
	for {
		select {
		case <-debugTick:
			output := redactBridgeOutput(currentBridgeLog.String(), bridgeArgs)
			if output != "" && output != lastDebugLog {
				log.Printf("AirPlay FFmpeg bridge output:\n%s", output)
				lastDebugLog = output
			}
		case <-ctx.Done():
			stoppedByRequest = true
			cancel()
			bridgeErr := <-bridgeDone
			receiverErr := <-receiverDone
			currentUntrack()
			m.finishMonitor(done, tempDir, stoppedByRequest, firstName, firstErr, bridgeErr, receiverErr, currentBridgeLog, receiverLog)
			return
		case receiverErr := <-receiverDone:
			firstName = "UxPlay receiver"
			firstErr = receiverErr
			cancel()
			bridgeErr := <-bridgeDone
			currentUntrack()
			m.finishMonitor(done, tempDir, stoppedByRequest, firstName, firstErr, bridgeErr, receiverErr, currentBridgeLog, receiverLog)
			return
		case <-formatChanges:
			// SPS/PPS changes are expected during portrait/landscape rotation.
			// The bridge uses a fixed output canvas, so keep the same FFmpeg
			// process alive and let its decoder accept the new input format.
			log.Printf("AirPlay H264 parameter-set change observed; keeping FFmpeg bridge alive")
		case bridgeErr := <-bridgeDone:
			if ctx.Err() != nil {
				stoppedByRequest = true
				cancel()
				receiverErr := <-receiverDone
				currentUntrack()
				m.finishMonitor(done, tempDir, stoppedByRequest, firstName, firstErr, bridgeErr, receiverErr, currentBridgeLog, receiverLog)
				return
			}

			exitOutput := redactBridgeOutput(currentBridgeLog.String(), bridgeArgs)
			log.Printf("AirPlay FFmpeg bridge exited: err=%v output=%q", bridgeErr, exitOutput)

			// A bridge that ran long enough before exiting did real work, so a
			// late isolated crash should not inherit a stale retry budget.
			if time.Since(lastBridgeStart) >= bridgeRespawnStableDuration {
				backoff.reset()
			}

			// FFmpeg can exit before the first RTP packet when the iPhone has not
			// connected yet: it only finished its input probe. Keep UxPlay and its
			// Bonjour advertisement alive and recreate the bridge without burning
			// the retry budget, so a later connection is accepted. Once video is
			// flowing, a bridge exit is a real failure: back off and, after
			// repeated failures, surface it instead of crash-looping forever.
			respawnDelay := time.Duration(0)
			if relay.HasReceivedVideo() {
				delay, exhausted := backoff.nextDelay()
				if exhausted {
					firstName = "FFmpeg bridge"
					firstErr = fmt.Errorf("gave up restarting FFmpeg bridge after %d rapid failures (last exit: %w)", backoff.attempts(), bridgeErr)
					cancel()
					receiverErr := <-receiverDone
					currentUntrack()
					m.finishMonitor(done, tempDir, stoppedByRequest, firstName, firstErr, bridgeErr, receiverErr, currentBridgeLog, receiverLog)
					return
				}
				respawnDelay = delay
				m.setStatusMessage(fmt.Sprintf("FFmpegブリッジ再接続中（%d回目）…", backoff.attempts()))
			} else {
				backoff.reset()
			}

			currentUntrack()
			select {
			case <-ctx.Done():
				stoppedByRequest = true
				cancel()
				receiverErr := <-receiverDone
				m.finishMonitor(done, tempDir, stoppedByRequest, firstName, firstErr, bridgeErr, receiverErr, currentBridgeLog, receiverLog)
				return
			case <-time.After(respawnDelay):
			}
			newBridge, newLog, newUntrack, err := startBridgeProcess(ctx, ffmpegPath, bridgeArgs)
			if err != nil {
				firstName = "FFmpeg bridge"
				firstErr = err
				cancel()
				receiverErr := <-receiverDone
				m.finishMonitor(done, tempDir, stoppedByRequest, firstName, firstErr, bridgeErr, receiverErr, currentBridgeLog, receiverLog)
				return
			}
			scheduleBridgeDecoderRefresh(ctx, relay)
			lastBridgeStart = time.Now()
			currentBridge = newBridge
			currentBridgeLog = newLog
			currentUntrack = newUntrack
			bridgeDone = make(chan error, 1)
			go waitForProcess(currentBridge, bridgeDone)
		}
	}
}

func (m *Manager) monitorGStreamer(ctx context.Context, cancel context.CancelFunc, done chan struct{}, relay *h264RTPRelay, audioRelay *l16RTPRelay, pipeline *gstreamerBridgeProcess, receiver *exec.Cmd, receiverLog *limitedBuffer, tempDir string) {
	defer relay.Close()
	defer audioRelay.Close()
	receiverDone := make(chan error, 1)
	go func() { receiverDone <- receiver.Wait() }()
	pipelineDone := make(chan struct {
		name string
		err  error
	}, 1)
	go func() {
		name, err := pipeline.wait()
		pipelineDone <- struct {
			name string
			err  error
		}{name: name, err: err}
	}()

	select {
	case <-ctx.Done():
		cancel()
		pipeline.stop()
		result := <-pipelineDone
		receiverErr := <-receiverDone
		if pipeline.untrack != nil {
			pipeline.untrack()
		}
		m.finishMonitor(done, tempDir, true, "", nil, result.err, receiverErr, pipeline.encoderLog, receiverLog)
	case receiverErr := <-receiverDone:
		cancel()
		pipeline.stop()
		result := <-pipelineDone
		if pipeline.untrack != nil {
			pipeline.untrack()
		}
		m.finishMonitor(done, tempDir, false, "UxPlay receiver", receiverErr, result.err, receiverErr, pipeline.encoderLog, receiverLog)
	case result := <-pipelineDone:
		log.Printf("AirPlay GStreamer pipeline exited: component=%s err=%v decoder_output=%q encoder_output=%q", result.name, result.err, pipeline.decoderLog.String(), pipeline.encoderLog.String())
		if ctx.Err() != nil {
			cancel()
			receiverErr := <-receiverDone
			if pipeline.untrack != nil {
				pipeline.untrack()
			}
			m.finishMonitor(done, tempDir, true, "", nil, result.err, receiverErr, pipeline.encoderLog, receiverLog)
			return
		}
		cancel()
		receiverErr := <-receiverDone
		if pipeline.untrack != nil {
			pipeline.untrack()
		}
		m.finishMonitor(done, tempDir, false, "FFmpeg bridge", result.err, result.err, receiverErr, pipeline.encoderLog, receiverLog)
	}
}

const directPublisherMaxRetries = 5

const directNoSignalTimeout = 3 * time.Minute

func directNoSignalExpired(startedAt, videoAt, audioAt, now time.Time) bool {
	latest := startedAt
	if videoAt.After(latest) {
		latest = videoAt
	}
	if audioAt.After(latest) {
		latest = audioAt
	}
	return !now.Before(latest.Add(directNoSignalTimeout))
}

func shouldRetryDirectPublisher(attempt int) bool {
	return attempt >= 1 && attempt <= directPublisherMaxRetries
}

func directPublisherRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 250 * time.Millisecond
	for i := 1; i < attempt && delay < 4*time.Second; i++ {
		delay *= 2
	}
	if delay > 4*time.Second {
		return 4 * time.Second
	}
	return delay
}

func (m *Manager) monitorDirect(ctx context.Context, cancel context.CancelFunc, done chan struct{}, relay *h264RTPRelay, audioRelay *l16RTPRelay, pipeline *gstreamerDirectProcess, restartPipeline func(context.Context) (*gstreamerDirectProcess, error), receiver *exec.Cmd, receiverLog *limitedBuffer, tempDir string, onStopped func()) {
	defer relay.Close()
	defer audioRelay.Close()
	receiverDone := make(chan error, 1)
	go func() { receiverDone <- receiver.Wait() }()
	pipelineDone := make(chan error, 1)
	waitPipeline := func(current *gstreamerDirectProcess) {
		go func() { pipelineDone <- current.wait() }()
	}
	waitPipeline(pipeline)
	retryCount := 0
	var debugTicker *time.Ticker
	var debugTick <-chan time.Time
	lastReceiverLog := ""
	lastPipelineLog := ""
	startedAt := time.Now()
	noSignalTicker := time.NewTicker(time.Second)
	defer noSignalTicker.Stop()
	if os.Getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") == "1" {
		debugTicker = time.NewTicker(time.Second)
		debugTick = debugTicker.C
		defer debugTicker.Stop()
	}

	for {
		select {
		case <-debugTick:
			output := receiverLog.String()
			if output != "" && output != lastReceiverLog {
				log.Printf("AirPlay UxPlay receiver output:\n%s", output)
				lastReceiverLog = output
			}
			if relay.HasReceivedVideo() {
				input, output := relay.Stats()
				log.Printf("AirPlay H264 RTP input detected packets_in=%d packets_out=%d", input, output)
			}
			pipelineOutput := pipeline.log.String()
			if pipelineOutput != "" && pipelineOutput != lastPipelineLog {
				log.Printf("AirPlay direct GStreamer output:\n%s", pipelineOutput)
				lastPipelineLog = pipelineOutput
			}
		case now := <-noSignalTicker.C:
			if directNoSignalExpired(startedAt, relay.LastActivity(), audioRelay.LastActivity(), now) {
				log.Printf("AirPlay direct publisher: no valid RTP signal for %s; stopping session", directNoSignalTimeout)
				cancel()
				pipeline.stop()
				pipelineErr := <-pipelineDone
				receiverErr := <-receiverDone
				m.finishMonitorWithCallback(done, tempDir, false, "AirPlay no signal timeout", nil, pipelineErr, receiverErr, pipeline.log, receiverLog, onStopped)
				return
			}
		case <-ctx.Done():
			cancel()
			pipeline.stop()
			pipelineErr := <-pipelineDone
			receiverErr := <-receiverDone
			m.finishMonitorWithCallback(done, tempDir, true, "", nil, pipelineErr, receiverErr, pipeline.log, receiverLog, onStopped)
			return
		case receiverErr := <-receiverDone:
			cancel()
			pipeline.stop()
			pipelineErr := <-pipelineDone
			m.finishMonitorWithCallback(done, tempDir, false, "UxPlay receiver", receiverErr, pipelineErr, receiverErr, pipeline.log, receiverLog, onStopped)
			return
		case pipelineErr := <-pipelineDone:
			log.Printf("AirPlay direct GStreamer pipeline exited: err=%v output=%q", pipelineErr, pipeline.log.String())
			if ctx.Err() != nil {
				cancel()
				receiverErr := <-receiverDone
				m.finishMonitorWithCallback(done, tempDir, true, "", nil, pipelineErr, receiverErr, pipeline.log, receiverLog, onStopped)
				return
			}
			restarted := false
			for attempt := retryCount + 1; shouldRetryDirectPublisher(attempt); attempt++ {
				m.setStatusMessage(fmt.Sprintf("AirPlay direct publisherを再接続しています (%d/%d)", attempt, directPublisherMaxRetries))
				timer := time.NewTimer(directPublisherRetryDelay(attempt))
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					cancel()
					receiverErr := <-receiverDone
					m.finishMonitorWithCallback(done, tempDir, true, "", nil, pipelineErr, receiverErr, pipeline.log, receiverLog, onStopped)
					return
				case <-timer.C:
				}
				next, err := restartPipeline(ctx)
				if err != nil {
					log.Printf("AirPlay direct GStreamer publisher restart failed: attempt=%d err=%v", attempt, err)
					retryCount = attempt
					if ctx.Err() != nil {
						break
					}
					continue
				}
				pipeline = next
				retryCount = attempt
				waitPipeline(pipeline)
				// The replacement publisher has a fresh GStreamer decoder and
				// must receive the latest complete SPS/PPS+IDR, not wait for an
				// arbitrary future keyframe from the iPhone.
				scheduleBridgeDecoderRefresh(ctx, relay)
				restarted = true
				break
			}
			if restarted {
				continue
			}
			if ctx.Err() != nil {
				cancel()
				receiverErr := <-receiverDone
				m.finishMonitorWithCallback(done, tempDir, true, "", nil, pipelineErr, receiverErr, pipeline.log, receiverLog, onStopped)
				return
			}
			cancel()
			receiverErr := <-receiverDone
			m.finishMonitorWithCallback(done, tempDir, false, "GStreamer direct publisher", pipelineErr, pipelineErr, receiverErr, pipeline.log, receiverLog, onStopped)
			return
		}
	}
}

func redactBridgeOutput(output string, args []string) string {
	if len(args) == 0 {
		return output
	}
	publishURL := args[len(args)-1]
	if publishURL == "" {
		return output
	}
	return strings.ReplaceAll(output, publishURL, "[REDACTED]")
}

func (m *Manager) finishMonitor(done chan struct{}, tempDir string, stoppedByRequest bool, firstName string, firstErr, bridgeErr, receiverErr error, bridgeLog, receiverLog *limitedBuffer) {
	m.finishMonitorWithCallback(done, tempDir, stoppedByRequest, firstName, firstErr, bridgeErr, receiverErr, bridgeLog, receiverLog, nil)
}

func (m *Manager) finishMonitorWithCallback(done chan struct{}, tempDir string, stoppedByRequest bool, firstName string, firstErr, bridgeErr, receiverErr error, bridgeLog, receiverLog *limitedBuffer, onStopped func()) {
	os.RemoveAll(tempDir)
	message := "AirPlay受信を停止しました。"
	if !stoppedByRequest {
		message = processExitMessage(firstName, firstErr, bridgeErr, receiverErr, bridgeLog, receiverLog)
	}
	m.mu.Lock()
	plannedReply := m.deliveryReconfigureReply
	m.running = false
	m.cancel = nil
	m.done = nil
	m.audioRelay = nil
	m.deliveryRetry = nil
	m.deliveryRetryPending = false
	m.deliveryReconfigure = nil
	m.deliveryReconfigurePending = false
	m.deliveryReconfigureRequestID = ""
	m.deliveryReconfigureReply = nil
	m.deliverySessionID = ""
	m.deliveryActiveGeneration = 0
	m.deliveryActivePublishURL = ""
	m.deliveryActiveOutput = DirectOutputConfig{}
	m.stopInitiator = ""
	m.status.Running = false
	m.status.ReceiverRunning = false
	m.status.BridgeRunning = false
	m.status.MediaReady = false
	m.status.MediaReadyKnown = false
	m.status.DeliveryPhase = ""
	m.status.DeliveryRequestID = ""
	m.status.Message = message
	m.mu.Unlock()
	replySourceClockDelivery(plannedReply, ErrDeliveryReconfigureUnavailable)
	m.notify()
	if onStopped != nil {
		onStopped()
	}
	close(done)
}

func processExitMessage(firstName string, firstErr, bridgeErr, receiverErr error, bridgeLog, receiverLog *limitedBuffer) string {
	if firstName == "" {
		return "AirPlay受信プロセスが終了しました。"
	}
	if firstName == "AirPlay no signal timeout" {
		return "AirPlay受信を無信号3分で終了しました。"
	}
	terminationErr := firstErr
	if terminationErr == nil {
		terminationErr = bridgeErr
	}
	if terminationErr == nil {
		terminationErr = receiverErr
	}
	detail := processExitDetail(terminationErr)
	if firstName == "GStreamer source-clock publisher" {
		return fmt.Sprintf("GStreamer source-clock publisherが終了しました%s: %s", exitDetailSuffix(detail), bridgeLog.String())
	}
	output := ""
	if firstName == "FFmpeg bridge" || firstName == "GStreamer direct publisher" {
		output = bridgeLog.String()
	} else {
		output = receiverLog.String()
	}
	if output != "" {
		return fmt.Sprintf("%sが終了しました%s: %s", firstName, exitDetailSuffix(detail), output)
	}
	if firstErr != nil {
		return fmt.Sprintf("%sが終了しました: %v", firstName, firstErr)
	}
	if bridgeErr != nil {
		return fmt.Sprintf("FFmpeg bridgeが終了しました: %v", bridgeErr)
	}
	if receiverErr != nil {
		return fmt.Sprintf("UxPlay receiverが終了しました: %v", receiverErr)
	}
	return fmt.Sprintf("%sが終了しました。", firstName)
}

func processExitDetail(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	if code := exitErr.ExitCode(); code >= 0 {
		return fmt.Sprintf("exit_code=%d", code)
	}
	return ""
}

func exitDetailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return " (" + detail + ")"
}

func (m *Manager) notify() {
	if m.onChange != nil {
		go m.onChange()
	}
}

// setStatusMessage updates the status message under the manager lock and
// notifies listeners, so a client watching status sees reconnect progress
// instead of a stalled "running" state.
func (m *Manager) setStatusMessage(msg string) {
	m.mu.Lock()
	m.status.Message = msg
	m.mu.Unlock()
	m.notify()
}

type limitedBuffer struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= b.max {
		b.data = append([]byte(nil), p[len(p)-b.max:]...)
	} else {
		b.data = append(b.data, p...)
		if len(b.data) > b.max {
			b.data = append([]byte(nil), b.data[len(b.data)-b.max:]...)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

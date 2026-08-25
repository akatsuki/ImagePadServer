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
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

const (
	envFeatureFlag       = "IMAGEPAD_AIRPLAY"
	envUxPlayPath        = "IMAGEPAD_UXPLAY"
	envReceiverPath      = "IMAGEPAD_AIRPLAY_RECEIVER"
	defaultReceiverTitle = "ImagePadServer-AirPlay"
)

var (
	ErrDisabled       = errors.New("airplay feature is disabled")
	ErrAlreadyRunning = errors.New("airplay is already running")
)

type Status struct {
	Enabled         bool   `json:"enabled"`
	Available       bool   `json:"available"`
	Running         bool   `json:"running"`
	ReceiverRunning bool   `json:"receiverRunning"`
	BridgeRunning   bool   `json:"bridgeRunning"`
	ReceiverPath    string `json:"receiverPath,omitempty"`
	AudioCodec      string `json:"audioCodec,omitempty"`
	Message         string `json:"message,omitempty"`
}

type Manager struct {
	// opMu serializes Start and Stop so a concurrent lifecycle request cannot leak a child process.
	opMu       sync.Mutex
	mu         sync.Mutex
	running    bool
	cancel     context.CancelFunc
	done       chan struct{}
	status     Status
	audioRelay *l16RTPRelay
	onChange   func()
}

func New(onChange func()) *Manager {
	return &Manager{
		onChange: onChange,
		status:   Status{Message: "AirPlay受信は停止中です。UxPlayを設定すると開始できます。"},
	}
}

func FeatureEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envFeatureFlag))) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
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
	if path, err := resolveReceiverOnPATH(); err == nil {
		return path, nil
	}
	if path, err := InstalledUxPlayPath(); err == nil {
		return path, nil
	}
	return "", errors.New("UxPlay was not found; set IMAGEPAD_UXPLAY or enable IMAGEPAD_AIRPLAY=1 for automatic Windows setup")
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
	m.mu.Lock()
	status := m.status
	running := m.running
	audioRelay := m.audioRelay
	m.mu.Unlock()
	status.Enabled = enabled
	status.AudioCodec = audioCodecLabel(audioRelay)
	if running {
		status.Available = true
		return status
	}
	if enabled {
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
	receiverPath, err := ResolveReceiverPath()
	if err != nil && !hasExplicitReceiverPath() {
		if preparedPath, setupErr := PrepareOnStartup(parent); setupErr == nil && preparedPath != "" {
			receiverPath = preparedPath
			err = nil
		} else if setupErr != nil {
			err = fmt.Errorf("%w; automatic AirPlay setup failed: %v", err, setupErr)
		}
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath, err = exec.LookPath("ffmpeg")
		if err != nil {
			return fmt.Errorf("ffmpeg was not found: %w", err)
		}
	}
	if err := validatePublishURL(publishURL); err != nil {
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
	sessionSDP := filepath.Join(tempDir, "session.sdp")
	if err := os.WriteFile(sessionSDP, []byte(BuildSessionSDP(videoPort, audioPort)), 0600); err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("write AirPlay session SDP: %w", err)
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
	receiverLog := &limitedBuffer{max: 8192}
	bridgeArgs := BuildBridgeArgs(sessionSDP, publishURL)
	bridge, bridgeLog, untrack, err := startBridgeProcess(ctx, ffmpegPath, bridgeArgs)
	if err != nil {
		cancel()
		relay.Close()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start AirPlay FFmpeg bridge: %w", err)
	}
	scheduleBridgeDecoderRefresh(ctx, relay)
	receiver := exec.CommandContext(ctx, receiverPath, BuildReceiverArgs(videoInputPort, audioInputPort, defaultReceiverTitle)...)
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
		Message:         "AirPlay受信待ちです。同一LANのiOSから画面ミラーリングを開始してください。",
	}
	m.mu.Unlock()
	m.notify()
	go m.monitor(ctx, cancel, done, relay, audioRelay, bridge, receiver, ffmpegPath, bridgeArgs, bridgeLog, receiverLog, untrack, tempDir)
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

func validatePublishURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "rtmp" || parsed.Host == "" {
		return fmt.Errorf("invalid AirPlay publish URL")
	}
	return nil
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
	return []string{
		"-n", title,
		"-vs", "0",
		"-vrtp", videoPipeline,
		"-artp", audioPipeline,
	}
}

func BuildBridgeArgs(sessionSDP, publishURL string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-protocol_whitelist", "file,udp,rtp",
		"-thread_queue_size", "512",
		"-buffer_size", "4194304",
		"-reorder_queue_size", "4096",
		"-analyzeduration", "2000000",
		"-probesize", "5000000",
		"-fflags", "+genpts+discardcorrupt",
		"-i", sessionSDP,
		"-map", "0:v:0",
		"-map", "0:a:0",
		"-c:v", "copy",
		// UxPlay Windows RTP output can leave H.264 packets without muxable DTS.
		// The relay already normalizes the 90 kHz RTP PTS, so preserve that
		// advancing timeline and copy it to DTS for this no-B-frame stream.
		"-bsf:v", "setts=pts=PTS:dts=PTS",
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
	}
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
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	running := m.running
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
	return bridge, output, video.TrackStartedFFmpeg(bridge), nil
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
	os.RemoveAll(tempDir)
	message := "AirPlay受信を停止しました。"
	if !stoppedByRequest {
		message = processExitMessage(firstName, firstErr, bridgeErr, receiverErr, bridgeLog, receiverLog)
	}
	m.mu.Lock()
	m.running = false
	m.cancel = nil
	m.done = nil
	m.audioRelay = nil
	m.status.Running = false
	m.status.ReceiverRunning = false
	m.status.BridgeRunning = false
	m.status.Message = message
	m.mu.Unlock()
	close(done)
	m.notify()
}

func processExitMessage(firstName string, firstErr, bridgeErr, receiverErr error, bridgeLog, receiverLog *limitedBuffer) string {
	if firstName == "" {
		return "AirPlay受信プロセスが終了しました。"
	}
	output := ""
	if firstName == "FFmpeg bridge" {
		output = bridgeLog.String()
	} else {
		output = receiverLog.String()
	}
	if output != "" {
		return fmt.Sprintf("%sが終了しました: %s", firstName, output)
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

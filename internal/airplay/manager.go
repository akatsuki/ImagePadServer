package airplay

import (
	"context"
	"errors"
	"fmt"
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
	defaultReceiverTitle = "ImagePadServer AirPlay"
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
	Message         string `json:"message,omitempty"`
}

type Manager struct {
	// opMu serializes Start and Stop so a concurrent lifecycle request cannot leak a child process.
	opMu     sync.Mutex
	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	done     chan struct{}
	status   Status
	onChange func()
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

func ResolveReceiverPath() (string, error) {
	for _, key := range []string{envUxPlayPath, envReceiverPath} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			return resolveExecutable(raw)
		}
	}
	for _, name := range []string{"uxplay", "uxplay.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("UxPlay was not found; set IMAGEPAD_UXPLAY to its executable")
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

func (m *Manager) Status() Status {
	enabled := FeatureEnabled()
	m.mu.Lock()
	status := m.status
	running := m.running
	m.mu.Unlock()
	status.Enabled = enabled
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
	if err := validatePublishURL(publishURL); err != nil {
		return err
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	m.mu.Unlock()

	videoPort, err := reserveUDPPort()
	if err != nil {
		return fmt.Errorf("reserve AirPlay video port: %w", err)
	}
	audioPort, err := reserveUDPPort()
	if err != nil {
		return fmt.Errorf("reserve AirPlay audio port: %w", err)
	}
	tempDir, err := os.MkdirTemp("", "imagepad-airplay-")
	if err != nil {
		return fmt.Errorf("create AirPlay session directory: %w", err)
	}
	videoSDP := filepath.Join(tempDir, "video.sdp")
	audioSDP := filepath.Join(tempDir, "audio.sdp")
	if err := os.WriteFile(videoSDP, []byte(BuildVideoSDP(videoPort)), 0600); err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("write AirPlay video SDP: %w", err)
	}
	if err := os.WriteFile(audioSDP, []byte(BuildAudioSDP(audioPort)), 0600); err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("write AirPlay audio SDP: %w", err)
	}

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	bridgeLog := &limitedBuffer{max: 8192}
	receiverLog := &limitedBuffer{max: 8192}
	bridge := exec.CommandContext(ctx, ffmpegPath, BuildBridgeArgs(videoSDP, audioSDP, publishURL)...)
	configureProcess(bridge, bridgeLog)
	if err := bridge.Start(); err != nil {
		cancel()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start AirPlay FFmpeg bridge: %w", err)
	}
	untrack := video.TrackStartedFFmpeg(bridge)
	receiver := exec.CommandContext(ctx, receiverPath, BuildReceiverArgs(videoPort, audioPort, defaultReceiverTitle)...)
	configureProcess(receiver, receiverLog)
	if err := receiver.Start(); err != nil {
		cancel()
		_, _ = bridge.Process, bridge.Wait()
		untrack()
		os.RemoveAll(tempDir)
		return fmt.Errorf("start AirPlay receiver: %w", err)
	}

	done := make(chan struct{})
	m.mu.Lock()
	m.running = true
	m.cancel = cancel
	m.done = done
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
	go m.monitor(ctx, cancel, done, bridge, receiver, bridgeLog, receiverLog, untrack, tempDir)
	return nil
}

func validatePublishURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "rtmp" || parsed.Host == "" {
		return fmt.Errorf("invalid AirPlay publish URL")
	}
	return nil
}

func reserveUDPPort() (int, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return 0, err
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	return port, conn.Close()
}

func BuildReceiverArgs(videoPort, audioPort int, title string) []string {
	videoPipeline := fmt.Sprintf("config-interval=1 ! udpsink host=127.0.0.1 port=%d", videoPort)
	audioPipeline := fmt.Sprintf("pt=96 ! udpsink host=127.0.0.1 port=%d", audioPort)
	return []string{
		"-n", title,
		"-vrtp", videoPipeline,
		"-artp", audioPipeline,
	}
}

func BuildBridgeArgs(videoSDP, audioSDP, publishURL string) []string {
	return []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-protocol_whitelist", "file,udp,rtp",
		"-thread_queue_size", "512",
		"-i", videoSDP,
		"-protocol_whitelist", "file,udp,rtp",
		"-thread_queue_size", "512",
		"-i", audioSDP,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-c:v", "copy",
		"-c:a", "aac",
		"-b:a", "160k",
		"-ar", "44100",
		"-ac", "2",
		"-f", "flv",
		publishURL,
	}
}

func BuildVideoSDP(port int) string {
	return fmt.Sprintf("v=0\r\n"+
		"o=- 0 0 IN IP4 127.0.0.1\r\n"+
		"s=ImagePadServer AirPlay video\r\n"+
		"c=IN IP4 127.0.0.1\r\n"+
		"t=0 0\r\n"+
		"m=video %d RTP/AVP 96\r\n"+
		"a=rtpmap:96 H264/90000\r\n"+
		"a=fmtp:96 packetization-mode=1\r\n"+
		"a=recvonly\r\n", port)
}

func BuildAudioSDP(port int) string {
	return fmt.Sprintf("v=0\r\n"+
		"o=- 0 0 IN IP4 127.0.0.1\r\n"+
		"s=ImagePadServer AirPlay audio\r\n"+
		"c=IN IP4 127.0.0.1\r\n"+
		"t=0 0\r\n"+
		"m=audio %d RTP/AVP 96\r\n"+
		"a=rtpmap:96 L16/44100/2\r\n"+
		"a=recvonly\r\n", port)
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

func (m *Manager) monitor(ctx context.Context, cancel context.CancelFunc, done chan struct{}, bridge, receiver *exec.Cmd, bridgeLog, receiverLog *limitedBuffer, untrack func(), tempDir string) {
	bridgeDone := make(chan error, 1)
	receiverDone := make(chan error, 1)
	go func() { bridgeDone <- bridge.Wait() }()
	go func() { receiverDone <- receiver.Wait() }()

	stoppedByRequest := false
	var firstName string
	var firstErr error
	select {
	case firstErr = <-bridgeDone:
		firstName = "FFmpeg bridge"
	case firstErr = <-receiverDone:
		firstName = "UxPlay receiver"
	case <-ctx.Done():
		stoppedByRequest = true
	}
	cancel()
	var bridgeErr, receiverErr error
	if firstName == "FFmpeg bridge" {
		bridgeErr = firstErr
		receiverErr = <-receiverDone
	} else if firstName == "UxPlay receiver" {
		receiverErr = firstErr
		bridgeErr = <-bridgeDone
	} else {
		bridgeErr = <-bridgeDone
		receiverErr = <-receiverDone
	}
	untrack()
	os.RemoveAll(tempDir)

	message := "AirPlay受信を停止しました。"
	if !stoppedByRequest {
		message = processExitMessage(firstName, firstErr, bridgeErr, receiverErr, bridgeLog, receiverLog)
	}
	m.mu.Lock()
	m.running = false
	m.cancel = nil
	m.done = nil
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

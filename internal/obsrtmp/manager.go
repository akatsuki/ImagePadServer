package obsrtmp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

type Callbacks struct {
	OnStart     func(Session)
	OnDone      func(Session)
	OnRTSPReady func(RTSPEndpoint)
	OnRTSPDone  func(RTSPEndpoint)
}

type RTSPEndpoint struct {
	SessionID  string
	Generation uint64
	Host       string
	Port       int
	RTPPort    int
	RTCPPort   int
	Path       string
	LocalURL   string
	// BackendURL is the MediaMTX loopback listener used by app-internal probes.
	// It is never a published or user-facing RTSP URL.
	BackendURL string
}

const (
	LatencyModeHLSHigh      = "hls-high"
	LatencyModeHLS          = "hls"
	LatencyModeRTSPLow      = "rtsp-low"
	LatencyModeRTSPUltra    = "rtsp-ultra"
	LatencyModeRTSPRealtime = "rtsp-realtime"

	LatencyModeLHLS  = "lhls"
	LatencyModeLLHLS = "llhls"
	LatencyModeRTSPT = "rtspt"
)

var legacyLatencyModeAliases = map[string]string{
	"auto":           LatencyModeHLS,
	"normal":         LatencyModeHLS,
	"low":            LatencyModeRTSPLow,
	"ultra":          LatencyModeRTSPUltra,
	LatencyModeLHLS:  LatencyModeRTSPLow,
	LatencyModeLLHLS: LatencyModeRTSPUltra,
	LatencyModeRTSPT: LatencyModeRTSPRealtime,
}

type LatencyProfile struct {
	Mode           string `json:"mode"`
	Label          string `json:"label"`
	Transport      string `json:"transport,omitempty"`
	Experimental   bool   `json:"experimental,omitempty"`
	Available      bool   `json:"available,omitempty"`
	Selectable     bool   `json:"selectable,omitempty"`
	PreviewURL     string `json:"previewURL,omitempty"`
	Target         string `json:"target"`
	SegmentSeconds string `json:"segmentSeconds"`
	ListSize       string `json:"listSize"`
	DVRListSize    string `json:"dvrListSize,omitempty"`
	FrameRate      string `json:"frameRate,omitempty"`
	GOPFrames      string `json:"gopFrames,omitempty"`
	Reencode       bool   `json:"reencode"`
	DVR            bool   `json:"dvr"`
	Message        string `json:"message,omitempty"`

	BitrateMultiplier int                  `json:"bitrateMultiplier,omitempty"`
	EncoderPurpose    video.EncoderPurpose `json:"-"`
}

var latencyProfiles = map[string]LatencyProfile{
	LatencyModeHLSHigh: {
		Mode:              LatencyModeHLSHigh,
		Label:             "最高画質HLS（遅延増）",
		Transport:         LatencyModeHLS,
		Available:         true,
		Selectable:        true,
		Target:            "10s+",
		SegmentSeconds:    "4",
		ListSize:          "6",
		DVRListSize:       "1800",
		FrameRate:         "30",
		GOPFrames:         "120",
		Reencode:          true,
		BitrateMultiplier: 1,
		EncoderPurpose:    video.EncoderStandard,
		Message:           "画質優先のHLS出力です。遅延は増えます。",
	},
	LatencyModeHLS: {
		Mode:              LatencyModeHLS,
		Label:             "高画質HLS（通常遅延）",
		Transport:         LatencyModeHLS,
		Available:         true,
		Selectable:        true,
		Target:            "5s",
		SegmentSeconds:    "1",
		ListSize:          "8",
		DVRListSize:       "1800",
		FrameRate:         "30",
		GOPFrames:         "30",
		Reencode:          true,
		BitrateMultiplier: 1,
		EncoderPurpose:    video.EncoderLowLatency,
		Message:           "通常遅延のHLS出力です。",
	},
	LatencyModeRTSPLow: {
		Mode:              LatencyModeRTSPLow,
		Label:             "低遅延RTSP",
		Transport:         LatencyModeRTSPT,
		Available:         true,
		Selectable:        true,
		Target:            "3-4s",
		SegmentSeconds:    "2",
		ListSize:          "4",
		DVRListSize:       "3600",
		FrameRate:         "30",
		GOPFrames:         "60",
		Reencode:          true,
		BitrateMultiplier: 1,
		EncoderPurpose:    video.EncoderLowLatency,
		Message:           "画質寄りのRTSP出力です。",
	},
	LatencyModeRTSPUltra: {
		Mode:              LatencyModeRTSPUltra,
		Label:             "超低遅延RTSP",
		Transport:         LatencyModeRTSPT,
		Available:         true,
		Selectable:        true,
		Target:            "1-2s",
		SegmentSeconds:    "1",
		ListSize:          "4",
		DVRListSize:       "3600",
		FrameRate:         "30",
		GOPFrames:         "30",
		Reencode:          true,
		BitrateMultiplier: 2,
		EncoderPurpose:    video.EncoderLowLatency,
		Message:           "低遅延と画質のバランスを取ったRTSP出力です。",
	},
	LatencyModeRTSPRealtime: {
		Mode:              LatencyModeRTSPRealtime,
		Label:             "リアルタイムRTSP",
		Transport:         LatencyModeRTSPT,
		Available:         true,
		Selectable:        true,
		Target:            "0.5s+",
		SegmentSeconds:    "0.5",
		ListSize:          "16",
		DVRListSize:       "3600",
		FrameRate:         "30",
		GOPFrames:         "15",
		Reencode:          true,
		BitrateMultiplier: 0,
		EncoderPurpose:    video.EncoderLowLatency,
		Message:           "最小遅延のRTSP出力です。受信安定性を優先してビットレートを抑えます。",
	},
}

type LatencyCapability struct {
	Mode         string `json:"mode"`
	Label        string `json:"label"`
	Transport    string `json:"transport"`
	Experimental bool   `json:"experimental"`
	Available    bool   `json:"available"`
	Selectable   bool   `json:"selectable"`
	PreviewURL   string `json:"previewURL,omitempty"`
	Message      string `json:"message,omitempty"`
}

type Manager struct {
	outDir  string
	host    string
	port    int
	key     string
	preset  func() video.QualityPreset
	latency func() LatencyProfile
	cb      Callbacks

	mu                    sync.Mutex
	running               bool
	stop                  context.CancelFunc
	done                  chan struct{}
	status                Status
	current               *Session
	sink                  *lhlsSink
	mtx                   *mediaMTXRuntime
	rtspGate              *rtspGate
	rtspEndpoint          *RTSPEndpoint
	listenerGeneration    uint64
	mediaGeneration       uint64
	latestMediaGeneration uint64

	// Test seams keep restart ownership deterministic without spawning tools.
	loopRunner            func(context.Context, uint64)
	beforeSessionFinalize func(Session)
}

type Session struct {
	ID             string
	Generation     uint64 `json:"-"`
	Title          string
	PlaylistName   string
	Recording      string
	HLSDirectory   string
	Published      bool
	StartedAt      time.Time
	FinishedAt     time.Time
	ActiveContract *OBSActiveSessionContract
}

// OBSActiveSessionContract freezes every desired setting consumed by one OBS
// media session. It is captured after a stream is accepted, never at listener
// start, so later settings changes only affect the next accepted stream.
type OBSActiveSessionContract struct {
	SessionID           string                    `json:"sessionId"`
	IngestURL           string                    `json:"ingestUrl"`
	StreamKey           string                    `json:"streamKey"`
	Port                int                       `json:"port"`
	LatencyProfile      LatencyProfile            `json:"latencyProfile"`
	QualityPreset       video.QualityPreset       `json:"qualityPreset"`
	VideoEncoderProfile video.VideoEncoderProfile `json:"videoEncoderProfile"`
}

func cloneSession(session Session) Session {
	copy := session
	if session.ActiveContract != nil {
		contract := *session.ActiveContract
		copy.ActiveContract = &contract
	}
	return copy
}

type ConnectionStatus struct {
	IP         string  `json:"ip"`
	Protocol   string  `json:"protocol"`
	Device     string  `json:"device"`
	State      string  `json:"state"`
	Quality    string  `json:"quality"`
	LagSeconds float64 `json:"lagSeconds"`
	LagLevel   string  `json:"lagLevel"`
	Note       string  `json:"note,omitempty"`
}

type Status struct {
	Enabled        bool                      `json:"enabled"`
	Listening      bool                      `json:"listening"`
	Connected      bool                      `json:"connected"`
	ServerAddress  string                    `json:"serverAddress"`
	StreamKey      string                    `json:"streamKey"`
	Port           int                       `json:"port"`
	MediaID        string                    `json:"mediaID,omitempty"`
	PreviewURL     string                    `json:"previewURL,omitempty"`
	RTSPTURL       string                    `json:"rtsptURL,omitempty"`
	Publishing     bool                      `json:"publishing"`
	Latency        LatencyProfile            `json:"latency"`
	Capabilities   []LatencyCapability       `json:"capabilities,omitempty"`
	Connections    []ConnectionStatus        `json:"connections,omitempty"`
	Message        string                    `json:"message"`
	StartedAt      time.Time                 `json:"startedAt,omitempty"`
	FinishedAt     time.Time                 `json:"finishedAt,omitempty"`
	EncoderName    string                    `json:"encoderName,omitempty"`
	HardwareEncode bool                      `json:"hardwareEncode"`
	ActiveSession  *OBSActiveSessionContract `json:"activeSession,omitempty"`
}

func New(outDir, host string, port int, key string, preset func() video.QualityPreset, latency func() LatencyProfile, cb Callbacks) *Manager {
	if port <= 0 {
		port = 1935
	}
	key = strings.TrimSpace(key)
	return &Manager{
		outDir:  outDir,
		host:    host,
		port:    port,
		key:     key,
		preset:  preset,
		latency: latency,
		cb:      cb,
		status: Status{
			Port:          port,
			ServerAddress: serverAddress(host, port),
			StreamKey:     key,
			Latency:       NormalizeLatencyProfile("auto"),
			Message:       "OBS RTMP受信は停止中です。",
		},
	}
}

func (m *Manager) Start() {
	m.mu.Lock()
	if strings.TrimSpace(m.key) == "" {
		m.status.Enabled = false
		m.status.Listening = false
		m.status.Message = "OBS受信はストリームキー未設定のため無効です。"
		m.mu.Unlock()
		return
	}
	if m.running {
		m.status.Enabled = true
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.listenerGeneration++
	generation := m.listenerGeneration
	m.running = true
	m.stop = cancel
	m.done = done
	m.status.Enabled = true
	m.status.Listening = true
	m.status.Message = "OBS RTMP受信は配信入力を待っています。"
	m.mu.Unlock()

	runner := m.loopRunner
	if runner == nil {
		runner = m.loop
	}
	go func() {
		defer close(done)
		runner(ctx, generation)
	}()
}

func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.stop
	m.listenerGeneration++ // revoke any old finalizer before cancellation returns.
	m.running = false
	m.stop = nil
	m.done = nil
	m.status.Enabled = false
	m.status.Listening = false
	m.status.Connected = false
	m.status.MediaID = ""
	m.status.RTSPTURL = ""
	m.status.Publishing = false
	m.status.Message = "OBS RTMP受信は停止中です。"
	m.current = nil
	m.sink = nil
	m.mtx = nil
	m.rtspGate = nil
	m.rtspEndpoint = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) Restart(timeout time.Duration) {
	m.StopAndWait(timeout)
	m.Start()
}

func (m *Manager) StartPublishing() bool {
	var session *Session
	var endpoint *RTSPEndpoint
	started := false
	m.mu.Lock()
	m.status.Publishing = true
	m.status.Message = "OBS publishing is armed. Waiting for a stream."
	if m.current != nil && m.status.Connected {
		started = true
		if !m.current.Published {
			m.current.Published = true
			copy := cloneSession(*m.current)
			session = &copy
		}
		if m.rtspEndpoint != nil && m.rtspEndpoint.SessionID == m.current.ID {
			copy := *m.rtspEndpoint
			endpoint = &copy
		}
		m.status.Message = "OBS stream is being published to HLS."
	}
	m.mu.Unlock()
	if session != nil && m.cb.OnStart != nil {
		m.cb.OnStart(*session)
	}
	if endpoint != nil && m.cb.OnRTSPReady != nil {
		m.cb.OnRTSPReady(*endpoint)
	}
	return started
}

func (m *Manager) SetRTSPURL(sessionID, publicURL, message string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != sessionID ||
		!m.currentSessionUsesRTSPTLocked() {
		return false
	}
	m.status.RTSPTURL = publicURL
	m.status.Message = message
	return true
}

// SetRTSPEndpointURL applies a server publication update only to the exact
// accepted media session that produced the endpoint.
func (m *Manager) SetRTSPEndpointURL(endpoint RTSPEndpoint, publicURL, message string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != endpoint.SessionID || m.current.Generation != endpoint.Generation || !m.currentSessionUsesRTSPTLocked() {
		return false
	}
	m.status.RTSPTURL = publicURL
	m.status.Message = message
	return true
}

func (m *Manager) setRTSPEndpoint(endpoint RTSPEndpoint) bool {
	m.mu.Lock()
	if m.current == nil || m.current.ID != endpoint.SessionID ||
		!m.currentSessionUsesRTSPTLocked() {
		m.mu.Unlock()
		return false
	}
	copy := endpoint
	copy.Generation = m.current.Generation
	m.rtspEndpoint = &copy
	m.status.RTSPTURL = ""
	m.status.Message = "RTSP TCPストリームを準備しました。外部公開を待っています。"
	publishing := m.status.Publishing
	m.mu.Unlock()
	if publishing && m.cb.OnRTSPReady != nil {
		m.cb.OnRTSPReady(copy)
	}
	return true
}

func (m *Manager) setRTSPEndpointForGeneration(endpoint RTSPEndpoint, generation uint64) bool {
	m.mu.Lock()
	if !m.ownsListenerGenerationLocked(generation) {
		m.mu.Unlock()
		return false
	}
	if m.current == nil || m.current.ID != endpoint.SessionID ||
		!m.currentSessionUsesRTSPTLocked() {
		m.mu.Unlock()
		return false
	}
	copy := endpoint
	copy.Generation = m.current.Generation
	m.rtspEndpoint = &copy
	m.status.RTSPTURL = ""
	m.status.Message = "RTSP TCPストリームを準備しました。外部公開を待っています。"
	publishing := m.status.Publishing
	m.mu.Unlock()
	if publishing && m.cb.OnRTSPReady != nil {
		m.cb.OnRTSPReady(copy)
	}
	return true
}

func (m *Manager) clearRTSPEndpoint(sessionID string) {
	m.mu.Lock()
	generation := m.listenerGeneration
	m.mu.Unlock()
	m.clearRTSPEndpointForGeneration(sessionID, generation)
}

func (m *Manager) clearRTSPEndpointForGeneration(sessionID string, generation uint64) {
	m.mu.Lock()
	if generation != m.listenerGeneration || m.rtspEndpoint == nil || m.rtspEndpoint.SessionID != sessionID {
		m.mu.Unlock()
		return
	}
	endpoint := *m.rtspEndpoint
	m.rtspEndpoint = nil
	m.status.RTSPTURL = ""
	m.mu.Unlock()
	if m.cb.OnRTSPDone != nil {
		m.cb.OnRTSPDone(endpoint)
	}
}

func (m *Manager) SetStreamKey(key string, timeout time.Duration) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	m.StopAndWait(timeout)
	m.mu.Lock()
	m.key = key
	m.status.StreamKey = key
	m.status.MediaID = ""
	m.status.RTSPTURL = ""
	m.status.Publishing = false
	m.current = nil
	m.mu.Unlock()
	m.Start()
}

func (m *Manager) StopAndWait(timeout time.Duration) {
	m.mu.Lock()
	cancel := m.stop
	done := m.done
	m.listenerGeneration++ // timeout may return, but the old generation stays revoked.
	m.running = false
	m.stop = nil
	m.done = nil
	m.status.Enabled = false
	m.status.Listening = false
	m.status.Connected = false
	m.status.MediaID = ""
	m.status.RTSPTURL = ""
	m.status.Publishing = false
	m.status.Message = "OBS RTMP受信を再起動しています。"
	m.current = nil
	m.sink = nil
	m.mtx = nil
	m.rtspGate = nil
	m.rtspEndpoint = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	if timeout <= 0 {
		<-done
		return
	}
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status
	if contract, ok := m.currentSessionContractLocked(); ok {
		copy := contract
		status.ActiveSession = &copy
		status.ServerAddress = serverAddress(m.host, contract.Port)
		status.StreamKey = contract.StreamKey
		status.Port = contract.Port
		status.Latency = contract.LatencyProfile
		return status
	}
	status.ServerAddress = serverAddress(m.host, m.port)
	status.StreamKey = m.key
	status.Port = m.port
	status.Latency = m.currentLatency()
	return status
}

func (m *Manager) ConnectionRows(timeout time.Duration) []ConnectionStatus {
	if timeout <= 0 {
		timeout = 250 * time.Millisecond
	}
	m.mu.Lock()
	runtime := m.mtx
	connected := m.status.Connected
	profile := NormalizeLatencyProfile("auto")
	if contract, ok := m.currentSessionContractLocked(); ok {
		profile = contract.LatencyProfile
	} else {
		profile = m.currentLatency()
	}
	path := ""
	if runtime != nil {
		path = runtime.cfg.Path
	}
	m.mu.Unlock()
	if !connected || runtime == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runtime.connectionRows(ctx, path, profile)
}

func (m *Manager) loop(ctx context.Context, generation uint64) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := m.runOne(ctx, generation); err != nil && ctx.Err() == nil {
			m.setStatusForGeneration(generation, func(status *Status) {
				status.Listening = false
				status.Connected = false
				status.MediaID = ""
				status.RTSPTURL = ""
				status.Message = err.Error()
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (m *Manager) runOne(parent context.Context, generation uint64) error {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return err
	}
	return m.runOneWithEncoder(parent, ffmpeg, video.VideoEncoderProfile{}, generation)
}

func (m *Manager) runOneWithEncoder(parent context.Context, ffmpeg string, encoder video.VideoEncoderProfile, generation uint64) error {
	contract := m.captureOBSActiveSessionContract(sessionID(), encoder)
	if contract.VideoEncoderProfile.Name == "" {
		purpose := contract.LatencyProfile.encoderPurpose()
		contract.VideoEncoderProfile = video.VideoEncoderProfile{Name: "copy", Purpose: purpose}
		if contract.LatencyProfile.Reencode {
			contract.VideoEncoderProfile = video.SelectVideoEncoder(parent, ffmpeg, purpose)
		}
	}
	err := m.runOneWithContract(parent, ffmpeg, contract, generation)
	if err == nil || !contract.VideoEncoderProfile.Hardware || parent.Err() != nil {
		return err
	}
	contract.VideoEncoderProfile = video.CPUVideoEncoder(contract.LatencyProfile.encoderPurpose())
	return m.runOneWithContract(parent, ffmpeg, contract, generation)
}

func (m *Manager) runOneWithContract(parent context.Context, ffmpeg string, contract OBSActiveSessionContract, generation uint64) error {
	if err := os.MkdirAll(m.outDir, 0700); err != nil {
		return err
	}

	id := contract.SessionID
	title := "OBS " + time.Now().Format("2006-01-02 15:04:05")
	recording := filepath.Join(m.outDir, "obs-recording-"+id+".mp4")
	publishArmed := m.isPublishingArmed()
	session := Session{
		ID:             id,
		Title:          title,
		PlaylistName:   video.PlaylistName(id),
		Recording:      recording,
		Published:      publishArmed,
		ActiveContract: &contract,
	}
	_ = os.Remove(filepath.Join(m.outDir, session.PlaylistName))
	matches, _ := filepath.Glob(filepath.Join(m.outDir, "current-"+id+"-*.ts"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	_ = os.Remove(recording)

	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	defer close(done)
	preset := contract.QualityPreset
	video.BeginExternalHLS(m.outDir, id, preset, cancel, done)
	defer video.EndExternalHLS(m.outDir, done)

	latency := contract.LatencyProfile
	lhls := latency.Transport == LatencyModeLHLS || latency.Mode == LatencyModeLHLS
	sidecar := latency.Transport == LatencyModeRTSPT || latency.Transport == LatencyModeLLHLS || latency.Mode == LatencyModeLLHLS || latency.Mode == LatencyModeRTSPT
	var rtsptURL string
	var rtspEndpoint *RTSPEndpoint
	var mediaMTXHLSDir string

	var args []string
	ready := func() bool { return fileExists(filepath.Join(m.outDir, session.PlaylistName)) }
	switch {
	case lhls:
		sink, err := newLHLSSink(m.outDir, id, lhlsSinkMaxBytes)
		if err != nil {
			cancel()
			return err
		}
		if err := sink.start(); err != nil {
			cancel()
			_ = sink.close()
			return err
		}
		if !m.installLHLSSinkForGeneration(generation, sink) {
			cancel()
			_ = sink.close()
			return nil
		}
		defer func() {
			m.mu.Lock()
			if m.ownsListenerGenerationLocked(generation) && m.sink == sink {
				m.sink = nil
			}
			m.mu.Unlock()
			_ = sink.close()
		}()
		// Only expose the public URL once #EXT-X-PREFETCH, the init segment,
		// and one media segment exist; a half-formed LHLS output stays hidden.
		ready = sink.ready
		args = m.ffmpegLHLSArgsForContract(contract, recording, sink.baseURL()+"/stream.mpd")
	case sidecar:
		mtxExe, err := EnsureMediaMTX(parent)
		if err != nil {
			cancel()
			return err
		}
		ports, err := allocMediaMTXPorts()
		if err != nil {
			cancel()
			return err
		}
		user, pass, err := mediaMTXCredential()
		if err != nil {
			cancel()
			return err
		}
		hlsVariant := ""
		hlsAlwaysRemux := false
		if latency.Transport == LatencyModeRTSPT {
			mediaMTXHLSDir = filepath.Join(m.outDir, "mediamtx-hls-"+id)
			session.HLSDirectory = mediaMTXHLSDir
			_ = os.RemoveAll(mediaMTXHLSDir)
			hlsVariant = "mpegts"
			hlsAlwaysRemux = true
		}
		runtime := newMediaMTXRuntime(mtxExe, mediaMTXSessionConfig{
			Path:           mediaMTXPathName(id),
			PublishUser:    user,
			PublishPass:    pass,
			Ports:          ports,
			AdvertiseHost:  m.host,
			DebugLogPath:   mediaMTXDebugLogPath(),
			HLSVariant:     hlsVariant,
			HLSAlwaysRemux: hlsAlwaysRemux,
			HLSDirectory:   mediaMTXHLSDir,
		})
		if err := runtime.start(ctx); err != nil {
			cancel()
			return err
		}
		var gate *rtspGate
		if latency.Transport == LatencyModeRTSPT {
			gate = newRTSPGate(rtspGateConfig{
				PublicRTSPPort:  ports.RTSP,
				PublicRTPPort:   ports.RTP,
				PublicRTCPPort:  ports.RTCP,
				BackendRTSPPort: ports.mediaMTXRTSPPort(),
				Path:            mediaMTXPathName(id),
			})
			if err := gate.start(ctx); err != nil {
				_ = runtime.stop(5 * time.Second)
				cancel()
				return err
			}
		}
		if !m.installRTSPRuntimeForGeneration(generation, runtime, gate) {
			if gate != nil {
				_ = gate.stop()
			}
			_ = runtime.stop(5 * time.Second)
			cancel()
			return nil
		}
		// Ordered shutdown: FFmpeg is cancelled via ctx and its exit is awaited
		// before this deferred stop runs, so the owned MediaMTX process is only
		// stopped after its publisher has disconnected and the path is removed.
		defer func() {
			m.clearRTSPEndpointForGeneration(id, generation)
			m.mu.Lock()
			if m.ownsListenerGenerationLocked(generation) && m.mtx == runtime {
				m.mtx = nil
			}
			if m.ownsListenerGenerationLocked(generation) && m.rtspGate == gate {
				m.rtspGate = nil
			}
			m.mu.Unlock()
			if gate != nil {
				_ = gate.stop()
			}
			_ = runtime.stop(5 * time.Second)
			if mediaMTXHLSDir != "" {
				_ = os.RemoveAll(mediaMTXHLSDir)
			}
		}()
		rtsptURL = runtime.rtspURL()
		if latency.Transport == LatencyModeRTSPT {
			rtspEndpoint = &RTSPEndpoint{
				SessionID: id,
				Host:      runtime.cfg.AdvertiseHost,
				Port:      runtime.cfg.Ports.RTSP,
				RTPPort:   runtime.cfg.Ports.RTP,
				RTCPPort:  runtime.cfg.Ports.RTCP,
				Path:      runtime.cfg.Path,
				LocalURL:  rtsptURL,
			}
		}
		if latency.Transport == LatencyModeLLHLS || latency.Mode == LatencyModeLLHLS {
			ready = func() bool {
				rc, rcancel := context.WithTimeout(ctx, 2*time.Second)
				defer rcancel()
				return runtime.hlsReady(rc, contract.LatencyProfile)
			}
		} else {
			ready = func() bool {
				rc, rcancel := context.WithTimeout(ctx, 2*time.Second)
				defer rcancel()
				return runtime.pathReady(rc)
			}
		}
		args = m.ffmpegRTSPArgsForContract(contract, recording, runtime.publishURL())
	default:
		args = m.ffmpegArgsForContract(contract, recording)
	}
	cmd := exec.Command(ffmpeg, args...)
	cmd.Dir = m.outDir
	hideWindow(cmd)
	stdin, _ := cmd.StdinPipe()

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("OBS RTMP受信の開始に失敗しました: %w", err)
	}
	untrack := video.TrackStartedFFmpeg(cmd)
	errCh := make(chan error, 1)
	waitDone := make(chan struct{})
	go func() {
		defer untrack()
		errCh <- cmd.Wait()
		close(waitDone)
	}()
	go func() {
		<-ctx.Done()
		if stdin != nil {
			_, _ = io.WriteString(stdin, "q\n")
			_ = stdin.Close()
		}
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	}()
	if !m.setStatusForGeneration(generation, func(status *Status) {
		status.Listening = true
		status.Connected = false
		status.MediaID = ""
		status.Message = "OBS RTMP受信は配信入力を待っています。"
		status.EncoderName = contract.VideoEncoderProfile.Name
		status.HardwareEncode = contract.VideoEncoderProfile.Hardware
	}) {
		cancel()
		<-errCh
		return nil
	}

	started, waitErr := m.waitForStart(ctx, generation, session, errCh, ready)
	if waitErr != nil {
		cancel()
		return waitErr
	}
	if started && sidecar {
		if latency.Transport == LatencyModeRTSPT {
			if rtspEndpoint != nil {
				m.setRTSPEndpointForGeneration(*rtspEndpoint, generation)
			}
		} else {
			m.setStatusForGeneration(generation, func(status *Status) {
				status.Message = "LL-HLSストリームを準備しました。"
			})
		}
	}
	processErr := <-errCh
	cancel()

	if started {
		session.FinishedAt = time.Now()
		if !lhls && !sidecar {
			// LHLS and the MediaMTX sidecar modes have no on-disk HLS playlist
			// to convert; their VOD is the separately recorded MP4.
			_ = video.FinalizeHLSPlaylist(m.outDir, id)
		} else if sidecar && mediaMTXHLSDir != "" {
			_, _ = importMediaMTXHLS(m.outDir, id, mediaMTXHLSDir, mediaMTXPathName(id))
		}
		m.finalizeAcceptedSession(&session, generation)
	}
	if parent.Err() != nil {
		return nil
	}
	if processErr != nil {
		return fmt.Errorf("OBS RTMP受信が停止しました: %w", processErr)
	}
	return nil
}

func (m *Manager) waitForStart(ctx context.Context, generation uint64, session Session, errCh <-chan error, ready func() bool) (bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case err := <-errCh:
			if err != nil {
				return false, fmt.Errorf("配信接続前にOBS RTMP受信が停止しました: %w", err)
			}
			return false, fmt.Errorf("配信接続前にOBS RTMP受信が停止しました")
		case <-ticker.C:
			if ready == nil || !ready() {
				continue
			}
			session.StartedAt = time.Now()
			if _, accepted := m.acceptSession(&session, generation); !accepted {
				return false, nil
			}
			return true, nil
		}
	}
}

func (m *Manager) ffmpegArgs(id, recording string, preset video.QualityPreset) []string {
	return m.ffmpegArgsWithEncoder(id, recording, preset, video.CPUVideoEncoder(m.currentLatency().encoderPurpose()))
}

func (m *Manager) ffmpegArgsWithEncoder(id, recording string, preset video.QualityPreset, encoder video.VideoEncoderProfile) []string {
	return m.ffmpegArgsForContract(m.argumentContract(id, preset, encoder), recording)
}

func (m *Manager) ffmpegArgsForContract(contract OBSActiveSessionContract, recording string) []string {
	id := contract.SessionID
	inputURL := contract.IngestURL
	latency := contract.LatencyProfile
	preset := scaledLatencyPreset(contract.QualityPreset, latency.BitrateMultiplier)
	encoder := contract.VideoEncoderProfile
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-listen", "1",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-analyzeduration", "100000",
		"-probesize", "32768",
		"-i", inputURL,
	}
	if !latency.Reencode {
		args = append(args,
			"-map", "0:v:0",
			"-map", "0:a:0?",
			"-c", "copy",
			"-f", "hls",
			"-hls_time", latency.SegmentSeconds,
			"-hls_list_size", latency.ListSize,
			"-hls_playlist_type", "event",
			"-hls_flags", "independent_segments",
			"-hls_segment_filename", video.SegmentPattern(id),
			video.PlaylistName(id),
			"-map", "0:v:0",
			"-map", "0:a:0?",
			"-c", "copy",
			"-movflags", "+faststart",
			recording,
		)
		return args
	}
	scaleFilter := "scale=w='min(1920,iw)':h='min(" + strconv.Itoa(preset.Height) + ",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,pad=ceil(iw/2)*2:ceil(ih/2)*2"
	args = append(args,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-vf", scaleFilter,
		"-r", latency.FrameRate,
	)
	encoderPreset := preset
	encoderPreset.BufferSize = preset.VideoBitrate
	args = append(args, encoder.FFmpegArgs(encoderPreset, "ultrafast")...)
	args = append(args,
		"-g", latency.GOPFrames,
		"-keyint_min", latency.GOPFrames,
		"-sc_threshold", "0",
		"-force_key_frames", "expr:gte(t,n_forced*"+latency.SegmentSeconds+")",
	)
	if !encoder.Hardware {
		args = append(args,
			"-b:v", preset.VideoBitrate,
			"-maxrate", preset.MaxRate,
			"-bufsize", preset.VideoBitrate,
		)
	}
	args = append(args,
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ar", "48000",
		"-ac", "2",
		"-flush_packets", "1",
		"-f", "hls",
		"-hls_time", latency.SegmentSeconds,
		"-hls_list_size", latency.ListSize,
		"-hls_playlist_type", "event",
		"-hls_allow_cache", "0",
		"-hls_flags", "independent_segments+program_date_time",
		"-hls_segment_filename", video.SegmentPattern(id),
		video.PlaylistName(id),
	)
	args = append(args,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-c", "copy",
		"-movflags", "+faststart",
		recording,
	)
	return args
}

// ffmpegLHLSArgs encodes the RTMP input to community LHLS (fMP4 with
// #EXT-X-PREFETCH) via FFmpeg's DASH muxer, streamed over HTTP PUT to the
// private loopback sink at output. A separate copy output records the MP4 VOD.
// Plain file output suppresses the prefetch tag, so HTTP output is required.
func (m *Manager) ffmpegLHLSArgs(id, recording, output string, preset video.QualityPreset, encoder video.VideoEncoderProfile) []string {
	return m.ffmpegLHLSArgsForContract(m.argumentContract(id, preset, encoder), recording, output)
}

func (m *Manager) ffmpegLHLSArgsForContract(contract OBSActiveSessionContract, recording, output string) []string {
	inputURL := contract.IngestURL
	latency := contract.LatencyProfile
	preset := scaledLatencyPreset(contract.QualityPreset, latency.BitrateMultiplier)
	encoder := contract.VideoEncoderProfile
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-listen", "1",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-analyzeduration", "100000",
		"-probesize", "32768",
		"-i", inputURL,
	}
	scaleFilter := "scale=w='min(1920,iw)':h='min(" + strconv.Itoa(preset.Height) + ",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,pad=ceil(iw/2)*2:ceil(ih/2)*2"
	args = append(args,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-vf", scaleFilter,
		"-r", latency.FrameRate,
	)
	encoderPreset := preset
	encoderPreset.BufferSize = preset.VideoBitrate
	args = append(args, encoder.FFmpegArgs(encoderPreset, "ultrafast")...)
	args = append(args,
		"-g", latency.GOPFrames,
		"-keyint_min", latency.GOPFrames,
		"-sc_threshold", "0",
		"-force_key_frames", "expr:gte(t,n_forced*"+latency.SegmentSeconds+")",
	)
	if !encoder.Hardware {
		// Explicit bitrates so the LHLS master playlist carries usable stream
		// metadata (BANDWIDTH). Hardware encoders set their own rate control.
		args = append(args,
			"-b:v", preset.VideoBitrate,
			"-maxrate", preset.MaxRate,
			"-bufsize", preset.VideoBitrate,
		)
	}
	args = append(args,
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ar", "48000",
		"-ac", "2",
		"-flush_packets", "1",
		"-strict", "experimental",
		"-f", "dash",
		"-method", "PUT",
		"-streaming", "1",
		"-lhls", "1",
		"-hls_playlist", "1",
		"-seg_duration", "1",
		"-window_size", latency.ListSize,
		output,
	)
	args = append(args,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-c", "copy",
		"-movflags", "+faststart",
		recording,
	)
	return args
}

// ffmpegRTSPArgs encodes the RTMP input to H.264/AAC and publishes it to the
// app-owned MediaMTX path over RTSP/TCP at rtspURL. MediaMTX repackages the
// stream into LL-HLS and serves the RTSP/TCP read path. A separate copy output
// records the MP4 VOD.
func (m *Manager) ffmpegRTSPArgs(id, recording, rtspURL string, preset video.QualityPreset, encoder video.VideoEncoderProfile) []string {
	return m.ffmpegRTSPArgsForContract(m.argumentContract(id, preset, encoder), recording, rtspURL)
}

func (m *Manager) ffmpegRTSPArgsForContract(contract OBSActiveSessionContract, recording, rtspURL string) []string {
	inputURL := contract.IngestURL
	latency := contract.LatencyProfile
	preset := scaledLatencyPreset(contract.QualityPreset, latency.BitrateMultiplier)
	encoder := contract.VideoEncoderProfile
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-listen", "1",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-analyzeduration", "100000",
		"-probesize", "32768",
		"-i", inputURL,
	}
	scaleFilter := "scale=w='min(1920,iw)':h='min(" + strconv.Itoa(preset.Height) + ",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,pad=ceil(iw/2)*2:ceil(ih/2)*2"
	args = append(args,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-vf", scaleFilter,
		"-r", latency.FrameRate,
	)
	encoderPreset := preset
	encoderPreset.BufferSize = preset.VideoBitrate
	args = append(args, encoder.FFmpegArgs(encoderPreset, "ultrafast")...)
	args = append(args,
		"-g", latency.GOPFrames,
		"-keyint_min", latency.GOPFrames,
		"-sc_threshold", "0",
		"-force_key_frames", "expr:gte(t,n_forced*"+latency.SegmentSeconds+")",
	)
	if !encoder.Hardware {
		args = append(args,
			"-b:v", preset.VideoBitrate,
			"-maxrate", preset.MaxRate,
			"-bufsize", preset.VideoBitrate,
		)
	}
	args = append(args,
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ar", "48000",
		"-ac", "2",
		"-flush_packets", "1",
		"-muxdelay", "0",
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		"-pkt_size", "1200",
		rtspURL,
	)
	args = append(args,
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-c", "copy",
		"-movflags", "+faststart",
		recording,
	)
	return args
}

// LHLSPublicFile resolves an allowlisted public LHLS artifact for the active
// session to an absolute path. ok is false unless id is the connected session,
// an LHLS sink is live, name passes the sink allowlist, and the file exists.
func (m *Manager) LHLSPublicFile(id, name string) (string, bool) {
	m.mu.Lock()
	sink := m.sink
	active := m.current != nil && m.current.ID == id
	m.mu.Unlock()
	if !active || sink == nil || !sink.publicReadable(name) {
		return "", false
	}
	full := filepath.Join(sink.dir, name)
	if !fileExists(full) {
		return "", false
	}
	return full, true
}

// ProxyLLHLS forwards a public HLS request to the active session's MediaMTX
// sidecar. It returns true when MediaMTX owns the active transport and id is
// the connected session, so the HLS-family handlers do not fall through to the
// generated-file path.
func (m *Manager) ProxyLLHLS(w http.ResponseWriter, r *http.Request, id, name string) bool {
	m.mu.Lock()
	runtime := m.mtx
	active := m.current != nil && m.current.ID == id
	contract, hasContract := m.currentSessionContractLocked()
	m.mu.Unlock()
	transport := contract.LatencyProfile.Transport
	if !hasContract {
		transport = m.currentLatency().Transport
	}
	if transport != LatencyModeLLHLS && transport != LatencyModeRTSPT {
		return false
	}
	if !active || runtime == nil {
		return false
	}
	runtime.proxyHLS(w, r, mediaMTXHLSName(id, name))
	return true
}

func (m *Manager) HLSPreviewReady(id, name string) bool {
	m.mu.Lock()
	runtime := m.mtx
	active := m.current != nil && m.current.ID == id
	contract, hasContract := m.currentSessionContractLocked()
	m.mu.Unlock()
	transport := contract.LatencyProfile.Transport
	if !hasContract {
		transport = m.currentLatency().Transport
	}
	if transport == LatencyModeLLHLS || transport == LatencyModeRTSPT {
		if !active || runtime == nil {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		return runtime.hlsArtifactReady(ctx, mediaMTXHLSName(id, name))
	}
	path := filepath.Join(m.outDir, video.PlaylistName(id))
	return fileExists(path)
}

func mediaMTXHLSName(id, name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "." || trimmed == "/" {
		return "index.m3u8"
	}
	clean := filepath.Base(trimmed)
	if clean == "." || clean == "/" || clean == "\\" || clean == "current.m3u8" || clean == video.PlaylistName(id) {
		return "index.m3u8"
	}
	return clean
}

func EnableDVR(profile LatencyProfile) LatencyProfile {
	profile = normalizeLatencyProfile(profile)
	if profile.DVRListSize != "" {
		profile.ListSize = profile.DVRListSize
	}
	profile.DVR = true
	if profile.Message == "" {
		profile.Message = "DVR is enabled. Up to 30 minutes of HLS segments are kept for players that support live seeking."
	}
	return profile
}

func (m *Manager) currentPreset() video.QualityPreset {
	if m.preset == nil {
		return video.ResolveQuality("auto", 0)
	}
	return m.preset()
}

func (m *Manager) captureOBSActiveSessionContract(id string, encoder video.VideoEncoderProfile) OBSActiveSessionContract {
	m.mu.Lock()
	port := m.port
	key := m.key
	preset := m.preset
	latency := m.latency
	m.mu.Unlock()

	profile := NormalizeLatencyProfile("auto")
	if latency != nil {
		profile = normalizeLatencyProfile(latency())
	}
	quality := video.ResolveQuality("auto", 0)
	if preset != nil {
		quality = preset()
	}
	return OBSActiveSessionContract{
		SessionID:           id,
		IngestURL:           fmt.Sprintf("rtmp://0.0.0.0:%d/live/%s", port, key),
		StreamKey:           key,
		Port:                port,
		LatencyProfile:      profile,
		QualityPreset:       quality,
		VideoEncoderProfile: encoder,
	}
}

func (m *Manager) argumentContract(id string, preset video.QualityPreset, encoder video.VideoEncoderProfile) OBSActiveSessionContract {
	profile := m.currentLatency()
	return OBSActiveSessionContract{
		SessionID:           id,
		IngestURL:           fmt.Sprintf("rtmp://0.0.0.0:%d/live/%s", m.port, m.key),
		StreamKey:           m.key,
		Port:                m.port,
		LatencyProfile:      profile,
		QualityPreset:       preset,
		VideoEncoderProfile: encoder,
	}
}

func (m *Manager) currentSessionContractLocked() (OBSActiveSessionContract, bool) {
	if m.current == nil || m.current.ActiveContract == nil || m.current.ActiveContract.SessionID != m.current.ID {
		return OBSActiveSessionContract{}, false
	}
	return *m.current.ActiveContract, true
}

func (m *Manager) currentSessionUsesRTSPTLocked() bool {
	if contract, ok := m.currentSessionContractLocked(); ok {
		return contract.LatencyProfile.Transport == LatencyModeRTSPT
	}
	return m.currentLatency().Transport == LatencyModeRTSPT
}

func (m *Manager) currentLatency() LatencyProfile {
	if m.latency == nil {
		return NormalizeLatencyProfile("auto")
	}
	return normalizeLatencyProfile(m.latency())
}

func NormalizeLatencyProfile(mode string) LatencyProfile {
	mode = NormalizeLatencyMode(mode)
	if profile, ok := latencyProfiles[mode]; ok {
		return profile
	}
	return latencyProfiles[LatencyModeHLS]
}

func ResolveLatencyProfile(mode string, uploadMbps int) LatencyProfile {
	_ = uploadMbps
	return NormalizeLatencyProfile(mode)
}

func normalizeLatencyProfile(profile LatencyProfile) LatencyProfile {
	normalized := NormalizeLatencyProfile(profile.Mode)
	if profile.DVR {
		if normalized.DVRListSize != "" {
			normalized.ListSize = normalized.DVRListSize
		}
		normalized.DVR = true
	} else {
		normalized.DVR = false
	}
	if profile.Message != "" {
		normalized.Message = profile.Message
	}
	return normalized
}

func NormalizeLatencyMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if alias, ok := legacyLatencyModeAliases[mode]; ok {
		return alias
	}
	switch mode {
	case LatencyModeHLSHigh, LatencyModeHLS, LatencyModeRTSPLow, LatencyModeRTSPUltra, LatencyModeRTSPRealtime:
		return mode
	default:
		return LatencyModeHLS
	}
}

func LatencyCapabilities() []LatencyCapability {
	result := make([]LatencyCapability, 0, len(latencyProfiles))
	for _, mode := range []string{LatencyModeHLSHigh, LatencyModeHLS, LatencyModeRTSPLow, LatencyModeRTSPUltra, LatencyModeRTSPRealtime} {
		profile := NormalizeLatencyProfile(mode)
		result = append(result, profile.Capability())
	}
	return result
}

func (p LatencyProfile) Capability() LatencyCapability {
	return LatencyCapability{
		Mode:         p.Mode,
		Label:        p.Label,
		Transport:    p.Transport,
		Experimental: p.Experimental,
		Available:    p.Available,
		Selectable:   p.Selectable,
		PreviewURL:   p.PreviewURL,
		Message:      p.Message,
	}
}

func (p LatencyProfile) encoderPurpose() video.EncoderPurpose {
	if p.EncoderPurpose != "" {
		return p.EncoderPurpose
	}
	return video.EncoderLowLatency
}

func scaledLatencyPreset(preset video.QualityPreset, multiplier int) video.QualityPreset {
	if multiplier == 0 {
		return video.ResolveQuality("720", 0)
	}
	if multiplier <= 1 {
		return preset
	}
	preset.VideoBitrate = scaleStreamingBitrate(preset.VideoBitrate, multiplier)
	preset.MaxRate = scaleStreamingBitrate(preset.MaxRate, multiplier)
	preset.BufferSize = scaleStreamingBitrate(preset.BufferSize, multiplier)
	return preset
}

func scaleStreamingBitrate(value string, multiplier int) string {
	if value == "" || multiplier <= 1 {
		return value
	}
	value = strings.TrimSpace(value)
	unit := ""
	number := value
	if len(value) > 0 {
		last := value[len(value)-1]
		if last < '0' || last > '9' {
			unit = value[len(value)-1:]
			number = value[:len(value)-1]
		}
	}
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 {
		return value
	}
	return strconv.Itoa(n*multiplier) + unit
}

func (m *Manager) isPublishingArmed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status.Publishing
}

func (m *Manager) acceptSession(session *Session, generation uint64) (bool, bool) {
	m.mu.Lock()
	if generation != m.listenerGeneration || !m.running || session == nil {
		m.mu.Unlock()
		return false, false
	}
	armed := m.status.Publishing
	m.mediaGeneration++
	session.Generation = m.mediaGeneration
	m.latestMediaGeneration = session.Generation
	session.Published = armed
	copy := cloneSession(*session)
	m.current = &copy
	m.status.Listening = true
	m.status.Connected = true
	m.status.MediaID = session.ID
	m.status.Publishing = armed
	m.status.StartedAt = session.StartedAt
	if armed {
		m.status.Message = "OBS stream is being published to HLS."
	} else {
		m.status.Message = "OBS stream is connected. Press publish to share it."
	}
	callback := m.cb.OnStart
	callbackSession := cloneSession(*session)
	m.mu.Unlock()
	if armed && callback != nil {
		callback(callbackSession)
	}
	return armed, true
}

func (m *Manager) finalizeAcceptedSession(session *Session, generation uint64) bool {
	m.mu.Lock()
	hook := m.beforeSessionFinalize
	m.mu.Unlock()
	if hook != nil && session != nil {
		hook(cloneSession(*session))
	}

	m.mu.Lock()
	if session == nil || generation != m.listenerGeneration || m.current == nil || m.current.ID != session.ID {
		m.mu.Unlock()
		return false
	}
	session.Published = session.Published || m.current.Published
	session.Generation = m.current.Generation
	m.status.Listening = true
	m.status.Connected = false
	m.status.MediaID = ""
	m.status.RTSPTURL = ""
	m.status.Publishing = false
	m.status.FinishedAt = session.FinishedAt
	m.status.Message = "OBS stream ended. Recording finalized as VOD."
	m.current = nil
	callback := m.cb.OnDone
	callbackSession := cloneSession(*session)
	m.mu.Unlock()
	if session.Published && callback != nil {
		callback(callbackSession)
	}
	return true
}

// IsSessionActive reports whether a notification still belongs to the active
// accepted media session. It is safe for callbacks to call without blocking
// listener restart or another accepted session.
func (m *Manager) IsSessionActive(sessionID string, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return generation != 0 && m.current != nil && m.current.ID == sessionID && m.current.Generation == generation
}

// IsLatestSession reports whether no newer accepted media session has replaced
// this notification. Terminal callbacks use this after expensive work.
func (m *Manager) IsLatestSession(sessionID string, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return generation != 0 && m.latestMediaGeneration == generation && (m.current == nil || m.current.ID == sessionID || m.current.Generation == generation)
}

func (m *Manager) setStatus(fn func(*Status)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.status)
}

func (m *Manager) ownsListenerGenerationLocked(generation uint64) bool {
	return generation != 0 && generation == m.listenerGeneration && m.running
}

func (m *Manager) setStatusForGeneration(generation uint64, fn func(*Status)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsListenerGenerationLocked(generation) {
		return false
	}
	fn(&m.status)
	return true
}

func (m *Manager) installLHLSSinkForGeneration(generation uint64, sink *lhlsSink) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsListenerGenerationLocked(generation) {
		return false
	}
	m.sink = sink
	return true
}

func (m *Manager) installRTSPRuntimeForGeneration(generation uint64, runtime *mediaMTXRuntime, gate *rtspGate) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsListenerGenerationLocked(generation) {
		return false
	}
	m.mtx = runtime
	m.rtspGate = gate
	return true
}

func serverAddress(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("rtmp://%s:%d/live", host, port)
}

func sessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

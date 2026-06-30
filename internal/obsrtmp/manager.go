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
	OnRTSPDone  func(sessionID string)
}

type RTSPEndpoint struct {
	SessionID string
	Host      string
	Port      int
	RTPPort   int
	RTCPPort  int
	Path      string
	LocalURL  string
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
		EncoderPurpose:    video.EncoderStandard,
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
		BitrateMultiplier: 3,
		EncoderPurpose:    video.EncoderLowLatency,
		Message:           "最小遅延のRTSP出力です。",
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

	mu           sync.Mutex
	running      bool
	stop         context.CancelFunc
	done         chan struct{}
	status       Status
	current      *Session
	sink         *lhlsSink
	mtx          *mediaMTXRuntime
	rtspGate     *rtspGate
	rtspEndpoint *RTSPEndpoint
}

type Session struct {
	ID           string
	Title        string
	PlaylistName string
	Recording    string
	HLSDirectory string
	Published    bool
	StartedAt    time.Time
	FinishedAt   time.Time
}

type Status struct {
	Enabled        bool                `json:"enabled"`
	Listening      bool                `json:"listening"`
	Connected      bool                `json:"connected"`
	ServerAddress  string              `json:"serverAddress"`
	StreamKey      string              `json:"streamKey"`
	Port           int                 `json:"port"`
	MediaID        string              `json:"mediaID,omitempty"`
	PreviewURL     string              `json:"previewURL,omitempty"`
	RTSPTURL       string              `json:"rtsptURL,omitempty"`
	Publishing     bool                `json:"publishing"`
	Latency        LatencyProfile      `json:"latency"`
	Capabilities   []LatencyCapability `json:"capabilities,omitempty"`
	Message        string              `json:"message"`
	StartedAt      time.Time           `json:"startedAt,omitempty"`
	FinishedAt     time.Time           `json:"finishedAt,omitempty"`
	EncoderName    string              `json:"encoderName,omitempty"`
	HardwareEncode bool                `json:"hardwareEncode"`
}

func New(outDir, host string, port int, key string, preset func() video.QualityPreset, latency func() LatencyProfile, cb Callbacks) *Manager {
	if port <= 0 {
		port = 1935
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = "imagepad"
	}
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
			Message:       "OBS RTMP receiver is stopped.",
		},
	}
}

func (m *Manager) Start() {
	m.mu.Lock()
	if m.running {
		m.status.Enabled = true
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.running = true
	m.stop = cancel
	m.done = done
	m.status.Enabled = true
	m.status.Listening = true
	m.status.Message = "OBS RTMP receiver is waiting for a stream."
	m.mu.Unlock()

	go m.loop(ctx, done)
}

func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.stop
	m.running = false
	m.stop = nil
	m.done = nil
	m.status.Enabled = false
	m.status.Listening = false
	m.status.Connected = false
	m.status.MediaID = ""
	m.status.RTSPTURL = ""
	m.status.Publishing = false
	m.status.Message = "OBS RTMP receiver is stopped."
	m.current = nil
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
	m.mu.Lock()
	m.status.Publishing = true
	m.status.Message = "OBS publishing is armed. Waiting for a stream."
	if m.current != nil && m.status.Connected {
		if !m.current.Published {
			m.current.Published = true
			copy := *m.current
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
	if session != nil && endpoint != nil && m.cb.OnRTSPReady != nil {
		m.cb.OnRTSPReady(*endpoint)
	}
	return session != nil
}

func (m *Manager) SetRTSPURL(sessionID, publicURL, message string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != sessionID ||
		m.currentLatency().Transport != LatencyModeRTSPT {
		return false
	}
	m.status.RTSPTURL = publicURL
	m.status.Message = message
	return true
}

func (m *Manager) setRTSPEndpoint(endpoint RTSPEndpoint) bool {
	m.mu.Lock()
	if m.current == nil || m.current.ID != endpoint.SessionID ||
		m.currentLatency().Transport != LatencyModeRTSPT {
		m.mu.Unlock()
		return false
	}
	copy := endpoint
	m.rtspEndpoint = &copy
	m.status.RTSPTURL = endpoint.LocalURL
	m.status.Message = "RTSP TCP stream is ready."
	publishing := m.status.Publishing
	m.mu.Unlock()
	if publishing && m.cb.OnRTSPReady != nil {
		m.cb.OnRTSPReady(endpoint)
	}
	return true
}

func (m *Manager) clearRTSPEndpoint(sessionID string) {
	m.mu.Lock()
	if m.rtspEndpoint == nil || m.rtspEndpoint.SessionID != sessionID {
		m.mu.Unlock()
		return
	}
	m.rtspEndpoint = nil
	m.status.RTSPTURL = ""
	m.mu.Unlock()
	if m.cb.OnRTSPDone != nil {
		m.cb.OnRTSPDone(sessionID)
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
	m.running = false
	m.stop = nil
	m.done = nil
	m.status.Enabled = false
	m.status.Listening = false
	m.status.Connected = false
	m.status.MediaID = ""
	m.status.RTSPTURL = ""
	m.status.Publishing = false
	m.status.Message = "OBS RTMP receiver is restarting."
	m.current = nil
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
	status.ServerAddress = serverAddress(m.host, m.port)
	status.StreamKey = m.key
	status.Port = m.port
	status.Latency = m.currentLatency()
	return status
}

func (m *Manager) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := m.runOne(ctx); err != nil && ctx.Err() == nil {
			m.setStatus(func(status *Status) {
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

func (m *Manager) runOne(parent context.Context) error {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return err
	}
	latency := m.currentLatency()
	purpose := latency.encoderPurpose()
	selected := video.VideoEncoderProfile{Name: "copy", Purpose: purpose}
	if latency.Reencode {
		selected = video.SelectVideoEncoder(parent, ffmpeg, purpose)
	}
	err = m.runOneWithEncoder(parent, ffmpeg, selected)
	if err == nil || !selected.Hardware || parent.Err() != nil {
		return err
	}
	return m.runOneWithEncoder(parent, ffmpeg, video.CPUVideoEncoder(purpose))
}

func (m *Manager) runOneWithEncoder(parent context.Context, ffmpeg string, encoder video.VideoEncoderProfile) error {
	if err := os.MkdirAll(m.outDir, 0700); err != nil {
		return err
	}

	id := sessionID()
	title := "OBS " + time.Now().Format("2006-01-02 15:04:05")
	recording := filepath.Join(m.outDir, "obs-recording-"+id+".mp4")
	publishArmed := m.isPublishingArmed()
	session := Session{
		ID:           id,
		Title:        title,
		PlaylistName: video.PlaylistName(id),
		Recording:    recording,
		Published:    publishArmed,
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
	preset := m.currentPreset()
	video.BeginExternalHLS(m.outDir, id, preset, cancel, done)
	defer video.EndExternalHLS(m.outDir, done)

	latency := m.currentLatency()
	lhls := latency.Transport == LatencyModeLHLS
	sidecar := latency.Transport == LatencyModeLLHLS || latency.Transport == LatencyModeRTSPT
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
		m.mu.Lock()
		m.sink = sink
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			if m.sink == sink {
				m.sink = nil
			}
			m.mu.Unlock()
			_ = sink.close()
		}()
		// Only expose the public URL once #EXT-X-PREFETCH, the init segment,
		// and one media segment exist; a half-formed LHLS output stays hidden.
		ready = sink.ready
		args = m.ffmpegLHLSArgs(id, recording, sink.baseURL()+"/stream.mpd", preset, encoder)
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
		m.mu.Lock()
		m.mtx = runtime
		m.rtspGate = gate
		m.mu.Unlock()
		// Ordered shutdown: FFmpeg is cancelled via ctx and its exit is awaited
		// before this deferred stop runs, so the owned MediaMTX process is only
		// stopped after its publisher has disconnected and the path is removed.
		defer func() {
			m.clearRTSPEndpoint(id)
			m.mu.Lock()
			if m.mtx == runtime {
				m.mtx = nil
			}
			if m.rtspGate == gate {
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
		if latency.Transport == LatencyModeLLHLS {
			ready = func() bool {
				rc, rcancel := context.WithTimeout(ctx, 2*time.Second)
				defer rcancel()
				return runtime.llhlsReady(rc)
			}
		} else {
			ready = func() bool {
				rc, rcancel := context.WithTimeout(ctx, 2*time.Second)
				defer rcancel()
				return runtime.pathReady(rc)
			}
		}
		args = m.ffmpegRTSPArgs(id, recording, runtime.publishURL(), preset, encoder)
	default:
		args = m.ffmpegArgsWithEncoder(id, recording, preset, encoder)
	}
	cmd := exec.Command(ffmpeg, args...)
	cmd.Dir = m.outDir
	hideWindow(cmd)
	stdin, _ := cmd.StdinPipe()

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start OBS RTMP receiver: %w", err)
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
	m.setStatus(func(status *Status) {
		status.Listening = true
		status.Connected = false
		status.MediaID = ""
		status.Message = "OBS RTMP receiver is waiting for a stream."
		status.EncoderName = encoder.Name
		status.HardwareEncode = encoder.Hardware
	})

	started, waitErr := m.waitForStart(ctx, session, errCh, ready)
	if waitErr != nil {
		cancel()
		return waitErr
	}
	if started && sidecar {
		if latency.Transport == LatencyModeRTSPT {
			if rtspEndpoint != nil {
				m.setRTSPEndpoint(*rtspEndpoint)
			}
		} else {
			m.setStatus(func(status *Status) {
				status.Message = "LL-HLS stream is ready."
			})
		}
	}
	processErr := <-errCh
	cancel()

	if started {
		session.FinishedAt = time.Now()
		session.Published = session.Published || m.sessionPublished(session.ID)
		if !lhls && !sidecar {
			// LHLS and the MediaMTX sidecar modes have no on-disk HLS playlist
			// to convert; their VOD is the separately recorded MP4.
			_ = video.FinalizeHLSPlaylist(m.outDir, id)
		} else if sidecar && mediaMTXHLSDir != "" {
			_, _ = importMediaMTXHLS(m.outDir, id, mediaMTXHLSDir, mediaMTXPathName(id))
		}
		if session.Published && m.cb.OnDone != nil {
			m.cb.OnDone(session)
		}
		m.setStatus(func(status *Status) {
			status.Listening = true
			status.Connected = false
			status.MediaID = ""
			status.RTSPTURL = ""
			status.Publishing = false
			status.FinishedAt = session.FinishedAt
			status.Message = "OBS stream ended. Recording finalized as VOD."
		})
		m.mu.Lock()
		if m.current != nil && m.current.ID == session.ID {
			m.current = nil
		}
		m.mu.Unlock()
	}
	if parent.Err() != nil {
		return nil
	}
	if processErr != nil {
		return fmt.Errorf("OBS RTMP receiver stopped: %w", processErr)
	}
	return nil
}

func (m *Manager) waitForStart(ctx context.Context, session Session, errCh <-chan error, ready func() bool) (bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case err := <-errCh:
			if err != nil {
				return false, fmt.Errorf("OBS RTMP receiver stopped before a stream connected: %w", err)
			}
			return false, fmt.Errorf("OBS RTMP receiver stopped before a stream connected")
		case <-ticker.C:
			if ready == nil || !ready() {
				continue
			}
			session.StartedAt = time.Now()
			published := m.publishSessionIfArmed(&session)
			m.setStatus(func(status *Status) {
				status.Listening = true
				status.Connected = true
				status.MediaID = session.ID
				status.Publishing = published
				status.StartedAt = session.StartedAt
				if published {
					status.Message = "OBS stream is being published to HLS."
				} else {
					status.Message = "OBS stream is connected. Press publish to share it."
				}
			})
			return true, nil
		}
	}
}

func (m *Manager) ffmpegArgs(id, recording string, preset video.QualityPreset) []string {
	return m.ffmpegArgsWithEncoder(id, recording, preset, video.CPUVideoEncoder(m.currentLatency().encoderPurpose()))
}

func (m *Manager) ffmpegArgsWithEncoder(id, recording string, preset video.QualityPreset, encoder video.VideoEncoderProfile) []string {
	inputURL := fmt.Sprintf("rtmp://0.0.0.0:%d/live/%s", m.port, m.key)
	latency := m.currentLatency()
	preset = scaledLatencyPreset(preset, latency.BitrateMultiplier)
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
		"-hls_allow_cache", "0",
		"-hls_playlist_type", "event",
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
	_ = id
	inputURL := fmt.Sprintf("rtmp://0.0.0.0:%d/live/%s", m.port, m.key)
	latency := m.currentLatency()
	preset = scaledLatencyPreset(preset, latency.BitrateMultiplier)
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
	_ = id
	inputURL := fmt.Sprintf("rtmp://0.0.0.0:%d/live/%s", m.port, m.key)
	latency := m.currentLatency()
	preset = scaledLatencyPreset(preset, latency.BitrateMultiplier)
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
	transport := m.currentLatency().Transport
	if transport != LatencyModeLLHLS && transport != LatencyModeRTSPT {
		return false
	}
	m.mu.Lock()
	runtime := m.mtx
	active := m.current != nil && m.current.ID == id
	m.mu.Unlock()
	if !active || runtime == nil {
		return false
	}
	runtime.proxyHLS(w, r, name)
	return true
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
	if multiplier <= 1 {
		return preset
	}
	preset.VideoBitrate = video.ScaleBitrateForStreaming(preset.VideoBitrate, multiplier)
	preset.MaxRate = video.ScaleBitrateForStreaming(preset.MaxRate, multiplier)
	preset.BufferSize = video.ScaleBitrateForStreaming(preset.BufferSize, multiplier)
	return preset
}

func (m *Manager) isPublishingArmed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status.Publishing
}

func (m *Manager) publishSessionIfArmed(session *Session) bool {
	m.mu.Lock()
	armed := m.status.Publishing
	if session != nil {
		session.Published = armed
		copy := *session
		m.current = &copy
	}
	m.mu.Unlock()
	if armed && session != nil && m.cb.OnStart != nil {
		m.cb.OnStart(*session)
	}
	return armed
}

func (m *Manager) sessionPublished(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current != nil && m.current.ID == id && m.current.Published
}

func (m *Manager) setStatus(fn func(*Status)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.status)
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

package obsrtmp

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

// RadioCallbacks notify the server about playlist-radio lifecycle events.
// Callbacks run on the radio goroutine; they must not block for long.
type RadioCallbacks struct {
	OnTrackStart func(trackID string)
	OnTrackEnd   func(trackID string, err error)
	OnIdle       func()
	OnRTSPReady  func(RTSPEndpoint)
	OnStopped    func()
}

// RadioStatus is a snapshot of the radio session.
type RadioStatus struct {
	Running        bool      `json:"running"`
	CurrentTrackID string    `json:"currentTrackId"`
	TrackStartedAt time.Time `json:"trackStartedAt"`
	RTSPURL        string    `json:"rtspUrl"`
	Path           string    `json:"path"`
}

// radioRuntime is the subset of mediaMTXRuntime the radio needs; split out so
// tests can substitute a fake.
type radioRuntime interface {
	start(ctx context.Context) error
	stop(timeout time.Duration) error
	publishURL() string
	rtspURL() string
	proxyHLS(w http.ResponseWriter, req *http.Request, name string)
	llhlsReady(ctx context.Context) bool
}

type radioGate interface {
	start(ctx context.Context) error
	stop() error
}

// RadioManager streams a music playlist as a continuous "radio" broadcast.
// It owns one MediaMTX instance (RTSP + LL-HLS on fixed URLs) and pushes
// pre-rendered track files into it one after another with codec copy. Track
// selection is delegated to next(), so the queue policy (shuffle, loop,
// interrupts) lives entirely in the playlist domain.
type RadioManager struct {
	mu     sync.Mutex
	outDir string
	host   string
	next   func() (mediaPath, trackID string, ok bool)
	cb     RadioCallbacks

	cancel     context.CancelFunc
	done       chan struct{}
	runtime    radioRuntime
	status     RadioStatus
	skipPush   context.CancelFunc
	wake       chan struct{}
	skipped    bool
	pathName   string
	rtspPublic RTSPEndpoint

	// test seams
	buildRuntime func(ctx context.Context) (radioRuntime, radioGate, RTSPEndpoint, error)
	runPush      func(ctx context.Context, mediaPath, publishURL string) error
}

func NewRadioManager(outDir, host string, next func() (mediaPath, trackID string, ok bool), cb RadioCallbacks) *RadioManager {
	m := &RadioManager{
		outDir: outDir,
		host:   host,
		next:   next,
		cb:     cb,
		wake:   make(chan struct{}, 1),
	}
	m.buildRuntime = m.buildMediaMTX
	m.runPush = m.runFFmpegPush
	return m
}

// Start launches the radio session. It is idempotent while running.
func (m *RadioManager) Start() error {
	m.mu.Lock()
	if m.done != nil {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.cancel = cancel
	m.done = done
	m.mu.Unlock()

	runtime, gate, endpoint, err := m.buildRuntime(ctx)
	if err != nil {
		m.mu.Lock()
		m.cancel = nil
		m.done = nil
		m.mu.Unlock()
		cancel()
		close(done)
		return err
	}

	m.mu.Lock()
	m.runtime = runtime
	m.pathName = endpoint.Path
	m.rtspPublic = endpoint
	m.status = RadioStatus{Running: true, RTSPURL: endpoint.LocalURL, Path: endpoint.Path}
	m.mu.Unlock()
	if m.cb.OnRTSPReady != nil {
		m.cb.OnRTSPReady(endpoint)
	}

	go m.run(ctx, done, runtime, gate)
	return nil
}

func (m *RadioManager) run(ctx context.Context, done chan struct{}, runtime radioRuntime, gate radioGate) {
	defer func() {
		if gate != nil {
			_ = gate.stop()
		}
		_ = runtime.stop(5 * time.Second)
		m.mu.Lock()
		m.runtime = nil
		m.status = RadioStatus{}
		m.cancel = nil
		m.done = nil
		m.mu.Unlock()
		close(done)
		if m.cb.OnStopped != nil {
			m.cb.OnStopped()
		}
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		mediaPath, trackID, ok := m.next()
		if !ok {
			m.setCurrent("")
			if m.cb.OnIdle != nil {
				m.cb.OnIdle()
			}
			select {
			case <-m.wake:
				continue
			case <-ctx.Done():
				return
			}
		}

		pushCtx, cancelPush := context.WithCancel(ctx)
		m.mu.Lock()
		m.skipPush = cancelPush
		m.skipped = false
		m.status.CurrentTrackID = trackID
		m.status.TrackStartedAt = time.Now()
		m.mu.Unlock()
		if m.cb.OnTrackStart != nil {
			m.cb.OnTrackStart(trackID)
		}

		err := m.runPush(pushCtx, mediaPath, runtime.publishURL())
		cancelPush()
		m.mu.Lock()
		skipped := m.skipped
		m.skipPush = nil
		m.mu.Unlock()
		m.setCurrent("")

		if ctx.Err() != nil {
			return
		}
		if skipped {
			err = nil
		}
		if m.cb.OnTrackEnd != nil {
			m.cb.OnTrackEnd(trackID, err)
		}
	}
}

func (m *RadioManager) setCurrent(trackID string) {
	m.mu.Lock()
	m.status.CurrentTrackID = trackID
	if trackID == "" {
		m.status.TrackStartedAt = time.Time{}
	}
	m.mu.Unlock()
}

// Wake nudges an idle radio to re-query next() (e.g. after a track was added
// or playback was requested).
func (m *RadioManager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// SkipCurrent aborts the current track push; the loop immediately asks next()
// for the following track. Used for both skip and 割り込み再生 (the server
// sets the queue's current track before calling this).
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

// Stop shuts the radio down and waits for the loop to exit.
func (m *RadioManager) Stop(timeout time.Duration) {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		select {
		case <-done:
		case <-time.After(timeout):
		}
	}
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
	status.Running = m.done != nil
	return status
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

// HLSReady reports whether the LL-HLS endpoint is serving media.
func (m *RadioManager) HLSReady(ctx context.Context) bool {
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	if runtime == nil {
		return false
	}
	rc, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return runtime.llhlsReady(rc)
}

// buildMediaMTX provisions the real MediaMTX instance plus the public RTSP
// gate, mirroring the OBS RTSPT sidecar but with the low-latency HLS variant
// enabled so RTSP and LL-HLS are served simultaneously.
func (m *RadioManager) buildMediaMTX(ctx context.Context) (radioRuntime, radioGate, RTSPEndpoint, error) {
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
	id := sessionID()
	path := "radio" + strings.TrimPrefix(mediaMTXPathName(id), "obs")
	runtime := newMediaMTXRuntime(mtxExe, mediaMTXSessionConfig{
		Path:          path,
		PublishUser:   user,
		PublishPass:   pass,
		Ports:         ports,
		AdvertiseHost: m.host,
		DebugLogPath:  mediaMTXDebugLogPath(),
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
		SessionID: id,
		Host:      m.host,
		Port:      ports.RTSP,
		RTPPort:   ports.RTP,
		RTCPPort:  ports.RTCP,
		Path:      path,
		LocalURL:  runtime.rtspURL(),
	}
	return runtime, gate, endpoint, nil
}

func (m *RadioManager) runFFmpegPush(ctx context.Context, mediaPath, publishURL string) error {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return err
	}
	return video.RunRadioPush(ctx, m.outDir, ffmpeg, mediaPath, publishURL)
}

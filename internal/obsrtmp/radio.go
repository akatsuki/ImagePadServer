package obsrtmp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

// errPublisherDown marks feeder failures caused by the persistent publisher
// process dying (write into its stdin failed); the session cannot continue.
var errPublisherDown = errors.New("radio publisher exited")

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
	// BaseOffsetSeconds is the in-track position the current feed started
	// from (>0 after resuming a paused track).
	BaseOffsetSeconds int    `json:"baseOffsetSeconds"`
	RTSPURL           string `json:"rtspUrl"`
	Path              string `json:"path"`
}

// radioRuntime is the subset of mediaMTXRuntime the radio needs; split out so
// tests can substitute a fake.
type radioRuntime interface {
	start(ctx context.Context) error
	stop(timeout time.Duration) error
	rtmpPublishURL() string
	rtspURL() string
	proxyHLS(w http.ResponseWriter, req *http.Request, name string)
	llhlsReady(ctx context.Context) bool
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

	cancel         context.CancelFunc
	done           chan struct{}
	runtime        radioRuntime
	status         RadioStatus
	skipPush       context.CancelFunc
	fillerCancel   context.CancelFunc
	wake           chan struct{}
	skipped        bool
	pathName       string
	rtspPublic     RTSPEndpoint
	fallbackPreset func() video.QualityPreset

	// test seams
	buildRuntime      func(ctx context.Context) (radioRuntime, radioGate, RTSPEndpoint, error)
	startPublisher    func(ctx context.Context, publishURL string) (radioPublisher, error)
	runFeeder         func(ctx context.Context, mediaPath string, startSeconds int, loop bool, timestampOffset float64, sink io.Writer) error
	runFallbackFeeder func(ctx context.Context, timestampOffset float64, sink io.Writer) error
}

func NewRadioManager(outDir, host string, next func() (mediaPath, trackID string, startSeconds int, ok bool), cb RadioCallbacks) *RadioManager {
	m := &RadioManager{
		outDir: outDir,
		host:   host,
		next:   next,
		cb:     cb,
		wake:   make(chan struct{}, 1),
	}
	m.buildRuntime = m.buildMediaMTX
	m.startPublisher = m.startFFmpegPublisher
	m.runFeeder = m.runFFmpegFeeder
	m.runFallbackFeeder = m.runFFmpegFallbackFeeder
	m.fallbackPreset = func() video.QualityPreset { return video.MusicRadioQualityPreset("auto", 0, 0) }
	return m
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

	publisher, err := m.startPublisher(ctx, runtime.rtmpPublishURL())
	if err != nil {
		return
	}
	defer publisher.close()

	timestampOffset := 0.0
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-publisher.done():
			return
		default:
		}
		mediaPath, trackID, startSeconds, ok := m.next()
		if !ok {
			m.setCurrent("")
			if m.cb.OnIdle != nil {
				m.cb.OnIdle()
			}
			timestampOffset += m.feedFillerUntilWake(ctx, publisher, timestampOffset)
			continue
		}

		pushCtx, cancelPush := context.WithCancel(ctx)
		m.mu.Lock()
		m.skipPush = cancelPush
		m.skipped = false
		m.status.CurrentTrackID = trackID
		m.status.TrackStartedAt = time.Now()
		m.status.BaseOffsetSeconds = startSeconds
		m.mu.Unlock()
		if m.cb.OnTrackStart != nil {
			m.cb.OnTrackStart(trackID)
		}

		feedStarted := time.Now()
		err := m.runFeeder(pushCtx, mediaPath, startSeconds, false, timestampOffset, publisher.sink())
		timestampOffset += elapsedFeedSeconds(feedStarted)
		cancelPush()
		m.mu.Lock()
		skipped := m.skipped
		m.skipPush = nil
		m.mu.Unlock()
		m.setCurrent("")

		if ctx.Err() != nil {
			return
		}
		if skipped && !errors.Is(err, errPublisherDown) {
			err = nil
		}
		if m.cb.OnTrackEnd != nil {
			m.cb.OnTrackEnd(trackID, err)
		}
		if errors.Is(err, errPublisherDown) {
			return
		}
	}
}

// feedFillerUntilWake broadcasts the active fallback until Wake/Skip arrives
// (or the session ends). It is called only while no track feeder is active.
func (m *RadioManager) feedFillerUntilWake(ctx context.Context, publisher radioPublisher, timestampOffset float64) float64 {
	// A wake queued during the previous track means new work is already
	// waiting — skip the filler entirely.
	select {
	case <-m.wake:
		return 0
	default:
	}
	fctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
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
	_ = m.runFallbackFeeder(fctx, timestampOffset, publisher.sink())
	cancel()
	m.mu.Lock()
	m.fillerCancel = nil
	m.mu.Unlock()
	return elapsedFeedSeconds(feedStarted)
}

func elapsedFeedSeconds(start time.Time) float64 {
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0.001
	}
	return elapsed
}

func (m *RadioManager) setCurrent(trackID string) {
	m.mu.Lock()
	m.status.CurrentTrackID = trackID
	if trackID == "" {
		m.status.TrackStartedAt = time.Time{}
		m.status.BaseOffsetSeconds = 0
	}
	m.mu.Unlock()
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
// and the loopback RTMP ingest enabled so RTSP and LL-HLS are served
// simultaneously from the persistent publisher.
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
		EnableRTMP:    true,
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

// --- real ffmpeg publisher / feeder -----------------------------------------

type ffmpegPublisher struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	exit chan error
}

func (p *ffmpegPublisher) sink() io.Writer    { return p.in }
func (p *ffmpegPublisher) done() <-chan error { return p.exit }
func (p *ffmpegPublisher) close() {
	_ = p.in.Close()
	select {
	case <-p.exit:
	case <-time.After(3 * time.Second):
		if p.cmd.Process != nil {
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
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	untrack := video.TrackStartedFFmpeg(cmd)
	exit := make(chan error, 1)
	go func() {
		defer untrack()
		exit <- cmd.Wait()
		close(exit)
	}()
	return &ffmpegPublisher{cmd: cmd, in: stdin, exit: exit}, nil
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

func (m *RadioManager) runFFmpegFallbackFeeder(ctx context.Context, timestampOffset float64, sink io.Writer) error {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return err
	}
	fonts, err := video.VisualizerFonts()
	if err != nil {
		return err
	}
	logoPath, err := video.WriteRadioFallbackLogo(m.outDir)
	if err != nil {
		return err
	}
	m.mu.Lock()
	presetFn := m.fallbackPreset
	m.mu.Unlock()
	preset := video.MusicRadioQualityPreset("auto", 0, 0)
	if presetFn != nil {
		preset = presetFn()
	}
	renderWidth, renderHeight := video.RadioFallbackRenderSize(preset)
	renderer, err := video.NewRadioFallbackRenderer(renderWidth, renderHeight, logoPath, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		return err
	}
	defer renderer.Close()
	cmd := exec.CommandContext(ctx, ffmpeg, video.RadioFallbackFeederArgs(preset, renderWidth, renderHeight, timestampOffset)...)
	hideWindow(cmd)
	cmd.Dir = m.outDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	untrack := video.TrackStartedFFmpeg(cmd)
	defer untrack()
	renderErr := make(chan error, 1)
	go func() {
		renderErr <- video.WriteRadioFallbackFrames(ctx, stdin, renderer)
		_ = stdin.Close()
	}()
	_, copyErr := io.Copy(sink, stdout)
	waitErr := cmd.Wait()
	if err := <-renderErr; err != nil && ctx.Err() == nil {
		return fmt.Errorf("fallback render: %w", err)
	}
	if ctx.Err() != nil {
		return nil
	}
	if copyErr != nil {
		return fmt.Errorf("%w: %v", errPublisherDown, copyErr)
	}
	if waitErr != nil {
		detail := stderr.String()
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		return fmt.Errorf("fallback feeder: %w: %s", waitErr, detail)
	}
	return nil
}

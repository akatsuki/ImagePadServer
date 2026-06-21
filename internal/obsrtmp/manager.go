package obsrtmp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
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
	OnStart func(Session)
	OnDone  func(Session)
}

type LatencyProfile struct {
	Mode           string `json:"mode"`
	Label          string `json:"label"`
	Target         string `json:"target"`
	SegmentSeconds string `json:"segmentSeconds"`
	ListSize       string `json:"listSize"`
	DVRListSize    string `json:"dvrListSize,omitempty"`
	FrameRate      string `json:"frameRate,omitempty"`
	GOPFrames      string `json:"gopFrames,omitempty"`
	Reencode       bool   `json:"reencode"`
	DVR            bool   `json:"dvr"`
	Message        string `json:"message,omitempty"`
}

var latencyProfiles = map[string]LatencyProfile{
	"auto": {
		Mode:           "auto",
		Label:          "自動",
		Target:         "10s",
		SegmentSeconds: "2",
		ListSize:       "5",
		DVRListSize:    "900",
		FrameRate:      "30",
		GOPFrames:      "60",
		Reencode:       true,
		Message:        "回線測定値が低い場合は普通より高い遅延を選びます。",
	},
	"normal": {
		Mode:           "normal",
		Label:          "普通",
		Target:         "5s",
		SegmentSeconds: "1",
		ListSize:       "8",
		DVRListSize:    "1800",
		FrameRate:      "30",
		GOPFrames:      "30",
		Reencode:       true,
	},
	"low": {
		Mode:           "low",
		Label:          "低遅延",
		Target:         "1s",
		SegmentSeconds: "0.5",
		ListSize:       "12",
		DVRListSize:    "3600",
		FrameRate:      "30",
		GOPFrames:      "15",
		Reencode:       true,
	},
	"ultra": {
		Mode:           "ultra",
		Label:          "超低遅延",
		Target:         "0.5s+",
		SegmentSeconds: "0.5",
		ListSize:       "16",
		DVRListSize:    "3600",
		FrameRate:      "30",
		GOPFrames:      "15",
		Reencode:       true,
		Message:        "0.5秒セグメントを長めに保持し、VRChat側の遅れによるセグメント欠落を避けます。",
	},
}

type Manager struct {
	outDir  string
	host    string
	port    int
	key     string
	preset  func() video.QualityPreset
	latency func() LatencyProfile
	cb      Callbacks

	mu      sync.Mutex
	running bool
	stop    context.CancelFunc
	done    chan struct{}
	status  Status
	current *Session
}

type Session struct {
	ID           string
	Title        string
	PlaylistName string
	Recording    string
	Published    bool
	StartedAt    time.Time
	FinishedAt   time.Time
}

type Status struct {
	Enabled        bool           `json:"enabled"`
	Listening      bool           `json:"listening"`
	Connected      bool           `json:"connected"`
	ServerAddress  string         `json:"serverAddress"`
	StreamKey      string         `json:"streamKey"`
	Port           int            `json:"port"`
	MediaID        string         `json:"mediaID,omitempty"`
	PreviewURL     string         `json:"previewURL,omitempty"`
	Publishing     bool           `json:"publishing"`
	Latency        LatencyProfile `json:"latency"`
	Message        string         `json:"message"`
	StartedAt      time.Time      `json:"startedAt,omitempty"`
	FinishedAt     time.Time      `json:"finishedAt,omitempty"`
	EncoderName    string         `json:"encoderName,omitempty"`
	HardwareEncode bool           `json:"hardwareEncode"`
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
	m.mu.Lock()
	m.status.Publishing = true
	m.status.Message = "OBS publishing is armed. Waiting for a stream."
	if m.current != nil && m.status.Connected {
		if !m.current.Published {
			m.current.Published = true
			copy := *m.current
			session = &copy
		}
		m.status.Message = "OBS stream is being published to HLS."
	}
	m.mu.Unlock()
	if session != nil && m.cb.OnStart != nil {
		m.cb.OnStart(*session)
	}
	return session != nil
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
	selected := video.VideoEncoderProfile{Name: "copy", Purpose: video.EncoderLowLatency}
	if m.currentLatency().Reencode {
		selected = video.SelectVideoEncoder(parent, ffmpeg, video.EncoderLowLatency)
	}
	err = m.runOneWithEncoder(parent, ffmpeg, selected)
	if err == nil || !selected.Hardware || parent.Err() != nil {
		return err
	}
	return m.runOneWithEncoder(parent, ffmpeg, video.CPUVideoEncoder(video.EncoderLowLatency))
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

	args := m.ffmpegArgsWithEncoder(id, recording, preset, encoder)
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

	started, waitErr := m.waitForStart(ctx, session, errCh)
	if waitErr != nil {
		cancel()
		return waitErr
	}
	processErr := <-errCh
	cancel()

	if started {
		session.FinishedAt = time.Now()
		session.Published = session.Published || m.sessionPublished(session.ID)
		_ = video.FinalizeHLSPlaylist(m.outDir, id)
		if session.Published && m.cb.OnDone != nil {
			m.cb.OnDone(session)
		}
		m.setStatus(func(status *Status) {
			status.Listening = true
			status.Connected = false
			status.MediaID = ""
			status.Publishing = false
			status.FinishedAt = session.FinishedAt
			status.Message = "OBS stream ended. HLS playlist finalized as VOD."
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

func (m *Manager) waitForStart(ctx context.Context, session Session, errCh <-chan error) (bool, error) {
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
			if !fileExists(filepath.Join(m.outDir, session.PlaylistName)) {
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
	return m.ffmpegArgsWithEncoder(id, recording, preset, video.CPUVideoEncoder(video.EncoderLowLatency))
}

func (m *Manager) ffmpegArgsWithEncoder(id, recording string, preset video.QualityPreset, encoder video.VideoEncoderProfile) []string {
	inputURL := fmt.Sprintf("rtmp://0.0.0.0:%d/live/%s", m.port, m.key)
	latency := m.currentLatency()
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
		"-hls_delete_threshold", "4",
		"-hls_allow_cache", "0",
		"-hls_flags", "delete_segments+independent_segments+program_date_time",
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
	mode = strings.ToLower(strings.TrimSpace(mode))
	if profile, ok := latencyProfiles[mode]; ok {
		return profile
	}
	return latencyProfiles["auto"]
}

func ResolveLatencyProfile(mode string, uploadMbps int) LatencyProfile {
	profile := NormalizeLatencyProfile(mode)
	if profile.Mode != "auto" {
		return profile
	}
	switch {
	case uploadMbps > 0 && uploadMbps < 3:
		profile.Target = "16s"
		profile.SegmentSeconds = "4"
		profile.ListSize = "4"
		profile.GOPFrames = "120"
	case uploadMbps > 0 && uploadMbps < 8:
		profile.Target = "10s"
		profile.SegmentSeconds = "2"
		profile.ListSize = "5"
		profile.GOPFrames = "60"
	default:
		profile.Target = "5s"
		profile.SegmentSeconds = "1"
		profile.ListSize = "5"
		profile.GOPFrames = "30"
	}
	profile.Message = "回線測定値に応じてOBS HLS遅延を自動調整します。低速時は普通より高い遅延になります。"
	return profile
}

func normalizeLatencyProfile(profile LatencyProfile) LatencyProfile {
	normalized := NormalizeLatencyProfile(profile.Mode)
	if profile.DVR {
		if normalized.DVRListSize != "" {
			normalized.ListSize = normalized.DVRListSize
		}
		normalized.DVR = true
		if profile.Message != "" {
			normalized.Message = profile.Message
		} else if normalized.Message == "" {
			normalized.Message = "DVR is enabled. Up to 30 minutes of HLS segments are kept for players that support live seeking."
		}
		return normalized
	}
	if profile.Mode != "auto" {
		return normalized
	}
	if profile.SegmentSeconds != "" {
		normalized.Target = profile.Target
		normalized.SegmentSeconds = profile.SegmentSeconds
		normalized.ListSize = profile.ListSize
		normalized.DVRListSize = profile.DVRListSize
		normalized.FrameRate = profile.FrameRate
		normalized.GOPFrames = profile.GOPFrames
		normalized.Reencode = profile.Reencode
		normalized.DVR = profile.DVR
		normalized.Message = profile.Message
		return normalized
	}
	return normalized
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

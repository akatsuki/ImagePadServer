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

type Manager struct {
	outDir string
	host   string
	port   int
	key    string
	preset func() video.QualityPreset
	cb     Callbacks

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
	Enabled       bool      `json:"enabled"`
	Listening     bool      `json:"listening"`
	Connected     bool      `json:"connected"`
	ServerAddress string    `json:"serverAddress"`
	StreamKey     string    `json:"streamKey"`
	Port          int       `json:"port"`
	MediaID       string    `json:"mediaID,omitempty"`
	PreviewURL    string    `json:"previewURL,omitempty"`
	Publishing    bool      `json:"publishing"`
	Message       string    `json:"message"`
	StartedAt     time.Time `json:"startedAt,omitempty"`
	FinishedAt    time.Time `json:"finishedAt,omitempty"`
}

func New(outDir, host string, port int, key string, preset func() video.QualityPreset, cb Callbacks) *Manager {
	if port <= 0 {
		port = 1935
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = "imagepad"
	}
	return &Manager{
		outDir: outDir,
		host:   host,
		port:   port,
		key:    key,
		preset: preset,
		cb:     cb,
		status: Status{
			Port:          port,
			ServerAddress: serverAddress(host, port),
			StreamKey:     key,
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

	args := m.ffmpegArgs(id, recording, preset)
	cmd := exec.Command(ffmpeg, args...)
	cmd.Dir = m.outDir
	hideWindow(cmd)
	stdin, _ := cmd.StdinPipe()

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start OBS RTMP receiver: %w", err)
	}
	errCh := make(chan error, 1)
	waitDone := make(chan struct{})
	go func() {
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
	})

	started, waitErr := m.waitForStart(ctx, session, errCh)
	if waitErr != nil {
		cancel()
		return waitErr
	}
	err = <-errCh
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
	if err != nil {
		return fmt.Errorf("OBS RTMP receiver stopped: %w", err)
	}
	return nil
}

func (m *Manager) waitForStart(ctx context.Context, session Session, errCh <-chan error) (bool, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
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
	inputURL := fmt.Sprintf("rtmp://0.0.0.0:%d/live/%s", m.port, m.key)
	common := []string{
		"-map", "0:v:0",
		"-map", "0:a:0?",
		"-c", "copy",
	}
	args := []string{
		"-hide_banner",
		"-loglevel", "warning",
		"-y",
		"-listen", "1",
		"-i", inputURL,
	}
	args = append(args, common...)
	args = append(args,
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", video.SegmentPattern(id),
		video.PlaylistName(id),
	)
	args = append(args, common...)
	args = append(args,
		"-movflags", "+faststart",
		recording,
	)
	return args
}

func (m *Manager) currentPreset() video.QualityPreset {
	if m.preset == nil {
		return video.ResolveQuality("auto", 0)
	}
	return m.preset()
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

package voicevoxruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"imagepadserver/internal/xposttts"
)

type Status struct {
	Phase    string `json:"phase"`
	Percent  int    `json:"percent"`
	Message  string `json:"message"`
	Error    string `json:"error,omitempty"`
	Endpoint string `json:"engineUrl"`
	Version  string `json:"version"`
}
type ownedProcess interface {
	Close() error
	Wait() error
}

type Manager struct {
	mu        sync.Mutex
	root      string
	endpoint  string
	status    Status
	done      chan struct{}
	cancel    context.CancelFunc
	closing   atomic.Int32
	onChange  func()
	installFn func(context.Context, string, Asset, Progress) (string, error)
	launchFn  func(context.Context, string, string) (ownedProcess, error)
	probeFn   func(context.Context, string) error
}

func New(root string, onChange func()) *Manager {
	return &Manager{root: root, endpoint: Endpoint, status: Status{Phase: "idle", Message: "アプリ内VOICEVOXを準備します", Endpoint: Endpoint, Version: Version}, onChange: onChange, installFn: install, launchFn: launch, probeFn: probe}
}
func (m *Manager) Status() Status { m.mu.Lock(); defer m.mu.Unlock(); return m.status }
func (m *Manager) set(phase string, p int, err error) {
	m.mu.Lock()
	m.setLocked(phase, p, err)
	m.mu.Unlock()
	if m.onChange != nil {
		m.onChange()
	}
}
func (m *Manager) setLocked(phase string, p int, err error) {
	m.status.Phase = phase
	m.status.Percent = p
	m.status.Error = ""
	labels := map[string]string{"downloading": "VOICEVOXをダウンロード中", "verifying": "VOICEVOXを検証中", "extracting": "VOICEVOXを展開中", "starting": "VOICEVOXを起動中", "ready": "アプリ内VOICEVOXを使用できます", "failed": "VOICEVOXの準備に失敗しました", "stopped": "VOICEVOXは停止しています"}
	m.status.Message = labels[phase]
	if err != nil {
		m.status.Error = err.Error()
	}
}
func (m *Manager) Start(parent context.Context) { m.start(parent, false) }
func (m *Manager) Retry(parent context.Context) { m.start(parent, true) }
func (m *Manager) start(parent context.Context, retry bool) {
	if m.closing.Load() > 0 {
		return
	}
	m.mu.Lock()
	if m.closing.Load() > 0 {
		m.mu.Unlock()
		return
	}
	if m.done != nil {
		select {
		case <-m.done:
		default:
			m.mu.Unlock()
			return
		}
	}
	if (m.status.Phase == "failed" || m.status.Phase == "stopped") && !retry {
		m.mu.Unlock()
		return
	}
	if parent.Err() != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	done := make(chan struct{})
	m.done = done
	m.status.Phase = "starting"
	m.status.Message = "アプリ内VOICEVOXを準備中"
	m.status.Error = ""
	m.status.Percent = 0
	m.mu.Unlock()
	go func() {
		defer cancel()
		err := m.run(ctx)
		m.mu.Lock()
		if ctx.Err() != nil {
			m.setLocked("stopped", 0, nil)
		} else {
			m.setLocked("failed", 0, err)
		}
		close(done) // Terminal state and retry availability change atomically.
		m.mu.Unlock()
		if m.onChange != nil {
			m.onChange()
		}
	}()
}
func (m *Manager) run(ctx context.Context) error {
	a, err := releaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	dir, err := m.installFn(ctx, m.root, a, func(phase string, p int) { m.set(phase, p, nil) })
	if err != nil {
		return err
	}
	m.set("starting", 0, nil)
	p, err := m.launchFn(ctx, dir, m.endpoint)
	if err != nil {
		return err
	}
	defer p.Close()
	exited := make(chan error, 1)
	go func() { exited <- p.Wait() }()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = p.Close()
			<-exited
			return ctx.Err()
		case err := <-exited:
			if err == nil {
				err = errors.New("VOICEVOX engine exited")
			}
			return err
		case <-deadline.C:
			_ = p.Close()
			<-exited
			return errors.New("VOICEVOX engine startup timed out")
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, time.Second)
			err := m.probeFn(probeCtx, m.endpoint)
			cancel()
			if err == nil {
				select {
				case err := <-exited:
					if err == nil {
						err = errors.New("VOICEVOX engine exited")
					}
					return err
				default:
				}
				m.set("ready", 100, nil)
				select {
				case <-ctx.Done():
					_ = p.Close()
					<-exited
					return ctx.Err()
				case err := <-exited:
					if err == nil {
						err = errors.New("VOICEVOX engine exited")
					}
					return err
				}
			}
		}
	}
}
func (m *Manager) Ensure(ctx, parent context.Context) error {
	m.Start(parent)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		s := m.Status()
		if s.Phase == "ready" {
			return nil
		}
		if s.Phase == "failed" || s.Phase == "stopped" {
			return fmt.Errorf("%s: %s", s.Message, s.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closing.Add(1)
	cancel, done := m.cancel, m.done
	if cancel != nil {
		cancel()
	} else if done == nil {
		m.setLocked("stopped", 0, nil)
	}
	m.mu.Unlock()
	defer m.closing.Add(-1)
	if done != nil {
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			return errors.New("VOICEVOX shutdown timed out")
		}
	}
	return nil
}
func probe(ctx context.Context, endpoint string) error {
	u, _ := url.Parse(endpoint)
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"/version", nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("VOICEVOX version unavailable")
	}
	var version string
	if json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&version) != nil || version != Version {
		return errors.New("VOICEVOX engine version mismatch")
	}
	c, err := xposttts.NewClient(endpoint)
	if err != nil {
		return err
	}
	speakers, err := c.Speakers(ctx)
	if err != nil {
		return err
	}
	if len(speakers) == 0 {
		return errors.New("VOICEVOX speakers are empty")
	}
	return nil
}

// A busy dedicated port is an error; never adopt or stop another service.
func availableEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", u.Host)
	if err != nil {
		return fmt.Errorf("VOICEVOX専用ポートを使用できません: %w", err)
	}
	return l.Close()
}

func isolatedEnv() []string {
	env := os.Environ()
	for _, key := range []string{"VV_CPU_NUM_THREADS", "VV_OUTPUT_LOG_UTF8", "VV_DISABLE_MUTABLE_API", "VV_PRESET_FILE"} {
		for i := len(env) - 1; i >= 0; i-- {
			if len(env[i]) > len(key) && env[i][:len(key)+1] == key+"=" {
				env = append(env[:i], env[i+1:]...)
			}
		}
	}
	return append(env, "VV_CPU_NUM_THREADS=1", "VV_OUTPUT_LOG_UTF8=1", "VV_DISABLE_MUTABLE_API=1")
}

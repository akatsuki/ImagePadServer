//go:build darwin || linux

package voicevoxruntime

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
)

type unixProcess struct {
	cmd       *exec.Cmd
	log       *os.File
	once      sync.Once
	mu        sync.Mutex
	exited    bool
	cancel    context.CancelFunc
	watchDone chan struct{}
}

func (p *unixProcess) Close() error {
	p.once.Do(func() {
		p.cancel()
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.exited {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	return nil
}
func (p *unixProcess) Wait() error {
	err := p.cmd.Wait()
	p.mu.Lock()
	p.exited = true
	p.mu.Unlock()
	close(p.watchDone)
	p.log.Close()
	return err
}
func launch(parent context.Context, dir, endpoint string) (ownedProcess, error) {
	if err := availableEndpoint(endpoint); err != nil {
		return nil, err
	}
	stateDir := filepath.Join(filepath.Dir(dir), "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(stateDir, "engine.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	ctx, cancel := context.WithCancel(parent)
	cmd := exec.Command(filepath.Join(dir, engineBinary()), "--host", "127.0.0.1", "--port", u.Port(), "--cpu_num_threads", "1", "--disable_mutable_api", "--setting_file", filepath.Join(stateDir, "settings.yaml"), "--preset_file", filepath.Join(stateDir, "presets.yaml"))
	cmd.Dir = dir
	cmd.Env = isolatedEnv()
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		cancel()
		log.Close()
		return nil, err
	}
	p := &unixProcess{cmd: cmd, log: log, cancel: cancel, watchDone: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			p.Close()
		case <-p.watchDone:
		}
	}()
	return p, nil
}

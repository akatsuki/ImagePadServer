//go:build windows

package voicevoxruntime

import (
	"context"
	"fmt"
	"imagepadserver/internal/nicoexportbudget"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type budgetProcess struct {
	p   *nicoexportbudget.SessionProcess
	log *os.File
}

func (p *budgetProcess) Close() error { return p.p.Close() }
func (p *budgetProcess) Wait() error {
	r, err := p.p.Wait()
	p.log.Close()
	if err == nil && r.ExitCode != 0 {
		return fmt.Errorf("VOICEVOX engine exited %d", r.ExitCode)
	}
	return err
}
func launch(ctx context.Context, dir, endpoint string) (ownedProcess, error) {
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
	p, err := nicoexportbudget.StartSession(ctx, nicoexportbudget.ProcessSpec{Exe: filepath.Join(dir, engineBinary()), Dir: dir, Args: []string{"--host", "127.0.0.1", "--port", u.Port(), "--cpu_num_threads", "1", "--disable_mutable_api", "--setting_file", filepath.Join(stateDir, "settings.yaml"), "--preset_file", filepath.Join(stateDir, "presets.yaml")}, Env: isolatedEnv(), Stdout: log, Stderr: log}, nicoexportbudget.Options{Percent: 20, SampleInterval: time.Minute})
	if err != nil {
		log.Close()
		return nil, err
	}
	return &budgetProcess{p, log}, nil
}

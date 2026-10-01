package nicoexportbudget

import (
	"context"
	"errors"
	"sync"
)

type sessionProcessBackend interface {
	// Close and Wait may run concurrently and implementations must make that safe.
	Close() error
	Wait() (Report, error)
	// Snapshot must also be safe concurrently with Close and Wait.
	Snapshot() (Report, error)
}

// SessionProcess serializes lifecycle ownership around a platform process backend.
// Platform startup is supplied separately; T4.2 tests this wrapper with a fake backend.
type SessionProcess struct {
	backend    sessionProcessBackend
	closeOnce  sync.Once
	closeErr   error
	waitOnce   sync.Once
	waitDone   chan struct{}
	watchDone  chan struct{}
	watchOnce  sync.Once
	mu         sync.RWMutex
	waitReport Report
	waitErr    error
}

func newSessionProcess(ctx context.Context, backend sessionProcessBackend) (*SessionProcess, error) {
	if backend == nil {
		return nil, errors.New("nico export budget: session process backend is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p := &SessionProcess{backend: backend, waitDone: make(chan struct{}), watchDone: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Close()
		case <-p.waitDone:
		case <-p.watchDone:
		}
	}()
	return p, nil
}

// Close requests backend termination at most once. Wait remains the sole reaper.
func (p *SessionProcess) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.watchOnce.Do(func() { close(p.watchDone) })
		p.closeErr = p.backend.Close()
	})
	return p.closeErr
}

// Wait reaps once and returns a detached copy of the cached result to every caller.
func (p *SessionProcess) Wait() (Report, error) {
	if p == nil {
		return Report{}, errors.New("nico export budget: session process is required")
	}
	p.waitOnce.Do(func() {
		report, err := p.backend.Wait()
		p.mu.Lock()
		p.waitReport = cloneReport(report)
		p.waitErr = err
		close(p.waitDone)
		p.mu.Unlock()
	})
	<-p.waitDone
	p.mu.RLock()
	report, err := cloneReport(p.waitReport), p.waitErr
	p.mu.RUnlock()
	return report, err
}

// Snapshot returns a detached cumulative backend snapshot without waiting for exit.
func (p *SessionProcess) Snapshot() (Report, error) {
	if p == nil {
		return Report{}, errors.New("nico export budget: session process is required")
	}
	report, err := p.backend.Snapshot()
	return cloneReport(report), err
}

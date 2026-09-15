package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type directBackendReservation struct {
	runtime  *mediaMTXRuntime
	cancel   context.CancelFunc
	started  chan struct{}
	startErr error
	stopping bool // Manager.mu; set before releasing ownership to a stop operation
}

func (m *Manager) startOwnedDirectBackend(ctx context.Context, runtime *mediaMTXRuntime) error {
	if runtime == nil {
		return errors.New("direct backend runtime is missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reservation := &directBackendReservation{runtime: runtime, cancel: cancel, started: make(chan struct{})}
	m.mu.Lock()
	if m.directBackendReservations[runtime] != nil {
		m.mu.Unlock()
		return errors.New("direct backend runtime is already owned")
	}
	runtime.mu.Lock()
	used := runtime.proc != nil || runtime.stopped || runtime.retiring || runtime.dir != ""
	runtime.mu.Unlock()
	if used {
		m.mu.Unlock()
		return errors.New("direct backend runtime cannot be reused")
	}
	if m.directBackendReservations == nil {
		m.directBackendReservations = make(map[*mediaMTXRuntime]*directBackendReservation)
	}
	m.directBackendReservations[runtime] = reservation
	m.mu.Unlock()
	err := runtime.start(ctx)
	if err == nil && ctx.Err() != nil {
		err = errors.Join(ctx.Err(), runtime.stop(runtime.stopGrace))
	}
	reservation.startErr = err
	close(reservation.started)
	if err != nil {
		runtime.mu.Lock()
		confirmed := runtime.proc == nil || runtime.stopped
		runtime.mu.Unlock()
		if confirmed {
			m.releaseDirectBackendReservation(runtime, reservation)
		}
	}
	return err
}

func (m *Manager) stopOwnedDirectBackend(runtime *mediaMTXRuntime, timeout time.Duration) error {
	m.mu.Lock()
	reservation := m.directBackendReservations[runtime]
	if reservation != nil {
		reservation.stopping = true
	}
	m.mu.Unlock()
	if reservation == nil {
		return nil
	}
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	reservation.cancel()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-reservation.started:
	case <-timer.C:
		return fmt.Errorf("direct backend startup has not completed: %w", errMediaMTXProcessExitUnconfirmed)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		remaining = time.Nanosecond
	}
	if err := runtime.stop(remaining); err != nil {
		return err
	}
	m.releaseDirectBackendReservation(runtime, reservation)
	return nil
}

func (m *Manager) releaseDirectBackendReservation(runtime *mediaMTXRuntime, expected *directBackendReservation) {
	m.mu.Lock()
	if m.directBackendReservations[runtime] == expected {
		delete(m.directBackendReservations, runtime)
	}
	m.mu.Unlock()
}

func (m *Manager) reapDirectBackendCleanup() {
	m.mu.Lock()
	targets := make(map[*mediaMTXRuntime]bool)
	for _, runtime := range m.directRetiringBackends {
		targets[runtime] = true
	}
	for runtime, reservation := range m.directBackendReservations {
		select {
		case <-reservation.started:
			targets[runtime] = true
		default:
		}
	}
	m.mu.Unlock()
	for runtime := range targets {
		if runtime == nil || !runtime.reapRetired() {
			continue
		}
		m.mu.Lock()
		delete(m.directBackendReservations, runtime)
		kept := m.directRetiringBackends[:0]
		for _, retained := range m.directRetiringBackends {
			if retained != runtime {
				kept = append(kept, retained)
			}
		}
		m.directRetiringBackends = kept
		m.mu.Unlock()
	}
}

func (m *Manager) cleanupDirectBackendReservations(timeout time.Duration) error {
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	m.mu.Lock()
	reservations := make([]*directBackendReservation, 0, len(m.directBackendReservations))
	for _, reservation := range m.directBackendReservations {
		reservations = append(reservations, reservation)
	}
	m.mu.Unlock()
	for _, reservation := range reservations {
		reservation.cancel()
	}
	results := make(chan error, len(reservations))
	for _, reservation := range reservations {
		go func() { results <- m.stopOwnedDirectBackend(reservation.runtime, timeout) }()
	}
	var result error
	for range reservations {
		result = errors.Join(result, <-results)
	}
	return result
}

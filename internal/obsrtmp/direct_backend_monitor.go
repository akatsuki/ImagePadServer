package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

// A generation monitor owns only its runtime's wait/cleanup. It never closes
// a gate, ends external HLS, cancels the receiver or completes a session.
type directBackendExit struct {
	route directBackendRoute
	err   error
}

type directBackendMonitor struct {
	route      directBackendRoute
	exit       chan directBackendExit
	done       chan struct{}
	cancel     context.CancelFunc
	cleanupErr error // read only after done closes
}

func (m *Manager) watchDirectBackend(ctx context.Context, route directBackendRoute, observer *directPublisherObserver, legacy bool) (*directBackendMonitor, error) {
	m.mu.Lock()
	if reservation := m.directBackendReservations[route.runtime]; reservation != nil {
		select {
		case <-reservation.started:
		default:
			m.mu.Unlock()
			return nil, errors.New("direct backend startup is not complete")
		}
		if reservation.stopping || reservation.startErr != nil {
			m.mu.Unlock()
			return nil, errors.New("direct backend ownership is already stopping or failed")
		}
		delete(m.directBackendReservations, route.runtime)
	}
	m.directBackendMonitorCount++
	m.mu.Unlock()
	return monitorDirectBackend(ctx, route, observer, legacy), nil
}

func monitorDirectBackend(ctx context.Context, route directBackendRoute, observer *directPublisherObserver, legacy bool) *directBackendMonitor {
	ctx, cancel := context.WithCancel(ctx)
	watch := &directBackendMonitor{route: route, exit: make(chan directBackendExit, 1), done: make(chan struct{}), cancel: cancel}
	wait := route.runtime.wait()
	pid := route.runtime.processID()
	go func() {
		defer close(watch.done)
		defer cancel()
		select {
		case <-ctx.Done():
			timeout := route.runtime.stopGrace
			if timeout <= 0 || timeout > 5*time.Second {
				timeout = 5 * time.Second
			}
			watch.cleanupErr = route.runtime.stop(timeout)
		case err := <-wait:
			if observer != nil {
				code, known := mediaMTXProcessExitCode(err)
				if legacy {
					observer.observeMediaMTXExit(pid, time.Now(), code, known)
				} else {
					observer.observeMediaMTXGenerationExit(route.generation, pid, time.Now(), code, known)
				}
			}
			// Wait confirmed exit: cleanup cannot target a newer runtime.
			route.runtime.confirmStopped()
			watch.exit <- directBackendExit{route: route, err: err}
		}
	}()
	return watch
}

// Called after cancelling all watches; stop requests run concurrently and
// remain individually bounded. An unconfirmed process remains manager-owned.
func (m *Manager) finishDirectBackendMonitors(watches map[directBackendRoute]*directBackendMonitor) {
	for _, watch := range watches {
		watch.cancel()
	}
	for _, watch := range watches {
		<-watch.done
		m.mu.Lock()
		m.directBackendMonitorCount--
		if watch.cleanupErr != nil {
			m.directRetiringBackends = append(m.directRetiringBackends, watch.route.runtime)
		}
		m.mu.Unlock()
	}
}

// An active backend failure affects delivery, not receiver/session lifetime.
// Lock order remains Manager -> router; no router method calls the Manager.
func (m *Manager) observeDirectBackendExit(router *directBackendRouter, event directBackendExit) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	router.mu.Lock()
	defer router.mu.Unlock()
	if !m.directPublishing || m.directHandle.ID != event.route.sessionID ||
		m.directHandle.Generation != event.route.sessionEpoch || m.listenerGeneration != event.route.sessionEpoch ||
		router.terminal || router.active != event.route {
		return false
	}
	router.failed = true
	m.status.Connected = false
	m.status.Publishing = false
	m.status.Message = fmt.Sprintf("AirPlay delivery backend exited; receiver session is retained: %v", event.err)
	return true
}

// Initial public readiness is committed under the same identity lock as the
// router. Slow probes run outside locks; their results carry the exact route.
func (m *Manager) publishDirectBackendSession(ctx context.Context, route directBackendRoute, session *Session, router *directBackendRouter, outDir string, stop context.CancelFunc, done chan struct{}, observer *directPublisherObserver, artifacts airplaycontract.PublisherArtifacts) bool {
	m.mu.Lock()
	router.mu.RLock()
	if ctx.Err() != nil || !m.running || !m.directPublishing || m.current != nil ||
		m.directHandle.ID != route.sessionID || m.directHandle.Generation != route.sessionEpoch || m.listenerGeneration != route.sessionEpoch ||
		router.active != route || router.terminal || router.draining || router.failed {
		router.mu.RUnlock()
		m.mu.Unlock()
		return false
	}
	if observer != nil && (artifacts.Generation != route.generation || !observer.claimCurrentForPublication(artifacts)) {
		router.mu.RUnlock()
		m.mu.Unlock()
		return false
	}
	preset := video.QualityPreset{}
	if session.ActiveContract != nil {
		preset = session.ActiveContract.QualityPreset
	}
	video.BeginExternalHLS(outDir, session.ID, preset, stop, done)
	session.StartedAt = time.Now()
	armed, published := m.acceptSessionLocked(session)
	endpoint := directRTSPEndpoint(route.runtime, session.ID)
	endpoint.Generation = published.Generation
	endpointReady := m.currentSessionUsesRTSPTLocked()
	if endpointReady {
		m.rtspEndpoint = &endpoint
		m.status.RTSPTURL = ""
		m.status.Message = "RTSP TCPストリームを準備しました。外部公開を待っています。"
	}
	onStart, onReady := m.cb.OnStart, m.cb.OnRTSPReady
	router.mu.RUnlock()
	m.mu.Unlock()
	if armed && onStart != nil {
		onStart(published)
	}
	if armed && endpointReady && onReady != nil {
		onReady(endpoint)
	}
	return true
}

func (m *Manager) claimDirectRecordingForBackend(ctx context.Context, router *directBackendRouter, route directBackendRoute, observer *directPublisherObserver, artifacts airplaycontract.PublisherArtifacts) bool {
	if observer == nil || artifacts.SessionID != route.sessionID || artifacts.Generation != route.generation {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	router.mu.RLock()
	defer router.mu.RUnlock()
	if ctx.Err() != nil || !m.running || !m.directPublishing || m.current == nil || m.current.ID != route.sessionID ||
		m.directHandle.ID != route.sessionID || m.directHandle.Generation != route.sessionEpoch || m.listenerGeneration != route.sessionEpoch ||
		router.active != route || router.terminal || router.draining || router.failed {
		return false
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.sessionID != route.sessionID || !observer.claimCurrentRecordingCandidateLocked(artifacts) {
		return false
	}
	m.current.Recording = artifacts.Recording
	return true
}

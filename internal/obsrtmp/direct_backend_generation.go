package obsrtmp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/airplaycontract"
)

var errDirectBackendGenerationUsed = errors.New("direct backend generation already attempted startup")

// The session coordinator must register this owner before start and retain it
// even when start returns an unconfirmed-stop error. It contains no public
// gate, external HLS lifetime, session callback or generation allocator.
type directBackendGeneration struct {
	descriptor     airplaycontract.DeliveryGeneration
	route          directBackendRoute
	runtime        *mediaMTXRuntime
	mu             sync.Mutex
	startAttempted bool
}

func newDirectBackendGeneration(exe string, descriptor airplaycontract.DeliveryGeneration, session mediaMTXSessionConfig, private mediaMTXPorts) (*directBackendGeneration, error) {
	if strings.TrimSpace(exe) == "" || descriptor.SessionID == "" || descriptor.SessionEpoch == 0 || descriptor.Generation == 0 || descriptor.RequestID == "" ||
		descriptor.ArtifactPaths.SessionID != descriptor.SessionID || descriptor.ArtifactPaths.Generation != descriptor.Generation ||
		descriptor.Output.Width <= 0 || descriptor.Output.Height <= 0 ||
		descriptor.ExpectedRecordingWidth != descriptor.Output.Width || descriptor.ExpectedRecordingHeight != descriptor.Output.Height ||
		session.Path != mediaMTXPathName(descriptor.SessionID) || strings.TrimSpace(descriptor.Profile.Mode) == "" ||
		(descriptor.Profile.HLSVariant != "lowLatency" && descriptor.Profile.HLSVariant != "fmp4") || descriptor.Profile.HLSSegmentCount < 1 {
		return nil, airplaycontract.ErrDeliveryGenerationInvalid
	}
	duration, err := time.ParseDuration(descriptor.Profile.HLSSegmentDuration)
	if err != nil || duration <= 0 || (descriptor.Profile.HLSVariant == "lowLatency" && descriptor.Profile.HLSSegmentCount < mediaMTXMinimumLowLatencyHLSSegments) {
		return nil, airplaycontract.ErrDeliveryGenerationInvalid
	}
	for _, port := range []int{session.Ports.RTSP, session.Ports.RTP, session.Ports.RTCP} {
		if port <= 0 || port > 65535 {
			return nil, errDirectBackendRouterInvalidRoute
		}
	}
	used := map[int]bool{}
	for _, port := range []int{session.Ports.RTSP, session.Ports.RTP, session.Ports.RTCP, session.Ports.API, session.Ports.HLS, session.Ports.BackendRTSP, session.Ports.BackendRTP, session.Ports.BackendRTCP, session.Ports.RTMP} {
		if port > 0 {
			used[port] = true
		}
	}
	for _, port := range []int{private.API, private.HLS, private.BackendRTSP, private.BackendRTP, private.BackendRTCP} {
		if port <= 0 || port > 65535 || used[port] {
			return nil, errDirectBackendRouterInvalidRoute
		}
		used[port] = true
	}
	if private.BackendRTP%2 != 0 || private.BackendRTCP != private.BackendRTP+1 {
		return nil, errDirectBackendRouterInvalidRoute
	}
	private.RTSP, private.RTP, private.RTCP = session.Ports.RTSP, session.Ports.RTP, session.Ports.RTCP
	private.RTMP = 0
	user, pass, err := mediaMTXCredential()
	if err != nil {
		return nil, err
	}
	cfg := session
	cfg.Ports, cfg.PublishUser, cfg.PublishPass = private, user, pass
	cfg.EnableRTMP = false
	// Generation artifacts stay below the fresh runtime workdir; never inherit
	// the old generation's recording/HLS path or shared diagnostic log.
	cfg.HLSDirectory, cfg.DebugLogPath = "", ""
	cfg.HLSVariant, cfg.HLSSegmentCount, cfg.HLSSegmentDuration = descriptor.Profile.HLSVariant, descriptor.Profile.HLSSegmentCount, descriptor.Profile.HLSSegmentDuration
	cfg.HLSAlwaysRemux = true
	runtime := newMediaMTXRuntime(exe, cfg)
	runtime.isolatedHLS = true
	route := directBackendRoute{sessionID: descriptor.SessionID, sessionEpoch: descriptor.SessionEpoch,
		generation: descriptor.Generation, requestID: descriptor.RequestID, privateRTSPPort: private.BackendRTSP, runtime: runtime}
	return &directBackendGeneration{descriptor: descriptor, route: route, runtime: runtime}, nil
}

// Called by the serial session coordinator. Startup is one-shot even when
// canceled/failed; retries require a freshly allocated generation and owner.
func (g *directBackendGeneration) start(parent context.Context) error {
	return g.startUsing(parent, g.runtime.start)
}

func (g *directBackendGeneration) startUsing(parent context.Context, start func(context.Context) error) error {
	g.mu.Lock()
	if g.startAttempted {
		g.mu.Unlock()
		return errDirectBackendGenerationUsed
	}
	g.startAttempted = true
	g.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	return start(ctx)
}

func (g *directBackendGeneration) startWithOwner(parent context.Context, owner *Manager) error {
	if owner == nil {
		return errors.New("direct backend manager owner is missing")
	}
	return g.startUsing(parent, func(ctx context.Context) error { return owner.startOwnedDirectBackend(ctx, g.runtime) })
}

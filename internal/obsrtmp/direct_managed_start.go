package obsrtmp

import (
	"context"

	"imagepadserver/internal/airplaycontract"
)

// Called before gate.start or exposing the observer to AirPlay. Allocation is
// owned here; the existing AirPlay initial PreparePublisher call claims it
// once. Launch reservation is distinct from actual media/publication readiness.
func newInitialDirectDeliveryCoordinator(ctx context.Context, m *Manager, gate *rtspGate, runtime *mediaMTXRuntime, observer *directPublisherObserver, handle DirectSessionHandle, plan DirectDeliveryPlan) (*directDeliveryCoordinator, error) {
	if ctx == nil || m == nil || gate == nil || runtime == nil || observer == nil || handle.ID != plan.SessionID || handle.Generation == 0 {
		return nil, errDirectReconfigureUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ledger, err := airplaycontract.NewDeliveryLedger(observer.root, plan.SessionID, handle.Generation)
	if err != nil {
		return nil, err
	}
	cfg := runtime.cfg
	descriptor, err := ledger.Allocate(plan.SessionID, handle.Generation, 0, plan.RequestID,
		airplaycontract.DeliveryProfile{Mode: plan.Profile.Mode, Transport: plan.Profile.Transport, HLSVariant: cfg.HLSVariant, HLSSegmentCount: cfg.HLSSegmentCount, HLSSegmentDuration: cfg.HLSSegmentDuration},
		airplaycontract.DeliveryOutput(plan.Output))
	if err != nil {
		return nil, err
	}
	router, err := newDirectBackendRouter(directBackendRoute{sessionID: plan.SessionID, sessionEpoch: handle.Generation, generation: descriptor.Generation, requestID: plan.RequestID, privateRTSPPort: cfg.Ports.mediaMTXRTSPPort(), runtime: runtime})
	if err != nil {
		return nil, err
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if observer.sealed || observer.deliveryLedger != nil || len(observer.artifacts) != 0 {
		return nil, errDirectReconfigureUnavailable
	}
	observer.deliveryLedger = ledger
	gate.backendRouter = router
	c := &directDeliveryCoordinator{ctx: ctx, manager: m, gate: gate, ledger: ledger, observer: observer, active: descriptor, activePlan: plan}
	c.publishDeliveryStateLocked()
	observer.recoveryOwner = c
	return c, nil
}

package obsrtmp

import (
	"testing"
	"time"
)

// A session-owned local gate is not permission to request an external mapping.
func TestDirectHLSDoesNotPublishRTSPEndpoint(t *testing.T) {
	for _, direct := range []bool{false, true} {
		m := &Manager{directPublishing: direct, current: &Session{
			ID: "same", Generation: 7,
			ActiveContract: &OBSActiveSessionContract{SessionID: "same", LatencyProfile: NormalizeLatencyProfile("hls")},
		}}
		m.status.Publishing = true
		m.cb.OnRTSPReady = func(RTSPEndpoint) { t.Error("HLS requested external RTSP mapping") }
		endpoint := RTSPEndpoint{SessionID: "same", Generation: 7, Port: 52000, Path: "obs_same"}
		if m.setRTSPEndpoint(endpoint) || m.SetRTSPEndpointURL(endpoint, "rtsp://192.0.2.1:52000/obs_same", "mapped") {
			t.Errorf("direct=%v: HLS accepted external RTSP publication", direct)
		}
		if m.rtspEndpoint != nil || m.status.RTSPTURL != "" {
			t.Errorf("direct=%v: HLS exposed RTSP state", direct)
		}
	}
}

func TestDirectHLSNotifiesRTSPOnlyAfterProfileCommit(t *testing.T) {
	c, a, old, next, output, _ := directReconfigureReadyFixture(t)
	m := c.manager
	m.directDelivery = c
	m.current.Generation = 7
	m.current.ActiveContract.LatencyProfile = NormalizeLatencyProfile("hls")
	c.publishDeliveryStateLocked()
	var ready []RTSPEndpoint
	m.cb.OnRTSPReady = func(endpoint RTSPEndpoint) {
		if !m.IsSessionActive(endpoint.SessionID, endpoint.Generation) {
			t.Error("stale publication callback")
		}
		ready = append(ready, endpoint)
	}
	m.notifyManagedDeliveryChanged()
	if len(ready) != 0 {
		t.Fatal("prepared RTSP candidate exposed before commit")
	}
	if err := c.CommitDirectReconfigure(a, old, next, output, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	m.notifyManagedDeliveryChanged()
	if len(ready) != 1 || ready[0].Generation != 7 || ready[0].Port != 49001 {
		t.Fatalf("committed RTSP endpoint=%+v", ready)
	}
	if !m.SetRTSPEndpointURL(ready[0], "rtsp://192.0.2.1:49001/test", "mapped") {
		t.Fatal("committed endpoint rejected")
	}
	m.notifyManagedDeliveryChanged()
	if len(ready) != 1 || m.status.RTSPTURL != "rtsp://192.0.2.1:49001/test" {
		t.Fatal("notification repeated or erased published URL")
	}
}

func TestDirectRTSPPublicationRejectsPendingOrRevokedDelivery(t *testing.T) {
	for _, cause := range []string{"pending", "canceled", "wrong-epoch", "failed-route", "stopped"} {
		t.Run(cause, func(t *testing.T) {
			c, a, old, next, output, cancel := directReconfigureReadyFixture(t)
			m := c.manager
			m.directDelivery = c
			if err := c.CommitDirectReconfigure(a, old, next, output, time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			switch cause {
			case "pending":
				c.transactionActive = true
				c.publishDeliveryStateLocked()
			case "canceled":
				cancel()
			case "wrong-epoch":
				m.directHandle.Generation++
			case "failed-route":
				c.gate.backendRouter.failed = true
			case "stopped":
				m.running = false
			}
			m.cb.OnRTSPReady = func(RTSPEndpoint) { t.Error("invalid delivery requested publication") }
			m.notifyManagedDeliveryChanged()
			if m.rtspEndpoint != nil {
				t.Fatal("invalid delivery exposed endpoint")
			}
		})
	}
}

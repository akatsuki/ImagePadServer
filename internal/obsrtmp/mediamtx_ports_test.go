package obsrtmp

import "testing"

// MediaMTX rejects odd RTP ports and non-consecutive RTP/RTCP pairs, so the
// allocator must always produce even RTP ports with RTCP = RTP+1.
func TestAllocMediaMTXPortsRTPPairInvariants(t *testing.T) {
	for i := 0; i < 20; i++ {
		ports, err := allocMediaMTXPorts()
		if err != nil {
			t.Fatalf("allocMediaMTXPorts: %v", err)
		}
		if ports.RTP%2 != 0 {
			t.Fatalf("public RTP port %d must be even", ports.RTP)
		}
		if ports.RTCP != ports.RTP+1 {
			t.Fatalf("public RTCP %d must be RTP+1 (RTP %d)", ports.RTCP, ports.RTP)
		}
		if ports.BackendRTP%2 != 0 {
			t.Fatalf("backend RTP port %d must be even", ports.BackendRTP)
		}
		if ports.BackendRTCP != ports.BackendRTP+1 {
			t.Fatalf("backend RTCP %d must be RTP+1 (RTP %d)", ports.BackendRTCP, ports.BackendRTP)
		}
		unique := map[int]bool{}
		for _, p := range []int{ports.API, ports.HLS, ports.RTSP, ports.RTP, ports.RTCP, ports.BackendRTSP, ports.BackendRTP, ports.BackendRTCP} {
			if p <= 0 {
				t.Fatalf("port not allocated: %+v", ports)
			}
			if unique[p] {
				t.Fatalf("duplicate port %d in %+v", p, ports)
			}
			unique[p] = true
		}
	}
}

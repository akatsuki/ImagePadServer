package obsrtmp

import "testing"

func TestMediaMTXPortAllocationExcludesPreviousGeneration(t *testing.T) {
	old := mediaMTXPorts{API: 49000, HLS: 49001, RTMP: 49002, RTSP: 49003, RTP: 49004, RTCP: 49005, BackendRTSP: 49006, BackendRTP: 49008, BackendRTCP: 49009}
	previous := []int{49000, 49001, 49002, 49003, 49004, 49005, 49006, 49008, 49009}
	candidates := append(append([]int(nil), previous...), 50000, 50001, 50002, 50003, 50004)
	next := 0
	udp := 51000
	ports, err := allocMediaMTXPortsUsing([]mediaMTXPorts{old}, func() (int, error) {
		if next >= len(candidates) {
			t.Fatal("unexpected TCP allocation")
		}
		p := candidates[next]
		next++
		return p, nil
	}, func(seen map[int]bool) (int, int, error) {
		for _, p := range previous {
			if !seen[p] {
				t.Fatalf("old port %d is not excluded", p)
			}
		}
		p := udp
		udp += 2
		return p, p + 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ports.API != 50000 || ports.BackendRTSP != 50004 || ports.BackendRTP != 51002 {
		t.Fatalf("allocation reused old generation: %+v", ports)
	}
}

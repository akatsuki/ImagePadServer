package obsrtmp

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

// Real Manager startup/stop, without a sender. This is not an iPhone test.
func TestDirectBackendOwnedStartupRealManager(t *testing.T) {
	if os.Getenv("IMAGEPAD_DIRECT_BACKEND_OUTPUT_REAL") != "1" {
		t.Skip("isolated real backend opt-in required")
	}
	exe, err := ResolveMediaMTX()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_MEDIAMTX", exe) // explicitly installed binary; never download
	t.Setenv("IMAGEPAD_MEDIAMTX_DEBUG_LOG", "")
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	root := t.TempDir()
	m := &Manager{outDir: root, host: "127.0.0.1"}
	plan, err := newDirectDeliveryPlan("0123456789abcdef", "owned-start", NormalizeLatencyProfile(LatencyModeRTSPUltra), "720", video.ResolveQuality("720", 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	info, err := m.StartDirectPublishingWithPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.StopDirect(info.Handle, 7*time.Second) })
	m.mu.Lock()
	runtime := m.mtx
	reserved, monitored := len(m.directBackendReservations), m.directBackendMonitorCount
	m.mu.Unlock()
	if runtime == nil || reserved != 0 || monitored != 1 {
		t.Fatal("startup ownership not handed to the session monitor")
	}
	ports := []int{runtime.cfg.Ports.API, runtime.cfg.Ports.HLS, runtime.cfg.Ports.BackendRTSP, runtime.cfg.Ports.RTSP}
	config := runtime.configPath
	if !m.StopDirect(info.Handle, 7*time.Second) {
		t.Fatal("real backend stop not confirmed")
	}
	m.mu.Lock()
	clean := len(m.directBackendReservations) == 0 && len(m.directRetiringBackends) == 0 && m.directBackendMonitorCount == 0
	m.mu.Unlock()
	if !clean {
		t.Fatal("real backend cleanup retained completed owners")
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("real config not cleaned: %v", err)
	}
	for _, port := range ports {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Fatalf("owned listener %d survived stop", port)
		}
	}
	if pids, err := readMediaMTXProcessIDs(); err != nil || len(pids) != 0 {
		t.Fatalf("isolated PID registry not empty: %v / %v", pids, err)
	}
}

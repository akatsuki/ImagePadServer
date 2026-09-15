package obsrtmp

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// SYN: real MediaMTX + real H.264 publisher + real ffprobe. No iPhone, native
// bridge or active route promotion is claimed by this backend-only check.
func TestDirectBackendOutputRealPrivateRTSP(t *testing.T) {
	if os.Getenv("IMAGEPAD_DIRECT_BACKEND_OUTPUT_REAL") != "1" {
		t.Skip("set IMAGEPAD_DIRECT_BACKEND_OUTPUT_REAL=1 for isolated real RTSP validation")
	}
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir()) // never touch the live process registry
	t.Cleanup(func() {
		pids, err := readMediaMTXProcessIDs()
		if err != nil || len(pids) != 0 {
			t.Errorf("isolated backend registry not reaped: %v / %v", pids, err)
		}
	})
	mtx, err := ResolveMediaMTX() // same pinned version as real Manager startup
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]string{"mediamtx": mtx}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		tools[name] = path
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	startBackend := func() *mediaMTXRuntime {
		ports, err := allocMediaMTXPorts()
		if err != nil {
			t.Fatal(err)
		}
		user, pass, err := mediaMTXCredential()
		if err != nil {
			t.Fatal(err)
		}
		rt := newMediaMTXRuntime(tools["mediamtx"], mediaMTXSessionConfig{
			Path: mediaMTXPathName("private-output"), Ports: ports, PublishUser: user, PublishPass: pass,
			AdvertiseHost: "127.0.0.1", RTSPTCPOnly: true,
		})
		startCtx, stopStart := context.WithTimeout(ctx, 10*time.Second)
		defer stopStart()
		if err := rt.start(startCtx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := rt.stop(3 * time.Second); err != nil {
				t.Errorf("owned backend cleanup: %v", err)
				return
			}
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ports.BackendRTSP), 100*time.Millisecond)
			if err == nil {
				conn.Close()
				t.Error("private listener survived confirmed backend stop")
			}
		})
		return rt
	}
	startVideo := func(rt *mediaMTXRuntime, size string) {
		pubCtx, stopPub := context.WithCancel(ctx)
		cmd := exec.CommandContext(pubCtx, tools["ffmpeg"], "-hide_banner", "-loglevel", "error",
			"-re", "-f", "lavfi", "-i", "testsrc2=size="+size+":rate=30", "-an",
			"-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-tune", "zerolatency",
			"-g", "30", "-bf", "0", "-pix_fmt", "yuv420p", "-f", "rtsp", "-rtsp_transport", "tcp", rt.directPublishURL())
		hideWindow(cmd)
		cmd.WaitDelay = 250 * time.Millisecond
		if err := cmd.Start(); err != nil {
			stopPub()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			stopPub()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("owned test publisher did not exit")
			}
		})
		readyCtx, stopReady := context.WithTimeout(ctx, 5*time.Second)
		defer stopReady()
		for !rt.pathReady(readyCtx) {
			select {
			case <-readyCtx.Done():
				t.Fatal("synthetic publisher path did not become ready")
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	old := startBackend()
	startVideo(old, "320x240")
	descriptor, route := backendOutputIdentityFixture(t)
	descriptor.Profile = testContractDeliveryProfile()
	ports, err := allocMediaMTXPorts()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newDirectBackendGeneration(tools["mediamtx"], descriptor, old.cfg, ports)
	if err != nil {
		t.Fatal(err)
	}
	candidate := owner.runtime
	manager := &Manager{}
	route = owner.route
	t.Cleanup(func() {
		if err := manager.stopOwnedDirectBackend(candidate, 3*time.Second); err != nil {
			t.Errorf("candidate cleanup: %v", err)
			return
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ports.BackendRTSP), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Error("candidate listener survived cleanup")
		}
	})
	if err := owner.startWithOwner(ctx, manager); err != nil {
		t.Fatal(err)
	}
	// Same public path, different private runtime. Old media cannot fill a
	// candidate with no publisher, despite the old session being perfectly ready.
	if _, err := probeDirectBackendGeneration(ctx, tools["ffprobe"], descriptor, route, exec.CommandContext); err == nil {
		t.Fatal("empty candidate accepted old runtime output")
	}
	startVideo(candidate, "640x360")
	proof, err := probeDirectBackendGeneration(ctx, tools["ffprobe"], descriptor, route, exec.CommandContext)
	if err != nil || !proof.matches(descriptor, route) {
		t.Fatalf("real candidate proof: %+v / %v", proof.decoded, err)
	}
	if _, err := runDirectBackendFFprobe(ctx, tools["ffprobe"], old.backendRTSPURL(), 640, 360, exec.CommandContext); err == nil {
		t.Fatal("old 320x240 output satisfied candidate 640x360 proof")
	}
	if !candidate.hlsReady(ctx, NormalizeLatencyProfile(LatencyModeRTSPUltra)) {
		t.Fatal("candidate HLS not readable")
	}
	candidate.mu.Lock()
	workdir := candidate.dir
	candidate.mu.Unlock()
	if _, err := os.Stat(filepath.Join(workdir, "hls")); err != nil {
		t.Fatalf("generation-owned HLS not created: %v", err)
	}
	if err := manager.stopOwnedDirectBackend(candidate, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("generation workdir survived stop: %v", err)
	}
	if !old.pathReady(ctx) {
		t.Fatal("candidate shutdown stopped old backend")
	}
	if len(manager.directBackendReservations) != 0 {
		t.Fatal("confirmed candidate owner was not released")
	}
	t.Logf("SYN video-only private RTSP decoded: %dx%d frames=%d keys=%d PTS=%.6f..%.6f; empty/old candidate rejected",
		proof.decoded.Width, proof.decoded.Height, proof.decoded.Frames, proof.decoded.KeyFrames, proof.decoded.FirstPTS, proof.decoded.LastPTS)
}

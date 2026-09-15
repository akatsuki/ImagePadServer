package obsrtmp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectBackendGenerationOwnsOnlyItsRuntimeAndHLS(t *testing.T) {
	descriptor, oldRoute := backendOutputIdentityFixture(t)
	descriptor.Profile = testContractDeliveryProfile()
	sessionCfg := oldRoute.runtime.cfg
	sessionCfg.Ports.RTP, sessionCfg.Ports.RTCP = 49002, 49003
	sessionCfg.Ports.API, sessionCfg.Ports.HLS = 49004, 49005
	sessionCfg.Ports.BackendRTP, sessionCfg.Ports.BackendRTCP = 49006, 49007
	sessionCfg.HLSDirectory = t.TempDir()
	sentinel := filepath.Join(sessionCfg.HLSDirectory, "old.m3u8")
	if err := os.WriteFile(sentinel, []byte("old-session"), 0600); err != nil {
		t.Fatal(err)
	}
	ports := mediaMTXPorts{API: 49100, HLS: 49101, BackendRTSP: 49102, BackendRTP: 49104, BackendRTCP: 49105}
	candidate, err := newDirectBackendGeneration("mediamtx", descriptor, sessionCfg, ports)
	if err != nil {
		t.Fatal(err)
	}
	proc := newFakeProcess()
	proc.exitOnStop = true
	starts := 0
	owner := &Manager{}
	registeredBeforeLaunch := false
	var configPath, rendered string
	candidate.runtime.startProcess = func(_ context.Context, _ string, path string) (managedProcess, error) {
		starts++
		owner.mu.Lock()
		registeredBeforeLaunch = owner.directBackendReservations[candidate.runtime] != nil
		owner.mu.Unlock()
		configPath = path
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rendered = string(data)
		return proc, nil
	}
	candidate.runtime.checkHealth = func(context.Context, string) error { return nil }
	t.Cleanup(func() { proc.finish(nil); _ = candidate.runtime.stop(time.Second) })
	if err := candidate.startWithOwner(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if !registeredBeforeLaunch {
		t.Fatal("candidate was not registered before launch")
	}
	if candidate.route.privateRTSPPort != 49102 || candidate.route.generation != 2 ||
		candidate.runtime.cfg.Ports.RTSP != 49001 || candidate.runtime.cfg.Ports.RTP != 49002 || candidate.runtime.cfg.Ports.RTCP != 49003 ||
		candidate.runtime.cfg.Path != sessionCfg.Path {
		t.Fatal("candidate changed public identity")
	}
	if !strings.Contains(rendered, "hlsDirectory:") || strings.Contains(rendered, filepath.ToSlash(sessionCfg.HLSDirectory)) || !strings.Contains(rendered, filepath.ToSlash(filepath.Join(filepath.Dir(configPath), "hls"))) {
		t.Fatal("candidate HLS not isolated in its owned workdir")
	}
	if !strings.Contains(rendered, "hlsVariant: lowLatency") || !strings.Contains(rendered, "hlsSegmentCount: 7") || !strings.Contains(rendered, "hlsSegmentDuration: 1s") {
		t.Fatal("descriptor profile not applied")
	}
	if err := candidate.startWithOwner(t.Context(), owner); err == nil || starts != 1 {
		t.Fatal("generation restarted or reused artifacts")
	}
	if err := owner.stopOwnedDirectBackend(candidate.runtime, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate config survived: %v", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "old-session" {
		t.Fatal("candidate cleanup changed old HLS")
	}
}

func TestDirectBackendGenerationRejectsSharedPrivatePorts(t *testing.T) {
	descriptor, route := backendOutputIdentityFixture(t)
	descriptor.Profile = testContractDeliveryProfile()
	base := route.runtime.cfg
	base.Ports.RTP, base.Ports.RTCP = 49002, 49003
	base.Ports.API, base.Ports.HLS = 49004, 49005
	base.Ports.BackendRTP, base.Ports.BackendRTCP = 49006, 49007
	for _, occupied := range []int{0, 65536, 49000, 49001, 49002, 49003, 49004, 49005, 49006, 49007, 49101} {
		ports := mediaMTXPorts{API: occupied, HLS: 49101, BackendRTSP: 49102, BackendRTP: 49104, BackendRTCP: 49105}
		if _, err := newDirectBackendGeneration("mediamtx", descriptor, base, ports); err == nil {
			t.Fatalf("candidate reused port %d", occupied)
		}
	}
}

func TestDirectBackendGenerationFailedStartKeepsRetiringOwner(t *testing.T) {
	descriptor, old := backendOutputIdentityFixture(t)
	descriptor.Profile = testContractDeliveryProfile()
	base := old.runtime.cfg
	base.Ports.RTP, base.Ports.RTCP = 49002, 49003
	ports := mediaMTXPorts{API: 49100, HLS: 49101, BackendRTSP: 49102, BackendRTP: 49104, BackendRTCP: 49105}
	candidate, err := newDirectBackendGeneration("mediamtx", descriptor, base, ports)
	if err != nil {
		t.Fatal(err)
	}
	proc := newFakeProcess()
	proc.exitOnKill = false
	candidate.runtime.stopGrace = 20 * time.Millisecond
	candidate.runtime.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	candidate.runtime.checkHealth = func(context.Context, string) error { return errors.New("unhealthy") }
	t.Cleanup(func() { proc.finish(nil); _ = candidate.runtime.stop(time.Second) })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := candidate.start(ctx); !errors.Is(err, errMediaMTXProcessExitUnconfirmed) {
		t.Fatalf("lost ownership error: %v", err)
	}
	if candidate.runtime.proc != proc || !candidate.runtime.retiring || candidate.runtime.dir == "" {
		t.Fatal("retiring owner not retained")
	}
	if err := candidate.start(t.Context()); err == nil {
		t.Fatal("failed generation can be reused")
	}
}

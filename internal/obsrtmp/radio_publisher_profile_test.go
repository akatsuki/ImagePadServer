package obsrtmp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRadioManagerDefaultsToCPUPublisherDespiteGPUEnvironment(t *testing.T) {
	t.Setenv("IMAGEPAD_ENCODER_MODE", "gpu")
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "1")
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	var cpuStarts, gpuStarts atomic.Int32
	started := make(chan string, 2)
	m.startPublisher = func(context.Context, string) (radioPublisher, error) {
		cpuStarts.Add(1)
		started <- "cpu"
		return &fakePublisher{exit: make(chan error)}, nil
	}
	m.startPlaylistGPUPublisher = func(context.Context, string) (radioPublisher, error) {
		gpuStarts.Add(1)
		started <- "gpu"
		return &fakePublisher{exit: make(chan error)}, nil
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		if got != "cpu" {
			t.Fatalf("default publisher profile selected %q, want cpu", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publisher was not started")
	}
	if cpuStarts.Load() != 1 || gpuStarts.Load() != 0 {
		t.Fatalf("publisher starts = cpu:%d gpu:%d, want cpu:1 gpu:0", cpuStarts.Load(), gpuStarts.Load())
	}
}

func TestRadioManagerUsesExplicitPlaylistGPUPublisherWithoutCPUFallback(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.SetPublisherProfile(func() RadioPublisherProfile {
		return RadioPublisherProfilePlaylistGPUEvaluation
	})
	var cpuStarts, gpuStarts atomic.Int32
	started := make(chan string, 2)
	m.startPublisher = func(context.Context, string) (radioPublisher, error) {
		cpuStarts.Add(1)
		started <- "cpu"
		return &fakePublisher{exit: make(chan error)}, nil
	}
	m.startPlaylistGPUPublisher = func(context.Context, string) (radioPublisher, error) {
		gpuStarts.Add(1)
		started <- "gpu"
		return &fakePublisher{exit: make(chan error)}, nil
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		if got != "gpu" {
			t.Fatalf("explicit GPU publisher profile selected %q, want gpu", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("GPU publisher was not started")
	}
	if cpuStarts.Load() != 0 || gpuStarts.Load() != 1 {
		t.Fatalf("publisher starts = cpu:%d gpu:%d, want cpu:0 gpu:1", cpuStarts.Load(), gpuStarts.Load())
	}
	status := m.Status()
	if status.ActiveSession == nil || status.ActiveSession.PublisherProfile != RadioPublisherProfilePlaylistGPUEvaluation {
		t.Fatalf("active publisher profile = %+v, want explicit playlist GPU", status.ActiveSession)
	}
}

func TestRadioManagerRejectsUnknownPublisherProfileWithoutStartingPublisher(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.SetPublisherProfile(func() RadioPublisherProfile {
		return RadioPublisherProfile("unexpected-profile")
	})
	var publisherStarts atomic.Int32
	m.startPublisher = func(context.Context, string) (radioPublisher, error) {
		publisherStarts.Add(1)
		return &fakePublisher{exit: make(chan error)}, nil
	}
	m.startPlaylistGPUPublisher = func(context.Context, string) (radioPublisher, error) {
		publisherStarts.Add(1)
		return &fakePublisher{exit: make(chan error)}, nil
	}
	if err := m.Start(); err == nil {
		t.Fatal("Start accepted an unknown publisher profile")
	}
	if publisherStarts.Load() != 0 {
		t.Fatalf("publisher started %d times after invalid profile", publisherStarts.Load())
	}
}

func TestRadioGPUEvaluationReadinessDoesNotProbeHLS(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.SetPublisherProfile(func() RadioPublisherProfile {
		return RadioPublisherProfilePlaylistGPUEvaluation
	})
	var hlsProbes atomic.Int32
	h.runtime.hlsReadyFunc = func(context.Context, LatencyProfile) bool {
		hlsProbes.Add(1)
		return true
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "rtsp-ready")
	time.Sleep(250 * time.Millisecond)
	if got := hlsProbes.Load(); got != 0 {
		t.Fatalf("GPU evaluation readiness issued %d HLS probes, want 0", got)
	}
	if st := m.Status(); st.HLSReady {
		t.Fatalf("GPU evaluation must not claim HLS readiness when HLS is not probed: %+v", st)
	}
}

func TestRadioCPUReadinessStillProbesHLS(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.SetPublisherProfile(func() RadioPublisherProfile {
		return RadioPublisherProfileCPUDefault
	})
	var hlsProbes atomic.Int32
	h.runtime.hlsReadyFunc = func(context.Context, LatencyProfile) bool {
		hlsProbes.Add(1)
		return true
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "rtsp-ready")
	if got := hlsProbes.Load(); got == 0 {
		t.Fatal("CPU readiness stopped probing HLS")
	}
	if st := m.Status(); !st.HLSReady {
		t.Fatalf("CPU readiness should retain HLS readiness: %+v", st)
	}
}

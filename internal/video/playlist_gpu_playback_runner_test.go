package video

import (
	"context"
	"testing"
)

func TestPlaylistGPUPlaybackRunnerRequiresBoundedOutputSink(t *testing.T) {
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlaylistGPUPlaybackRunner(&PlaylistGPUWorker{}, 1, assets, timeline, nil); err == nil {
		t.Fatal("runner accepted a nil output sink")
	}
}

func TestPlaylistGPUPlaybackRunnerFailsClosedWithoutSidecar(t *testing.T) {
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	sinkCalls := 0
	runner, err := NewPlaylistGPUPlaybackRunner(&PlaylistGPUWorker{}, 1, assets, timeline, func(EncodedH264Frame) error {
		sinkCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.RenderNextWindow(context.Background()); err == nil {
		t.Fatal("runner rendered without a connected sidecar")
	}
	if runner.NextFrame() != 0 || sinkCalls != 0 {
		t.Fatalf("failed window advanced or reached sink: next=%d sink=%d", runner.NextFrame(), sinkCalls)
	}
	if err := runner.Complete(); err == nil {
		t.Fatal("partial runner stream finalized")
	}
}

func TestPlaylistGPUPlaybackRunnerStopsNormalSubmit(t *testing.T) {
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewPlaylistGPUPlaybackRunner(&PlaylistGPUWorker{}, 1, assets, timeline, func(EncodedH264Frame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.StopNormalSubmit(); err != nil {
		t.Fatal(err)
	}
	if !runner.Stopped() {
		t.Fatal("runner did not stop normal submit")
	}
	if err := runner.RenderNextWindow(context.Background()); err == nil {
		t.Fatal("runner accepted a new window after stop")
	}
}

func TestComputePlaylistBatchSizeAmortizesFixedRPC(t *testing.T) {
	cases := []struct {
		name        string
		submit, drn float64
		frames      int
		want        int
	}{
		{"degenerate zero frames", 0.010, 0.009, 0, PlaylistGPUProbeBatchFrames},
		{"zero submit uses max window (ring)", 0, 0.009, 8, GPUH264MaxBatchFrames},
		{"degenerate zero drain", 0.010, 0, 8, PlaylistGPUProbeBatchFrames},
		{"busy GPU clamps to min", 0.100, 0.010, 8, PlaylistGPUProbeBatchFrames},
		{"idle GPU clamps to max", 0.010, 0.009, 8, GPUH264MaxBatchFrames},
		{"neutral stays at probe size", 0.016, 0.008, 8, PlaylistGPUProbeBatchFrames},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computePlaylistBatchSize(tc.submit, tc.drn, tc.frames)
			if got != tc.want {
				t.Fatalf("computePlaylistBatchSize(%v, %v, %d) = %d, want %d", tc.submit, tc.drn, tc.frames, got, tc.want)
			}
		})
	}
}

func TestPlaylistRealTimeBudgetExceededFailsClosedUnderSharedLoad(t *testing.T) {
	cases := []struct {
		name        string
		submit, drn float64
		frames      int
		fps         uint32
		want        bool
	}{
		{"degenerate zero frames", 0.010, 0.009, 0, 30, false},
		{"degenerate zero submit", 0, 0.009, 8, 30, false},
		{"degenerate zero fps", 0.010, 0.009, 8, 0, false},
		{"idle GPU sustains real-time", 0.010, 0.009, 8, 30, false},
		{"busy but sustainable", 0.100, 0.010, 8, 30, false},
		{"overloaded GPU fails the budget", 0.400, 0.010, 8, 30, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := playlistRealTimeBudgetExceeded(tc.submit, tc.drn, tc.frames, tc.fps); got != tc.want {
				t.Fatalf("playlistRealTimeBudgetExceeded(%v, %v, %d, %d) = %v, want %v", tc.submit, tc.drn, tc.frames, tc.fps, got, tc.want)
			}
		})
	}
}

func TestPlaylistGPUPlaybackRunnerRenderAllFailsClosedWithoutSidecar(t *testing.T) {
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	sinkCalls := 0
	runner, err := NewPlaylistGPUPlaybackRunner(&PlaylistGPUWorker{}, 1, assets, timeline, func(EncodedH264Frame) error {
		sinkCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.RenderAll(context.Background()); err == nil {
		t.Fatal("runner rendered without a connected sidecar")
	}
	if runner.NextFrame() != 0 || sinkCalls != 0 {
		t.Fatalf("failed RenderAll advanced or reached sink: next=%d sink=%d", runner.NextFrame(), sinkCalls)
	}
	if err := runner.Complete(); err == nil {
		t.Fatal("failed RenderAll left a completable partial stream")
	}
}

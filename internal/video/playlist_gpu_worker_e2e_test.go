package video

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlaylistGPUWorkerRealSidecarTimelineH264E2E(t *testing.T) {
	executable := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if executable == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not set")
	}
	if _, err := os.Stat(filepath.Clean(executable)); err != nil {
		t.Fatalf("configured playlist sidecar is unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// The pipelined submit/drain trace markers are emitted only when stage
	// timing is enabled; the overlap assertion below depends on them.
	t.Setenv("IMAGEPAD_GPU_STAGE_TIMING", "1")
	worker, err := StartPlaylistGPUWorker(ctx, executable, "real-playlist-e2e")
	if err != nil {
		t.Fatal(err)
	}
	process := worker.process
	defer worker.Close()
	// Eight frames produce two four-frame command chunks, so the trace must
	// show the second chunk's render/submit before the first frame's drain.
	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"},
		Analysis: AudioAnalysis{FPS: 30, Duration: 8.0 / 30.0, Frames: make([]AudioFrame, 8)},
	}
	assets, timeline, err := worker.PrepareCompiledTrack(ctx, 1, input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	if assets.TextOverlay == nil || assets.TextOverlay.Kind != "screen_rgba" || len(assets.TextOverlay.Payload) == 0 || assets.TextOverlay.AssetHash == "" {
		t.Fatalf("playlist text overlay is not a CPU-rasterized screen_rgba asset: %+v", assets.TextOverlay)
	}
	frames := make([]EncodedH264Frame, 0, timeline.FrameCount)
	if err := worker.RenderPreparedTimeline(ctx, 1, assets, timeline, func(frame EncodedH264Frame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if uint64(len(frames)) != timeline.FrameCount {
		t.Fatalf("encoded frame count = %d, want %d", len(frames), timeline.FrameCount)
	}
	for i, frame := range frames {
		if err := frame.Validate(); err != nil {
			t.Fatalf("frame %d validation failed: %v", i, err)
		}
		if frame.Sequence != timeline.Frames[i].Sequence || frame.PTSNs != timeline.Frames[i].PTSNs {
			t.Fatalf("frame %d identity = sequence %d/%d pts %d/%d", i, frame.Sequence, timeline.Frames[i].Sequence, frame.PTSNs, timeline.Frames[i].PTSNs)
		}
		if frame.PixelReadbackBytes != 0 {
			t.Fatalf("frame %d reported pixel readback: %d", i, frame.PixelReadbackBytes)
		}
	}
	_ = worker.Close()
	report := process.Diagnostics().Stderr
	for _, marker := range []string{
		"rust_playlist_trace event=render_chunk_submit",
		"rust_playlist_trace event=nvenc_submit",
		"rust_playlist_trace event=nvenc_drain",
	} {
		if !strings.Contains(report, marker) {
			t.Fatalf("runtime trace missing %q; stderr=%s", marker, report)
		}
	}
	renderChunk1 := strings.Index(report, "rust_playlist_trace event=render_chunk_submit chunk_index=1")
	drainFrame0 := strings.Index(report, "rust_playlist_trace event=nvenc_drain frame_index=0")
	if renderChunk1 < 0 || drainFrame0 < 0 || renderChunk1 >= drainFrame0 {
		t.Fatalf("runtime trace did not show pipelined render(chunk 1) before drain(frame 0); stderr=%s", report)
	}
}

func TestPlaylistGPUWorkerRealSidecarTransitionFadeTailE2E(t *testing.T) {
	executable := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if executable == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not set")
	}
	if _, err := os.Stat(filepath.Clean(executable)); err != nil {
		t.Fatalf("configured playlist sidecar is unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	worker, err := StartPlaylistGPUWorker(ctx, executable, "real-playlist-transition-e2e")
	if err != nil {
		t.Fatal(err)
	}
	process := worker.process
	defer worker.Close()
	assets, source, err := worker.PrepareCompiledTrack(ctx, 7, playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	targetAssets, target, err := worker.CompilePreparedTrackAssets(ctx, playlistGPUWorkerTestInput(), "track-b")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewPlaylistGPUTransitionController(7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.AttachTransitionController(controller); err != nil {
		t.Fatal(err)
	}
	plan, err := CompilePlaylistTransition(TransitionRequest{
		Schema:               GPUPlaylistTimelineSchema,
		Epoch:                7,
		Reason:               PlaylistTransitionTrackChange,
		SourceTrackID:        "track-a",
		PlaybackHeadSequence: 10,
		PlaybackHeadPTSNs:    333_333_330,
	}, "track-b", 15, 500_000_000, 2, 3, 33_333_333, AudioFadePlan{Curve: "linear", DurationNS: 99_999_999})
	if err != nil {
		t.Fatal(err)
	}
	frames := make([]EncodedH264Frame, 0, plan.DurationFrames+target.FrameCount)
	if err := worker.ExecutePlaylistGPUContinuousTrack(ctx, assets, source, targetAssets, target, plan, 8, func(frame EncodedH264Frame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantFrames := plan.DurationFrames + target.FrameCount
	if uint64(len(frames)) != wantFrames {
		t.Fatalf("continuous transition encoded frames = %d, want %d", len(frames), wantFrames)
	}
	if worker.TransitionState() != PlaylistGPUStateRunning {
		t.Fatalf("continuous transition state = %s, want resumable %s", worker.TransitionState(), PlaylistGPUStateRunning)
	}
	for i, frame := range frames {
		if err := frame.Validate(); err != nil {
			t.Fatalf("fade frame %d validation failed: %v", i, err)
		}
		if frame.PixelReadbackBytes != 0 {
			t.Fatalf("fade frame %d reported pixel readback: %d", i, frame.PixelReadbackBytes)
		}
		if i > 0 && frame.Sequence != frames[i-1].Sequence+1 {
			t.Fatalf("frame %d sequence=%d is not contiguous after %d", i, frame.Sequence, frames[i-1].Sequence)
		}
		if i > 0 && frame.PTSNs <= frames[i-1].PTSNs {
			t.Fatalf("frame %d PTS=%d is not strictly increasing after %d", i, frame.PTSNs, frames[i-1].PTSNs)
		}
	}
	_ = worker.Close()
	report := process.Diagnostics().Stderr
	if !strings.Contains(report, "rust_h264_flush_seconds=") {
		t.Fatalf("transition trace missing flush/EOS evidence; stderr=%s", report)
	}
}

func TestPlaylistGPUWorkerRealSidecarTransitionReliabilityFixedSeed20E2E(t *testing.T) {
	executable := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if executable == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not set")
	}
	if _, err := os.Stat(filepath.Clean(executable)); err != nil {
		t.Fatalf("configured playlist sidecar is unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	rng := rand.New(rand.NewSource(0x5eed2026))
	boundaries := []string{"track-start", "batch-before", "batch-boundary", "batch-inside", "track-end", "stop", "track-change", "stale-epoch", "rapid-repeat"}
	for iteration := 0; iteration < 20; iteration++ {
		boundary := boundaries[iteration%len(boundaries)]
		epoch := uint64(iteration + 1)
		baseSequence := uint64(1000 + rng.Intn(500))
		if boundary == "track-start" {
			baseSequence = 0
		}
		reason := PlaylistTransitionStop
		targetID := ""
		if boundary == "track-change" || iteration%3 == 0 {
			reason = PlaylistTransitionTrackChange
			targetID = "track-next"
		}
		worker, err := StartPlaylistGPUWorker(ctx, executable, fmt.Sprintf("real-playlist-reliability-%02d", iteration))
		if err != nil {
			t.Fatalf("iteration %d (%s): start worker: %v", iteration, boundary, err)
		}
		process := worker.process
		assets, source, err := worker.PrepareCompiledTrack(ctx, epoch, playlistGPUWorkerTestInput(), "track-current")
		if err != nil {
			_ = worker.Close()
			t.Fatalf("iteration %d (%s): prepare track: %v", iteration, boundary, err)
		}
		controller, err := NewPlaylistGPUTransitionController(epoch, baseSequence)
		if err != nil {
			_ = worker.Close()
			t.Fatalf("iteration %d (%s): create controller: %v", iteration, boundary, err)
		}
		if err := worker.AttachTransitionController(controller); err != nil {
			_ = worker.Close()
			t.Fatalf("iteration %d (%s): attach controller: %v", iteration, boundary, err)
		}
		plan, err := CompilePlaylistTransition(TransitionRequest{
			Schema: GPUPlaylistTimelineSchema, Epoch: epoch, Reason: reason, SourceTrackID: "track-current",
			PlaybackHeadSequence: baseSequence, PlaybackHeadPTSNs: int64(baseSequence) * 33_333_333,
		}, targetID, baseSequence+20, int64(baseSequence+20)*33_333_333, 2, 6, 33_333_333, AudioFadePlan{Curve: "linear", DurationNS: 199_999_998})
		if err != nil {
			_ = worker.Close()
			t.Fatalf("iteration %d (%s): compile plan: %v", iteration, boundary, err)
		}
		stale := plan
		stale.Epoch++
		if err := controller.Request(stale); err == nil {
			_ = worker.Close()
			t.Fatalf("iteration %d (%s): stale epoch was accepted", iteration, boundary)
		}
		var targetAssets TrackAssets
		var target TrackTimeline
		if reason == PlaylistTransitionTrackChange {
			targetAssets, target, err = worker.CompilePreparedTrackAssets(ctx, playlistGPUWorkerTestInput(), targetID)
			if err != nil {
				_ = worker.Close()
				t.Fatalf("iteration %d (%s): compile target track: %v", iteration, boundary, err)
			}
		}
		frames := make([]EncodedH264Frame, 0, plan.DurationFrames+target.FrameCount)
		execute := func(frame EncodedH264Frame) error {
			frames = append(frames, frame)
			return nil
		}
		if reason == PlaylistTransitionTrackChange {
			err = worker.ExecutePlaylistGPUContinuousTrack(ctx, assets, source, targetAssets, target, plan, epoch+1, execute)
		} else {
			err = worker.ExecutePlaylistGPUTransition(ctx, assets, source, plan, execute)
		}
		if err != nil {
			_ = worker.Close()
			t.Fatalf("iteration %d (%s): execute transition: %v", iteration, boundary, err)
		}
		wantFrames := plan.DurationFrames
		if reason == PlaylistTransitionTrackChange {
			wantFrames += target.FrameCount
		}
		if uint64(len(frames)) != wantFrames {
			_ = worker.Close()
			t.Fatalf("iteration %d (%s): frame count=%d want=%d", iteration, boundary, len(frames), wantFrames)
		}
		for frameIndex, frame := range frames {
			if err := frame.Validate(); err != nil {
				_ = worker.Close()
				t.Fatalf("iteration %d (%s): frame %d validation: %v", iteration, boundary, frameIndex, err)
			}
			if frame.PixelReadbackBytes != 0 {
				_ = worker.Close()
				t.Fatalf("iteration %d (%s): frame %d pixel readback=%d", iteration, boundary, frameIndex, frame.PixelReadbackBytes)
			}
			if frameIndex > 0 && frame.Sequence != frames[frameIndex-1].Sequence+1 {
				_ = worker.Close()
				t.Fatalf("iteration %d (%s): frame %d sequence=%d is not contiguous", iteration, boundary, frameIndex, frame.Sequence)
			}
			if frameIndex > 0 && frame.PTSNs <= frames[frameIndex-1].PTSNs {
				_ = worker.Close()
				t.Fatalf("iteration %d (%s): frame %d PTS=%d is not strictly increasing", iteration, boundary, frameIndex, frame.PTSNs)
			}
		}
		wantState := PlaylistGPUStateTransitionComplete
		if reason == PlaylistTransitionTrackChange {
			wantState = PlaylistGPUStateRunning
		}
		if worker.TransitionState() != wantState {
			_ = worker.Close()
			t.Fatalf("iteration %d (%s): state=%s want=%s", iteration, boundary, worker.TransitionState(), wantState)
		}
		if boundary == "rapid-repeat" {
			if err := controller.Request(plan); err == nil {
				_ = worker.Close()
				t.Fatalf("iteration %d (%s): completed transition accepted a repeated request", iteration, boundary)
			}
		}
		_ = worker.Close()
		report := process.Diagnostics().Stderr
		if !strings.Contains(report, "output_fence_completed=") || !strings.Contains(report, "rust_h264_flush_seconds=") {
			t.Fatalf("iteration %d (%s): missing fence/flush trace: %s", iteration, boundary, report)
		}
	}
}

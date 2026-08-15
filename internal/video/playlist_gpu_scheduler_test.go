package video

import (
	"context"
	"testing"
)

func TestPlaylistGPUBoundedSchedulerRequiresWorker(t *testing.T) {
	if _, err := NewPlaylistGPUBoundedScheduler(nil); err == nil {
		t.Fatal("nil worker must be rejected")
	}
}

func TestPlaylistGPUBoundedSchedulerRejectsOversizedWindowBeforeSubmit(t *testing.T) {
	scheduler, err := NewPlaylistGPUBoundedScheduler(&PlaylistGPUWorker{})
	if err != nil {
		t.Fatal(err)
	}
	chunk := TimelineChunk{Frames: make([]FrameDirective, PlaylistGPUWindowSlots+1)}
	if err := scheduler.Submit(context.Background(), 1, 1, "hash", 1, chunk); err == nil {
		t.Fatal("scheduler must reject a window larger than eight frames")
	}
	if scheduler.Pending() {
		t.Fatal("rejected submit must not leave a pending window")
	}
}

func TestPlaylistGPUBoundedSchedulerFailsClosedWithoutConnectedWorker(t *testing.T) {
	scheduler, err := NewPlaylistGPUBoundedScheduler(&PlaylistGPUWorker{})
	if err != nil {
		t.Fatal(err)
	}
	chunk := TimelineChunk{Frames: make([]FrameDirective, 1)}
	if err := scheduler.Submit(context.Background(), 1, 1, "hash", 1, chunk); err == nil {
		t.Fatal("scheduler must fail closed when the worker process is not connected")
	}
	if scheduler.Pending() {
		t.Fatal("failed submit must not leave a pending window")
	}
}

func TestPlaylistGPUBoundedSchedulerRejectsFlushWithPendingWindow(t *testing.T) {
	worker := &PlaylistGPUWorker{}
	scheduler := &PlaylistGPUBoundedScheduler{
		worker: worker,
		pending: &playlistSchedulerPending{
			batchID: 1,
			epoch:   1,
			frames:  []FrameDirective{{Sequence: 1, PTSNs: 1}},
		},
	}
	if err := scheduler.Flush(context.Background()); err == nil {
		t.Fatal("flush must reject an undrained pending window")
	}
}

func TestPlaylistGPUBoundedSchedulerStopsNormalSubmitAfterTransitionRequest(t *testing.T) {
	plan := testPlaylistGPUTransitionPlan(t)
	controller, err := NewPlaylistGPUTransitionController(plan.Epoch, plan.PlaybackHeadSequence)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Request(plan); err != nil {
		t.Fatal(err)
	}
	if err := controller.StopNormalSubmit(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewPlaylistGPUBoundedScheduler(&PlaylistGPUWorker{})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.AttachTransitionController(controller); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Submit(context.Background(), 1, 1, "hash", 1, TimelineChunk{}); err == nil {
		t.Fatal("normal submit must be rejected after transition stop")
	}
}

func TestPlaylistGPUBoundedSchedulerFadeSubmitRequiresFadeState(t *testing.T) {
	plan := testPlaylistGPUTransitionPlan(t)
	controller, err := NewPlaylistGPUTransitionController(plan.Epoch, plan.PlaybackHeadSequence)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Request(plan); err != nil {
		t.Fatal(err)
	}
	if err := controller.StopNormalSubmit(); err != nil {
		t.Fatal(err)
	}
	if err := controller.BeginDrainInFlight(); err != nil {
		t.Fatal(err)
	}
	if err := controller.BeginFadeTailRendering(); err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewPlaylistGPUBoundedScheduler(&PlaylistGPUWorker{})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.AttachTransitionController(controller); err != nil {
		t.Fatal(err)
	}
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	chunk := TimelineChunk{
		ScrollProfiles: timeline.ScrollProfiles,
		Frames:         append([]FrameDirective(nil), timeline.Frames[:1]...),
	}
	err = scheduler.SubmitFadeTail(context.Background(), 1, plan.Epoch, assets.AssetsHash, timeline.TimelineVersion, chunk)
	if err != ErrGPUUnavailable {
		t.Fatalf("fade submit should reach the disconnected worker after state validation, got %v", err)
	}
	if scheduler.Pending() {
		t.Fatal("failed fade submit must not leave a pending window")
	}
}

func testPlaylistGPUTransitionPlan(t *testing.T) TransitionPlan {
	t.Helper()
	plan, err := CompilePlaylistTransition(
		TransitionRequest{
			Schema:               GPUPlaylistTimelineSchema,
			Epoch:                7,
			Reason:               PlaylistTransitionTrackChange,
			SourceTrackID:        "track-a",
			PlaybackHeadSequence: 10,
			PlaybackHeadPTSNs:    333_333_330,
		},
		"track-b",
		15,
		500_000_000,
		2,
		6,
		33_333_333,
		AudioFadePlan{Curve: "linear", DurationNS: 199_999_998},
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

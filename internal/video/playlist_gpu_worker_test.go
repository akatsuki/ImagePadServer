package video

import (
	"context"
	"image/color"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPlaylistGPUOverlayLayoutUsesOutputCoordinates(t *testing.T) {
	layout, err := playlistGPUOverlayLayout(640, 360)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Title.X != 216 || layout.Title.Y != 76 {
		t.Fatalf("overlay title origin = (%d,%d), want canonical 640x360 origin (216,76)", layout.Title.X, layout.Title.Y)
	}
	if layout.Artwork.W != 144 || layout.Artwork.H != 144 {
		t.Fatalf("overlay artwork size = (%d,%d), want 144x144", layout.Artwork.W, layout.Artwork.H)
	}
}

func TestPlaylistGPUForegroundPaletteUsesAdaptiveMode(t *testing.T) {
	assets := TrackAssets{}
	applyPlaylistGPUForegroundPalette(&assets, ForegroundMode{
		PrimaryColor: color.RGBA{R: 1, G: 2, B: 3, A: 255},
		AccentColor:  color.RGBA{R: 4, G: 5, B: 6, A: 255},
		Overlay:      color.RGBA{A: 92},
	})
	if assets.Palette.Primary != [4]uint8{1, 2, 3, 255} || assets.Palette.Accent != [4]uint8{4, 5, 6, 255} {
		t.Fatalf("adaptive palette was not copied: %+v", assets.Palette)
	}
}

func TestPlaylistGPUWorkerPrepareCompiledTrackFailsClosedWithoutSidecar(t *testing.T) {
	worker := &PlaylistGPUWorker{}
	if _, _, err := worker.PrepareCompiledTrack(context.Background(), 1, playlistGPUWorkerTestInput(), "track-a"); err == nil {
		t.Fatal("compiled track preparation must fail closed without a connected sidecar")
	}
}

func TestPlaylistGPUWorkerRenderPreparedChunkRejectsTrackMismatch(t *testing.T) {
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	timeline.TrackID = "track-b"
	if _, err := (&PlaylistGPUWorker{}).RenderPreparedTimelineChunk(context.Background(), 1, 1, assets, timeline, 0, 1); err == nil {
		t.Fatal("prepared asset/timeline track mismatch must be rejected")
	}
}

func TestPlaylistGPUWorkerRenderPreparedChunkFailsClosedWithoutSidecar(t *testing.T) {
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&PlaylistGPUWorker{}).RenderPreparedTimelineChunk(context.Background(), 1, 1, assets, timeline, 0, 1); err == nil {
		t.Fatal("prepared chunk render must fail closed without a connected sidecar")
	}
}

func TestPlaylistGPUWorkerRenderPreparedTimelineDoesNotPartiallySinkWithoutSidecar(t *testing.T) {
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	sinkCalls := 0
	err = (&PlaylistGPUWorker{}).RenderPreparedTimeline(context.Background(), 1, assets, timeline, func(EncodedH264Frame) error {
		sinkCalls++
		return nil
	})
	if err == nil {
		t.Fatal("prepared timeline render must fail closed without a connected sidecar")
	}
	if sinkCalls != 0 {
		t.Fatalf("partial output reached sink %d times", sinkCalls)
	}
}

func TestPlaylistGPUWorkerRenderPreparedTimelineUsesBoundedPlaybackRunner(t *testing.T) {
	assets, timeline, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	oldRunner := newPlaylistGPUPlaybackRunner
	called := false
	newPlaylistGPUPlaybackRunner = func(worker *PlaylistGPUWorker, epoch uint64, gotAssets TrackAssets, gotTimeline TrackTimeline, sink func(EncodedH264Frame) error) (*PlaylistGPUPlaybackRunner, error) {
		called = true
		if worker == nil || epoch != 7 || gotAssets.TrackID != assets.TrackID || gotTimeline.TrackID != timeline.TrackID || sink == nil {
			t.Fatal("RenderPreparedTimeline passed an invalid bounded runner contract")
		}
		return oldRunner(worker, epoch, gotAssets, gotTimeline, sink)
	}
	t.Cleanup(func() { newPlaylistGPUPlaybackRunner = oldRunner })

	if err := (&PlaylistGPUWorker{}).RenderPreparedTimeline(context.Background(), 7, assets, timeline, func(EncodedH264Frame) error {
		return nil
	}); err == nil {
		t.Fatal("disconnected worker unexpectedly rendered")
	}
	if !called {
		t.Fatal("RenderPreparedTimeline bypassed the bounded playback runner")
	}
}

func TestPlaylistGPUWorkerRenderPreparedTimelineDrivesThreeSidecarWindows(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1", "IMAGEPAD_SIDECAR_ADVERTISE_H264=1")
	process, err := startSidecarCommand(context.Background(), cmd, "playlist-worker-runner-session")
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := process.Hello(ctx, "playlist-worker-runner-session"); err != nil {
		t.Fatal(err)
	}
	worker := &PlaylistGPUWorker{
		process: process,
		width:   playlistGPUSurfaceWidth,
		height:  playlistGPUSurfaceHeight,
		fps:     playlistGPUFPS,
	}
	scheduler, err := NewPlaylistGPUBoundedScheduler(worker)
	if err != nil {
		t.Fatal(err)
	}
	worker.scheduler = scheduler

	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Playlist", Artist: "Artist", Album: "Album"},
		Analysis: AudioAnalysis{FPS: 30, Duration: float64(3*GPUH264MaxBatchFrames) / 30, Frames: make([]AudioFrame, 3*GPUH264MaxBatchFrames)},
	}
	assets, timeline, err := CompilePlaylistGPUTrack(input, "track-worker-runner")
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.PrepareTrack(ctx, 11, assets); err != nil {
		t.Fatal(err)
	}

	frames := make([]EncodedH264Frame, 0, len(timeline.Frames))
	if err := worker.RenderPreparedTimeline(ctx, 11, assets, timeline, func(frame EncodedH264Frame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(frames) != len(timeline.Frames) {
		t.Fatalf("runner emitted %d frames, want %d", len(frames), len(timeline.Frames))
	}
	for i, frame := range frames {
		if frame.Sequence != timeline.Frames[i].Sequence || frame.PTSNs != timeline.Frames[i].PTSNs {
			t.Fatalf("frame %d identity = sequence %d/%d PTS %d/%d", i, frame.Sequence, timeline.Frames[i].Sequence, frame.PTSNs, timeline.Frames[i].PTSNs)
		}
		if frame.PixelReadbackBytes != 0 {
			t.Fatalf("frame %d reported pixel readback=%d", i, frame.PixelReadbackBytes)
		}
	}
	process.mu.Lock()
	cursor := process.playlistTimelineCursor
	pending := process.playlistPendingBatch != nil
	process.mu.Unlock()
	if !cursor.initialized || cursor.timelineVersion != timeline.TimelineVersion || cursor.lastSequence != uint64(3*GPUH264MaxBatchFrames-1) || cursor.lastFrameIndex != uint64(3*GPUH264MaxBatchFrames-1) {
		t.Fatalf("sidecar cursor after runner = %#v", cursor)
	}
	if pending {
		t.Fatal("runner left a sidecar batch pending after RenderAll")
	}
}

func TestPlaylistGPUWorkerRenderFadeTailFailsClosedWithoutSidecar(t *testing.T) {
	assets, source, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
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
	sinkCalls := 0
	err = (&PlaylistGPUWorker{}).RenderPlaylistGPUFadeTail(context.Background(), 7, assets, plan, source, func(EncodedH264Frame) error {
		sinkCalls++
		return nil
	})
	if err == nil {
		t.Fatal("fade tail render must fail closed without a connected sidecar")
	}
	if sinkCalls != 0 {
		t.Fatalf("fade tail partial output reached sink %d times", sinkCalls)
	}
}

func TestPlaylistGPUWorkerExecuteTransitionFailsClosedWithoutSidecar(t *testing.T) {
	assets, source, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
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
	sinkCalls := 0
	err = (&PlaylistGPUWorker{}).ExecutePlaylistGPUTransition(context.Background(), assets, source, plan, func(EncodedH264Frame) error {
		sinkCalls++
		return nil
	})
	if err == nil {
		t.Fatal("transition executor must fail closed without a connected sidecar")
	}
	if sinkCalls != 0 {
		t.Fatalf("transition partial output reached sink %d times", sinkCalls)
	}
}

func playlistGPUWorkerTestInput() AudioRenderInput {
	return AudioRenderInput{
		Metadata: AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"},
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 0.1,
			Frames: []AudioFrame{
				{Spectrum24: [24]float64{0.25, 0.5}},
				{Spectrum24: [24]float64{0.5, 0.75}},
				{Spectrum24: [24]float64{0.75, 1}},
			},
		},
	}
}

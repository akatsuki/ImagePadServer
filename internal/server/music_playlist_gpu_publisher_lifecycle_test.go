package server

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/playlist"
	"imagepadserver/internal/video"
)

type publisherLifecycleRadio struct {
	*runningMusicRadio
	mu       sync.Mutex
	audioTee func([]byte, time.Duration) error
	gpuOwned bool
}

func (r *publisherLifecycleRadio) SetPlaylistAudioTee(tee func([]byte, time.Duration) error) {
	r.mu.Lock()
	r.audioTee = tee
	r.mu.Unlock()
}

func (r *publisherLifecycleRadio) SetPlaylistGPUOutputActive(active bool) {
	r.mu.Lock()
	r.gpuOwned = active
	r.mu.Unlock()
}

func (r *publisherLifecycleRadio) SendPlaylistAudio(samples []byte, pts time.Duration) error {
	r.mu.Lock()
	tee := r.audioTee
	r.mu.Unlock()
	if tee == nil {
		return errors.New("playlist audio tee is not installed")
	}
	return tee(samples, pts)
}

func (r *publisherLifecycleRadio) OutputState() (teeInstalled, gpuOwned bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.audioTee != nil, r.gpuOwned
}

func TestMusicPlaylistGPUContinuousServerPublisherLifecycleE2E(t *testing.T) {
	sidecar := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if sidecar == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not set")
	}
	if os.Getenv("IMAGEPAD_GPU_H264_DRAFT") != "1" {
		t.Skip("requires explicit IMAGEPAD_GPU_H264_DRAFT=1 evaluation capability")
	}
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg is not installed")
		}
	}
	t.Setenv("IMAGEPAD_FFMPEG", ffmpeg)
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "1")

	srv, _, current := setupExplicitGPUTransitionFailureTest(t)
	target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady, MediaPath: "target.ts"})
	if !srv.musicQueue.SetCurrent(current.ID) {
		t.Fatal("SetCurrent(Current) failed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	worker, err := video.StartPlaylistGPUWorker(ctx, sidecar, "server-publisher-lifecycle-e2e")
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	input := video.AudioRenderInput{
		Metadata: video.AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"},
		Analysis: video.AudioAnalysis{
			FPS:      30,
			Duration: 0.2,
			Frames: []video.AudioFrame{
				{Spectrum24: [24]float64{0.25, 0.5}},
				{Spectrum24: [24]float64{0.5, 0.75}},
				{Spectrum24: [24]float64{0.75, 1}},
				{Spectrum24: [24]float64{1, 0.75}},
				{Spectrum24: [24]float64{0.75, 0.5}},
				{Spectrum24: [24]float64{0.5, 0.25}},
			},
		},
	}
	sourceAssets, sourceTimeline, err := worker.PrepareCompiledTrack(ctx, 7, input, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	targetAssets, targetTimeline, err := worker.CompilePreparedTrackAssets(ctx, input, target.ID)
	if err != nil {
		t.Fatal(err)
	}

	controller, err := video.NewPlaylistGPUTransitionController(7, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.AttachTransitionController(controller); err != nil {
		t.Fatal(err)
	}
	radio := &publisherLifecycleRadio{runningMusicRadio: &runningMusicRadio{status: obsrtmp.RadioStatus{
		Running:        true,
		CurrentTrackID: current.ID,
		TrackStartedAt: time.Now(),
	}}}
	srv.radio = radio
	serverPublisher := &bytes.Buffer{}
	srv.musicPlaylistPublisherMu.Lock()
	srv.musicPlaylistPublisherSink = serverPublisher
	srv.musicPlaylistPublisherMu.Unlock()
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUAssets = map[string]video.TrackAssets{
		current.ID: sourceAssets,
		target.ID:  targetAssets,
	}
	srv.musicPlaylistGPUTimelines = map[string]video.TrackTimeline{
		current.ID: sourceTimeline,
		target.ID:  targetTimeline,
	}
	srv.musicPlaylistGPUFrameSink = nil
	srv.musicPlaylistGPUContinuousReady = true
	srv.musicPlaylistGPUWorker = worker
	srv.musicPlaylistGPUController = controller
	srv.musicTimelineMu.Unlock()

	plan, err := video.CompilePlaylistTransition(video.TransitionRequest{
		Schema:               video.GPUPlaylistTimelineSchema,
		Epoch:                7,
		Reason:               video.PlaylistTransitionTrackChange,
		SourceTrackID:        current.ID,
		PlaybackHeadSequence: 10,
		PlaybackHeadPTSNs:    333_333_330,
	}, target.ID, 15, 500_000_000, 2, 3, 33_333_333, video.AudioFadePlan{Curve: "linear", DurationNS: 99_999_999})
	if err != nil {
		t.Fatal(err)
	}

	audioDone := make(chan error, 1)
	go func() {
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		for {
			teeInstalled, gpuOwned := radio.OutputState()
			if teeInstalled && gpuOwned {
				break
			}
			select {
			case <-deadline.C:
				audioDone <- errors.New("server GPU audio tee was not installed")
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		chunk := make([]byte, 1600*2*2)
		for i := 0; i < int(plan.DurationFrames+targetTimeline.FrameCount); i++ {
			pts := time.Duration(plan.FadeStartPTSNs) + time.Duration(i)*time.Second/30
			if err := radio.SendPlaylistAudio(chunk, pts); err != nil {
				audioDone <- err
				return
			}
		}
		audioDone <- nil
	}()
	if err := srv.submitPlaylistGPUContinuousTrack(sourceAssets, sourceTimeline, plan); err != nil {
		t.Fatalf("server continuous GPU publisher path: %v", err)
	}
	teeInstalled, gpuOwned := radio.OutputState()
	if !teeInstalled || !gpuOwned {
		t.Fatalf("server did not retain GPU publisher ownership after video completion: tee=%v gpu=%v", teeInstalled, gpuOwned)
	}
	if serverPublisher.Len() != 0 {
		t.Fatalf("publisher received MPEG-TS before target audio/EOF drain: %d bytes", serverPublisher.Len())
	}

	if err := <-audioDone; err != nil {
		t.Fatalf("server GPU audio feeder: %v", err)
	}
	if err := srv.finishPlaylistGPUActiveOutput(target.ID); err != nil {
		t.Fatalf("server GPU output CloseInputs/Wait: %v", err)
	}
	teeInstalled, gpuOwned = radio.OutputState()
	if teeInstalled || gpuOwned {
		t.Fatalf("server did not release GPU publisher ownership after stdout drain: tee=%v gpu=%v", teeInstalled, gpuOwned)
	}
	output := serverPublisher.Bytes()
	if len(output) == 0 || output[0] != 0x47 {
		t.Fatalf("server publisher did not receive MPEG-TS: len=%d", len(output))
	}
}

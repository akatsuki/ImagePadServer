package video

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"
)

func TestCompilePlaylistTrackProducesImmutableAssetsAndFrameIndexedControls(t *testing.T) {
	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"},
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 0.1,
			Frames: []AudioFrame{
				{Spectrum24: [24]float64{0.25, 0.5}},
				{Spectrum24: [24]float64{0.5, 0.75}},
				{Spectrum24: [24]float64{0.75, 1}},
			},
			WaveformFrames: [][]uint16{{100, 200}, {300, 400}, {500, 600}},
		},
	}

	assets, timeline, err := CompilePlaylistGPUTrack(input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	if assets.TrackID != "track-a" || assets.AssetsHash == "" {
		t.Fatalf("invalid immutable assets identity: %#v", assets)
	}
	if assets.Artwork == nil || assets.GlyphAtlas == nil {
		t.Fatalf("compiled assets missing required artwork/glyph assets: %#v", assets)
	}
	if timeline.TrackID != "track-a" || timeline.FrameCount != 3 || len(timeline.Frames) != 3 {
		t.Fatalf("invalid timeline shape: %#v", timeline)
	}
	for i, frame := range timeline.Frames {
		if frame.Sequence != uint64(i) || frame.FrameIndex != uint64(i) {
			t.Fatalf("frame %d identity = %#v", i, frame)
		}
		if i > 0 && frame.PTSNs <= timeline.Frames[i-1].PTSNs {
			t.Fatalf("frame %d PTS did not increase", i)
		}
	}
	if timeline.Frames[1].SpectrumQ16[0] != 32768 || timeline.Frames[2].SpectrumQ16[1] != 65535 {
		t.Fatalf("spectrum was not quantized into the timeline: %#v", timeline.Frames)
	}
	if !reflect.DeepEqual(timeline.Frames[1].WaveformQ16, []uint16{300, 400}) {
		t.Fatalf("waveform was not copied into the timeline: %#v", timeline.Frames[1].WaveformQ16)
	}
	wire, err := json.Marshal(timeline)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) == "" || containsJSONKey(string(wire), "pcm_f32le") {
		t.Fatalf("timeline wire unexpectedly contains per-frame PCM: %s", wire)
	}

	assetsAgain, timelineAgain, err := CompilePlaylistGPUTrack(input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(assets, assetsAgain) || !reflect.DeepEqual(timeline, timelineAgain) {
		t.Fatal("compiling the same track was not deterministic")
	}
}

func TestRebasePlaylistTimelineUsesAuthoritativeSequenceAndPTS(t *testing.T) {
	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Title"},
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 0.1,
			Frames:   []AudioFrame{{}, {}, {}},
		},
	}
	_, timeline, err := CompilePlaylistGPUTrack(input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	rebased, err := RebasePlaylistTimeline(timeline, 100, 3_333_333_300)
	if err != nil {
		t.Fatal(err)
	}
	if rebased.Frames[0].Sequence != 100 || rebased.Frames[0].PTSNs != 3_333_333_300 {
		t.Fatalf("first frame was not rebased: %#v", rebased.Frames[0])
	}
	if rebased.Frames[1].Sequence != 101 || rebased.Frames[1].PTSNs <= rebased.Frames[0].PTSNs {
		t.Fatalf("second frame did not preserve authoritative ordering: %#v", rebased.Frames[1])
	}
	if rebased.TimelineHash == timeline.TimelineHash {
		t.Fatal("rebasing sequence/PTS did not change timeline hash")
	}
	if timeline.Frames[0].Sequence != 0 || timeline.Frames[0].PTSNs != 0 {
		t.Fatal("rebasing mutated the original immutable timeline")
	}
}

func TestRebasePlaylistTimelineFrameIndexContinuesAfterFadeTail(t *testing.T) {
	input := AudioRenderInput{
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 0.1,
			Frames:   []AudioFrame{{}, {}, {}},
		},
	}
	_, timeline, err := CompilePlaylistGPUTrack(input, "track-b")
	if err != nil {
		t.Fatal(err)
	}
	rebased, err := RebasePlaylistTimelineFrameIndex(timeline, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i, frame := range rebased.Frames {
		if frame.FrameIndex != uint64(i+3) {
			t.Fatalf("frame %d index = %d, want %d", i, frame.FrameIndex, i+3)
		}
	}
	if rebased.Frames[0].Sequence != timeline.Frames[0].Sequence || rebased.Frames[0].PTSNs != timeline.Frames[0].PTSNs {
		t.Fatal("frame-index rebase changed authoritative sequence or PTS")
	}
	if timeline.Frames[0].FrameIndex != 0 {
		t.Fatal("frame-index rebase mutated the original immutable timeline")
	}
}

func TestCompilePlaylistGPUFadeTailUsesTransitionPlanBoundaries(t *testing.T) {
	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Title"},
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 0.1,
			Frames:   []AudioFrame{{Spectrum24: [24]float64{0.25}}, {Spectrum24: [24]float64{0.5}}, {Spectrum24: [24]float64{0.75}}},
		},
	}
	_, source, err := CompilePlaylistGPUTrack(input, "track-a")
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
	tail, err := CompilePlaylistGPUFadeTail(plan, source)
	if err != nil {
		t.Fatal(err)
	}
	if tail.FrameCount != 3 || tail.Frames[0].Sequence != plan.FadeStartSequence || tail.Frames[2].Sequence != plan.FadeEndSequence {
		t.Fatalf("fade tail sequence boundaries = %#v", tail.Frames)
	}
	if tail.Frames[0].PTSNs != plan.FadeStartPTSNs || tail.Frames[2].PTSNs != plan.FadeEndPTSNs {
		t.Fatalf("fade tail PTS boundaries = %#v", tail.Frames)
	}
	if tail.Frames[0].TextAlphaQ16 != ^uint16(0) || tail.Frames[0].BlackAlphaQ16 != 0 || tail.Frames[2].TextAlphaQ16 != 0 || tail.Frames[2].BlackAlphaQ16 != ^uint16(0) {
		t.Fatalf("fade alpha boundaries = %#v", tail.Frames)
	}
	if timelineFrame := source.Frames[len(source.Frames)-1]; !reflect.DeepEqual(tail.Frames[0].SpectrumQ16, timelineFrame.SpectrumQ16) {
		t.Fatal("fade tail did not reuse the source timeline controls")
	}
	if source.Frames[0].Sequence != 0 || source.Frames[0].PTSNs != 0 {
		t.Fatal("fade tail compilation mutated the source timeline")
	}
}

func TestPlaylistTrackContractsRejectMutatedAssetsAndTimeline(t *testing.T) {
	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Title"},
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: 0.1,
			Frames:   []AudioFrame{{}, {}, {}},
		},
	}
	assets, timeline, err := CompilePlaylistGPUTrack(input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	assets.Artwork.Payload[0] ^= 0xff
	if err := assets.Validate(); err == nil {
		t.Fatal("mutated immutable artwork payload was accepted")
	}

	_, timeline, err = CompilePlaylistGPUTrack(input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	timeline.Frames[1].ProgressQ16++
	if err := timeline.Validate(); err == nil {
		t.Fatal("mutated frame control was accepted")
	}
	if err := timeline.ValidateChunk(0, GPUH264MaxBatchFrames+1); err == nil {
		t.Fatal("timeline chunk larger than the GPU batch bound was accepted")
	}
	chunk := TimelineChunk{
		ScrollProfiles: timeline.ScrollProfiles,
		Frames:         append([]FrameDirective(nil), timeline.Frames[:2]...),
	}
	if err := chunk.Validate(); err != nil {
		t.Fatalf("valid timeline chunk rejected: %v", err)
	}
	chunk.Frames[1].PTSNs = chunk.Frames[0].PTSNs
	if err := chunk.Validate(); err == nil {
		t.Fatal("timeline chunk with duplicate PTS was accepted")
	}
}

func TestCompilePlaylistTransitionUsesQueueTailAndRejectsStalePlan(t *testing.T) {
	request := TransitionRequest{
		Schema:               GPUPlaylistTimelineSchema,
		Epoch:                7,
		Reason:               PlaylistTransitionTrackChange,
		SourceTrackID:        "track-a",
		PlaybackHeadSequence: 10,
		PlaybackHeadPTSNs:    333_333_330,
		ObservedBufferDepth:  5,
	}
	audio := AudioFadePlan{Curve: "linear", DurationNS: 199_999_998}
	plan, err := CompilePlaylistTransition(request, "track-b", 15, 500_000_000, 2, 6, 33_333_333, audio)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FadeStartSequence != 16 || plan.FadeEndSequence != 21 {
		t.Fatalf("fade sequence was not queue-tail based: %#v", plan)
	}
	if plan.FadeStartPTSNs != 533_333_333 || plan.FadeEndPTSNs != 699_999_998 {
		t.Fatalf("fade PTS was not deterministic: %#v", plan)
	}
	if plan.TargetTrackID != "track-b" || plan.AudioFadePlan.StartPTSNs != plan.FadeStartPTSNs {
		t.Fatalf("transition target/audio plan mismatch: %#v", plan)
	}
	if err := plan.ValidateFor(7, 15); err != nil {
		t.Fatalf("current transition plan rejected: %v", err)
	}
	if err := plan.ValidateFor(8, 15); err == nil {
		t.Fatal("stale transition epoch was accepted")
	}
	if err := plan.ValidateFor(7, 16); err == nil {
		t.Fatal("transition whose fade start was already committed was accepted")
	}
}

func TestPlaylistGPUTransitionControllerEnforcesDeterministicDrainLifecycle(t *testing.T) {
	request := TransitionRequest{
		Schema:               GPUPlaylistTimelineSchema,
		Epoch:                7,
		Reason:               PlaylistTransitionTrackChange,
		SourceTrackID:        "track-a",
		PlaybackHeadSequence: 10,
		PlaybackHeadPTSNs:    333_333_330,
	}
	plan, err := CompilePlaylistTransition(
		request,
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
	controller, err := NewPlaylistGPUTransitionController(7, 15)
	if err != nil {
		t.Fatal(err)
	}
	if controller.State() != PlaylistGPUStateRunning {
		t.Fatalf("initial state = %q", controller.State())
	}
	if err := controller.BeginDrainInFlight(); err == nil {
		t.Fatal("in-flight drain was allowed before normal submit stopped")
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
	if err := controller.BeginNextTrackRendering(plan.Epoch + 1); err != nil {
		t.Fatal(err)
	}
	if err := controller.BeginFlushOutputAfterNextTrack(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CompleteTransition(plan.FadeEndSequence - 1); err == nil {
		t.Fatal("transition completed before fade tail was committed")
	}
	if err := controller.CompleteTransition(plan.FadeEndSequence); err != nil {
		t.Fatal(err)
	}
	if controller.State() != PlaylistGPUStateTransitionComplete {
		t.Fatalf("completed state = %q", controller.State())
	}
	if err := controller.BeginNextTrack(8); err == nil {
		t.Fatal("same epoch was accepted for next track")
	}
	if err := controller.BeginNextTrack(9); err != nil {
		t.Fatal(err)
	}
	if err := controller.ResumeRunning(); err != nil {
		t.Fatal(err)
	}
	if controller.State() != PlaylistGPUStateRunning {
		t.Fatalf("resumed state = %q", controller.State())
	}
	if _, ok := controller.Plan(); ok {
		t.Fatal("completed transition plan was retained after next-track transition")
	}
}

func TestPlaylistGPUTransitionControllerResumesRunningAfterStopTransition(t *testing.T) {
	plan, err := CompilePlaylistTransition(TransitionRequest{
		Schema:               GPUPlaylistTimelineSchema,
		Epoch:                5,
		Reason:               PlaylistTransitionStop,
		SourceTrackID:        "track-current",
		PlaybackHeadSequence: 10,
		PlaybackHeadPTSNs:    333_333_330,
	}, "", 12, 400_000_000, 1, 2, 33_333_333, AudioFadePlan{Curve: "linear", DurationNS: 66_666_666})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewPlaylistGPUTransitionController(plan.Epoch, plan.PlaybackHeadSequence)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return controller.Request(plan) },
		controller.StopNormalSubmit,
		controller.BeginDrainInFlight,
		controller.BeginFadeTailRendering,
		controller.BeginFlushOutput,
		func() error { return controller.CompleteTransition(plan.FadeEndSequence) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := controller.ResumeRunningAfterStopTransition(); err != nil {
		t.Fatal(err)
	}
	if controller.State() != PlaylistGPUStateRunning {
		t.Fatalf("resumed stop state = %q, want running", controller.State())
	}
	if _, ok := controller.Plan(); ok {
		t.Fatal("resumed stop controller retained completed transition plan")
	}
	if err := controller.Request(plan); err == nil {
		t.Fatal("same stop transition was accepted after resume")
	}
}

func TestPlaylistGPUTransitionControlPlaneReliabilityFixedSeed20Iterations(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5eed2026))
	boundaryCases := []string{"track-start", "batch-before", "batch-boundary", "batch-inside", "track-end", "stop", "track-change", "stale-epoch", "rapid-repeat"}
	for iteration := 0; iteration < 20; iteration++ {
		boundary := boundaryCases[iteration%len(boundaryCases)]
		baseSequence := uint64(1000 + rng.Intn(500))
		epoch := uint64(iteration + 1)
		reason := PlaylistTransitionStop
		targetID := ""
		if boundary == "track-change" || iteration%3 == 0 {
			reason = PlaylistTransitionTrackChange
			targetID = "track-next"
		}
		plan, err := CompilePlaylistTransition(TransitionRequest{
			Schema: GPUPlaylistTimelineSchema, Epoch: epoch, Reason: reason, SourceTrackID: "track-current",
			PlaybackHeadSequence: baseSequence, PlaybackHeadPTSNs: int64(baseSequence) * 33_333_333,
		}, targetID, baseSequence+20, int64(baseSequence+20)*33_333_333, 2, 6, 33_333_333, AudioFadePlan{Curve: "linear", DurationNS: 199_999_998})
		if err != nil {
			t.Fatalf("iteration %d (%s): compile plan: %v", iteration, boundary, err)
		}
		controller, err := NewPlaylistGPUTransitionController(epoch, baseSequence)
		if err != nil {
			t.Fatalf("iteration %d (%s): create controller: %v", iteration, boundary, err)
		}
		stale := plan
		stale.Epoch++
		if err := controller.Request(stale); err == nil {
			t.Fatalf("iteration %d (%s): stale epoch was accepted", iteration, boundary)
		}
		if err := controller.Request(plan); err != nil {
			t.Fatalf("iteration %d (%s): request plan: %v", iteration, boundary, err)
		}
		if boundary == "rapid-repeat" {
			if err := controller.Request(plan); err == nil {
				t.Fatalf("iteration %d (%s): repeated request was accepted", iteration, boundary)
			}
		}
		steps := []struct {
			name string
			fn   func() error
		}{
			{"stop-normal-submit", controller.StopNormalSubmit},
			{"drain-in-flight", controller.BeginDrainInFlight},
			{"fade-tail", controller.BeginFadeTailRendering},
		}
		for _, step := range steps {
			if err := step.fn(); err != nil {
				t.Fatalf("iteration %d (%s): %s: %v", iteration, boundary, step.name, err)
			}
		}
		if reason == PlaylistTransitionTrackChange {
			if err := controller.BeginNextTrackRendering(epoch + 1); err != nil {
				t.Fatalf("iteration %d (%s): begin next-track rendering: %v", iteration, boundary, err)
			}
			if err := controller.BeginFlushOutputAfterNextTrack(); err != nil {
				t.Fatalf("iteration %d (%s): flush after next-track rendering: %v", iteration, boundary, err)
			}
		} else if err := controller.BeginFlushOutput(); err != nil {
			t.Fatalf("iteration %d (%s): flush-eos: %v", iteration, boundary, err)
		}
		if err := controller.CompleteTransition(plan.FadeEndSequence); err != nil {
			t.Fatalf("iteration %d (%s): complete transition: %v", iteration, boundary, err)
		}
		if err := controller.BeginNextTrack(epoch + 2); err != nil {
			t.Fatalf("iteration %d (%s): begin next track: %v", iteration, boundary, err)
		}
		if err := controller.ResumeRunning(); err != nil {
			t.Fatalf("iteration %d (%s): resume running: %v", iteration, boundary, err)
		}
		if controller.State() != PlaylistGPUStateRunning {
			t.Fatalf("iteration %d (%s): final state = %s", iteration, boundary, controller.State())
		}
	}
}

func TestPlaylistGPUTransitionContinuousTrackKeepsFlushUntilTargetRendered(t *testing.T) {
	plan, err := CompilePlaylistTransition(TransitionRequest{
		Schema:               GPUPlaylistTimelineSchema,
		Epoch:                3,
		Reason:               PlaylistTransitionTrackChange,
		SourceTrackID:        "track-current",
		PlaybackHeadSequence: 100,
		PlaybackHeadPTSNs:    3_333_333_300,
	}, "track-next", 120, 4_000_000_000, 2, 4, 33_333_333, AudioFadePlan{Curve: "linear", DurationNS: 133_333_332})
	if err != nil {
		t.Fatal(err)
	}
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
	if err := controller.BeginFlushOutput(); err == nil {
		t.Fatal("continuous track allowed Flush/EOS before target rendering")
	}
	if err := controller.BeginNextTrackRendering(plan.Epoch + 1); err != nil {
		t.Fatal(err)
	}
	if controller.State() != PlaylistGPUStateNextTrack {
		t.Fatalf("next-track state = %q", controller.State())
	}
	if err := controller.BeginNextTrackRendering(plan.Epoch + 2); err == nil {
		t.Fatal("duplicate next-track rendering was accepted")
	}
	if err := controller.BeginFlushOutputAfterNextTrack(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CompleteTransition(plan.FadeEndSequence); err != nil {
		t.Fatal(err)
	}
}

func TestPlaylistGPUTransitionControllerResumesRunningAfterContinuousTrack(t *testing.T) {
	plan, err := CompilePlaylistTransition(TransitionRequest{
		Schema:               GPUPlaylistTimelineSchema,
		Epoch:                3,
		Reason:               PlaylistTransitionTrackChange,
		SourceTrackID:        "track-current",
		PlaybackHeadSequence: 100,
		PlaybackHeadPTSNs:    3_333_333_300,
	}, "track-next", 120, 4_000_000_000, 2, 4, 33_333_333, AudioFadePlan{Curve: "linear", DurationNS: 133_333_332})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewPlaylistGPUTransitionController(plan.Epoch, plan.PlaybackHeadSequence)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return controller.Request(plan) },
		controller.StopNormalSubmit,
		controller.BeginDrainInFlight,
		controller.BeginFadeTailRendering,
		func() error { return controller.BeginNextTrackRendering(plan.Epoch + 1) },
		controller.BeginFlushOutputAfterNextTrack,
		func() error { return controller.CompleteTransition(plan.FadeEndSequence) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := controller.ResumeRunningAfterContinuousTrack(plan.Epoch+1, plan.FadeEndSequence+4); err != nil {
		t.Fatal(err)
	}
	if controller.State() != PlaylistGPUStateRunning {
		t.Fatalf("resumed state = %q, want running", controller.State())
	}
	if _, ok := controller.Plan(); ok {
		t.Fatal("resumed controller retained completed transition plan")
	}
}

func TestCompilePlaylistGPUTrackCarriesLoudnessGraphTexture(t *testing.T) {
	assets, _, err := CompilePlaylistGPUTrack(playlistGPUWorkerTestInput(), "track-a")
	if err != nil {
		t.Fatal(err)
	}
	if assets.LoudnessTexture == nil {
		t.Fatal("compiled playlist assets must carry a CPU-prepared loudness graph texture")
	}
	if err := assets.LoudnessTexture.Validate(); err != nil {
		t.Fatalf("loudness graph texture is invalid: %v", err)
	}
	if assets.LoudnessTexture.Width != 1000 || assets.LoudnessTexture.Height != 1 {
		t.Fatalf("loudness graph texture dimensions = %dx%d, want 1000x1", assets.LoudnessTexture.Width, assets.LoudnessTexture.Height)
	}
}

func containsJSONKey(raw, key string) bool {
	return len(raw) > 0 && stringContains(raw, `"`+key+`"`)
}

func stringContains(raw, needle string) bool {
	for i := 0; i+len(needle) <= len(raw); i++ {
		if raw[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

package obsrtmp

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

type fakePersistentProgramEncoder struct {
	mu                sync.Mutex
	reader            *io.PipeReader
	writer            *io.PipeWriter
	videoWrites       int
	sourceVideoWrites int
	audioWrites       int
	failVideoAfter    int
	closed            bool
}

type blockedFinalProgramEncoder struct {
	*fakePersistentProgramEncoder
	mu           sync.Mutex
	blockAudio   bool
	audioEntered chan struct{}
	releaseAudio <-chan struct{}
	enteredOnce  sync.Once
}

func newBlockedFinalProgramEncoder(releaseAudio <-chan struct{}) *blockedFinalProgramEncoder {
	return &blockedFinalProgramEncoder{
		fakePersistentProgramEncoder: newFakePersistentProgramEncoder(),
		audioEntered:                 make(chan struct{}),
		releaseAudio:                 releaseAudio,
	}
}

func (e *blockedFinalProgramEncoder) WriteVideoRGBA(frame []byte, pts time.Duration) error {
	if err := e.fakePersistentProgramEncoder.WriteVideoRGBA(frame, pts); err != nil {
		return err
	}
	for _, value := range frame {
		if value != 0 {
			e.mu.Lock()
			e.blockAudio = true
			e.mu.Unlock()
			break
		}
	}
	return nil
}

func (e *blockedFinalProgramEncoder) WriteAudioPCM(frame []byte, pts time.Duration) error {
	e.mu.Lock()
	block := e.blockAudio
	e.blockAudio = false
	e.mu.Unlock()
	if block {
		e.enteredOnce.Do(func() { close(e.audioEntered) })
		<-e.releaseAudio
	}
	return e.fakePersistentProgramEncoder.WriteAudioPCM(frame, pts)
}

func newFakePersistentProgramEncoder() *fakePersistentProgramEncoder {
	r, w := io.Pipe()
	return &fakePersistentProgramEncoder{reader: r, writer: w}
}

func (e *fakePersistentProgramEncoder) WriteVideoRGBA(frame []byte, _ time.Duration) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.videoWrites++
	for _, value := range frame {
		if value != 0 {
			e.sourceVideoWrites++
			break
		}
	}
	if e.failVideoAfter > 0 && e.videoWrites >= e.failVideoAfter {
		return errors.New("program encoder died")
	}
	return nil
}

func (e *fakePersistentProgramEncoder) sourceWrites() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sourceVideoWrites
}

func (e *fakePersistentProgramEncoder) WriteAudioPCM(_ []byte, _ time.Duration) error {
	e.mu.Lock()
	e.audioWrites++
	e.mu.Unlock()
	return nil
}

func (e *fakePersistentProgramEncoder) Output() io.Reader { return e.reader }
func (e *fakePersistentProgramEncoder) Healthy() error    { return nil }
func (e *fakePersistentProgramEncoder) Close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	_ = e.writer.Close()
	_ = e.reader.Close()
}

func (e *fakePersistentProgramEncoder) counts() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.videoWrites, e.audioWrites
}

func configureProgramHarness(t *testing.T, m *RadioManager, encoder *fakePersistentProgramEncoder) (encoderStarts *int, publisherStarts *int) {
	t.Helper()
	encStarts := 0
	pubStarts := 0
	m.SetFallbackPreset(func() video.QualityPreset {
		return video.QualityPreset{Height: 2, AudioBitrate: "96k", RadioLatency: "rtsp-ultra"}
	})
	m.SetOutputMode(func() RadioOutputMode { return RadioOutputModeProgram })
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		encStarts++
		return encoder, nil
	}
	originalStartPublisher := m.startPublisher
	m.startPublisher = func(ctx context.Context, publishURL string) (radioPublisher, error) {
		pubStarts++
		return originalStartPublisher(ctx, publishURL)
	}
	return &encStarts, &pubStarts
}

func TestRadioProgramModeKeepsSinglePublisherEncoderAndEndpointIdentity(t *testing.T) {
	tracks := []string{"a", "b"}
	index := 0
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if index >= len(tracks) {
			return "", "", false
		}
		id := tracks[index]
		index++
		return id, id, true
	}, nil, nil)
	encoder := newFakePersistentProgramEncoder()
	encoderStarts, publisherStarts := configureProgramHarness(t, m, encoder)
	overlaySnapshots := 0
	m.SetOverlaySource(func() OverlaySnapshot {
		overlaySnapshots++
		return OverlaySnapshot{Mode: OverlayModeNotifications, Revision: uint64(overlaySnapshots)}
	})
	m.runProgramFeeder = func(ctx context.Context, _ string, _ int, width, height int, frames chan<- ProgramSourceFrame) error {
		frame := ProgramSourceFrame{
			VideoRGBA: make([]byte, width*height*4),
			AudioPCM:  make([]byte, programSamplesPerFrame*programAudioChannels*programAudioBytes),
		}
		select {
		case frames <- frame:
			return nil
		case <-ctx.Done():
			return nil
		}
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	h.waitEvent(t, "end")
	h.waitEvent(t, "start")
	h.waitEvent(t, "end")
	h.waitEvent(t, "idle")

	status := m.Status()
	if *encoderStarts != 1 || *publisherStarts != 1 {
		t.Fatalf("starts: encoder=%d publisher=%d, want one each", *encoderStarts, *publisherStarts)
	}
	if status.OutputMode != RadioOutputModeProgram || status.Path != "radio" {
		t.Fatalf("program identity changed: %+v", status)
	}
	if status.ActiveSession == nil || status.ActiveSession.OutputMode != RadioOutputModeProgram {
		t.Fatalf("active contract did not retain program mode: %+v", status.ActiveSession)
	}
	if overlaySnapshots != 2 {
		t.Fatalf("overlay snapshots = %d, want one per track boundary", overlaySnapshots)
	}
}

func TestRadioProgramHandledTrackSkipsCPUFeeder(t *testing.T) {
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return "gpu-owned.ts", "gpu-owned", true
	}, nil, nil)
	encoder := newFakePersistentProgramEncoder()
	configureProgramHarness(t, m, encoder)
	cpuFeederCalls := 0
	m.runProgramFeeder = func(context.Context, string, int, int, int, chan<- ProgramSourceFrame) error {
		cpuFeederCalls++
		return errors.New("handled GPU track must not invoke the CPU feeder")
	}
	m.SetTrackClaimResolver(func(mediaPath, trackID string, startSeconds int) (RadioTrackClaimMode, error) {
		if mediaPath != "gpu-owned.ts" || trackID != "gpu-owned" || startSeconds != 0 {
			t.Fatalf("unexpected handled-track claim: media=%q track=%q start=%d", mediaPath, trackID, startSeconds)
		}
		return RadioTrackClaimHandled, nil
	})

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	h.waitEvent(t, "end")
	h.waitEvent(t, "idle")
	if cpuFeederCalls != 0 {
		t.Fatalf("handled GPU track invoked CPU feeder %d times", cpuFeederCalls)
	}
}

func TestRadioProgramDecoderFailureRetriesOnceThenContinuesFallback(t *testing.T) {
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return "broken.mp4", "broken", true
	}, nil, nil)
	encoder := newFakePersistentProgramEncoder()
	_, publisherStarts := configureProgramHarness(t, m, encoder)
	attempts := 0
	m.runProgramFeeder = func(context.Context, string, int, int, int, chan<- ProgramSourceFrame) error {
		attempts++
		return errors.New("decoder failed")
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	h.waitEvent(t, "end")
	h.waitEvent(t, "idle")
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		videoWrites, audioWrites := encoder.counts()
		if videoWrites > 0 && audioWrites > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fallback was not emitted promptly: video=%d audio=%d", videoWrites, audioWrites)
		}
		time.Sleep(10 * time.Millisecond)
	}

	status := m.Status()
	if attempts != 2 {
		t.Fatalf("decoder attempts = %d, want initial plus one retry", attempts)
	}
	if *publisherStarts != 1 || !status.Running || status.Phase == RadioPhaseFailed {
		t.Fatalf("decoder failure disrupted persistent output: starts=%d status=%+v", *publisherStarts, status)
	}
	if status.OverlayError == "" {
		t.Fatal("decoder disable reason was not retained in overlay status")
	}
}

func TestRadioProgramDecoderExitAfterFullFramesRetriesThenFallsBack(t *testing.T) {
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return "broken.mp4", "broken", true
	}, nil, nil)
	encoder := newFakePersistentProgramEncoder()
	configureProgramHarness(t, m, encoder)
	feeder := newProgramTrackFeederWithFailingVideoDecoder(t, 4, 2)
	attempts := 0
	m.runProgramFeeder = func(ctx context.Context, mediaPath string, startSeconds, width, height int, frames chan<- ProgramSourceFrame) error {
		attempts++
		return feeder.Run(ctx, mediaPath, startSeconds, frames)
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	end := h.waitEvent(t, "end")
	if end.err == nil {
		t.Fatal("decoder exit after full frame completed without an error")
	}
	h.waitEvent(t, "idle")
	if attempts != 2 {
		t.Fatalf("decoder attempts = %d, want initial plus one retry", attempts)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		videoWrites, _ := encoder.counts()
		if videoWrites > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fallback did not continue after decoder retries")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRadioProgramRealFFmpegUnalignedFinalPCMCompletesWithoutRetry(t *testing.T) {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	fixture := writeUnalignedProgramFixture(t, ffmpeg)
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return fixture, "unaligned", true
	}, nil, nil)
	encoder := newFakePersistentProgramEncoder()
	configureProgramHarness(t, m, encoder)
	realFeeder := m.runProgramFeeder
	attempts := 0
	m.runProgramFeeder = func(ctx context.Context, mediaPath string, startSeconds, width, height int, frames chan<- ProgramSourceFrame) error {
		attempts++
		return realFeeder(ctx, mediaPath, startSeconds, width, height, frames)
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	end := h.waitEvent(t, "end")
	if end.err != nil {
		t.Fatalf("unaligned track completed with error: %v", end.err)
	}
	h.waitEvent(t, "idle")
	if attempts != 1 {
		t.Fatalf("decoder attempts = %d, want one successful completion", attempts)
	}
	if got := encoder.sourceWrites(); got != 4 {
		t.Fatalf("source video frames written = %d, want 4 including final partial-audio frame", got)
	}
}

func TestRadioProgramDrainsBoundedDecoderFramesBeforeTrackBoundary(t *testing.T) {
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return "track.mp4", "track", true
	}, nil, nil)
	encoder := newFakePersistentProgramEncoder()
	configureProgramHarness(t, m, encoder)
	m.runProgramFeeder = func(ctx context.Context, _ string, _ int, width, height int, frames chan<- ProgramSourceFrame) error {
		if got, want := cap(frames), programVideoFrameRate/2; got != want {
			t.Errorf("program frame buffer capacity = %d, want %d", got, want)
		}
		for i := 0; i < 3; i++ {
			frame := ProgramSourceFrame{
				VideoRGBA: make([]byte, width*height*4),
				AudioPCM:  make([]byte, programSamplesPerFrame*programAudioChannels*programAudioBytes),
			}
			frame.VideoRGBA[0] = 0xff
			select {
			case frames <- frame:
			case <-ctx.Done():
				return nil
			}
		}
		return nil
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	h.waitEvent(t, "end")
	if got := encoder.sourceWrites(); got != 3 {
		t.Fatalf("source frames written before boundary = %d, want 3", got)
	}
}

func TestRadioProgramWaitsForFinalWriteTickBeforeTrackBoundary(t *testing.T) {
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return "track.mp4", "track", true
	}, nil, nil)
	releaseAudio := make(chan struct{})
	encoder := newBlockedFinalProgramEncoder(releaseAudio)
	configureProgramHarness(t, m, encoder.fakePersistentProgramEncoder)
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		return encoder, nil
	}
	m.runProgramFeeder = func(ctx context.Context, _ string, _ int, width, height int, frames chan<- ProgramSourceFrame) error {
		frame := ProgramSourceFrame{
			VideoRGBA: make([]byte, width*height*4),
			AudioPCM:  make([]byte, programSamplesPerFrame*programAudioChannels*programAudioBytes),
		}
		frame.VideoRGBA[0] = 0xff
		select {
		case frames <- frame:
			return nil
		case <-ctx.Done():
			return nil
		}
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	select {
	case <-encoder.audioEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("final source audio write did not block")
	}
	track := m.CurrentTrackGeneration()
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case <-track.Completed:
			t.Fatal("track completed before final WriteTick audio write finished")
		case event := <-h.eventCh:
			if event.kind == "end" {
				t.Fatal("OnTrackEnd fired before final WriteTick audio write finished")
			}
		case <-deadline:
			goto release
		}
	}

release:
	close(releaseAudio)
	end := h.waitEvent(t, "end")
	if end.err != nil {
		t.Fatalf("track ended with error after final write release: %v", end.err)
	}
}

func TestRadioProgramWaitsForDeliveredFrameBeforeDecoderFailureTrackBoundary(t *testing.T) {
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return "track.mp4", "track", true
	}, nil, nil)
	releaseAudio := make(chan struct{})
	encoder := newBlockedFinalProgramEncoder(releaseAudio)
	configureProgramHarness(t, m, encoder.fakePersistentProgramEncoder)
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		return encoder, nil
	}
	attempts := 0
	m.runProgramFeeder = func(ctx context.Context, _ string, _ int, width, height int, frames chan<- ProgramSourceFrame) error {
		attempts++
		if attempts == 1 {
			frame := ProgramSourceFrame{
				VideoRGBA: make([]byte, width*height*4),
				AudioPCM:  make([]byte, programSamplesPerFrame*programAudioChannels*programAudioBytes),
			}
			frame.VideoRGBA[0] = 0xff
			select {
			case frames <- frame:
			case <-ctx.Done():
				return nil
			}
			select {
			case <-encoder.audioEntered:
			case <-ctx.Done():
				return nil
			}
		}
		return errors.New("decoder failed")
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	select {
	case <-encoder.audioEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("source audio write did not block")
	}
	track := m.CurrentTrackGeneration()
	assertProgramTrackBoundaryBlocked(t, h, track.Completed)

	close(releaseAudio)
	end := h.waitEvent(t, "end")
	if end.err == nil {
		t.Fatal("decoder failure did not reach OnTrackEnd after frame acknowledgement")
	}
}

func TestRadioProgramWaitsForDeliveredFrameBeforeSkipTrackBoundary(t *testing.T) {
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return "track.mp4", "track", true
	}, nil, nil)
	releaseAudio := make(chan struct{})
	encoder := newBlockedFinalProgramEncoder(releaseAudio)
	configureProgramHarness(t, m, encoder.fakePersistentProgramEncoder)
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		return encoder, nil
	}
	m.runProgramFeeder = func(ctx context.Context, _ string, _ int, width, height int, frames chan<- ProgramSourceFrame) error {
		frame := ProgramSourceFrame{
			VideoRGBA: make([]byte, width*height*4),
			AudioPCM:  make([]byte, programSamplesPerFrame*programAudioChannels*programAudioBytes),
		}
		frame.VideoRGBA[0] = 0xff
		select {
		case frames <- frame:
		case <-ctx.Done():
			return nil
		}
		<-ctx.Done()
		return nil
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	select {
	case <-encoder.audioEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("source audio write did not block")
	}
	track := m.CurrentTrackGeneration()
	m.SkipCurrent()
	assertProgramTrackBoundaryBlocked(t, h, track.Completed)

	close(releaseAudio)
	end := h.waitEvent(t, "end")
	if end.err != nil {
		t.Fatalf("skipped track ended with error after frame acknowledgement: %v", end.err)
	}
}

func TestRadioProgramSessionCancellationWaitsForDeliveredFrameBeforeTrackEnd(t *testing.T) {
	claimed := false
	m, h := newRadioHarness(t, func() (string, string, bool) {
		if claimed {
			return "", "", false
		}
		claimed = true
		return "track.mp4", "track", true
	}, nil, nil)
	releaseAudio := make(chan struct{})
	encoder := newBlockedFinalProgramEncoder(releaseAudio)
	configureProgramHarness(t, m, encoder.fakePersistentProgramEncoder)
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		return encoder, nil
	}
	m.runProgramFeeder = func(ctx context.Context, _ string, _ int, width, height int, frames chan<- ProgramSourceFrame) error {
		frame := ProgramSourceFrame{
			VideoRGBA: make([]byte, width*height*4),
			AudioPCM:  make([]byte, programSamplesPerFrame*programAudioChannels*programAudioBytes),
		}
		frame.VideoRGBA[0] = 0xff
		select {
		case frames <- frame:
			return nil
		case <-ctx.Done():
			return nil
		}
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	select {
	case <-encoder.audioEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("source audio write did not block")
	}
	track := m.CurrentTrackGeneration()
	m.mu.Lock()
	cancelSession := m.cancel
	m.mu.Unlock()
	if cancelSession == nil {
		t.Fatal("radio session cancel function is nil")
	}
	cancelSession()
	assertProgramTrackBoundaryBlocked(t, h, track.Completed)

	close(releaseAudio)
	end := h.waitEvent(t, "end")
	if end.err != nil {
		t.Fatalf("session-cancelled track ended with error after frame acknowledgement: %v", end.err)
	}
}

func TestRelayProgramFramesCancellationDoesNotWaitForUndeliveredFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan ProgramSourceFrame)
	pipelineFrames := make(chan ProgramSourceFrame)
	drained := relayProgramFrames(ctx, frames, pipelineFrames)
	sent := make(chan struct{})
	go func() {
		frames <- ProgramSourceFrame{}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not receive source frame")
	}
	cancel()
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("relay cancellation error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay waited for undelivered frame acknowledgement")
	}
}

func TestWaitProgramFramesDrainedCancellationDoesNotAllowTrackEndBeforeDeliveredFrameAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan ProgramSourceFrame)
	pipelineFrames := make(chan ProgramSourceFrame)
	framesDrained := relayProgramFrames(ctx, frames, pipelineFrames)

	go func() { frames <- ProgramSourceFrame{} }()
	delivered := <-pipelineFrames
	feederResult := make(chan error, 1)
	feederResult <- nil
	if err := <-feederResult; err != nil {
		t.Fatalf("feeder result = %v, want nil", err)
	}
	cancel()

	trackEnd := make(chan *programFailure, 1)
	go func() { trackEnd <- waitProgramFramesDrained(framesDrained, make(chan programFailure)) }()
	select {
	case failure := <-trackEnd:
		t.Fatalf("track end ran before delivered frame acknowledgement: %+v", failure)
	case <-time.After(100 * time.Millisecond):
	}

	delivered.written <- nil
	select {
	case failure := <-trackEnd:
		if failure != nil {
			t.Fatalf("track end failure after delivered frame acknowledgement: %+v", failure)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("track end did not run after delivered frame acknowledgement")
	}
}

func TestProgramTrackResultCancellationDrainsDeliveredFrameBeforeTerminalCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	result <- nil
	framesDrained := make(chan error)
	pushCanceled := make(chan struct{})
	type terminalOutcome struct {
		canceled bool
		failure  *programFailure
	}
	terminal := make(chan terminalOutcome, 1)

	// Make the feeder result and session cancellation simultaneously selectable.
	cancel()
	go func() {
		_, canceled, failure := waitProgramTrackResult(ctx, result, framesDrained, make(chan programFailure), func() { close(pushCanceled) })
		terminal <- terminalOutcome{canceled: canceled, failure: failure}
	}()

	select {
	case <-pushCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("session cancellation did not cancel the push")
	}
	select {
	case <-terminal:
		t.Fatal("terminal callback ran before delivered frame acknowledgement")
	default:
	}

	framesDrained <- nil
	select {
	case outcome := <-terminal:
		if outcome.failure != nil {
			t.Fatalf("terminal failure = %+v", outcome.failure)
		}
		if !outcome.canceled {
			t.Fatal("session cancellation selected feeder result")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal callback did not run after delivered frame acknowledgement")
	}
}

func assertProgramTrackBoundaryBlocked(t *testing.T, h *radioHarness, completed <-chan struct{}) {
	t.Helper()
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case <-completed:
			t.Fatal("track completed before delivered frame audio write finished")
		case event := <-h.eventCh:
			if event.kind == "end" {
				t.Fatal("OnTrackEnd fired before delivered frame audio write finished")
			}
		case <-deadline:
			return
		}
	}
}

func TestRadioProgramEncoderPreflightFailureDoesNotStartPublisher(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	m.SetOutputMode(func() RadioOutputMode { return RadioOutputModeProgram })
	publisherStarts := 0
	m.startPublisher = func(context.Context, string) (radioPublisher, error) {
		publisherStarts++
		return &fakePublisher{exit: make(chan error, 1)}, nil
	}
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		return nil, errors.New("encoder preflight failed")
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	waitRadioStatus(t, m, func(status RadioStatus) bool { return status.Phase == RadioPhaseFailed })
	if publisherStarts != 0 {
		t.Fatalf("publisher started %d times after failed encoder preflight", publisherStarts)
	}
	if status := m.Status(); status.OutputMode != RadioOutputModeProgram {
		t.Fatalf("failed status lost selected mode: %+v", status)
	}
}

func TestRadioProgramEncoderFailureIsTerminalWithoutPublisherRestart(t *testing.T) {
	m, _ := newRadioHarness(t, func() (string, string, bool) { return "", "", false }, nil, nil)
	encoder := newFakePersistentProgramEncoder()
	encoder.failVideoAfter = 1
	_, publisherStarts := configureProgramHarness(t, m, encoder)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	waitRadioStatus(t, m, func(status RadioStatus) bool { return status.Phase == RadioPhaseFailed })
	if *publisherStarts != 1 {
		t.Fatalf("publisher starts = %d, want exactly one", *publisherStarts)
	}
	status := m.Status()
	if status.LastError == "" || status.Running {
		t.Fatalf("encoder failure was not retained as terminal: %+v", status)
	}
}

func TestRadioOutputModeCannotHotSwapRunningCopySession(t *testing.T) {
	m, h := newRadioHarness(t, func() (string, string, bool) { return "copy", "copy", true }, nil, map[string]bool{"copy": true})
	programStarts := 0
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		programStarts++
		return newFakePersistentProgramEncoder(), nil
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	h.waitEvent(t, "start")
	m.SetOutputMode(func() RadioOutputMode { return RadioOutputModeProgram })
	time.Sleep(100 * time.Millisecond)

	status := m.Status()
	if status.OutputMode != RadioOutputModeCompatibilityCopy || status.ActiveSession == nil || status.ActiveSession.OutputMode != RadioOutputModeCompatibilityCopy {
		t.Fatalf("running copy session hot-swapped mode: %+v", status)
	}
	if programStarts != 0 {
		t.Fatalf("program encoder started %d times in copy session", programStarts)
	}
}

func waitRadioStatus(t *testing.T, m *RadioManager, predicate func(RadioStatus) bool) RadioStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := m.Status()
		if predicate(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for radio status; last=%+v", m.Status())
	return RadioStatus{}
}

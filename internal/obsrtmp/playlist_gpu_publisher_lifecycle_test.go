package obsrtmp

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

type lifecyclePublisherSink struct {
	mu sync.Mutex
	bytes.Buffer
}

func (s *lifecyclePublisherSink) Write(payload []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Buffer.Write(payload)
}

func (s *lifecyclePublisherSink) BytesCopy() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.Buffer.Bytes()...)
}

type lifecyclePublisher struct {
	sinkWriter *lifecyclePublisherSink
	exit       chan error
}

func (p *lifecyclePublisher) sink() io.Writer    { return p.sinkWriter }
func (p *lifecyclePublisher) done() <-chan error { return p.exit }
func (p *lifecyclePublisher) close()             {}

type lifecycleOutputReader struct {
	data   []byte
	closed <-chan struct{}
	first  bool
}

func (r *lifecycleOutputReader) Read(dst []byte) (int, error) {
	if !r.first {
		r.first = true
		n := copy(dst, r.data)
		return n, nil
	}
	<-r.closed
	return 0, io.EOF
}

type lifecycleProgramEncoder struct {
	output    *lifecycleOutputReader
	closeOnce sync.Once
	closeCh   chan struct{}
}

func newLifecycleProgramEncoder() *lifecycleProgramEncoder {
	closeCh := make(chan struct{})
	return &lifecycleProgramEncoder{
		output:  &lifecycleOutputReader{data: []byte("CPU_PROGRAM_BYTES"), closed: closeCh},
		closeCh: closeCh,
	}
}

func (e *lifecycleProgramEncoder) WriteVideoRGBA([]byte, time.Duration) error { return nil }
func (e *lifecycleProgramEncoder) WriteAudioPCM([]byte, time.Duration) error  { return nil }
func (e *lifecycleProgramEncoder) Output() io.Reader                          { return e.output }
func (e *lifecycleProgramEncoder) Healthy() error                             { return nil }
func (e *lifecycleProgramEncoder) Close() {
	e.closeOnce.Do(func() { close(e.closeCh) })
}

// Test the real RadioManager program session around the explicit evaluation
// bridge. This is diagnostic process/publisher evidence: it requires the
// explicit draft H.264 capability and does not alter the normal CPU route.
func TestPlaylistGPURealSidecarPersistentPublisherAudioTeeLifecycleE2E(t *testing.T) {
	if os.Getenv("IMAGEPAD_GPU_H264_DRAFT") != "1" {
		t.Skip("requires explicit IMAGEPAD_GPU_H264_DRAFT=1 evaluation capability")
	}
	sidecar := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if sidecar == "" {
		t.Skip("IMAGEPAD_PLAYLIST_COMPOSITORD is not set")
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	worker, err := video.StartPlaylistGPUWorker(ctx, sidecar, "real-publisher-lifecycle-e2e")
	if err != nil {
		t.Fatal(err)
	}
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
	assets, timeline, err := worker.PrepareCompiledTrack(ctx, 1, input, "publisher-track")
	if err != nil {
		_ = worker.Close()
		t.Fatal(err)
	}
	frames := make([]video.EncodedH264Frame, 0, timeline.FrameCount)
	if err := worker.RenderPreparedTimeline(ctx, 1, assets, timeline, func(frame video.EncodedH264Frame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		_ = worker.Close()
		t.Fatal(err)
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 {
		t.Fatal("GPU worker produced no encoded frames")
	}

	trackID := "publisher-track"
	nextCalls := 0
	m, _ := newRadioHarness(t, func() (string, string, bool) {
		if nextCalls != 0 {
			return "", "", false
		}
		nextCalls++
		return trackID, trackID, true
	}, nil, nil)
	m.SetOutputMode(func() RadioOutputMode { return RadioOutputModeProgram })
	m.SetPublisherProfile(func() RadioPublisherProfile {
		return RadioPublisherProfilePlaylistGPUEvaluation
	})
	m.SetFallbackPreset(func() video.QualityPreset { return video.QualityPreset{Height: 2, RadioLatency: "rtsp-ultra"} })

	publisherSink := &lifecyclePublisherSink{}
	publisher := &lifecyclePublisher{sinkWriter: publisherSink, exit: make(chan error)}
	encoder := newLifecycleProgramEncoder()
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		return encoder, nil
	}
	m.startPublisher = func(context.Context, string) (radioPublisher, error) {
		return publisher, nil
	}

	bridgeReady := make(chan error, 1)
	feedDone := make(chan error, 1)
	lifecycleDone := make(chan error, 1)
	var bridge *PlaylistGPUMuxBridge
	var cleanupOnce sync.Once
	var audioTeeCalls atomic.Int32
	cleanup := func() {
		cleanupOnce.Do(func() {
			if err := <-feedDone; err != nil {
				lifecycleDone <- err
				return
			}
			var firstErr error
			if bridge != nil {
				if err := bridge.CloseInputs(); err != nil {
					firstErr = err
				}
				if err := bridge.Wait(); err != nil && firstErr == nil {
					firstErr = err
				}
				_ = bridge.Close()
			}
			m.SetPlaylistAudioTee(nil)
			m.SetPlaylistGPUOutputActive(false)
			lifecycleDone <- firstErr
		})
	}

	m.cb.OnPublisherSink = func(sink io.Writer) {
		bridge, err = StartPlaylistGPUMuxBridge(ctx, ffmpeg, sink, 640, 360, 30, 48_000, 2, frames[0].PTSNs)
		if err != nil {
			bridgeReady <- err
			return
		}
		m.SetPlaylistGPUOutputActive(true)
		plan := video.AudioFadePlan{StartPTSNs: frames[0].PTSNs, DurationNS: 200_000_000, Curve: "linear"}
		m.SetPlaylistAudioTee(func(samples []byte, pts time.Duration) error {
			audioTeeCalls.Add(1)
			faded, err := ApplyPlaylistGPUAudioFadePCM(samples, pts.Nanoseconds(), 48_000, 2, plan)
			if err != nil {
				return err
			}
			return bridge.AudioTee(faded, pts)
		})
		bridgeReady <- nil
		go func() {
			for _, frame := range frames {
				if err := bridge.VideoSink(frame); err != nil {
					feedDone <- err
					return
				}
			}
			feedDone <- nil
		}()
	}
	m.cb.OnTrackEnd = func(string, error) { cleanup() }
	m.cb.OnPublisherDone = func() { cleanup() }
	m.runProgramFeeder = func(ctx context.Context, _ string, _ int, width, height int, out chan<- ProgramSourceFrame) error {
		for range frames {
			frame := ProgramSourceFrame{
				VideoRGBA: bytes.Repeat([]byte{0x7f}, width*height*4),
				AudioPCM:  make([]byte, programSamplesPerFrame*programAudioChannels*programAudioBytes),
			}
			select {
			case out <- frame:
			case <-ctx.Done():
				return nil
			}
		}
		return nil
	}

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-bridgeReady:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-lifecycleDone:
		if err != nil {
			t.Fatalf("publisher/audio tee lifecycle: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if audioTeeCalls.Load() == 0 {
		t.Fatal("persistent RadioManager program session did not forward PCM through the GPU audio tee")
	}
	beforeRestore := publisherSink.BytesCopy()
	if bytes.Contains(beforeRestore, []byte("CPU_PROGRAM_BYTES")) {
		t.Fatal("CPU program bytes reached publisher while GPU owned output")
	}

	m.Stop(10 * time.Second)
	m.mu.Lock()
	teeInstalled := m.playlistAudioTee != nil
	gpuOwned := m.playlistGPUOutputActive
	m.mu.Unlock()
	if teeInstalled || gpuOwned {
		t.Fatal("GPU publisher ownership was not released after FFmpeg stdout drain")
	}
	if _, err := m.writeProgramOutput(publisher.sink(), []byte("CPU_AFTER_RELEASE")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(publisherSink.BytesCopy(), []byte("CPU_AFTER_RELEASE")) {
		t.Fatal("CPU publisher output was not restored after GPU ownership release")
	}
	output := publisherSink.BytesCopy()
	if len(output) == 0 || output[0] != 0x47 {
		t.Fatalf("persistent publisher received invalid MPEG-TS output: len=%d", len(output))
	}
}

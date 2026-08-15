package obsrtmp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

type diagnosticCountingWriter struct {
	dst          io.Writer
	writes       atomic.Int64
	requested    atomic.Int64
	bytes        atomic.Int64
	firstWriteAt atomic.Int64
	lastWriteAt  atomic.Int64
}

type diagnosticCaptureWriter struct {
	mu   sync.Mutex
	data []byte
}

func (w *diagnosticCaptureWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	w.data = append(w.data, payload...)
	w.mu.Unlock()
	return len(payload), nil
}

func (w *diagnosticCaptureWriter) Snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.data...)
}

func (w *diagnosticCountingWriter) Write(payload []byte) (int, error) {
	now := time.Now().UnixNano()
	w.firstWriteAt.CompareAndSwap(0, now)
	w.lastWriteAt.Store(now)
	w.writes.Add(1)
	w.requested.Add(int64(len(payload)))
	n, err := w.dst.Write(payload)
	if n > 0 {
		w.bytes.Add(int64(n))
	}
	return n, err
}

// This is an explicit evaluation-only test. It uses the real MediaMTX runtime,
// real FFmpeg persistent publisher, real GPU sidecar output, and the existing
// RadioManager publisher callback boundary. It does not exercise the default
// CPU route or promote production readiness.
func TestPlaylistGPURealMediaMTXPublisherLifecycleE2E(t *testing.T) {
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
	if _, err := ResolveMediaMTX(); err != nil {
		t.Skipf("MediaMTX is not installed: %v", err)
	}
	t.Setenv("IMAGEPAD_FFMPEG", ffmpeg)
	debugLogPath := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_REAL_MEDIAMTX_LOG"))
	if debugLogPath == "" {
		debugLogPath = filepath.Join(t.TempDir(), "mediamtx.log")
	}
	publisherDebugPath := strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_REAL_PUBLISHER_LOG"))
	if publisherDebugPath == "" {
		publisherDebugPath = filepath.Join(t.TempDir(), "publisher.log")
	}
	t.Setenv("IMAGEPAD_MEDIAMTX_DEBUG_LOG", debugLogPath)
	t.Setenv("IMAGEPAD_FFMPEG_PUBLISHER_DEBUG", publisherDebugPath)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	worker, err := video.StartPlaylistGPUWorker(ctx, sidecar, "real-mediamtx-publisher-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	input := video.AudioRenderInput{
		Metadata: video.AudioMetadata{Title: "Publisher title", Artist: "Publisher artist", Album: "Publisher album"},
		Analysis: video.AudioAnalysis{
			FPS:      30,
			Duration: 2.0,
			Frames: []video.AudioFrame{
				{Spectrum24: [24]float64{0.20, 0.40}},
				{Spectrum24: [24]float64{0.40, 0.60}},
				{Spectrum24: [24]float64{0.60, 0.80}},
				{Spectrum24: [24]float64{0.80, 1.00}},
				{Spectrum24: [24]float64{1.00, 0.80}},
				{Spectrum24: [24]float64{0.80, 0.60}},
				{Spectrum24: [24]float64{0.60, 0.40}},
				{Spectrum24: [24]float64{0.40, 0.20}},
				{Spectrum24: [24]float64{0.20, 0.10}},
				{Spectrum24: [24]float64{0.10, 0.05}},
				{Spectrum24: [24]float64{0.05, 0.10}},
				{Spectrum24: [24]float64{0.10, 0.20}},
			},
		},
	}
	for len(input.Analysis.Frames) < 60 {
		input.Analysis.Frames = append(input.Analysis.Frames, input.Analysis.Frames[len(input.Analysis.Frames)%12])
	}
	assets, timeline, err := worker.PrepareCompiledTrack(ctx, 1, input, "real-publisher-track")
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
		t.Fatal("GPU worker produced no H.264 access units")
	}
	t.Logf("GPU H264 frames: count=%d first={sequence:%d pts_ns:%d payload_bytes:%d} last={sequence:%d pts_ns:%d payload_bytes:%d}",
		len(frames), frames[0].Sequence, frames[0].PTSNs, len(frames[0].Payload),
		frames[len(frames)-1].Sequence, frames[len(frames)-1].PTSNs, len(frames[len(frames)-1].Payload))
	for i, frame := range frames {
		t.Logf("GPU H264 NAL diagnostic frame=%d sequence=%d types=%v head=% x", i, frame.Sequence, h264NALTypes(frame.Payload), h264PayloadHead(frame.Payload))
	}

	var nextMu sync.Mutex
	nextCalls := 0
	m := NewRadioManager(t.TempDir(), "127.0.0.1", func() (string, string, int, bool) {
		nextMu.Lock()
		defer nextMu.Unlock()
		if nextCalls > 0 {
			return "", "", 0, false
		}
		nextCalls++
		return "evaluation-track", "evaluation-track", 0, true
	}, RadioCallbacks{})
	m.SetOutputMode(func() RadioOutputMode { return RadioOutputModeProgram })
	m.SetPublisherProfile(func() RadioPublisherProfile {
		return RadioPublisherProfilePlaylistGPUEvaluation
	})
	radioErrors := make(chan RadioError, 1)
	var capturePreTeardown func(string)
	m.cb.OnError = func(radioErr RadioError) {
		t.Logf("RadioManager child error: stage=%s message=%s", radioErr.Stage, radioErr.Message)
		if radioErr.Stage == RadioErrorStagePublisher || radioErr.Stage == RadioErrorStageMediaMTX {
			capturePreTeardown("radio-error:" + string(radioErr.Stage))
		}
		select {
		case radioErrors <- radioErr:
		default:
		}
	}
	m.startProgramEncoder = func(context.Context, RadioActiveSessionContract) (ProgramEncoder, error) {
		return newLifecycleProgramEncoder(), nil
	}
	m.runProgramFeeder = func(ctx context.Context, _ string, _ int, width, height int, out chan<- ProgramSourceFrame) error {
		for index := range frames {
			frame := ProgramSourceFrame{
				VideoRGBA: make([]byte, width*height*4),
				AudioPCM:  make([]byte, programSamplesPerFrame*programAudioChannels*programAudioBytes),
			}
			select {
			case out <- frame:
			case <-ctx.Done():
				return nil
			}
			// The real program feeder is a wall-clock paced FFmpeg reader. Keep
			// this injected source paced as well; an unpaced fixture returns
			// before ProgramPipeline has acknowledged its final frame and trips
			// the 750 ms drain guard after roughly 34 of the 60 audio tee calls.
			if index+1 < len(frames) {
				timer := time.NewTimer(time.Second / programVideoFrameRate)
				select {
				case <-timer.C:
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return nil
				}
			}
		}
		return nil
	}

	ready := make(chan error, 1)
	finished := make(chan error, 1)
	drainBridge := make(chan struct{})
	drained := make(chan error, 1)
	var bridge *PlaylistGPUMuxBridge
	var bridgeMu sync.Mutex
	var pathReadyObserved atomic.Bool
	var pathReadyAt atomic.Int64
	var pathReadyObserverCancel context.CancelFunc
	var publisherBytes *diagnosticCountingWriter
	var publisherInput *diagnosticCaptureWriter
	var videoSent atomic.Int32
	videoSentDone := make(chan struct{})
	var audioTeeAttempts atomic.Int32
	var audioTeeCompleted atomic.Int32
	audioComplete := make(chan struct{})
	var audioCompleteOnce sync.Once
	var audioFailure = make(chan error, 1)
	var preTeardownOnce sync.Once
	capturePreTeardown = func(reason string) {
		preTeardownOnce.Do(func() {
			t.Logf("pre-teardown capture reason=%s", reason)
			var capturedInput []byte
			if publisherBytes != nil {
				t.Logf("pre-teardown publisher sink: calls=%d requested_bytes=%d completed_bytes=%d first_write_ns=%d last_write_ns=%d", publisherBytes.writes.Load(), publisherBytes.requested.Load(), publisherBytes.bytes.Load(), publisherBytes.firstWriteAt.Load(), publisherBytes.lastWriteAt.Load())
			}
			t.Logf("pre-teardown app path readiness: observed=%t observed_at_ns=%d", pathReadyObserved.Load(), pathReadyAt.Load())
			if publisherInput != nil {
				capturedInput = publisherInput.Snapshot()
				writePublisherInputCapture(capturedInput)
				t.Logf("pre-teardown publisher stdin: bytes=%d sha256=%x", len(capturedInput), sha256.Sum256(capturedInput))
			}
			bridgeMu.Lock()
			bridgeLocal := bridge
			bridgeMu.Unlock()
			if bridgeLocal != nil && bridgeLocal.process != nil {
				process := bridgeLocal.process
				process.mu.Lock()
				closed, inputsClosed, hasFrame, sequence := process.closed, process.inputsClosed, process.hasFrame, process.sequence
				process.mu.Unlock()
				t.Logf("pre-teardown mux: closed=%t inputs_closed=%t has_frame=%t sequence=%d", closed, inputsClosed, hasFrame, sequence)
				t.Logf("pre-teardown mux_stderr=%q", process.stderr.String())
				bufferStats := bridgeLocal.buffer.stats()
				t.Logf("pre-teardown mux_output_buffer: accepted_bytes=%d written_bytes=%d queued_bytes=%d write_calls=%d closed=%t err=%v", bufferStats.AcceptedBytes, bufferStats.WrittenBytes, bufferStats.QueuedBytes, bufferStats.WriteCalls, bufferStats.Closed, bufferStats.Err)
			}
			m.mu.Lock()
			runtimeValue := m.runtime
			m.mu.Unlock()
			if rt, ok := runtimeValue.(*mediaMTXRuntime); ok {
				if rt.proc != nil {
					processDone := "false"
					select {
					case processErr := <-rt.proc.done():
						processDone = fmt.Sprintf("true err=%v", processErr)
					default:
					}
					t.Logf("pre-teardown MediaMTX process: pid=%d exe=%s done=%s", rt.proc.pid(), rt.exe, processDone)
				}
				t.Logf("pre-teardown MediaMTX runtime ports: api=%d hls=%d rtmp=%d rtsp=%d backend_rtsp=%d rtp=%d rtcp=%d backend_rtp=%d backend_rtcp=%d", rt.cfg.Ports.API, rt.cfg.Ports.HLS, rt.cfg.Ports.RTMP, rt.cfg.Ports.RTSP, rt.cfg.Ports.BackendRTSP, rt.cfg.Ports.RTP, rt.cfg.Ports.RTCP, rt.cfg.Ports.BackendRTP, rt.cfg.Ports.BackendRTCP)
				t.Logf("pre-teardown MediaMTX RTMP URL: %s", sanitizeRadioErrorMessage(rt.rtmpPublishURL()))
				if netstat, netstatErr := exec.Command("netstat", "-ano").CombinedOutput(); netstatErr == nil {
					ports := []int{rt.cfg.Ports.API, rt.cfg.Ports.HLS, rt.cfg.Ports.RTMP, rt.cfg.Ports.RTSP, rt.cfg.Ports.BackendRTSP, rt.cfg.Ports.RTP, rt.cfg.Ports.RTCP, rt.cfg.Ports.BackendRTP, rt.cfg.Ports.BackendRTCP}
					for _, line := range strings.Split(string(netstat), "\n") {
						for _, port := range ports {
							if strings.Contains(line, fmt.Sprintf(":%d", port)) {
								t.Logf("pre-teardown netstat port=%d: %s", port, strings.TrimSpace(line))
								break
							}
						}
					}
				} else {
					t.Logf("pre-teardown netstat unavailable=%v", netstatErr)
				}
				if configData, configErr := os.ReadFile(rt.configPath); configErr == nil {
					t.Logf("pre-teardown MediaMTX generated config:\n%s", redactMediaMTXDiagnostic(rt, string(configData)))
				} else {
					t.Logf("pre-teardown MediaMTX generated config unavailable=%v", configErr)
				}
				diagnosticCtx, diagnosticCancel := context.WithTimeout(context.Background(), 2*time.Second)
				t.Logf("pre-teardown MediaMTX path API: %s", mediaMTXDiagnosticAPI(diagnosticCtx, rt, "/v3/paths/get/"+rt.cfg.Path))
				t.Logf("pre-teardown MediaMTX RTMP API: %s", mediaMTXDiagnosticAPI(diagnosticCtx, rt, "/v3/rtmpconns/list"))
				diagnosticCancel()
			}
			if data, err := os.ReadFile(publisherDebugPath); err == nil {
				t.Logf("pre-teardown publisher diagnostic:\n%s", string(data))
			} else {
				t.Logf("pre-teardown publisher diagnostic unavailable=%v", err)
			}
			if data, err := os.ReadFile(debugLogPath); err == nil {
				text := redactMediaMTXDiagnostic(nil, string(data))
				if len(text) > 12_000 {
					text = text[len(text)-12_000:]
				}
				t.Logf("pre-teardown MediaMTX debug log tail:\n%s", text)
			} else {
				t.Logf("pre-teardown MediaMTX debug log unavailable=%v", err)
			}
		})
	}
	m.cb.OnPublisherSink = func(sink io.Writer) {
		publisherInput = &diagnosticCaptureWriter{}
		publisherBytes = &diagnosticCountingWriter{dst: io.MultiWriter(sink, publisherInput)}
		bridgeLocal, bridgeErr := StartPlaylistGPUMuxBridge(ctx, ffmpeg, publisherBytes, 640, 360, 30, 48_000, 2, frames[0].PTSNs)
		if bridgeErr != nil {
			ready <- bridgeErr
			return
		}
		bridgeMu.Lock()
		bridge = bridgeLocal
		bridgeMu.Unlock()
		observerCtx, observerCancel := context.WithCancel(ctx)
		pathReadyObserverCancel = observerCancel
		if runtimeValue, ok := m.runtime.(*mediaMTXRuntime); ok {
			go func() {
				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()
				for {
					if runtimeValue.pathReady(observerCtx) {
						pathReadyAt.Store(time.Now().UnixNano())
						pathReadyObserved.Store(true)
						t.Log("application MediaMTX path readiness observed while publisher remained active")
						return
					}
					select {
					case <-observerCtx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
		}
		m.SetPlaylistGPUOutputActive(true)
		m.SetPlaylistAudioTee(func(samples []byte, pts time.Duration) error {
			audioTeeAttempts.Add(1)
			// FFmpeg's live H.264 input may delay opening/servicing the
			// independent PCM listener until it has probed the video stream.
			// Keep this diagnostic lifecycle deterministic by pre-filling the
			// video input before allowing the first PCM write. This tests the
			// ordering hypothesis without changing the normal CPU route.
			select {
			case <-videoSentDone:
			case <-ctx.Done():
				return ctx.Err()
			}
			faded, fadeErr := ApplyPlaylistGPUAudioFadePCM(samples, pts.Nanoseconds(), 48_000, 2, video.AudioFadePlan{
				StartPTSNs: frames[0].PTSNs,
				DurationNS: 400_000_000,
				Curve:      "linear",
			})
			if fadeErr != nil {
				audioFailure <- fadeErr
				audioCompleteOnce.Do(func() { close(audioComplete) })
				return fadeErr
			}
			if audioErr := bridgeLocal.AudioTee(faded, pts); audioErr != nil {
				audioFailure <- audioErr
				audioCompleteOnce.Do(func() { close(audioComplete) })
				return audioErr
			}
			if audioTeeCompleted.Add(1) == int32(len(frames)) {
				audioCompleteOnce.Do(func() { close(audioComplete) })
			}
			return nil
		})
		ready <- nil
		go func() {
			for _, frame := range frames {
				if videoErr := bridgeLocal.VideoSink(frame); videoErr != nil {
					finished <- videoErr
					return
				}
				videoSent.Add(1)
			}
			close(videoSentDone)
			if videoErr := bridgeLocal.CloseVideoInput(); videoErr != nil {
				finished <- fmt.Errorf("close GPU mux video input after video feed: %w", videoErr)
				return
			}
			select {
			case audioErr := <-audioFailure:
				_ = bridgeLocal.Close()
				finished <- audioErr
				return
			case <-audioComplete:
				// Keep the mux and persistent publisher alive while the real
				// MediaMTX path is observed. Closing the bridge here would send
				// EOF before the publisher-readiness gate and would turn this
				// into a short artifact test rather than a publisher lifecycle
				// test.
			case <-ctx.Done():
				_ = bridgeLocal.Close()
				finished <- ctx.Err()
				return
			}
			finished <- nil
			select {
			case <-drainBridge:
			case <-ctx.Done():
				_ = bridgeLocal.Close()
				drained <- ctx.Err()
				return
			}
			if closeErr := bridgeLocal.CloseInputs(); closeErr != nil {
				drained <- closeErr
				return
			}
			drained <- bridgeLocal.Wait()
		}()
	}
	cleanup := func() {
		bridgeMu.Lock()
		bridgeLocal := bridge
		bridgeMu.Unlock()
		if pathReadyObserverCancel != nil {
			pathReadyObserverCancel()
		}
		if bridgeLocal != nil {
			_ = bridgeLocal.Close()
		}
		m.SetPlaylistAudioTee(nil)
		m.SetPlaylistGPUOutputActive(false)
		m.Stop(20 * time.Second)
	}
	t.Cleanup(cleanup)

	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		bridgeMu.Lock()
		bridgeLocal := bridge
		bridgeMu.Unlock()
		t.Logf("publisher lifecycle boundary timeout: video_sent=%d audio_tee_attempts=%d audio_tee_completed=%d", videoSent.Load(), audioTeeAttempts.Load(), audioTeeCompleted.Load())
		if publisherBytes != nil {
			t.Logf("publisher lifecycle boundary sink: calls=%d requested_bytes=%d completed_bytes=%d", publisherBytes.writes.Load(), publisherBytes.requested.Load(), publisherBytes.bytes.Load())
		}
		if publisherInput != nil {
			input := publisherInput.Snapshot()
			t.Logf("publisher lifecycle boundary stdin_bytes=%d", len(input))
			for _, probesize := range []string{"32", "4k", "16k", "64k", "256k"} {
				t.Logf("publisher lifecycle boundary stdin_eof_probe: %s", probeCapturedMPEGTS(ffmpeg, t.TempDir(), input, probesize))
			}
		}
		if bridgeLocal != nil && bridgeLocal.process != nil {
			process := bridgeLocal.process
			process.mu.Lock()
			closed, inputsClosed, hasFrame, sequence := process.closed, process.inputsClosed, process.hasFrame, process.sequence
			process.mu.Unlock()
			t.Logf("publisher lifecycle boundary mux: closed=%t inputs_closed=%t has_frame=%t sequence=%d", closed, inputsClosed, hasFrame, sequence)
			t.Logf("publisher lifecycle boundary mux_stderr=%q", process.stderr.String())
			select {
			case muxErr := <-process.done:
				t.Logf("publisher lifecycle boundary mux_exit=%v", muxErr)
			default:
				t.Log("publisher lifecycle boundary mux_running=true")
			}
			t.Logf("publisher lifecycle boundary stdout_drain_error=%v", bridgeLocal.OutputError())
			bufferStats := bridgeLocal.buffer.stats()
			t.Logf("publisher lifecycle boundary mux_output_buffer: accepted_bytes=%d written_bytes=%d queued_bytes=%d write_calls=%d closed=%t err=%v", bufferStats.AcceptedBytes, bufferStats.WrittenBytes, bufferStats.QueuedBytes, bufferStats.WriteCalls, bufferStats.Closed, bufferStats.Err)
		}
		if data, err := os.ReadFile(publisherDebugPath); err == nil {
			t.Logf("publisher lifecycle boundary publisher_diagnostic:\n%s", string(data))
		} else {
			t.Logf("publisher lifecycle boundary publisher_diagnostic_unavailable=%v", err)
		}
		m.mu.Lock()
		runtime := m.runtime
		m.mu.Unlock()
		if rt, ok := runtime.(*mediaMTXRuntime); ok {
			diagnosticCtx, diagnosticCancel := context.WithTimeout(context.Background(), 2*time.Second)
			t.Logf("publisher lifecycle boundary MediaMTX path API: %s", mediaMTXDiagnosticAPI(diagnosticCtx, rt, "/v3/paths/get/"+rt.cfg.Path))
			t.Logf("publisher lifecycle boundary MediaMTX RTMP API: %s", mediaMTXDiagnosticAPI(diagnosticCtx, rt, "/v3/rtmpconns/list"))
			diagnosticCancel()
		}
		// Capture publisher/MediaMTX exit diagnostics before the test cleanup
		// closes the owned runtime and destroys its temporary config directory.
		m.Stop(10 * time.Second)
		if data, err := os.ReadFile(publisherDebugPath); err == nil {
			t.Logf("publisher lifecycle boundary publisher_diagnostic_after_stop:\n%s", string(data))
		} else {
			t.Logf("publisher lifecycle boundary publisher_diagnostic_after_stop_unavailable=%v", err)
		}
		if data, err := os.ReadFile(debugLogPath); err == nil {
			text := redactMediaMTXDiagnostic(nil, string(data))
			if len(text) > 12_000 {
				text = text[len(text)-12_000:]
			}
			t.Logf("publisher lifecycle boundary MediaMTX log tail:\n%s", text)
		} else {
			t.Logf("publisher lifecycle boundary MediaMTX log unavailable=%v", err)
		}
		t.Fatal("real publisher program session did not finish feeding the GPU mux")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case radioErr := <-radioErrors:
		t.Fatalf("real RadioManager publisher failed before readiness: stage=%s message=%s", radioErr.Stage, radioErr.Message)
	default:
	}

	// The actual RadioManager publisher is the sink owner. Verify that the
	// MediaMTX API sees the published path, not merely that bytes reached a fake
	// io.Writer.
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	if runtime == nil {
		t.Fatal("RadioManager did not retain the real MediaMTX runtime")
	}
	rt, ok := runtime.(*mediaMTXRuntime)
	if !ok {
		t.Fatalf("runtime type = %T, want *mediaMTXRuntime", runtime)
	}
	pathCtx, pathCancel := context.WithTimeout(ctx, 15*time.Second)
	defer pathCancel()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case radioErr := <-radioErrors:
			t.Fatalf("real RadioManager publisher failed before MediaMTX readiness: stage=%s message=%s", radioErr.Stage, radioErr.Message)
		default:
		}
		if rt.pathReady(pathCtx) {
			t.Logf("real MediaMTX path ready: %s", rt.cfg.Path)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !rt.pathReady(pathCtx) {
		configData, configErr := os.ReadFile(rt.configPath)
		if configErr == nil {
			t.Logf("MediaMTX generated config:\n%s", redactMediaMTXDiagnostic(rt, string(configData)))
		} else {
			t.Logf("MediaMTX generated config unavailable: %v", configErr)
		}
		if publisherBytes != nil {
			t.Logf("publisher sink writes: calls=%d requested_bytes=%d completed_bytes=%d", publisherBytes.writes.Load(), publisherBytes.requested.Load(), publisherBytes.bytes.Load())
		}
		if publisherInput != nil {
			input := publisherInput.Snapshot()
			writePublisherInputCapture(input)
			syncBytes := 0
			for i := 0; i < len(input); i += 188 {
				if input[i] == 0x47 {
					syncBytes++
				}
			}
			head := input
			if len(head) > 376 {
				head = head[:376]
			}
			t.Logf("publisher stdin capture: bytes=%d mpegts_sync_at_188=%d head=% x", len(input), syncBytes, head)
			for _, probesize := range []string{"32", "4k", "16k", "64k", "256k"} {
				t.Logf("publisher input EOF probe: %s", probeCapturedMPEGTS(ffmpeg, t.TempDir(), input, probesize))
			}
		}
		bridgeMu.Lock()
		bridgeLocal := bridge
		bridgeMu.Unlock()
		if bridgeLocal != nil && bridgeLocal.process != nil && bridgeLocal.process.stderr != nil {
			t.Logf("GPU mux FFmpeg stderr:\n%s", bridgeLocal.process.stderr.String())
			select {
			case muxErr := <-bridgeLocal.process.done:
				t.Logf("GPU mux FFmpeg exit: %v", muxErr)
			default:
				t.Log("GPU mux FFmpeg is still running at failure inspection")
			}
		}
		// Capture the MediaMTX state before closing the owned RadioManager. An API
		// request after Stop only observes cleanup (usually connection refused),
		// not the live failure boundary.
		diagnosticCtx, diagnosticCancel := context.WithTimeout(context.Background(), 2*time.Second)
		preTeardownPathAPI := mediaMTXDiagnosticAPI(diagnosticCtx, rt, "/v3/paths/get/"+rt.cfg.Path)
		preTeardownRTMPAPI := mediaMTXDiagnosticAPI(diagnosticCtx, rt, "/v3/rtmpconns/list")
		diagnosticCancel()
		t.Logf("pre-teardown MediaMTX path API: %s", preTeardownPathAPI)
		t.Logf("pre-teardown MediaMTX RTMP API: %s", preTeardownRTMPAPI)
		if data, err := os.ReadFile(debugLogPath); err == nil {
			text := redactMediaMTXDiagnostic(rt, string(data))
			if len(text) > 12_000 {
				text = text[len(text)-12_000:]
			}
			t.Logf("pre-teardown MediaMTX debug log tail:\n%s", text)
		} else {
			t.Logf("pre-teardown MediaMTX debug log unavailable=%v", err)
		}
		// Close the owned RadioManager before reading the publisher diagnostic so
		// cmd.Wait() appends the final stderr/exit state. This never scans or
		// terminates unrelated FFmpeg/MediaMTX processes.
		m.Stop(10 * time.Second)
		if data, err := os.ReadFile(publisherDebugPath); err == nil {
			t.Logf("publisher FFmpeg diagnostic after stop:\n%s", string(data))
		} else {
			t.Logf("publisher FFmpeg diagnostic unavailable after stop: %v", err)
		}
		if data, err := os.ReadFile(debugLogPath); err == nil {
			text := redactMediaMTXDiagnostic(rt, string(data))
			var rtmpLines []string
			for _, line := range strings.Split(text, "\n") {
				lower := strings.ToLower(line)
				if strings.Contains(lower, "[rtmp]") || strings.Contains(lower, "authentication") || strings.Contains(lower, "publish") || strings.Contains(lower, "listen") || strings.Contains(lower, "error") {
					rtmpLines = append(rtmpLines, line)
				}
			}
			if len(rtmpLines) > 0 {
				t.Logf("MediaMTX RTMP/auth diagnostic lines:\n%s", strings.Join(rtmpLines, "\n"))
			}
			if len(text) > 12_000 {
				text = text[len(text)-12_000:]
			}
			t.Logf("MediaMTX debug log tail:\n%s", text)
		} else {
			t.Logf("MediaMTX debug log unavailable: %v", err)
		}
		t.Fatalf("MediaMTX path did not become ready; path=%s", rt.cfg.Path)
	}
	close(drainBridge)
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	m.Stop(20 * time.Second)
}

// replayCapturedMPEGTSFile is a diagnostic discriminator only. It sends the
// exact application-captured MPEG-TS bytes through the same GPU publisher
// arguments, changing only pipe:0 to a regular file. It runs before the
// app-owned MediaMTX runtime is torn down and never promotes the E2E gate.
func replayCapturedMPEGTSFile(ctx context.Context, ffmpeg string, rt *mediaMTXRuntime, dir string, payload []byte) (ready bool, exitErr error, stderr string) {
	path := filepath.Join(dir, "application-publisher-replay.ts")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return false, err, ""
	}
	args := playlistGPUEvaluationPublisherArgs(rt.rtmpPublishURL())
	for i, arg := range args {
		if arg == "pipe:0" {
			args[i] = path
		}
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderrBuilder strings.Builder
	cmd.Stderr = &stderrBuilder
	if err := cmd.Start(); err != nil {
		return false, err, ""
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case exitErr = <-done:
			return ready || rt.pathReady(context.Background()), exitErr, sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
		case <-ticker.C:
			if rt.pathReady(ctx) {
				ready = true
			}
			if ready {
				select {
				case exitErr = <-done:
					return true, exitErr, sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
				default:
				}
			}
		case <-ctx.Done():
			return ready, ctx.Err(), sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
		}
	}
}

// replayCapturedMPEGTSOpenPipe is the paired topology discriminator for the
// exact application payload. It writes the complete payload in one burst, keeps
// stdin open while checking MediaMTX, then sends EOF and checks once more. The
// result is diagnostic only: it does not alter the production publisher.
func replayCapturedMPEGTSOpenPipe(ctx context.Context, ffmpeg string, rt *mediaMTXRuntime, payload []byte) (readyWhileOpen, readyAfterEOF bool, exitErr error, stderr string) {
	args := playlistGPUEvaluationPublisherArgs(rt.rtmpPublishURL())
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	var stderrBuilder strings.Builder
	cmd.Stderr = &stderrBuilder
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false, false, err, ""
	}
	if err := cmd.Start(); err != nil {
		return false, false, err, ""
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if _, err := stdin.Write(payload); err != nil {
		_ = stdin.Close()
		return false, false, err, sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
	}
	check := func() bool {
		return rt.pathReady(context.Background())
	}
	openDeadline := time.Now().Add(4 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	for time.Now().Before(openDeadline) {
		if check() {
			readyWhileOpen = true
		}
		select {
		case exitErr = <-done:
			return readyWhileOpen, readyAfterEOF || check(), exitErr, sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
		case <-ticker.C:
		case <-ctx.Done():
			ticker.Stop()
			_ = stdin.Close()
			return readyWhileOpen, false, ctx.Err(), sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
		}
	}
	_ = stdin.Close()
	ticker.Stop()
	afterEOFDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(afterEOFDeadline) {
		if check() {
			readyAfterEOF = true
		}
		select {
		case exitErr = <-done:
			return readyWhileOpen, readyAfterEOF || check(), exitErr, sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return readyWhileOpen, readyAfterEOF, ctx.Err(), sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
		}
	}
	return readyWhileOpen, readyAfterEOF || check(), <-done, sanitizeRadioErrorMessage(strings.TrimSpace(stderrBuilder.String()))
}

func probeCapturedMPEGTS(ffmpeg, dir string, payload []byte, probesize string) string {
	path := filepath.Join(dir, "publisher-input.ts")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Sprintf("probesize=%s write_error=%v", probesize, err)
	}
	// A partial live capture is intentionally allowed to be undecodable here,
	// but the diagnostic must never hold the parent test past its boundary
	// snapshot. FFmpeg can remain in demux/probe read state for an incomplete
	// MPEG-TS file, so use a bounded child context and classify that result as a
	// probe timeout rather than allowing -test.timeout to panic before the
	// publisher/mux diagnostics are emitted.
	probeCtx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, ffmpeg,
		"-hide_banner", "-loglevel", "warning",
		"-probesize", probesize,
		"-analyzeduration", "100000",
		"-fpsprobesize", "2",
		"-f", "mpegts", "-i", path,
		"-map", "0:v:0", "-map", "0:a:0",
		"-f", "null", "-",
	)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if len(text) > 600 {
		text = text[len(text)-600:]
	}
	if probeCtx.Err() != nil {
		return fmt.Sprintf("probesize=%s timeout=%v stderr=%q", probesize, probeCtx.Err(), text)
	}
	return fmt.Sprintf("probesize=%s exit=%v stderr=%q", probesize, err, text)
}

func writePublisherInputCapture(payload []byte) {
	path := strings.TrimSpace(os.Getenv("IMAGEPAD_PUBLISHER_INPUT_CAPTURE"))
	if path == "" || len(payload) == 0 {
		return
	}
	_ = os.WriteFile(path, payload, 0o600)
}

func h264PayloadHead(payload []byte) []byte {
	if len(payload) > 96 {
		payload = payload[:96]
	}
	return append([]byte(nil), payload...)
}

func h264NALTypes(payload []byte) []int {
	var types []int
	for offset := 0; offset+3 < len(payload); {
		start := -1
		prefixLen := 0
		for i := offset; i+3 < len(payload); i++ {
			if payload[i] == 0 && payload[i+1] == 0 && payload[i+2] == 1 {
				start, prefixLen = i, 3
				break
			}
			if i+4 < len(payload) && payload[i] == 0 && payload[i+1] == 0 && payload[i+2] == 0 && payload[i+3] == 1 {
				start, prefixLen = i, 4
				break
			}
		}
		if start < 0 || start+prefixLen >= len(payload) {
			break
		}
		types = append(types, int(payload[start+prefixLen]&0x1f))
		offset = start + prefixLen
	}
	return types
}

func mediaMTXDiagnosticAPI(ctx context.Context, rt *mediaMTXRuntime, path string) string {
	if rt == nil {
		return "runtime unavailable"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rt.apiBaseURL()+path, nil)
	if err != nil {
		return fmt.Sprintf("request error: %v", err)
	}
	resp, err := rt.httpClient.Do(req)
	if err != nil {
		return fmt.Sprintf("transport error: %v", err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	text := redactMediaMTXDiagnostic(rt, string(data))
	if readErr != nil {
		return fmt.Sprintf("HTTP %s read error: %v body=%q", resp.Status, readErr, text)
	}
	return fmt.Sprintf("HTTP %s body=%q", resp.Status, text)
}

func redactMediaMTXDiagnostic(rt *mediaMTXRuntime, text string) string {
	if rt == nil {
		return text
	}
	text = strings.ReplaceAll(text, rt.cfg.PublishUser, "<redacted-user>")
	text = strings.ReplaceAll(text, rt.cfg.PublishPass, "<redacted-pass>")
	return text
}

package obsrtmp

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

type playlistMuxCountWriter struct {
	dst io.Writer
	n   atomic.Int64
}

func (w *playlistMuxCountWriter) Write(payload []byte) (int, error) {
	n, err := w.dst.Write(payload)
	if n > 0 {
		w.n.Add(int64(n))
	}
	return n, err
}

func TestPlaylistGPURealSidecarMuxArtifactFFmpegE2E(t *testing.T) {
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
	ffprobe := os.Getenv("IMAGEPAD_FFPROBE")
	if ffprobe == "" {
		var err error
		ffprobe, err = exec.LookPath("ffprobe")
		if err != nil {
			t.Skip("ffprobe is not installed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	worker, err := video.StartPlaylistGPUWorker(ctx, sidecar, "real-playlist-mux-e2e")
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	input := video.AudioRenderInput{
		Metadata: video.AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"},
		Analysis: video.AudioAnalysis{
			FPS:      30,
			Duration: 0.1,
			Frames: []video.AudioFrame{
				{Spectrum24: [24]float64{0.25, 0.5}},
				{Spectrum24: [24]float64{0.5, 0.75}},
				{Spectrum24: [24]float64{0.75, 1}},
			},
		},
	}
	assets, timeline, err := worker.PrepareCompiledTrack(ctx, 1, input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	frames := make([]video.EncodedH264Frame, 0, timeline.FrameCount)
	if err := worker.RenderPreparedTimeline(ctx, 1, assets, timeline, func(frame video.EncodedH264Frame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mux, err := StartPlaylistGPUMuxProcess(ffmpeg, 640, 360, 30, 48_000, 2, frames[0].PTSNs)
	if err != nil {
		t.Fatal(err)
	}
	var ts bytes.Buffer
	countedOutput := &playlistMuxCountWriter{dst: &ts}
	outputDone := make(chan error, 1)
	go func() { outputDone <- CopyPlaylistGPUMuxOutput(countedOutput, mux) }()
	for _, frame := range frames {
		if err := mux.WriteVideoFrame(frame); err != nil {
			_ = mux.Close()
			t.Fatal(err)
		}
	}
	pcm := make([]byte, 48_000/10*2*2)
	if err := mux.WriteAudioPCM(pcm, 2); err != nil {
		_ = mux.Close()
		t.Fatal(err)
	}
	liveDeadline := time.Now().Add(3 * time.Second)
	for countedOutput.n.Load() == 0 && time.Now().Before(liveDeadline) {
		select {
		case err := <-outputDone:
			t.Fatalf("GPU mux output ended before live flush: %v; stderr=%s", err, mux.stderr.String())
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if countedOutput.n.Load() == 0 {
		_ = mux.Close()
		t.Fatalf("GPU mux produced no MPEG-TS bytes while inputs remained open; stderr=%s", mux.stderr.String())
	}
	if err := mux.CloseInputs(); err != nil {
		t.Fatal(err)
	}
	if err := mux.Wait(); err != nil {
		t.Fatalf("ffmpeg mux wait: %v; stderr=%s", err, mux.stderr.String())
	}
	if err := <-outputDone; err != nil {
		t.Fatal(err)
	}
	if ts.Len() == 0 {
		t.Fatal("GPU mux produced empty MPEG-TS output")
	}
	path := filepath.Join(t.TempDir(), "real-playlist-gpu.ts")
	if err := os.WriteFile(path, ts.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	decode := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-f", "null", "-")
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("decode GPU artifact: %v: %s", err, output)
	}
	report, err := video.ProbePlaylistGPUArtifact(ctx, ffprobe, path, 30, timeline.FrameCount)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real GPU artifact report: %+v", report)
	if err := report.Validate(); err != nil {
		t.Fatalf("GPU artifact gate: %v; report=%+v", err, report)
	}
}

func TestPlaylistGPURealSidecarTransitionMuxArtifactFFmpegE2E(t *testing.T) {
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
	ffprobe := os.Getenv("IMAGEPAD_FFPROBE")
	if ffprobe == "" {
		var err error
		ffprobe, err = exec.LookPath("ffprobe")
		if err != nil {
			t.Skip("ffprobe is not installed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	worker, err := video.StartPlaylistGPUWorker(ctx, sidecar, "real-playlist-transition-mux-e2e")
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	input := video.AudioRenderInput{
		Metadata: video.AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"},
		Analysis: video.AudioAnalysis{
			FPS:      30,
			Duration: 0.1,
			Frames: []video.AudioFrame{
				{Spectrum24: [24]float64{0.25, 0.5}},
				{Spectrum24: [24]float64{0.5, 0.75}},
				{Spectrum24: [24]float64{0.75, 1}},
			},
		},
	}
	sourceAssets, source, err := worker.PrepareCompiledTrack(ctx, 7, input, "track-a")
	if err != nil {
		t.Fatal(err)
	}
	targetAssets, target, err := worker.CompilePreparedTrackAssets(ctx, input, "track-b")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := video.NewPlaylistGPUTransitionController(7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.AttachTransitionController(controller); err != nil {
		t.Fatal(err)
	}
	plan, err := video.CompilePlaylistTransition(video.TransitionRequest{
		Schema:               video.GPUPlaylistTimelineSchema,
		Epoch:                7,
		Reason:               video.PlaylistTransitionTrackChange,
		SourceTrackID:        "track-a",
		PlaybackHeadSequence: 10,
		PlaybackHeadPTSNs:    333_333_330,
	}, "track-b", 15, 500_000_000, 2, 3, 33_333_333, video.AudioFadePlan{Curve: "linear", DurationNS: 99_999_999})
	if err != nil {
		t.Fatal(err)
	}
	frames := make([]video.EncodedH264Frame, 0, plan.DurationFrames+target.FrameCount)
	if err := worker.ExecutePlaylistGPUContinuousTrack(ctx, sourceAssets, source, targetAssets, target, plan, 8, func(frame video.EncodedH264Frame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantFrames := plan.DurationFrames + target.FrameCount
	if uint64(len(frames)) != wantFrames {
		t.Fatalf("transition encoded frame count = %d, want %d", len(frames), wantFrames)
	}
	for i, frame := range frames {
		if err := frame.Validate(); err != nil {
			t.Fatalf("transition frame %d validation failed: %v", i, err)
		}
		if frame.PixelReadbackBytes != 0 {
			t.Fatalf("transition frame %d reported pixel readback: %d", i, frame.PixelReadbackBytes)
		}
		if i > 0 {
			if frame.Sequence <= frames[i-1].Sequence {
				t.Fatalf("transition sequence reordered at frame %d: %d <= %d", i, frame.Sequence, frames[i-1].Sequence)
			}
			if frame.PTSNs <= frames[i-1].PTSNs {
				t.Fatalf("transition PTS reordered at frame %d: %d <= %d", i, frame.PTSNs, frames[i-1].PTSNs)
			}
		}
	}
	mux, err := StartPlaylistGPUMuxProcess(ffmpeg, 640, 360, 30, 48_000, 2, frames[0].PTSNs)
	if err != nil {
		t.Fatal(err)
	}
	var ts bytes.Buffer
	outputDone := make(chan error, 1)
	go func() { outputDone <- CopyPlaylistGPUMuxOutput(&ts, mux) }()
	for _, frame := range frames {
		if err := mux.WriteVideoFrame(frame); err != nil {
			_ = mux.Close()
			t.Fatal(err)
		}
	}
	pcm := make([]byte, int(wantFrames)*48_000/30*2*2)
	if err := mux.WriteAudioPCM(pcm, 2); err != nil {
		_ = mux.Close()
		t.Fatal(err)
	}
	if err := mux.CloseInputs(); err != nil {
		t.Fatal(err)
	}
	if err := mux.Wait(); err != nil {
		t.Fatalf("transition ffmpeg mux wait: %v; stderr=%s", err, mux.stderr.String())
	}
	if err := <-outputDone; err != nil {
		t.Fatal(err)
	}
	if ts.Len() == 0 {
		t.Fatal("transition GPU mux produced empty MPEG-TS output")
	}
	path := filepath.Join(t.TempDir(), "real-playlist-transition-gpu.ts")
	if err := os.WriteFile(path, ts.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	decode := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-f", "null", "-")
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("decode transition GPU artifact: %v: %s", err, output)
	}
	report, err := video.ProbePlaylistGPUArtifact(ctx, ffprobe, path, 30, wantFrames)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transition GPU artifact report: %+v", report)
	if err := report.Validate(); err != nil {
		t.Fatalf("transition GPU artifact gate: %v; report=%+v", err, report)
	}
}

func TestPlaylistGPURealSidecarContinuousTrackMuxArtifactFFmpegE2E(t *testing.T) {
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
	ffprobe := os.Getenv("IMAGEPAD_FFPROBE")
	if ffprobe == "" {
		var err error
		ffprobe, err = exec.LookPath("ffprobe")
		if err != nil {
			t.Skip("ffprobe is not installed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	worker, err := video.StartPlaylistGPUWorker(ctx, sidecar, "real-playlist-continuous-mux-e2e")
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
	targetWorker, err := video.StartPlaylistGPUWorker(ctx, sidecar, "real-playlist-continuous-target-assets-e2e")
	if err != nil {
		t.Fatal(err)
	}
	targetAssets, targetTimeline, err := targetWorker.PrepareCompiledTrack(ctx, 8, input, "track-b")
	if err != nil {
		_ = targetWorker.Close()
		t.Fatal(err)
	}
	if err := targetWorker.Close(); err != nil {
		t.Fatal(err)
	}
	sourceAssets, sourceTimeline, err := worker.PrepareCompiledTrack(ctx, 7, input, "track-a")
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
	plan, err := video.CompilePlaylistTransition(video.TransitionRequest{
		Schema:               video.GPUPlaylistTimelineSchema,
		Epoch:                7,
		Reason:               video.PlaylistTransitionTrackChange,
		SourceTrackID:        "track-a",
		PlaybackHeadSequence: 10,
		PlaybackHeadPTSNs:    333_333_330,
	}, "track-b", 15, 500_000_000, 2, 3, 33_333_333, video.AudioFadePlan{Curve: "linear", DurationNS: 99_999_999})
	if err != nil {
		t.Fatal(err)
	}
	frames := make([]video.EncodedH264Frame, 0, plan.DurationFrames+targetTimeline.FrameCount)
	if err := worker.ExecutePlaylistGPUContinuousTrack(ctx, sourceAssets, sourceTimeline, targetAssets, targetTimeline, plan, 8, func(frame video.EncodedH264Frame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantFrames := plan.DurationFrames + targetTimeline.FrameCount
	if uint64(len(frames)) != wantFrames {
		t.Fatalf("continuous encoded frame count = %d, want %d", len(frames), wantFrames)
	}
	for i, frame := range frames {
		if err := frame.Validate(); err != nil {
			t.Fatalf("continuous frame %d validation failed: %v", i, err)
		}
		if frame.PixelReadbackBytes != 0 {
			t.Fatalf("continuous frame %d reported pixel readback: %d", i, frame.PixelReadbackBytes)
		}
		if i > 0 {
			if frame.Sequence <= frames[i-1].Sequence {
				t.Fatalf("continuous sequence reordered at frame %d: %d <= %d", i, frame.Sequence, frames[i-1].Sequence)
			}
			if frame.PTSNs <= frames[i-1].PTSNs {
				t.Fatalf("continuous PTS reordered at frame %d: %d <= %d", i, frame.PTSNs, frames[i-1].PTSNs)
			}
		}
	}
	mux, err := StartPlaylistGPUMuxProcess(ffmpeg, 640, 360, 30, 48_000, 2, frames[0].PTSNs)
	if err != nil {
		t.Fatal(err)
	}
	var ts bytes.Buffer
	outputDone := make(chan error, 1)
	go func() { outputDone <- CopyPlaylistGPUMuxOutput(&ts, mux) }()
	for _, frame := range frames {
		if err := mux.WriteVideoFrame(frame); err != nil {
			_ = mux.Close()
			t.Fatal(err)
		}
	}
	pcm := make([]byte, int(wantFrames)*48_000/30*2*2)
	if err := mux.WriteAudioPCM(pcm, 2); err != nil {
		_ = mux.Close()
		t.Fatal(err)
	}
	if err := mux.CloseInputs(); err != nil {
		t.Fatal(err)
	}
	if err := mux.Wait(); err != nil {
		t.Fatalf("continuous ffmpeg mux wait: %v; stderr=%s", err, mux.stderr.String())
	}
	if err := <-outputDone; err != nil {
		t.Fatal(err)
	}
	if ts.Len() == 0 {
		t.Fatal("continuous GPU mux produced empty MPEG-TS output")
	}
	path := filepath.Join(t.TempDir(), "real-playlist-continuous-gpu.ts")
	if err := os.WriteFile(path, ts.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	decode := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-f", "null", "-")
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("decode continuous GPU artifact: %v: %s", err, output)
	}
	report, err := video.ProbePlaylistGPUArtifact(ctx, ffprobe, path, 30, wantFrames)
	if err != nil {
		t.Fatalf("continuous GPU artifact probe: %v; report=%+v", err, report)
	}
	t.Logf("continuous GPU artifact report: %+v", report)
	if err := report.Validate(); err != nil {
		t.Fatalf("continuous GPU artifact gate: %v; report=%+v", err, report)
	}
}

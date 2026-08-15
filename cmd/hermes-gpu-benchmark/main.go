package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/video"
)

type benchmarkResult struct {
	Lane        string             `json:"lane"`
	Run         int                `json:"run"`
	WallSeconds float64            `json:"wall_seconds"`
	Frames      uint64             `json:"frames"`
	Duration    float64            `json:"duration_seconds"`
	Stages      map[string]float64 `json:"stages_seconds"`
	Error       string             `json:"error,omitempty"`
}

type artifactProbe struct {
	Frames   uint64
	Duration float64
}

func main() {
	ffmpeg := requiredEnv("IMAGEPAD_BENCH_FFMPEG")
	ffprobe := requiredEnv("IMAGEPAD_BENCH_FFPROBE")
	sidecar := requiredEnv("IMAGEPAD_BENCH_SIDECAR")
	root := requiredEnv("IMAGEPAD_BENCH_ROOT")
	if err := os.MkdirAll(root, 0o700); err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	source := filepath.Join(root, "fixed-synthetic-music.wav")
	if err := generateSource(ctx, ffmpeg, source); err != nil {
		fatal(fmt.Errorf("generate source: %w", err))
	}

	// One warm-up per lane is discarded. Timed runs include analysis and the
	// lane's real render/mux/probe path, but never include compilation.
	for _, lane := range []string{"cpu", "gpu"} {
		if _, err := runLane(ctx, lane, 0, ffmpeg, ffprobe, sidecar, source, root); err != nil {
			fatal(fmt.Errorf("warmup %s: %w", lane, err))
		}
	}

	enc := json.NewEncoder(os.Stdout)
	for run := 1; run <= 5; run++ {
		for _, lane := range []string{"cpu", "gpu"} {
			result, err := runLane(ctx, lane, run, ffmpeg, ffprobe, sidecar, source, root)
			if err != nil {
				result.Error = err.Error()
				_ = enc.Encode(result)
				fatal(fmt.Errorf("run %s/%d: %w", lane, run, err))
			}
			if err := enc.Encode(result); err != nil {
				fatal(err)
			}
		}
	}
}

func requiredEnv(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		fatal(fmt.Errorf("%s is required", name))
	}
	return value
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// benchDimensions resolves the benchmark render resolution from
// IMAGEPAD_BENCH_HEIGHT (360/720/1080, default 360). Both lanes and the GPU
// mux agree on the same 16:9 size so CPU/GPU artifacts are comparable.
func benchDimensions() (width, height int, mode string) {
	switch strings.TrimSpace(os.Getenv("IMAGEPAD_BENCH_HEIGHT")) {
	case "1080":
		return 1920, 1080, "1080"
	case "720":
		return 1280, 720, "720"
	default:
		return 640, 360, "360"
	}
}

func generateSource(ctx context.Context, ffmpeg, path string) error {
	duration := strings.TrimSpace(os.Getenv("IMAGEPAD_BENCH_DURATION"))
	if duration == "" {
		duration = "4.0"
	}
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", duration, "-ac", "2", "-c:a", "pcm_s16le", path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg source: %w: %s", err, output)
	}
	return nil
}

func runLane(ctx context.Context, lane string, run int, ffmpeg, ffprobe, sidecar, source, root string) (benchmarkResult, error) {
	started := time.Now()
	result := benchmarkResult{Lane: lane, Run: run, Stages: make(map[string]float64)}
	analysisStarted := time.Now()
	analysis, err := video.AnalyzeAudioForKind(ctx, ffmpeg, source, video.SourceMusic)
	result.Stages["analysis"] = time.Since(analysisStarted).Seconds()
	if err != nil {
		result.WallSeconds = time.Since(started).Seconds()
		return result, fmt.Errorf("analyze: %w", err)
	}
	input := video.AudioRenderInput{
		SourcePath: source,
		Kind:       video.SourceMusic,
		Metadata:   video.AudioMetadata{Title: "Benchmark title", Artist: "Benchmark artist", Album: "Benchmark album"},
		Analysis:   analysis,
	}
	if lane == "cpu" {
		probe, err := runCPU(ctx, ffmpeg, ffprobe, input, root, run, result.Stages)
		result.WallSeconds = time.Since(started).Seconds()
		if err != nil {
			return result, err
		}
		result.Frames = probe.Frames
		result.Duration = probe.Duration
		return result, nil
	}
	probe, err := runGPU(ctx, ffmpeg, ffprobe, sidecar, input, root, run, result.Stages)
	result.WallSeconds = time.Since(started).Seconds()
	if err != nil {
		return result, err
	}
	result.Frames = probe.Frames
	result.Duration = probe.Duration
	return result, nil
}

func runCPU(ctx context.Context, ffmpeg, ffprobe string, input video.AudioRenderInput, root string, run int, stages map[string]float64) (artifactProbe, error) {
	out := filepath.Join(root, fmt.Sprintf("cpu-%02d", run))
	if err := os.MkdirAll(out, 0o700); err != nil {
		return artifactProbe{}, err
	}
	defer os.RemoveAll(out)
	_, _, mode := benchDimensions()
	preset := video.ResolveQuality(mode, 100)
	id := fmt.Sprintf("bench-cpu-%02d", run)
	renderStarted := time.Now()
	if err := video.RunAudioVisualizerHLSCPUReference(ctx, out, ffmpeg, input, id, preset); err != nil {
		stages["render"] = time.Since(renderStarted).Seconds()
		return artifactProbe{}, fmt.Errorf("CPU production render: %w", err)
	}
	stages["render"] = time.Since(renderStarted).Seconds()
	playlists, err := filepath.Glob(filepath.Join(out, "*.m3u8"))
	if err != nil || len(playlists) != 1 {
		return artifactProbe{}, fmt.Errorf("CPU production render produced %d playlists", len(playlists))
	}
	probeStarted := time.Now()
	probe, err := probeVideo(ctx, ffprobe, playlists[0])
	stages["ffprobe"] = time.Since(probeStarted).Seconds()
	return probe, err
}

func runGPU(ctx context.Context, ffmpeg, ffprobe, sidecar string, input video.AudioRenderInput, root string, run int, stages map[string]float64) (artifactProbe, error) {
	out := filepath.Join(root, fmt.Sprintf("gpu-%02d", run))
	if err := os.MkdirAll(out, 0o700); err != nil {
		return artifactProbe{}, err
	}
	defer os.RemoveAll(out)
	width, height, _ := benchDimensions()
	if err := os.Setenv("IMAGEPAD_PLAYLIST_SURFACE_HEIGHT", strconv.Itoa(height)); err != nil {
		return artifactProbe{}, err
	}
	workerStarted := time.Now()
	worker, err := video.StartPlaylistGPUWorker(ctx, sidecar, fmt.Sprintf("benchmark-%02d", run))
	stages["sidecar_start_and_hello"] = time.Since(workerStarted).Seconds()
	if err != nil {
		return artifactProbe{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = worker.Close()
		}
	}()
	prepareStarted := time.Now()
	assets, timeline, err := worker.PrepareCompiledTrack(ctx, 1, input, fmt.Sprintf("bench-track-%02d", run))
	stages["asset_timeline_prepare"] = time.Since(prepareStarted).Seconds()
	if err != nil {
		return artifactProbe{}, fmt.Errorf("GPU asset/timeline preparation: %w", err)
	}
	frames := make([]video.EncodedH264Frame, 0, timeline.FrameCount)
	renderStarted := time.Now()
	if err := worker.RenderPreparedTimeline(ctx, 1, assets, timeline, func(frame video.EncodedH264Frame) error {
		frames = append(frames, frame)
		return nil
	}); err != nil {
		stages["gpu_bounded_render"] = time.Since(renderStarted).Seconds()
		return artifactProbe{}, fmt.Errorf("GPU bounded render: %w", err)
	}
	stages["gpu_bounded_render"] = time.Since(renderStarted).Seconds()
	if uint64(len(frames)) != timeline.FrameCount {
		return artifactProbe{}, fmt.Errorf("GPU frame count=%d want=%d", len(frames), timeline.FrameCount)
	}
	muxStarted := time.Now()
	mux, err := obsrtmp.StartPlaylistGPUMuxProcess(ffmpeg, width, height, 30, 48_000, 2, frames[0].PTSNs)
	if err != nil {
		stages["mpegts_mux"] = time.Since(muxStarted).Seconds()
		return artifactProbe{}, err
	}
	var ts bytes.Buffer
	outputDone := make(chan error, 1)
	go func() { outputDone <- obsrtmp.CopyPlaylistGPUMuxOutput(&ts, mux) }()
	for _, frame := range frames {
		if err := mux.WriteVideoFrame(frame); err != nil {
			_ = mux.Close()
			stages["mpegts_mux"] = time.Since(muxStarted).Seconds()
			return artifactProbe{}, err
		}
	}
	pcm := make([]byte, len(input.Analysis.PCMInterleavedS16)*2)
	for i, sample := range input.Analysis.PCMInterleavedS16 {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	if len(pcm) == 0 {
		_ = mux.Close()
		stages["mpegts_mux"] = time.Since(muxStarted).Seconds()
		return artifactProbe{}, errors.New("GPU benchmark analysis returned no PCM")
	}
	if err := mux.WriteAudioPCM(pcm, 2); err != nil {
		_ = mux.Close()
		stages["mpegts_mux"] = time.Since(muxStarted).Seconds()
		return artifactProbe{}, err
	}
	if err := mux.CloseInputs(); err != nil {
		stages["mpegts_mux"] = time.Since(muxStarted).Seconds()
		return artifactProbe{}, err
	}
	if err := mux.Wait(); err != nil {
		stages["mpegts_mux"] = time.Since(muxStarted).Seconds()
		return artifactProbe{}, fmt.Errorf("GPU mux wait: %w", err)
	}
	if err := <-outputDone; err != nil {
		stages["mpegts_mux"] = time.Since(muxStarted).Seconds()
		return artifactProbe{}, err
	}
	stages["mpegts_mux"] = time.Since(muxStarted).Seconds()
	if ts.Len() == 0 {
		return artifactProbe{}, errors.New("GPU mux produced empty MPEG-TS")
	}
	path := filepath.Join(out, "benchmark.ts")
	if err := os.WriteFile(path, ts.Bytes(), 0o600); err != nil {
		return artifactProbe{}, err
	}
	probeStarted := time.Now()
	if report, err := video.ProbePlaylistGPUArtifact(ctx, ffprobe, path, 30, timeline.FrameCount); err != nil {
		stages["ffprobe"] = time.Since(probeStarted).Seconds()
		return artifactProbe{}, err
	} else {
		stages["ffprobe"] = time.Since(probeStarted).Seconds()
		if report.VideoFrames != timeline.FrameCount {
			return artifactProbe{}, fmt.Errorf("GPU artifact frames=%d want=%d", report.VideoFrames, timeline.FrameCount)
		}
		if err := worker.Close(); err != nil {
			return artifactProbe{}, fmt.Errorf("GPU worker close: %w", err)
		}
		closed = true
		return artifactProbe{Frames: report.VideoFrames, Duration: report.VideoDurationSeconds}, nil
	}
}

func probeVideo(ctx context.Context, ffprobe, path string) (artifactProbe, error) {
	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error", "-count_frames",
		"-show_entries", "stream=codec_type,nb_read_frames,duration:format=duration",
		"-of", "json", path,
	)
	stdout, stderr, err := separateOutput(cmd)
	if err != nil {
		return artifactProbe{}, fmt.Errorf("ffprobe: %w: %s", err, stderr)
	}
	var document struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Frames    string `json:"nb_read_frames"`
			Duration  string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(stdout, &document); err != nil {
		return artifactProbe{}, err
	}
	for _, stream := range document.Streams {
		if stream.CodecType != "video" {
			continue
		}
		frames, err := strconv.ParseUint(stream.Frames, 10, 64)
		if err != nil {
			return artifactProbe{}, err
		}
		durationText := stream.Duration
		if durationText == "" {
			durationText = document.Format.Duration
		}
		duration, err := strconv.ParseFloat(durationText, 64)
		if err != nil {
			return artifactProbe{}, err
		}
		return artifactProbe{Frames: frames, Duration: duration}, nil
	}
	return artifactProbe{}, errors.New("ffprobe found no video stream")
}

func separateOutput(cmd *exec.Cmd) ([]byte, []byte, error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	stdout, stdoutErr := io.ReadAll(stdoutPipe)
	stderr, stderrErr := io.ReadAll(stderrPipe)
	waitErr := cmd.Wait()
	if stdoutErr != nil {
		return stdout, stderr, stdoutErr
	}
	if stderrErr != nil {
		return stdout, stderr, stderrErr
	}
	return stdout, stderr, waitErr
}

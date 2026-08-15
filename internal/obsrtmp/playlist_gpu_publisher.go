package obsrtmp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"imagepadserver/internal/video"
)

// playlistGPUEvaluationPublisherArgs is deliberately separate from
// video.RadioPublisherArgs. The latter is the normal CPU production publisher
// contract and must not change when this evaluation path is tuned.
//
// This profile is only selected for an explicit playlist GPU session. It is
// fail-closed on missing video/audio streams and asks FFmpeg to emit live FLV
// packets while the MPEG-TS stdin remains open. These options are an
// evaluation candidate, not production readiness evidence.
func playlistGPUEvaluationPublisherArgs(publishURL string) []string {
	logLevel := "warning"
	if strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_PUBLISHER_DEBUG_LEVEL")) == "debug" {
		logLevel = "debug"
	}
	return []string{
		"-hide_banner",
		"-loglevel", logLevel,
		"-nostdin",
		// Keep the input demuxer buffered until PAT/PMT and the H.264
		// parameter sets have been observed. The previous +nobuffer/direct
		// combination made the live pipe decoder consume early PES payloads
		// before SPS/PPS was available, even though the same MPEG-TS bytes
		// decoded successfully after EOF.
		"-fflags", "+genpts",
		"-thread_queue_size", "512",
		"-probesize", "64k",
		"-analyzeduration", "500000",
		"-fpsprobesize", "2",
		"-f", "mpegts",
		"-i", "pipe:0",
		"-map", "0:v:0",
		"-map", "0:a:0",
		"-c:v", "copy",
		"-c:a", "copy",
		// A zero delta makes FFmpeg wait for an unbounded cross-stream
		// interleave window when the MPEG-TS stdin remains open. The GPU
		// evaluation publisher must emit FLV packets before EOF so MediaMTX
		// can promote the RTMP path to published/online.
		"-max_interleave_delta", "100000",
		"-flush_packets", "1",
		// Apply direct I/O only to the FLV network output. Placing this
		// before the MPEG-TS input changes live probing and can consume H.264
		// PES payloads before SPS/PPS has been registered.
		"-avioflags", "direct",
		"-flvflags", "no_duration_filesize",
		"-f", "flv",
		publishURL,
	}
}

// newPlaylistGPUEvaluationPublisherContext deliberately does not inherit the
// RadioManager session context. The GPU evaluation publisher owns an ordered
// EOS sequence: session cancellation stops new program work, then
// ffmpegPublisher.close closes stdin and waits for the muxer to flush. The
// publisher context is cancelled only by that explicit close path or by its
// timeout fallback. The normal CPU publisher keeps its existing
// CommandContext(sessionCtx) behavior.
func newPlaylistGPUEvaluationPublisherContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func (m *RadioManager) startFFmpegPlaylistGPUPublisher(ctx context.Context, publishURL string) (radioPublisher, error) {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return nil, err
	}
	args := playlistGPUEvaluationPublisherArgs(publishURL)
	tracePath := playlistGPUTracePathFromEnv()
	publisherCtx, cancelPublisher := newPlaylistGPUEvaluationPublisherContext()
	cmd := exec.CommandContext(publisherCtx, ffmpeg, args...)
	hideWindow(cmd)
	cmd.Dir = m.outDir
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancelPublisher()
		return nil, err
	}
	_ = recordPlaylistGPUTrace(tracePath, "gpu_publisher_started", map[string]any{
		"pid": cmd.Process.Pid,
	})
	if path := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_PUBLISHER_DEBUG")); path != "" {
		message := fmt.Sprintf("started pid=%d\npublisherProfile=%s\npublishURL=%s\nargs=%q\n", cmd.Process.Pid, RadioPublisherProfilePlaylistGPUEvaluation, sanitizeRadioErrorMessage(publishURL), sanitizeRadioArgs(args))
		_ = os.WriteFile(path, []byte(message), 0600)
	}
	untrack := video.TrackStartedFFmpeg(cmd)
	exit := make(chan error, 1)
	go func() {
		defer untrack()
		err := cmd.Wait()
		if err != nil {
			detail := strings.TrimSpace(stderr.String())
			if len(detail) > 800 {
				detail = detail[len(detail)-800:]
			}
			if detail != "" {
				err = fmt.Errorf("playlist GPU publisher FFmpeg: %w: %s", err, detail)
			}
		}
		_ = recordPlaylistGPUTrace(tracePath, "gpu_publisher_exit", map[string]any{
			"error": errorString(err),
		})
		if path := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_PUBLISHER_DEBUG")); path != "" {
			message := fmt.Sprintf("publisherProfile=%s\npublishURL=%s\nexit=%v\nstderr=%s\n", RadioPublisherProfilePlaylistGPUEvaluation, sanitizeRadioErrorMessage(publishURL), err, stderr.String())
			f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
			if openErr == nil {
				_, _ = f.WriteString(message)
				_ = f.Close()
			}
		}
		exit <- err
		close(exit)
	}()
	return &ffmpegPublisher{cmd: cmd, in: stdin, exit: exit, stderr: stderr, tracePath: tracePath, cancel: cancelPublisher}, nil
}

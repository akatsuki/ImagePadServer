package airplay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

const (
	envAirPlayPipeline            = "IMAGEPAD_AIRPLAY_PIPELINE"
	envAirPlayGStreamerBridgePath = "IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE"
)

var errDirectGStreamerProcessExitUnconfirmed = errors.New("direct GStreamer process exit was not confirmed")

func gstreamerPipelineEnabled() bool {
	return CurrentPipelineMode() == PipelineGStreamerDirect
}

// DirectPipelineEnabled selects the native GStreamer publisher that sends
// RTSP/TCP straight to the app-owned MediaMTX sidecar.
func DirectPipelineEnabled() bool {
	mode := CurrentPipelineMode()
	return mode == PipelineGStreamerDirect || mode == PipelineSourceClock
}

// ResolveGStreamerBridgePath resolves only an existing executable. Automatic
// provisioning is intentionally kept outside this function so a missing
// native runtime cannot silently fall back to a partially configured process.
func ResolveGStreamerBridgePath() (string, error) {
	if raw := strings.TrimSpace(os.Getenv(envAirPlayGStreamerBridgePath)); raw != "" {
		return resolveExecutable(raw)
	}
	if runtimeSet, err := ResolvePinnedAirPlayRuntime(); err == nil {
		return runtimeSet.BridgePath, nil
	}
	for _, name := range []string{"airplay-gstreamer-bridge", "airplay-gstreamer-bridge.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	for _, dir := range bundledGStreamerBridgeDirectories() {
		for _, name := range []string{"airplay-gstreamer-bridge.exe", "airplay-gstreamer-bridge"} {
			path := filepath.Join(dir, name)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return filepath.Abs(path)
			}
		}
	}
	return "", errors.New("AirPlay GStreamer bridge was not found; set IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE")
}

func bundledGStreamerBridgeDirectories() []string {
	directories := make([]string, 0, 4)
	if executable, err := os.Executable(); err == nil {
		if absolute, absErr := filepath.Abs(executable); absErr == nil {
			base := filepath.Dir(absolute)
			directories = append(directories, base, filepath.Join(base, "airplay-gstreamer"), filepath.Join(base, "airplay", "gstreamer"), filepath.Join(base, "modules", "airplay-gstreamer"))
		}
	}
	if dataDir := settings.Dir(); dataDir != "" {
		directories = append(directories, filepath.Join(dataDir, "modules", "airplay-gstreamer", "1.26.11"))
	}
	return directories
}

type gstreamerBridgeProcess struct {
	decoder    *exec.Cmd
	encoder    *exec.Cmd
	decoderLog *limitedBuffer
	encoderLog *limitedBuffer
	untrack    func()
}

type gstreamerDirectProcess struct {
	process           *exec.Cmd
	forceCancel       context.CancelFunc
	log               *limitedBuffer
	logFile           *os.File
	stopFile          string
	restartPath       string
	restartArgs       []string
	gracefulLifecycle bool
}

type directPublisherStopReason string

const (
	directPublisherStopNormal   directPublisherStopReason = "stop"
	directPublisherStopNoSignal directPublisherStopReason = "no-signal"
)

func directPublisherLogPath(stopFile string) string {
	if strings.TrimSpace(stopFile) == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(stopFile), ".stop") {
		return strings.TrimSuffix(stopFile, filepath.Ext(stopFile)) + ".log"
	}
	return filepath.Join(filepath.Dir(stopFile), "gstreamer-direct.log")
}

// startGStreamerDirectProcess starts the native GStreamer publisher as the
// only AirPlay media child. Its stdout is intentionally unused; media travels
// over RTSP/TCP directly to MediaMTX and stderr remains diagnostic text.
func startGStreamerDirectProcess(ctx context.Context, bridgePath string, bridgeArgs []string, stopFile string) (*gstreamerDirectProcess, error) {
	return startGStreamerDirectProcessWithLifecycle(ctx, bridgePath, bridgeArgs, stopFile, false)
}

func startSourceClockGStreamerProcess(ctx context.Context, bridgePath string, bridgeArgs []string, stopFile string) (*gstreamerDirectProcess, error) {
	return startGStreamerDirectProcessWithLifecycle(ctx, bridgePath, bridgeArgs, stopFile, true)
}

func startGStreamerDirectProcessWithLifecycle(ctx context.Context, bridgePath string, bridgeArgs []string, stopFile string, gracefulLifecycle bool) (*gstreamerDirectProcess, error) {
	if strings.TrimSpace(bridgePath) == "" {
		return nil, errors.New("GStreamer bridge path is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	output := &limitedBuffer{max: 8192}
	processParent := ctx
	if gracefulLifecycle {
		processParent = context.WithoutCancel(ctx)
	}
	processContext, forceCancel := context.WithCancel(processParent)
	process := exec.CommandContext(processContext, bridgePath, bridgeArgs...)
	configureGStreamerBridgeProcess(process, output, bridgePath)
	var logFile *os.File
	if logPath := directPublisherLogPath(stopFile); logPath != "" {
		if file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
			logFile = file
			process.Stderr = io.MultiWriter(output, file)
		}
	}
	if err := process.Start(); err != nil {
		forceCancel()
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("start direct GStreamer publisher: %w", err)
	}
	return &gstreamerDirectProcess{
		process:           process,
		forceCancel:       forceCancel,
		log:               output,
		logFile:           logFile,
		stopFile:          stopFile,
		restartPath:       bridgePath,
		restartArgs:       append([]string(nil), bridgeArgs...),
		gracefulLifecycle: gracefulLifecycle,
	}, nil
}

func (p *gstreamerDirectProcess) restart(ctx context.Context) (*gstreamerDirectProcess, error) {
	if p == nil || strings.TrimSpace(p.restartPath) == "" || len(p.restartArgs) == 0 {
		return nil, errors.New("direct GStreamer restart configuration is incomplete")
	}
	return startGStreamerDirectProcessWithLifecycle(
		ctx, p.restartPath, append([]string(nil), p.restartArgs...), p.stopFile,
		p.gracefulLifecycle)
}

func (p *gstreamerDirectProcess) processID() int {
	if p == nil || p.process == nil || p.process.Process == nil {
		return 0
	}
	return p.process.Process.Pid
}

func (p *gstreamerDirectProcess) stop() {
	p.requestStop(directPublisherStopNormal)
}

func (p *gstreamerDirectProcess) requestStop(reason directPublisherStopReason) {
	if p == nil || p.process == nil || p.process.Process == nil {
		return
	}
	if strings.TrimSpace(p.stopFile) != "" {
		value := strings.TrimSpace(string(reason))
		if value == "" {
			value = string(directPublisherStopNormal)
		}
		if err := writeDirectPublisherStopRequest(p.stopFile, value+"\n"); err == nil {
			return
		}
	}
	p.forceStop()
}

func writeDirectPublisherStopRequest(path, value string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.WriteString(value); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (p *gstreamerDirectProcess) forceStop() {
	if p == nil || p.process == nil || p.process.Process == nil {
		return
	}
	if p.forceCancel != nil {
		p.forceCancel()
	}
	_ = p.process.Process.Kill()
}

func (p *gstreamerDirectProcess) wait() error {
	if p == nil || p.process == nil {
		return errors.New("direct GStreamer process is incomplete")
	}
	err := p.process.Wait()
	if p.forceCancel != nil {
		p.forceCancel()
	}
	if p.logFile != nil {
		_ = p.logFile.Close()
	}
	return err
}

func (p *gstreamerDirectProcess) stopAndWait(timeout time.Duration) error {
	if p == nil || p.process == nil {
		return errors.New("direct GStreamer process is incomplete")
	}
	done := make(chan error, 1)
	go func() { done <- p.wait() }()
	return p.stopAndWaitDone(done, directPublisherStopNormal, timeout)
}

func (p *gstreamerDirectProcess) stopAndWaitDone(done <-chan error, reason directPublisherStopReason, timeout time.Duration) error {
	if p == nil || p.process == nil || done == nil {
		return errors.New("direct GStreamer process is incomplete")
	}
	p.requestStop(reason)
	if timeout <= 0 {
		return <-done
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		p.forceStop()
	}
	killDeadline := time.NewTimer(2 * time.Second)
	defer killDeadline.Stop()
	select {
	case err := <-done:
		return err
	case <-killDeadline.C:
		return errDirectGStreamerProcessExitUnconfirmed
	}
}

// stopAndWaitDoneWithin is the planned-generation exchange stop primitive.
// Graceful request, forced termination, and the final Wait confirmation all
// share one deadline so a failed stop cannot silently extend the fixed-port
// handoff window.
func (p *gstreamerDirectProcess) stopAndWaitDoneWithin(done <-chan error, reason directPublisherStopReason, timeout time.Duration) error {
	if p == nil || p.process == nil || done == nil {
		return errors.New("direct GStreamer process is incomplete")
	}
	if timeout <= 0 {
		return <-done
	}
	started := time.Now()
	p.requestStop(reason)
	killBudget := timeout / 5
	if killBudget <= 0 {
		killBudget = time.Nanosecond
	}
	if killBudget > 2*time.Second {
		killBudget = 2 * time.Second
	}
	if killBudget > timeout {
		killBudget = timeout
	}
	gracefulBudget := timeout - killBudget
	remaining := func() time.Duration {
		left := timeout - time.Since(started)
		if left < 0 {
			return 0
		}
		return left
	}
	wait := func(limit time.Duration) (error, bool) {
		if limit <= 0 {
			return nil, false
		}
		timer := time.NewTimer(limit)
		defer timer.Stop()
		select {
		case err := <-done:
			return err, true
		case <-timer.C:
			return nil, false
		}
	}
	checkDone := func() (error, bool) {
		select {
		case err := <-done:
			return err, true
		default:
			return nil, false
		}
	}
	gracefulWait := gracefulBudget - time.Since(started)
	if gracefulWait > 0 {
		if err, ok := wait(gracefulWait); ok {
			return err
		}
	}
	// The graceful deadline is absolute and includes writing the stop request.
	// Once it expires, enter forced termination before consuming a late result
	// so the reserved confirmation window cannot be lost to a slow filesystem.
	p.forceStop()
	killWait := killBudget
	if left := remaining(); left < killWait {
		killWait = left
	}
	if err, ok := wait(killWait); ok {
		return err
	}
	if err, ok := checkDone(); ok {
		return err
	}
	return errDirectGStreamerProcessExitUnconfirmed
}

// startGStreamerBridgeProcess connects the native bridge's binary stdout to
// FFmpeg stdin. The two child processes remain separate so diagnostics and
// failure reasons can be attributed without contaminating the media stream.
func startGStreamerBridgeProcess(ctx context.Context, bridgePath string, bridgeArgs []string, ffmpegPath string, ffmpegArgs []string) (*gstreamerBridgeProcess, error) {
	if strings.TrimSpace(bridgePath) == "" {
		return nil, errors.New("GStreamer bridge path is empty")
	}
	if strings.TrimSpace(ffmpegPath) == "" {
		return nil, errors.New("FFmpeg path is empty")
	}
	decoderLog := &limitedBuffer{max: 8192}
	encoderLog := &limitedBuffer{max: 8192}
	decoder := exec.CommandContext(ctx, bridgePath, bridgeArgs...)
	decoderStdout, err := decoder.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create GStreamer bridge stdout pipe: %w", err)
	}
	configureGStreamerBridgeProcess(decoder, decoderLog, bridgePath)

	encoder := exec.CommandContext(ctx, ffmpegPath, ffmpegArgs...)
	encoder.Stdin = decoderStdout
	configureProcess(encoder, encoderLog)

	// Start the consumer first so the bridge can never block on a pipe whose
	// reader has not been created yet.
	if err := encoder.Start(); err != nil {
		_ = decoderStdout.Close()
		return nil, fmt.Errorf("start FFmpeg for GStreamer bridge: %w", err)
	}
	if err := decoder.Start(); err != nil {
		_ = encoder.Process.Kill()
		_ = encoder.Wait()
		return nil, fmt.Errorf("start GStreamer bridge: %w", err)
	}
	untrack, err := video.TrackStartedFFmpeg(encoder)
	if err != nil {
		_ = decoder.Process.Kill()
		_ = encoder.Process.Kill()
		_ = decoder.Wait()
		_ = encoder.Wait()
		return nil, errors.Join(err, fmt.Errorf("track GStreamer FFmpeg process"))
	}
	return &gstreamerBridgeProcess{
		decoder:    decoder,
		encoder:    encoder,
		decoderLog: decoderLog,
		encoderLog: encoderLog,
		untrack:    untrack,
	}, nil
}

func (p *gstreamerBridgeProcess) stop() {
	if p == nil {
		return
	}
	if p.decoder != nil && p.decoder.Process != nil {
		_ = p.decoder.Process.Kill()
	}
	if p.encoder != nil && p.encoder.Process != nil {
		_ = p.encoder.Process.Kill()
	}
}

func (p *gstreamerBridgeProcess) wait() (string, error) {
	if p == nil || p.decoder == nil || p.encoder == nil {
		return "", errors.New("GStreamer bridge process is incomplete")
	}
	decoderDone := make(chan error, 1)
	encoderDone := make(chan error, 1)
	go func() { decoderDone <- p.decoder.Wait() }()
	go func() { encoderDone <- p.encoder.Wait() }()
	select {
	case err := <-decoderDone:
		return "GStreamer bridge", err
	case err := <-encoderDone:
		if p.decoder.Process != nil {
			_ = p.decoder.Process.Kill()
		}
		return "FFmpeg bridge", err
	}
}

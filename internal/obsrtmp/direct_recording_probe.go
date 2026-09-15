package obsrtmp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

// directRecordingProbeFunc must return promptly after its context is canceled.
// The production implementation enforces this with CommandContext and WaitDelay.
type directRecordingProbeFunc func(context.Context, string) (video.MediaProbe, error)
type directRecordingProbeCommandFunc func(context.Context, string, ...string) *exec.Cmd

var errDirectRecordingProbeUnavailable = errors.New("direct recording probe is unavailable")
var errDirectRecordingProbeOutputTooLarge = errors.New("direct recording probe output is too large")

const (
	directRecordingProbeOutputLimit = 4 << 20
	directRecordingProbeErrorLimit  = 64 << 10
	directRecordingProbeWaitDelay   = 250 * time.Millisecond
	directRecordingProbeTimeout     = 5 * time.Second
)

type directRecordingProbeBuffer struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (b *directRecordingProbeBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := b.limit - b.data.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = b.data.Write(data[:remaining])
	}
	if remaining < len(data) {
		b.overflow = true
	}
	return written, nil
}

func (b *directRecordingProbeBuffer) Bytes() []byte {
	return b.data.Bytes()
}

func (b *directRecordingProbeBuffer) String() string {
	return b.data.String()
}

func probeDirectRecording(parent context.Context, timeout time.Duration, artifacts airplaycontract.PublisherArtifacts, finalized airplaycontract.Event, hasRealVideo bool, expectedWidth, expectedHeight int, probeFunc directRecordingProbeFunc) airplaycontract.RecordingOutcome {
	if !hasRealVideo || !finalized.RecordingFinalizedFor(artifacts.SessionID, artifacts.Generation, artifacts.Recording) {
		return classifyDirectRecordingOutcome(artifacts, finalized, hasRealVideo, expectedWidth, expectedHeight, video.MediaProbe{}, nil)
	}
	if probeFunc == nil {
		return classifyDirectRecordingOutcome(artifacts, finalized, true, expectedWidth, expectedHeight, video.MediaProbe{}, errDirectRecordingProbeUnavailable)
	}
	if parent == nil {
		parent = context.Background()
	}
	probeContext, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	probe, probeErr := probeFunc(probeContext, artifacts.Recording)
	if contextErr := probeContext.Err(); contextErr != nil {
		probeErr = contextErr
	}
	return classifyDirectRecordingOutcome(artifacts, finalized, true, expectedWidth, expectedHeight, probe, probeErr)
}

func probeDirectRecordingGeneration(parent context.Context, timeout time.Duration, descriptor airplaycontract.DeliveryGeneration, finalized airplaycontract.Event, hasRealVideo bool, probeFunc directRecordingProbeFunc) airplaycontract.RecordingOutcome {
	return probeDirectRecording(
		parent,
		timeout,
		descriptor.ArtifactPaths,
		finalized,
		hasRealVideo,
		descriptor.ExpectedRecordingWidth,
		descriptor.ExpectedRecordingHeight,
		probeFunc,
	)
}

func runDirectRecordingFFprobe(ctx context.Context, ffprobe, path string) (video.MediaProbe, error) {
	return runDirectRecordingFFprobeWithCommand(ctx, ffprobe, path, exec.CommandContext)
}

func configureDirectRecordingProbeForSession(observer *directPublisherObserver, output DirectOutputSettings, ffprobe string, resolveErr error) error {
	if observer == nil {
		return errors.New("direct publisher observer is nil")
	}
	ffprobe = strings.TrimSpace(ffprobe)
	if resolveErr == nil && ffprobe == "" {
		resolveErr = errors.New("ffprobe path is empty")
	}
	probe := func(ctx context.Context, path string) (video.MediaProbe, error) {
		if resolveErr != nil {
			return video.MediaProbe{}, fmt.Errorf("%w: %v", errDirectRecordingProbeUnavailable, resolveErr)
		}
		return runDirectRecordingFFprobe(ctx, ffprobe, path)
	}
	return observer.configureRecordingProbe(directRecordingProbeTimeout, output.Width, output.Height, probe)
}

func runDirectRecordingFFprobeWithCommand(ctx context.Context, ffprobe, path string, command directRecordingProbeCommandFunc) (video.MediaProbe, error) {
	ffprobe = strings.TrimSpace(ffprobe)
	path = strings.TrimSpace(path)
	if ffprobe == "" || path == "" {
		return video.MediaProbe{}, errors.New("direct recording probe requires ffprobe and recording path")
	}
	if command == nil {
		return video.MediaProbe{}, errDirectRecordingProbeUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := command(
		ctx,
		ffprobe,
		"-v", "error",
		"-show_streams",
		"-show_format",
		"-of", "json",
		path,
	)
	if cmd == nil {
		return video.MediaProbe{}, errDirectRecordingProbeUnavailable
	}
	stdout := &directRecordingProbeBuffer{limit: directRecordingProbeOutputLimit}
	stderr := &directRecordingProbeBuffer{limit: directRecordingProbeErrorLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = directRecordingProbeWaitDelay
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return video.MediaProbe{}, contextErr
		}
		return video.MediaProbe{}, fmt.Errorf("direct recording ffprobe failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow {
		return video.MediaProbe{}, errDirectRecordingProbeOutputTooLarge
	}
	return video.ParseMediaProbeJSON(stdout.Bytes())
}

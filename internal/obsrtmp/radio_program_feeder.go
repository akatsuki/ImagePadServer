package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/video"
)

type ProgramTrackFeeder struct {
	outDir       string
	width        int
	height       int
	ensureFFmpeg func() (string, error)
	command      func(ctx context.Context, name string, args ...string) *exec.Cmd
	started      func(cmd *exec.Cmd) func()
}

func NewProgramTrackFeeder(outDir string, width, height int) *ProgramTrackFeeder {
	return &ProgramTrackFeeder{
		outDir:       outDir,
		width:        width,
		height:       height,
		ensureFFmpeg: video.EnsureFFmpeg,
		command:      exec.CommandContext,
		started:      video.TrackStartedFFmpeg,
	}
}

func (m *RadioManager) runFFmpegProgramFeeder(ctx context.Context, mediaPath string, startSeconds, width, height int, frames chan<- ProgramSourceFrame) error {
	return NewProgramTrackFeeder(m.outDir, width, height).Run(ctx, mediaPath, startSeconds, frames)
}

func (f *ProgramTrackFeeder) Run(ctx context.Context, mediaPath string, startSeconds int, frames chan<- ProgramSourceFrame) error {
	ffmpeg, err := f.ensureFFmpeg()
	if err != nil {
		return err
	}
	if f.width <= 0 || f.height <= 0 {
		return errors.New("program decoder dimensions are invalid")
	}
	videoCtx, cancelVideo := context.WithCancel(ctx)
	audioCtx, cancelAudio := context.WithCancel(ctx)
	defer cancelVideo()
	defer cancelAudio()

	videoCmd := f.command(videoCtx, ffmpeg, programVideoDecoderArgs(mediaPath, startSeconds, f.width, f.height)...)
	audioCmd := f.command(audioCtx, ffmpeg, programAudioDecoderArgs(mediaPath, startSeconds)...)
	hideWindow(videoCmd)
	hideWindow(audioCmd)
	videoCmd.Dir = f.outDir
	audioCmd.Dir = f.outDir
	var videoStderr, audioStderr synchronizedBuffer
	videoCmd.Stderr = &videoStderr
	audioCmd.Stderr = &audioStderr
	videoOut, videoPipe, err := os.Pipe()
	if err != nil {
		return err
	}
	defer videoOut.Close()
	defer videoPipe.Close()
	videoCmd.Stdout = videoPipe
	audioOut, audioPipe, err := os.Pipe()
	if err != nil {
		return err
	}
	defer audioOut.Close()
	defer audioPipe.Close()
	audioCmd.Stdout = audioPipe
	if err := videoCmd.Start(); err != nil {
		return err
	}
	if err := videoPipe.Close(); err != nil {
		cancelVideo()
		_ = videoCmd.Wait()
		return err
	}
	untrackVideo := f.started(videoCmd)
	if err := audioCmd.Start(); err != nil {
		cancelVideo()
		_ = videoCmd.Wait()
		untrackVideo()
		return err
	}
	if err := audioPipe.Close(); err != nil {
		cancelVideo()
		cancelAudio()
		_ = videoCmd.Wait()
		_ = audioCmd.Wait()
		untrackVideo()
		return err
	}
	untrackAudio := f.started(audioCmd)
	decodersWaited := false
	videoWait := make(chan error, 1)
	audioWait := make(chan error, 1)
	go func() { videoWait <- videoCmd.Wait() }()
	go func() { audioWait <- audioCmd.Wait() }()
	defer func() {
		cancelVideo()
		cancelAudio()
		if !decodersWaited {
			<-videoWait
			<-audioWait
		}
		untrackVideo()
		untrackAudio()
	}()
	waitForDecoders := func(ended string) error {
		decodersWaited = true
		var videoErr, audioErr, videoDrainErr, audioDrainErr error
		canceledVideo, canceledAudio := false, false
		if ended == "video" {
			videoErr = <-videoWait
			// The video decoder reached EOF first. Give the audio decoder a
			// short chance to report its own exit so a racing failure is not
			// mistaken for the cancellation used to stop a blocked peer.
			audioErr, canceledAudio = waitForPeerDecoder(audioWait, cancelAudio)
		} else {
			// Symmetric handling for an audio-first EOF.
			audioErr = <-audioWait
			videoErr, canceledVideo = waitForPeerDecoder(videoWait, cancelVideo)
		}
		if ctx.Err() != nil {
			return nil
		}
		var decoderErrs []error
		if videoErr != nil && (!canceledVideo || !isDecoderCancellation(videoErr)) {
			decoderErrs = append(decoderErrs, fmt.Errorf("program video decoder: %w: %s", videoErr, videoStderr.Tail(400)))
		}
		if audioErr != nil && (!canceledAudio || !isDecoderCancellation(audioErr)) {
			decoderErrs = append(decoderErrs, fmt.Errorf("program audio decoder: %w: %s", audioErr, audioStderr.Tail(400)))
		}
		if videoDrainErr != nil {
			decoderErrs = append(decoderErrs, fmt.Errorf("program video decoder: %w: %s", videoDrainErr, videoStderr.Tail(400)))
		}
		if audioDrainErr != nil {
			decoderErrs = append(decoderErrs, fmt.Errorf("program audio decoder: %w: %s", audioDrainErr, audioStderr.Tail(400)))
		}
		return errors.Join(decoderErrs...)
	}

	videoFrameBytes := f.width * f.height * 4
	audioFrameBytes := programSamplesPerFrame * programAudioChannels * programAudioBytes
	for frameIndex := uint64(0); ; frameIndex++ {
		videoFrame := make([]byte, videoFrameBytes)
		if _, err := io.ReadFull(videoOut, videoFrame); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return waitForDecoders("video")
			}
			return fmt.Errorf("program video decoder: %w: %s", err, videoStderr.Tail(400))
		}
		audioFrame := make([]byte, audioFrameBytes)
		audioBytes, err := io.ReadFull(audioOut, audioFrame)
		finalPartialAudioFrame := false
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return waitForDecoders("audio")
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) || audioBytes == 0 {
				return fmt.Errorf("program audio decoder: %w: %s", err, audioStderr.Tail(400))
			}
			finalPartialAudioFrame = true
		}
		videoPTS, audioPTS := programSourcePTS(frameIndex)
		if delta := videoPTS - audioPTS; delta > 100*time.Millisecond || delta < -100*time.Millisecond {
			return fmt.Errorf("program decoder clock divergence: video=%s audio=%s", videoPTS, audioPTS)
		}
		frame := ProgramSourceFrame{
			VideoRGBA:      videoFrame,
			AudioPCM:       audioFrame,
			SourceVideoPTS: videoPTS,
			SourceAudioPTS: audioPTS,
		}
		select {
		case frames <- frame:
		case <-ctx.Done():
			return nil
		}
		if finalPartialAudioFrame {
			return waitForDecoders("audio")
		}
	}
}

func waitForPeerDecoder(wait <-chan error, cancel context.CancelFunc) (error, bool) {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-wait:
		return err, false
	case <-timer.C:
		cancel()
		return <-wait, true
	}
}

func isDecoderCancellation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	// exec.CommandContext reports a process terminated by its context as a
	// killed process. Preserve ordinary non-zero exit statuses even when the
	// peer EOF caused us to cancel the decoder at nearly the same time.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil && exitErr.ProcessState.ExitCode() == 1 {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "signal: killed") || strings.Contains(message, "process terminated")
}

func programSourcePTS(frameIndex uint64) (videoPTS, audioPTS time.Duration) {
	videoPTS = time.Duration(frameIndex) * time.Second / programVideoFrameRate
	audioPTS = time.Duration(frameIndex*programSamplesPerFrame) * time.Second / programAudioSampleRate
	return videoPTS, audioPTS
}

func programVideoDecoderArgs(mediaPath string, startSeconds, width, height int) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}
	if startSeconds > 0 {
		args = append(args, "-ss", strconv.Itoa(startSeconds))
	}
	return append(args,
		"-i", mediaPath,
		"-map", "0:v:0",
		"-vf", fmt.Sprintf("fps=30,scale=%d:%d:flags=bicubic,format=rgba", width, height),
		"-an",
		"-f", "rawvideo",
		"pipe:1",
	)
}

func programAudioDecoderArgs(mediaPath string, startSeconds int) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}
	if startSeconds > 0 {
		args = append(args, "-ss", strconv.Itoa(startSeconds))
	}
	return append(args,
		"-i", mediaPath,
		"-map", "0:a:0",
		"-vn",
		"-ac", "2",
		"-ar", "48000",
		"-f", "s16le",
		"pipe:1",
	)
}

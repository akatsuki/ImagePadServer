package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"strconv"
)

const showwavesPCMSampleRate = 192000
const showwavesPCMSamplesPerTick = showwavesPCMSampleRate / 30
const showwavesPCMLoudnormDelayTicks = 3

// StreamShowwavesPCMHistoryFrames decodes the exact post-loudnorm S16 signal
// consumed by the CPU showwaves filter. It emits a bounded two-tick history
// window; only the GPU turns these samples into pixels.
func StreamShowwavesPCMHistoryFrames(ctx context.Context, ffmpeg string, input AudioRenderInput, waveWidth, frames int, onFrame func(int, []uint16) error) error {
	return StreamWaveformPCMHistoryFrames(ctx, ffmpeg, input, waveWidth, frames, onFrame)
}

// StreamWaveformPCMHistoryFrames transports decoded audio history samples.
// Pixel generation remains exclusively in the GPU compositor.
func StreamWaveformPCMHistoryFrames(ctx context.Context, ffmpeg string, input AudioRenderInput, waveWidth, frames int, onFrame func(int, []uint16) error) error {
	if waveWidth <= 0 || frames <= 0 || onFrame == nil {
		return fmt.Errorf("invalid showwaves PCM stream parameters")
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-i", input.SourcePath}
	if filter := audioLoudnormFilter(input.Kind); filter != "" {
		args = append(args, "-af", filter)
	}
	args = append(args, "-t", strconv.FormatFloat(float64(frames)/30, 'f', 9, 64), "-f", "s16le", "-acodec", "pcm_s16le", "-ar", strconv.Itoa(showwavesPCMSampleRate), "-ac", "2", "pipe:1")
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	const channels = 2
	const tickValues = showwavesPCMSamplesPerTick * channels
	previous := make([]uint16, tickValues)
	for i := range previous {
		previous[i] = 32768
	}
	tickBytes := make([]byte, tickValues*2)
	samplesPerColumn := (showwavesPCMSamplesPerTick + waveWidth - 1) / waveWidth
	historyValues := samplesPerColumn * waveWidth * channels
	if historyValues > MusicMaxWaveformSamples {
		return fmt.Errorf("showwaves PCM history exceeds contract: %d", historyValues)
	}
	// loudnorm's 192 kHz true-peak path carries 19,200 samples (100 ms) of
	// latency. Raw PCM has no timestamps, so retain the three 30 Hz ticks here;
	// otherwise GPU showwaves visibly leads the CPU filter graph.
	var delayed [][]uint16
	audioEOF := false
	for frameIndex := 0; frameIndex < frames; frameIndex++ {
		clear(tickBytes) // zero PCM is centre-biased silence after uint16 conversion
		if !audioEOF {
			_, readErr := io.ReadFull(stdout, tickBytes)
			switch readErr {
			case nil:
			case io.EOF, io.ErrUnexpectedEOF:
				// Video duration is authoritative. FFmpeg can end the decoded audio
				// a fractional sample before an exact 30 Hz boundary, especially
				// after loudnorm latency. Preserve the partial tick and pad the rest
				// with silence instead of dropping the final video frame.
				audioEOF = true
			default:
				return fmt.Errorf("showwaves PCM frame %d: %w", frameIndex, readErr)
			}
		}
		current := make([]uint16, tickValues)
		for i := range current {
			current[i] = uint16(int32(int16(binary.LittleEndian.Uint16(tickBytes[i*2:]))) + 32768)
		}
		history := make([]uint16, historyValues)
		copied := copy(history, previous)
		copy(history[copied:], current)
		delayed = append(delayed, history)
		output := delayed[0]
		if frameIndex >= showwavesPCMLoudnormDelayTicks {
			delayed = delayed[1:]
		}
		if err := onFrame(frameIndex, output); err != nil {
			return err
		}
		previous = current
	}
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("showwaves PCM process: %w: %s", err, trimOutput(stderr.Bytes()))
	}
	return ctx.Err()
}

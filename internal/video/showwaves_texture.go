package video

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
)

// StreamShowwavesFrames runs the same FFmpeg showwaves branch as the CPU
// reference and yields one transparent RGBA waveform tile per 30 Hz frame.
// The callback owns the byte slice until it returns; the next frame reuses no
// storage so callers may upload it asynchronously after copying.
func StreamAudioWaveFrames(ctx context.Context, ffmpeg, audioPath string, width, height, frames int, color string, audioFilter string, onFrame func(index int, rgba []byte) error) error {
	if width <= 0 || height <= 0 || frames < 1 || onFrame == nil {
		return fmt.Errorf("invalid showwaves stream parameters")
	}
	if color == "" {
		color = "#FFFFFF@0.55"
	}
	wavePrefix := "[0:a]"
	if audioFilter != "" {
		wavePrefix += audioFilter + ","
	}
	wave := fmt.Sprintf("%sshowwaves=s=%dx%d:rate=30:mode=line:colors=%s,format=rgba[out]", wavePrefix, width, height, color)
	args := []string{"-hide_banner", "-loglevel", "error", "-i", audioPath}
	args = append(args, "-filter_complex", wave, "-map", "[out]", "-frames:v", strconv.Itoa(frames), "-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	frameBytes := width * height * 4
	for i := 0; i < frames; i++ {
		buf := make([]byte, frameBytes)
		if _, err := io.ReadFull(out, buf); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("showwaves frame %d: %w", i, err)
		}
		if err := onFrame(i, buf); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("showwaves process: %w", err)
	}
	return nil
}

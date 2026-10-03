package xpostcodec

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"imagepadserver/internal/xpostvideo"
)

const pcmChunkSamples = 32 * 1024

func mixAudio(ctx context.Context, ffmpeg, ffprobe, output string, plan xpostvideo.Plan) error {
	if plan.Options.FPS < 1 || plan.TotalFrames < 1 {
		return errors.New("invalid audio clock")
	}
	totalSamples := int64(plan.TotalFrames) * pcmRate * pcmChannels / int64(plan.Options.FPS)
	if totalSamples < 1 || totalSamples > int64(math.MaxInt64/4) {
		return errors.New("audio output length exceeds supported bounds")
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("create audio mix: %w", err)
	}
	defer f.Close()
	if err := f.Truncate(totalSamples * 4); err != nil {
		return err
	}
	ranges := make([]sampleRange, 0, len(plan.Audio))
	for _, span := range plan.Audio {
		if span.Path == "" || span.Frames <= 0 {
			continue
		}
		clipStart := int64(span.StartFrame) * pcmRate * pcmChannels / int64(plan.Options.FPS)
		clipEnd := clipStart + int64(span.Frames)*pcmRate*pcmChannels/int64(plan.Options.FPS)
		startSamples := clipStart
		skipSamples := int64(0)
		if span.Media {
			offset, err := mediaAudioOffset(ctx, ffprobe, span.Path)
			if err != nil {
				return fmt.Errorf("probe source audio timing: %w", err)
			}
			offsetSamples := int64(math.Round(offset*pcmRate)) * pcmChannels
			if offsetSamples < 0 {
				// Audio that begins before its video's first frame must not leak
				// into preceding timeline content. Start mixing at the video
				// boundary and discard the corresponding source audio prefix.
				skipSamples = -offsetSamples
			} else {
				startSamples += offsetSamples
			}
		}
		spanSamples := clipEnd - startSamples
		if startSamples < 0 {
			skipSamples += -startSamples
			spanSamples += startSamples
			startSamples = 0
		}
		if spanSamples <= 0 || startSamples >= totalSamples {
			continue
		}
		if spanSamples > totalSamples-startSamples {
			spanSamples = totalSamples - startSamples
		}
		if err := mixOneSpan(ctx, ffmpeg, f, span.Path, startSamples, spanSamples, skipSamples); err != nil {
			return fmt.Errorf("mix audio span at frame %d: %w", span.StartFrame, err)
		}
		ranges = append(ranges, sampleRange{start: startSamples, end: startSamples + spanSamples})
	}
	if err := clampPCM(f, ranges); err != nil {
		return err
	}
	return f.Sync()
}

type sampleRange struct{ start, end int64 }

func clampPCM(f *os.File, ranges []sampleRange) error {
	if len(ranges) == 0 {
		return nil
	}
	// Ranges are few (at most the narration pages plus media clips). Merging
	// keeps overlap clipping correct while untouched PCM silence stays sparse.
	for i := 1; i < len(ranges); i++ {
		for j := i; j > 0 && ranges[j].start < ranges[j-1].start; j-- {
			ranges[j], ranges[j-1] = ranges[j-1], ranges[j]
		}
	}
	merged := ranges[:0]
	for _, r := range ranges {
		if r.end <= r.start {
			continue
		}
		if len(merged) > 0 && r.start <= merged[len(merged)-1].end {
			if r.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = r.end
			}
			continue
		}
		merged = append(merged, r)
	}
	buf := make([]byte, pcmChunkSamples*4)
	for _, r := range merged {
		for pos := r.start; pos < r.end; {
			n := int64(len(buf) / 4)
			if n > r.end-pos {
				n = r.end - pos
			}
			bytesN := int(n) * 4
			if _, err := f.ReadAt(buf[:bytesN], pos*4); err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for off := 0; off < bytesN; off += 4 {
				v := math.Float32frombits(binary.LittleEndian.Uint32(buf[off:]))
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					v = 0
				}
				if v > 1 {
					v = 1
				}
				if v < -1 {
					v = -1
				}
				binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v))
			}
			if _, err := f.WriteAt(buf[:bytesN], pos*4); err != nil {
				return err
			}
			pos += n
		}
	}
	return nil
}

func mediaAudioOffset(ctx context.Context, ffprobe, path string) (float64, error) {
	p, err := probe(ctx, ffprobe, path)
	if err != nil {
		return 0, err
	}
	var videoStart, audioStart *float64
	for i := range p.Streams {
		s := p.Streams[i]
		if s.StartTime == "" || s.StartTime == "N/A" {
			continue
		}
		n, e := strconv.ParseFloat(s.StartTime, 64)
		if e != nil {
			continue
		}
		if s.CodecType == "video" && videoStart == nil {
			videoStart = &n
		}
		if s.CodecType == "audio" && audioStart == nil {
			audioStart = &n
		}
	}
	if audioStart == nil {
		return 0, nil
	}
	base := 0.0
	if videoStart != nil {
		base = *videoStart
	}
	offset := *audioStart - base
	if math.IsNaN(offset) || math.IsInf(offset, 0) {
		return 0, nil
	}
	return offset, nil
}

func mixOneSpan(ctx context.Context, ffmpeg string, destination *os.File, path string, startSample, maxSamples, skipSamples int64) error {
	if maxSamples <= 0 {
		return nil
	}
	decodeSamples := maxSamples + skipSamples
	duration := float64(decodeSamples) / float64(pcmRate*pcmChannels)
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-map", "0:a:0", "-vn", "-sn", "-dn", "-ac", "2", "-ar", "48000", "-t", strconv.FormatFloat(duration, 'f', 9, 64), "-f", "f32le", "pipe:1")
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	waited := false
	defer func() {
		if !waited {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		}
	}()
	samples := make([]float32, pcmChunkSamples)
	encoded := make([]byte, pcmChunkSamples*4)
	dstBytes := make([]byte, pcmChunkSamples*4)
	var offset int64
	for skipSamples > 0 {
		want := int64(len(samples))
		if want > skipSamples {
			want = skipSamples
		}
		n, readErr := io.CopyN(io.Discard, stdout, want*4)
		skipped := n / 4
		skipSamples -= skipped
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				var finishErr error
				waited, finishErr = finishAudioDecoder(ctx, stdout, cmd, &stderr)
				return finishErr
			}
			return readErr
		}
	}
	for offset < maxSamples {
		want := int64(len(samples))
		if want > maxSamples-offset {
			want = maxSamples - offset
		}
		bytesN := int(want) * 4
		n, readErr := io.ReadFull(stdout, encoded[:bytesN])
		if readErr != nil && !(errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)) {
			return readErr
		}
		got := n / 4
		if got == 0 {
			break
		}
		for i := 0; i < got; i++ {
			samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[i*4:]))
		}
		fileOffset := (startSample + offset) * 4
		if _, err := destination.ReadAt(dstBytes[:got*4], fileOffset); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		mix := make([]float32, got)
		for i := 0; i < got; i++ {
			mix[i] = math.Float32frombits(binary.LittleEndian.Uint32(dstBytes[i*4:]))
		}
		mixPCMAt(mix, samples[:got], 0)
		for i, v := range mix {
			binary.LittleEndian.PutUint32(dstBytes[i*4:], math.Float32bits(v))
		}
		if _, err := destination.WriteAt(dstBytes[:got*4], fileOffset); err != nil {
			return err
		}
		offset += int64(got)
		if readErr != nil {
			break
		}
	}
	var finishErr error
	waited, finishErr = finishAudioDecoder(ctx, stdout, cmd, &stderr)
	return finishErr
}

func finishAudioDecoder(ctx context.Context, stdout io.ReadCloser, cmd *exec.Cmd, stderr *limitedBuffer) (bool, error) {
	// Consume any rounding tail left by FFmpeg's output -t before Wait. Closing
	// the pipe early can make an otherwise successful decoder report EPIPE.
	if _, err := io.Copy(io.Discard, stdout); err != nil {
		_ = stdout.Close()
		return false, fmt.Errorf("drain audio decoder: %w", err)
	}
	_ = stdout.Close()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, fmt.Errorf("decode audio: %w: %s", err, trimDiagnostics(stderr.String()))
	}
	return true, nil
}

func muxArgsWithBitrate(videoPath, pcmPath, outputPath, bitrate string) []string {
	args := muxArgs(videoPath, pcmPath, outputPath)
	if strings.TrimSpace(bitrate) == "" {
		bitrate = "192k"
	}
	for i, arg := range args {
		if arg == "-c:a" {
			args = append(args[:i+2], append([]string{"-b:a", bitrate}, args[i+2:]...)...)
			break
		}
	}
	return args
}

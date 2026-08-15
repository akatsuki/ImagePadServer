package obsrtmp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"

	"imagepadserver/internal/video"
)

// playlistGPUMuxArgs describes the explicit evaluation-only mux boundary. The
// video input is already GPU-encoded H.264; no RGBA/rawvideo input is allowed.
// Audio remains CPU-prepared PCM and is encoded to AAC only at the output
// boundary. outputPTSNs is the server-authoritative first PTS of this segment.
func playlistGPUMuxArgs(width, height, fps, sampleRate, channels int, outputPTSNs int64) ([]string, error) {
	return playlistGPUMuxArgsWithInputs(width, height, fps, sampleRate, channels, outputPTSNs, "pipe:3", "pipe:4")
}

func playlistGPUMuxArgsWithInputs(width, height, fps, sampleRate, channels int, outputPTSNs int64, videoInput, audioInput string) ([]string, error) {
	if width <= 0 || height <= 0 || fps <= 0 || sampleRate <= 0 || channels <= 0 || outputPTSNs < 0 {
		return nil, errors.New("invalid playlist GPU MPEG-TS mux contract")
	}
	if videoInput == "" || audioInput == "" {
		return nil, errors.New("playlist GPU MPEG-TS mux inputs are required")
	}
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-copyts",
		"-start_at_zero",
		"-output_ts_offset", fmt.Sprintf("%.9f", float64(outputPTSNs)/1_000_000_000),
		"-thread_queue_size", "512",
		"-probesize", "32",
		"-analyzeduration", "0",
		"-f", "h264",
		"-i", videoInput,
		"-thread_queue_size", "512",
		"-f", "s16le",
		"-ar", strconv.Itoa(sampleRate),
		"-ac", strconv.Itoa(channels),
		"-i", audioInput,
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-r", strconv.Itoa(fps),
		"-colorspace", "bt709",
		"-color_primaries", "bt709",
		"-color_trc", "bt709",
		"-color_range", "tv",
		"-c:v", "copy",
		"-bsf:v", "h264_metadata=video_full_range_flag=0:colour_primaries=1:transfer_characteristics=1:matrix_coefficients=1",
		"-c:a", "aac",
		"-b:a", "192k",
		"-muxdelay", "0",
		"-muxpreload", "0",
		// The MPEG-TS stream feeds a persistent publisher through pipe:1. Do
		// not leave the first packets in FFmpeg's AVIO buffer until EOF: the
		// publisher must receive live bytes while both loopback inputs remain
		// open.
		"-flush_packets", "1",
		"-f", "mpegts",
		"pipe:1",
	}, nil
}

// ApplyPlaylistGPUAudioFadePCM applies the authoritative transition fade at
// the CPU-prepared audio/mux boundary. PCM is never sent to the GPU.
func ApplyPlaylistGPUAudioFadePCM(samples []byte, ptsNS int64, sampleRate, channels int, plan video.AudioFadePlan) ([]byte, error) {
	if sampleRate <= 0 || channels <= 0 || ptsNS < 0 || len(samples) == 0 || len(samples)%(channels*2) != 0 {
		return nil, errors.New("invalid playlist GPU audio fade PCM contract")
	}
	if plan.DurationNS <= 0 || plan.Curve != "linear" || plan.StartPTSNs < 0 {
		return nil, errors.New("unsupported playlist GPU audio fade plan")
	}
	out := append([]byte(nil), samples...)
	frameBytes := channels * 2
	for offset := 0; offset < len(out); offset += frameBytes {
		sampleIndex := offset / frameBytes
		samplePTSNs := ptsNS + int64(sampleIndex)*int64(1_000_000_000/sampleRate)
		gain := 1.0
		if samplePTSNs >= plan.StartPTSNs+plan.DurationNS {
			gain = 0
		} else if samplePTSNs > plan.StartPTSNs {
			gain = 1 - float64(samplePTSNs-plan.StartPTSNs)/float64(plan.DurationNS)
		}
		for channel := 0; channel < channels; channel++ {
			index := offset + channel*2
			sample := int16(binary.LittleEndian.Uint16(out[index : index+2]))
			scaled := int64(math.Round(float64(sample) * gain))
			if scaled > math.MaxInt16 {
				scaled = math.MaxInt16
			} else if scaled < math.MinInt16 {
				scaled = math.MinInt16
			}
			binary.LittleEndian.PutUint16(out[index:index+2], uint16(int16(scaled)))
		}
	}
	return out, nil
}

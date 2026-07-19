package video

// SignedPCMToWaveformRawQ16 preserves bounded interleaved PCM for the GPU
// showwaves-compatible shader. Values use the existing centre-biased uint16
// wire representation: -32768 maps to 0 and 32767 maps to 65535.
func SignedPCMToWaveformRawQ16(pcm []int16, channels, maxValues int) []uint16 {
	if channels <= 0 || len(pcm) < channels || maxValues < channels || maxValues%channels != 0 {
		return nil
	}
	count := len(pcm)
	if count > maxValues {
		count = maxValues
		count -= count % channels
	}
	out := make([]uint16, count)
	for i := range out {
		out[i] = uint16(int32(pcm[i]) + 32768)
	}
	return out
}

// buildShowwavesRawHistoryQ16 reconstructs the two-tick circular history used
// by FFmpeg showwaves. Each analyzed frame stores one 1600-sample stereo tick;
// the scene transports previous+current without duplicating that storage for
// the entire track. Frame zero uses a silent previous tick.
func buildShowwavesRawHistoryQ16(frames [][]uint16, frameIndex int) []uint16 {
	const tickValues = sampleRate / 30 * 2
	if frameIndex < 0 || frameIndex >= len(frames) {
		return nil
	}
	out := make([]uint16, tickValues*2)
	for i := range out {
		out[i] = 32768
	}
	if frameIndex > 0 {
		copy(out[:tickValues], frames[frameIndex-1])
	}
	copy(out[tickValues:], frames[frameIndex])
	return out
}

// PCMToWaveformQ16 converts interleaved signed PCM into a bounded, per-video
// frame peak envelope. Values are unsigned Q16 amplitudes (0..65535), where
// 65535 represents full-scale PCM. frameIndex is the first video-frame tick
// to emit; it makes the conversion deterministic when a renderer requests a
// suffix of a shared audio buffer.
func PCMToWaveformQ16(pcm []int16, channels, sampleRate, frameRate, frameIndex, maxSamples int) []uint16 {
	if channels <= 0 || sampleRate <= 0 || frameRate <= 0 || frameIndex < 0 || maxSamples <= 0 || len(pcm) < channels {
		return nil
	}
	totalFrames := len(pcm) / channels
	first := (int64(frameIndex) * int64(sampleRate)) / int64(frameRate)
	if first >= int64(totalFrames) {
		return nil
	}
	// Ceil the number of output ticks covering the remaining PCM. The final
	// partial window is intentional: it is the last real video frame.
	available := (int64(totalFrames)*int64(frameRate)+int64(sampleRate)-1)/int64(sampleRate) - int64(frameIndex)
	if available <= 0 {
		return nil
	}
	count := available
	if count > int64(maxSamples) {
		count = int64(maxSamples)
	}
	out := make([]uint16, int(count))
	for i := int64(0); i < count; i++ {
		start := (int64(frameIndex) + i) * int64(sampleRate) / int64(frameRate)
		end := ((int64(frameIndex)+i+1)*int64(sampleRate) + int64(frameRate) - 1) / int64(frameRate)
		if end > int64(totalFrames) {
			end = int64(totalFrames)
		}
		if end <= start {
			continue
		}
		var peak int64
		for sample := start; sample < end; sample++ {
			base := sample * int64(channels)
			for ch := 0; ch < channels; ch++ {
				v := int64(pcm[base+int64(ch)])
				if v < 0 {
					v = -v
				}
				if v > peak {
					peak = v
				}
			}
		}
		if peak >= 32768 {
			out[i] = 65535
		} else {
			out[i] = uint16((peak*65535 + 16384) / 32768)
		}
	}
	return out
}

// SignedPCMToWaveformQ16 converts interleaved PCM to a signed, centre-biased
// Q16 envelope. The returned uint16 values are wire-encoded int16 samples:
// 32768 is the centre line, 0 and 65535 are the negative/positive limits.
// Keeping the wire type uint16 preserves the existing JSON contract while the
// GPU experiment can decode the centre bias explicitly.
func SignedPCMToWaveformQ16(pcm []int16, channels, sampleRate, frameRate, frameIndex, maxSamples int) []uint16 {
	if channels <= 0 || sampleRate <= 0 || frameRate <= 0 || frameIndex < 0 || maxSamples <= 0 || len(pcm) < channels {
		return nil
	}
	totalFrames := len(pcm) / channels
	first := (int64(frameIndex) * int64(sampleRate)) / int64(frameRate)
	if first >= int64(totalFrames) {
		return nil
	}
	available := (int64(totalFrames)*int64(frameRate)+int64(sampleRate)-1)/int64(sampleRate) - int64(frameIndex)
	if available <= 0 {
		return nil
	}
	count := available
	if count > int64(maxSamples) {
		count = int64(maxSamples)
	}
	out := make([]uint16, int(count))
	for i := int64(0); i < count; i++ {
		start := (int64(frameIndex) + i) * int64(sampleRate) / int64(frameRate)
		end := ((int64(frameIndex)+i+1)*int64(sampleRate) + int64(frameRate) - 1) / int64(frameRate)
		if end > int64(totalFrames) {
			end = int64(totalFrames)
		}
		if end <= start {
			continue
		}
		var lo, hi int64
		lo, hi = 32767, -32768
		for sample := start; sample < end; sample++ {
			base := sample * int64(channels)
			for ch := 0; ch < channels; ch++ {
				v := int64(pcm[base+int64(ch)])
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
		}
		// showwaves' centre-line waveform is represented by the midpoint of
		// the observed signed range, normalized to the int16 domain.
		v := (lo + hi) / 2
		if v < -32768 {
			v = -32768
		}
		if v > 32767 {
			v = 32767
		}
		out[i] = uint16(v + 32768)
	}
	return out
}

// SignedPCMToWaveformMinMaxQ16 converts each time window into a signed Q16
// range. The wire payload stores [min,max] pairs (two values per column),
// centre-biased at 32768. maxSamples is the maximum number of columns, so the
// returned payload is bounded to 2*maxSamples values (and callers can cap it
// at the 4096-value scene contract). This preserves transients that a single
// midpoint cannot represent while remaining JSON-compatible with []uint16.
func SignedPCMToWaveformMinMaxQ16(pcm []int16, channels, sampleRate, frameRate, frameIndex, maxSamples int) []uint16 {
	if channels <= 0 || sampleRate <= 0 || frameRate <= 0 || frameIndex < 0 || maxSamples <= 0 || len(pcm) < channels {
		return nil
	}
	totalFrames := len(pcm) / channels
	first := (int64(frameIndex) * int64(sampleRate)) / int64(frameRate)
	if first >= int64(totalFrames) {
		return nil
	}
	available := (int64(totalFrames)*int64(frameRate)+int64(sampleRate)-1)/int64(sampleRate) - int64(frameIndex)
	if available <= 0 {
		return nil
	}
	count := available
	if count > int64(maxSamples) {
		count = int64(maxSamples)
	}
	out := make([]uint16, int(count)*2)
	for i := int64(0); i < count; i++ {
		start := (int64(frameIndex) + i) * int64(sampleRate) / int64(frameRate)
		end := ((int64(frameIndex)+i+1)*int64(sampleRate) + int64(frameRate) - 1) / int64(frameRate)
		if end > int64(totalFrames) {
			end = int64(totalFrames)
		}
		if end <= start {
			continue
		}
		lo, hi := int64(32767), int64(-32768)
		for sample := start; sample < end; sample++ {
			base := sample * int64(channels)
			for ch := 0; ch < channels; ch++ {
				v := int64(pcm[base+int64(ch)])
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
		}
		out[2*i] = uint16(lo + 32768)
		out[2*i+1] = uint16(hi + 32768)
	}
	return out
}

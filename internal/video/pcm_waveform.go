package video

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

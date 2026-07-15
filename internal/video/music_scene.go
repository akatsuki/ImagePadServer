package video

import "math"

// CanonicalMusicScene builds the one scene payload used by both the HLS
// single-track path and playlist pre-rendering. All timing and feature
// quantization lives here so the two muxers cannot drift visually.
func CanonicalMusicScene(input AudioRenderInput, frameIndex uint64, ptsNS int64) MusicScenePayload {
	var spectrum []uint16
	if int(frameIndex) < len(input.Analysis.Frames) {
		f := input.Analysis.Frames[frameIndex].Spectrum24
		spectrum = make([]uint16, len(f))
		for i, v := range f {
			if v < 0 || math.IsNaN(v) {
				v = 0
			}
			if v > 1 {
				v = 1
			}
			spectrum[i] = uint16(math.Round(v * 65535))
		}
	}
	return MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{
		Schema: GPUContractVersion, SampleRateHz: 48000, FrameIndex: frameIndex,
		PTSNs: ptsNS, SpectrumQ16: spectrum,
	}}
}

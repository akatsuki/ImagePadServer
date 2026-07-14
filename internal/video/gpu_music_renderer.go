package video

import (
	"embed"
	"fmt"
)

// MusicRenderPolicy describes ownership of dynamic music visuals.  Once the
// GPU compositor is selected, FFmpeg must not apply showwaves/showfreqs or a
// second ASS overlay; otherwise the waveform/text is rendered twice.
type MusicRenderPolicy struct {
	GPU              bool
	DisableShowwaves bool
	DisableShowfreqs bool
	DisableASS       bool
}

func GPUOnlyMusicRenderPolicy() MusicRenderPolicy {
	return MusicRenderPolicy{GPU: true, DisableShowwaves: true, DisableShowfreqs: true, DisableASS: true}
}

func (p MusicRenderPolicy) Validate() error {
	if !p.GPU {
		return fmt.Errorf("music renderer requires GPU")
	}
	if !p.DisableShowwaves || !p.DisableShowfreqs || !p.DisableASS {
		return fmt.Errorf("GPU music renderer requires exclusive visual ownership")
	}
	return nil
}

//go:embed shaders/music_visualizer.wgsl
var musicVisualizerShaderFS embed.FS

func MusicVisualizerWGSL() (string, error) {
	b, err := musicVisualizerShaderFS.ReadFile("shaders/music_visualizer.wgsl")
	if err != nil {
		return "", fmt.Errorf("read music shader: %w", err)
	}
	return string(b), nil
}

// CompactAudioFeatureFrame converts CPU analysis into the bounded wire format
// consumed by the WGSL storage buffer. Spectrum values are quantized Q16 and
// clamped so NaN/Inf or oversized analysis data cannot reach the GPU.
func CompactAudioFeatureFrame(frame AudioFrame, rms, peak float64, frameIndex uint64, ptsNs int64, sampleRate uint32) AudioFeatureFrame {
	f := AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: sampleRate, FrameIndex: frameIndex, PTSNs: ptsNs}
	if rms < 0 {
		rms = 0
	}
	if rms > 1 {
		rms = 1
	}
	if peak < 0 {
		peak = 0
	}
	if peak > 1 {
		peak = 1
	}
	f.RMSQ15 = uint16(rms * 32767)
	f.PeakQ15 = uint16(peak * 32767)
	for _, v := range frame.Spectrum24 {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		f.SpectrumQ16 = append(f.SpectrumQ16, uint16(v*65535))
	}
	return f
}

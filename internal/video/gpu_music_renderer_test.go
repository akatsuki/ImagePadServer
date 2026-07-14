package video

import (
	"strings"
	"testing"
)

func TestGPUOnlyMusicPolicyPreventsDoubleRendering(t *testing.T) {
	p := GPUOnlyMusicRenderPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMusicVisualizerShaderContract(t *testing.T) {
	s, err := MusicVisualizerWGSL()
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"@vertex", "@fragment", "@binding(0)", "spectrum", "rms_q15"} {
		if !strings.Contains(s, needle) {
			t.Fatalf("shader missing %q", needle)
		}
	}
}

func TestCompactAudioFeatureFrameQuantizesAndClamps(t *testing.T) {
	f := CompactAudioFeatureFrame(AudioFrame{Spectrum24: [24]float64{2}}, 2, -1, 7, 99, 48000)
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if f.RMSQ15 != 32767 || f.PeakQ15 != 0 || f.SpectrumQ16[0] != 65535 {
		t.Fatalf("unexpected quantization: %#v", f)
	}
}

func TestCompactAudioFeatureFrameDynamicFixtureChangesWithAudio(t *testing.T) {
	quiet := CompactAudioFeatureFrame(AudioFrame{Spectrum24: [24]float64{0.1}}, 0.1, 0.2, 1, 0, 48000)
	loud := CompactAudioFeatureFrame(AudioFrame{Spectrum24: [24]float64{0.9}}, 0.9, 1, 2, 33333333, 48000)
	if quiet.RMSQ15 >= loud.RMSQ15 || quiet.PeakQ15 >= loud.PeakQ15 {
		t.Fatalf("dynamic RMS/peak fixture did not change: quiet=%#v loud=%#v", quiet, loud)
	}
	if quiet.SpectrumQ16[0] >= loud.SpectrumQ16[0] || quiet.FrameIndex >= loud.FrameIndex || quiet.PTSNs >= loud.PTSNs {
		t.Fatalf("dynamic spectrum/timing fixture did not change: quiet=%#v loud=%#v", quiet, loud)
	}
	if err := quiet.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := loud.Validate(); err != nil {
		t.Fatal(err)
	}
}

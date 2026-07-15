package video

import (
	"reflect"
	"testing"
)

func TestCanonicalMusicSceneIsStableAcrossRenderRoutes(t *testing.T) {
	in := AudioRenderInput{Analysis: AudioAnalysis{Frames: []AudioFrame{{Spectrum24: [24]float64{0, .5, 1}}}}}
	a := CanonicalMusicScene(in, 0, 0)
	b := CanonicalMusicScene(in, 0, 0)
	if a.Schema != MusicSceneSchema || a.Feature.Schema != GPUContractVersion {
		t.Fatalf("invalid scene schema: %#v", a)
	}
	if len(a.Feature.SpectrumQ16) != 24 || a.Feature.SpectrumQ16[1] != 32768 || a.Feature.SpectrumQ16[2] != 65535 {
		t.Fatalf("unexpected canonical quantization: %#v", a.Feature.SpectrumQ16[:3])
	}
	if !reflect.DeepEqual(a.Feature, b.Feature) {
		t.Fatalf("single and playlist scene inputs diverged: %#v != %#v", a.Feature, b.Feature)
	}
}

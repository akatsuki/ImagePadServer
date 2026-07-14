package video

import "testing"

func TestGPUContractFrameValidation(t *testing.T) {
	f := GpuFrame{Schema: 1, Width: 64, Height: 2, RowStride: 256, Format: PixelRGBA8, ColorSpace: ColorSRGB, Ownership: "OwnedByTransport", Payload: make([]byte, 512)}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	f.RowStride = 260
	if f.Validate() == nil {
		t.Fatal("unaligned stride accepted")
	}
}
func TestGPUContractAudioValidation(t *testing.T) {
	a := AudioFeatureFrame{Schema: 1, SampleRateHz: 48000, RMSQ15: 2, PeakQ15: 3}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	a.Schema = 2
	if a.Validate() == nil {
		t.Fatal("wrong schema accepted")
	}
}
func TestGPUContractJSONRoundTrip(t *testing.T) {
	s := SceneSnapshot{Schema: 1, Sequence: 4, PTSNs: 10, Width: 640, Height: 480, Overlays: []OverlayCommand{{Kind: "text", ID: "title", Text: "x"}}}
	b, err := EncodeGPUContract(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty encoding")
	}
}

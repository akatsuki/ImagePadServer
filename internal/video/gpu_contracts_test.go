package video

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTextOverlayMetadataRoundTripAndValidation(t *testing.T) {
	o := TextOverlayMetadata{Title: "T", RendererID: "cpu-ass", RendererVersion: "1", Width: 1, Height: 1, RowStride: 256, Format: PixelRGBA8, ColorSpace: ColorSRGB, AssetHash: strings.Repeat("a", 64), Payload: make([]byte, 256), Opacity: 1}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var got TextOverlayMetadata
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o, got) {
		t.Fatalf("roundtrip mismatch: %#v != %#v", o, got)
	}
	got.AssetHash = "bad"
	if err := got.Validate(); err == nil {
		t.Fatal("expected malformed asset hash rejection")
	}
	screen := o
	screen.Kind = "screen_rgba"
	screen.ScreenRect = SceneRect{W: 1, H: 1}
	screen.AlphaMode = "premultiplied"
	screen.PixelOrigin = "top_left"
	if err := screen.Validate(); err != nil {
		t.Fatalf("screen payload rejected: %v", err)
	}
	screen.PixelOrigin = "bottom_left"
	if err := screen.Validate(); err == nil {
		t.Fatal("invalid screen payload semantics accepted")
	}
}

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

func TestGPUContractWaveformBackwardCompatibleAndBounded(t *testing.T) {
	var legacy AudioFeatureFrame
	if err := json.Unmarshal([]byte(`{"schema":1,"sample_rate_hz":48000,"frame_index":0,"pts_ns":0,"spectrum_q16":[],"rms_q15":0,"peak_q15":0}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy.WaveformQ16) != 0 {
		t.Fatalf("legacy waveform = %d, want empty", len(legacy.WaveformQ16))
	}
	legacy.WaveformQ16 = make([]uint16, MusicMaxWaveformSamples+1)
	if legacy.Validate() == nil {
		t.Fatal("oversized waveform accepted")
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

func TestMusicScenePayloadValidationAndRoundTrip(t *testing.T) {
	scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, SpectrumQ16: make([]uint16, 24)}, Artwork: &ArtworkMetadata{TextureID: "cover", Width: 64, Height: 64, RowStride: 256, Format: PixelRGBA8, ColorSpace: ColorSRGB, Payload: make([]byte, 16384)}, GlyphAtlas: &GlyphAtlasMetadata{TextureID: "atlas", FontFamily: "sans", Width: 256, Height: 256, RowStride: 1024, GlyphCount: 32, MissingGlyphID: "missing"}}
	if err := scene.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := EncodeGPUContract(scene)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MusicScenePayload
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMusicScenePayloadRejectsOversizedData(t *testing.T) {
	scene := MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000}, Artwork: &ArtworkMetadata{TextureID: "cover", Width: MusicMaxArtworkDimension + 1, Height: 1, RowStride: 256, Format: PixelRGBA8, ColorSpace: ColorSRGB}}
	if err := scene.Validate(); err == nil {
		t.Fatal("expected oversized artwork rejection")
	}
	scene = MusicScenePayload{Schema: MusicSceneSchema, Feature: AudioFeatureFrame{Schema: GPUContractVersion, SampleRateHz: 48000, SpectrumQ16: make([]uint16, MusicMaxFeatureBins+1)}}
	if err := scene.Validate(); err == nil {
		t.Fatal("expected oversized feature rejection")
	}
}

func TestBaseTextureMetadataValidationAndRoundTrip(t *testing.T) {
	b := BaseTextureMetadata{TextureID: "base", Width: 2, Height: 2, RowStride: 256, Format: PixelRGBA8, ColorSpace: ColorSRGB, Payload: make([]byte, 512)}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var got BaseTextureMetadata
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b, got) {
		t.Fatalf("roundtrip mismatch: %#v != %#v", b, got)
	}
	got.Payload = got.Payload[:1]
	if got.Validate() == nil {
		t.Fatal("short base texture payload accepted")
	}
}

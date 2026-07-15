package video

import (
	"encoding/json"
	"errors"
	"fmt"
)

const GPUContractVersion uint16 = 1
const GPURowAlignment uint32 = 256
const GPUMaxDimension uint32 = 16384
const GPUMaxPayload = 256 * 1024 * 1024
const MusicSceneSchema uint16 = 1
const MusicMaxFeatureBins = 256
const MusicMaxArtworkDimension uint32 = 4096
const MusicMaxArtworkBytes = 16 * 1024 * 1024
const MusicMaxGlyphs = 4096
const MusicMaxTextBytes = 64 * 1024

// MusicScenePayload is an optional, versioned extension to Render. It is
// deliberately bounded so malformed metadata cannot turn a JSONL request into
// an unbounded texture or glyph upload. Existing Render requests omit this
// field and remain valid.
type MusicScenePayload struct {
	Schema     uint16              `json:"schema"`
	Feature    AudioFeatureFrame   `json:"feature"`
	Artwork    *ArtworkMetadata    `json:"artwork,omitempty"`
	GlyphAtlas *GlyphAtlasMetadata `json:"glyph_atlas,omitempty"`
}

type ArtworkMetadata struct {
	TextureID  string      `json:"texture_id"`
	Width      uint32      `json:"width"`
	Height     uint32      `json:"height"`
	RowStride  uint32      `json:"row_stride"`
	Format     PixelFormat `json:"format"`
	ColorSpace ColorSpace  `json:"color_space"`
	Alpha      bool        `json:"alpha"`
	Payload    []byte      `json:"payload,omitempty"`
}

type GlyphAtlasMetadata struct {
	TextureID      string   `json:"texture_id"`
	FontFamily     string   `json:"font_family"`
	FontWeight     uint16   `json:"font_weight"`
	FallbackOrder  []string `json:"fallback_order,omitempty"`
	Width          uint32   `json:"width"`
	Height         uint32   `json:"height"`
	RowStride      uint32   `json:"row_stride"`
	GlyphCount     uint32   `json:"glyph_count"`
	MissingGlyphID string   `json:"missing_glyph_id"`
}

func (s MusicScenePayload) Validate() error {
	if s.Schema != MusicSceneSchema {
		return errors.New("invalid music scene schema")
	}
	if err := s.Feature.Validate(); err != nil {
		return fmt.Errorf("scene feature: %w", err)
	}
	if len(s.Feature.SpectrumQ16) > MusicMaxFeatureBins {
		return errors.New("too many feature bins")
	}
	if s.Artwork != nil {
		if err := s.Artwork.Validate(); err != nil {
			return fmt.Errorf("scene artwork: %w", err)
		}
	}
	if s.GlyphAtlas != nil {
		if err := s.GlyphAtlas.Validate(); err != nil {
			return fmt.Errorf("scene glyph atlas: %w", err)
		}
	}
	return nil
}

func (a ArtworkMetadata) Validate() error {
	if a.TextureID == "" || a.Width == 0 || a.Height == 0 || a.Width > MusicMaxArtworkDimension || a.Height > MusicMaxArtworkDimension {
		return errors.New("invalid artwork metadata")
	}
	if a.RowStride < a.Width*4 || a.RowStride%GPURowAlignment != 0 {
		return errors.New("invalid artwork stride")
	}
	n := uint64(a.RowStride) * uint64(a.Height)
	if n > MusicMaxArtworkBytes {
		return errors.New("artwork texture too large")
	}
	if len(a.Payload) != 0 && uint64(len(a.Payload)) != n {
		return errors.New("invalid artwork payload length")
	}
	if a.Format != PixelRGBA8 && a.Format != PixelBGRA8 {
		return errors.New("invalid artwork format")
	}
	if a.ColorSpace != ColorSRGB && a.ColorSpace != ColorLinear {
		return errors.New("invalid artwork color space")
	}
	return nil
}

func (g GlyphAtlasMetadata) Validate() error {
	if g.TextureID == "" || g.FontFamily == "" || g.Width == 0 || g.Height == 0 || g.Width > MusicMaxArtworkDimension || g.Height > MusicMaxArtworkDimension || g.GlyphCount > MusicMaxGlyphs || g.MissingGlyphID == "" {
		return errors.New("invalid glyph atlas metadata")
	}
	if g.RowStride < g.Width*4 || g.RowStride%GPURowAlignment != 0 || uint64(g.RowStride)*uint64(g.Height) > MusicMaxArtworkBytes {
		return errors.New("invalid glyph atlas bounds")
	}
	if len(g.FontFamily)+len(g.MissingGlyphID) > MusicMaxTextBytes {
		return errors.New("glyph metadata too large")
	}
	return nil
}

type SceneSnapshot struct {
	Schema   uint16           `json:"schema"`
	Sequence uint64           `json:"sequence"`
	PTSNs    int64            `json:"pts_ns"`
	Width    uint32           `json:"width"`
	Height   uint32           `json:"height"`
	Overlays []OverlayCommand `json:"overlays"`
}
type OverlayCommand struct {
	Kind      string   `json:"kind"`
	ID        string   `json:"id"`
	Text      string   `json:"text,omitempty"`
	X         float32  `json:"x,omitempty"`
	Y         float32  `json:"y,omitempty"`
	SizePx    float32  `json:"size_px,omitempty"`
	RGBA      [4]uint8 `json:"rgba,omitempty"`
	TextureID string   `json:"texture_id,omitempty"`
	Width     float32  `json:"width,omitempty"`
	Height    float32  `json:"height,omitempty"`
	Opacity   float32  `json:"opacity,omitempty"`
}
type AudioFeatureFrame struct {
	Schema       uint16   `json:"schema"`
	SampleRateHz uint32   `json:"sample_rate_hz"`
	FrameIndex   uint64   `json:"frame_index"`
	PTSNs        int64    `json:"pts_ns"`
	SpectrumQ16  []uint16 `json:"spectrum_q16"`
	RMSQ15       uint16   `json:"rms_q15"`
	PeakQ15      uint16   `json:"peak_q15"`
}
type PixelFormat string

const (
	PixelRGBA8 PixelFormat = "Rgba8"
	PixelBGRA8 PixelFormat = "Bgra8"
)

type ColorSpace string

const (
	ColorSRGB   ColorSpace = "Srgb"
	ColorLinear ColorSpace = "Linear"
)

type GpuFrame struct {
	Schema     uint16      `json:"schema"`
	Sequence   uint64      `json:"sequence"`
	PTSNs      int64       `json:"pts_ns"`
	Width      uint32      `json:"width"`
	Height     uint32      `json:"height"`
	RowStride  uint32      `json:"row_stride"`
	Format     PixelFormat `json:"format"`
	ColorSpace ColorSpace  `json:"color_space"`
	Alpha      bool        `json:"alpha"`
	Ownership  string      `json:"ownership"`
	Payload    []byte      `json:"payload"`
}

func (s SceneSnapshot) Validate() error {
	if s.Schema != GPUContractVersion || s.Width == 0 || s.Height == 0 || s.Width > GPUMaxDimension || s.Height > GPUMaxDimension {
		return errors.New("invalid scene snapshot")
	}
	return nil
}
func (a AudioFeatureFrame) Validate() error {
	if a.Schema != GPUContractVersion || a.SampleRateHz == 0 || a.RMSQ15 > 0x7fff || a.PeakQ15 > 0x7fff {
		return errors.New("invalid audio feature frame")
	}
	return nil
}
func (f GpuFrame) Validate() error {
	if f.Schema != GPUContractVersion || f.Width == 0 || f.Height == 0 || f.Width > GPUMaxDimension || f.Height > GPUMaxDimension {
		return errors.New("invalid gpu frame dimensions")
	}
	if f.RowStride < f.Width*4 || f.RowStride%GPURowAlignment != 0 {
		return errors.New("invalid gpu row stride")
	}
	n := uint64(f.RowStride) * uint64(f.Height)
	if n > GPUMaxPayload {
		return errors.New("gpu payload too large")
	}
	if uint64(len(f.Payload)) != n {
		return fmt.Errorf("gpu payload length %d, want %d", len(f.Payload), n)
	}
	if f.Ownership != "OwnedByTransport" {
		return errors.New("invalid gpu ownership")
	}
	return nil
}
func EncodeGPUContract(v any) ([]byte, error) { return json.Marshal(v) }

package video

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
const MusicMaxLoudnessSamples = 1000
const MusicMaxLayoutRects = 8

// MusicScenePayload is an optional, versioned extension to Render. It is
// deliberately bounded so malformed metadata cannot turn a JSONL request into
// an unbounded texture or glyph upload. Existing Render requests omit this
// field and remain valid.
type MusicScenePayload struct {
	Schema      uint16              `json:"schema"`
	Feature     AudioFeatureFrame   `json:"feature"`
	Artwork     *ArtworkMetadata    `json:"artwork,omitempty"`
	GlyphAtlas  *GlyphAtlasMetadata `json:"glyph_atlas,omitempty"`
	Layout      MusicSceneLayout    `json:"layout"`
	Dynamics    MusicSceneDynamics  `json:"dynamics"`
	Palette     MusicScenePalette   `json:"palette"`
	Fingerprint string              `json:"fingerprint,omitempty"`
}

type SceneRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type MusicSceneLayout struct {
	Artwork  SceneRect `json:"artwork"`
	Title    SceneRect `json:"title"`
	Artist   SceneRect `json:"artist"`
	Album    SceneRect `json:"album"`
	Spectrum SceneRect `json:"spectrum"`
	Loudness SceneRect `json:"loudness"`
	Progress SceneRect `json:"progress"`
	Time     SceneRect `json:"time"`
}

type MusicSceneDynamics struct {
	CurrentSeconds   float64   `json:"current_seconds,omitempty"`
	DurationSeconds  float64   `json:"duration_seconds,omitempty"`
	ProgressRatio    float64   `json:"progress_ratio,omitempty"`
	EdgeFadeAlpha    float32   `json:"edge_fade_alpha,omitempty"`
	EndFadeAlpha     float32   `json:"end_fade_alpha,omitempty"`
	LoudnessEnvelope []uint16  `json:"loudness_envelope,omitempty"`
	LoudnessTrend    []uint16  `json:"loudness_trend,omitempty"`
	LoudnessGuides   [4]uint16 `json:"loudness_guides"`
}

type MusicScenePalette struct {
	Primary      [4]uint8 `json:"primary"`
	Accent       [4]uint8 `json:"accent"`
	Background   [4]uint8 `json:"background"`
	Overlay      [4]uint8 `json:"overlay"`
	BlurStrength float32  `json:"blur_strength"`
	Readability  float32  `json:"readability"`
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
	AssetHash  string      `json:"asset_hash,omitempty"`
}

type GlyphAtlasMetadata struct {
	TextureID      string       `json:"texture_id"`
	FontFamily     string       `json:"font_family"`
	FontWeight     uint16       `json:"font_weight"`
	FallbackOrder  []string     `json:"fallback_order,omitempty"`
	Width          uint32       `json:"width"`
	Height         uint32       `json:"height"`
	RowStride      uint32       `json:"row_stride"`
	GlyphCount     uint32       `json:"glyph_count"`
	MissingGlyphID string       `json:"missing_glyph_id"`
	Payload        []byte       `json:"payload,omitempty"`
	AssetHash      string       `json:"asset_hash,omitempty"`
	Glyphs         []GlyphEntry `json:"glyphs,omitempty"`
	TextRuns       []TextRun    `json:"text_runs,omitempty"`
}

type GlyphEntry struct {
	ID      string  `json:"id"`
	X       uint32  `json:"x"`
	Y       uint32  `json:"y"`
	Width   uint32  `json:"width"`
	Height  uint32  `json:"height"`
	Advance float32 `json:"advance"`
}
type TextRun struct {
	Text    string   `json:"text"`
	X       float32  `json:"x"`
	Y       float32  `json:"y"`
	SizePx  float32  `json:"size_px"`
	RGBA    [4]uint8 `json:"rgba"`
	Opacity float32  `json:"opacity,omitempty"`
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
	if err := s.validateDynamics(); err != nil {
		return err
	}
	if s.Fingerprint != "" && len(s.Fingerprint) != 64 {
		return errors.New("invalid scene fingerprint")
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

func (s MusicScenePayload) validateDynamics() error {
	if len(s.Dynamics.LoudnessEnvelope) > MusicMaxLoudnessSamples || len(s.Dynamics.LoudnessTrend) > MusicMaxLoudnessSamples {
		return errors.New("too many loudness samples")
	}
	for _, v := range append(append([]uint16{}, s.Dynamics.LoudnessEnvelope...), s.Dynamics.LoudnessTrend...) {
		if v > 65535 {
			return errors.New("invalid loudness sample")
		}
	}
	for _, v := range []float64{s.Dynamics.CurrentSeconds, s.Dynamics.DurationSeconds, s.Dynamics.ProgressRatio} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return errors.New("invalid scene timing")
		}
	}
	if s.Dynamics.ProgressRatio > 1 || s.Dynamics.EdgeFadeAlpha < 0 || s.Dynamics.EdgeFadeAlpha > 1 || s.Dynamics.EndFadeAlpha < 0 || s.Dynamics.EndFadeAlpha > 1 {
		return errors.New("invalid scene fade")
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
	GlyphAtlasReceipt *GlyphAtlasReceipt `json:"glyph_atlas_receipt,omitempty"`
}

type GlyphAtlasReceipt struct {
	SHA256 string `json:"sha256"`
	Width uint32 `json:"width"`
	Height uint32 `json:"height"`
	RowStride uint32 `json:"row_stride"`
	GlyphCount uint32 `json:"glyph_count"`
	TextRunCount uint32 `json:"text_run_count"`
	Format string `json:"format"`
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

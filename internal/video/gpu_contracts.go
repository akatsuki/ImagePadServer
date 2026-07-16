package video

import (
	"encoding/hex"
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
	Schema      uint16               `json:"schema"`
	Feature     AudioFeatureFrame    `json:"feature"`
	Artwork     *ArtworkMetadata     `json:"artwork,omitempty"`
	BaseTexture *BaseTextureMetadata `json:"base_texture,omitempty"`
	GlyphAtlas  *GlyphAtlasMetadata  `json:"glyph_atlas,omitempty"`
	TextOverlay *TextOverlayMetadata `json:"text_overlay,omitempty"`
	Layout      MusicSceneLayout     `json:"layout"`
	Dynamics    MusicSceneDynamics   `json:"dynamics"`
	Palette     MusicScenePalette    `json:"palette"`
	Fingerprint string               `json:"fingerprint,omitempty"`
}

// BaseTextureMetadata is the immutable CPU compositor output shared with the
// GPU route. It is uploaded once per render job and reused by every frame.
type BaseTextureMetadata struct {
	TextureID  string      `json:"texture_id"`
	Width      uint32      `json:"width"`
	Height     uint32      `json:"height"`
	RowStride  uint32      `json:"row_stride"`
	Format     PixelFormat `json:"format"`
	ColorSpace ColorSpace  `json:"color_space"`
	Payload    []byte      `json:"payload,omitempty"`
	AssetHash  string      `json:"asset_hash,omitempty"`
}

func (b BaseTextureMetadata) Validate() error {
	if b.Width == 0 || b.Height == 0 || b.Width > GPUMaxDimension || b.Height > GPUMaxDimension {
		return errors.New("invalid base texture dimensions")
	}
	if b.RowStride < b.Width*4 || b.RowStride%4 != 0 {
		return errors.New("invalid base texture stride")
	}
	if b.Format != PixelRGBA8 || b.ColorSpace == "" {
		return errors.New("invalid base texture format")
	}
	if uint64(b.RowStride)*uint64(b.Height) > GPUMaxPayload || len(b.Payload) != int(b.RowStride*b.Height) {
		return errors.New("invalid base texture payload")
	}
	return nil
}

// TextOverlayMetadata is an optional, bounded description of the canonical
// metadata overlay. It is transport-only until a renderer explicitly opts in.
type TextOverlayMetadata struct {
	Title           string      `json:"title,omitempty"`
	Artist          string      `json:"artist,omitempty"`
	Album           string      `json:"album,omitempty"`
	FontFamily      string      `json:"font_family,omitempty"`
	FontWeight      uint16      `json:"font_weight,omitempty"`
	SizePx          float32     `json:"size_px,omitempty"`
	RGBA            [4]uint8    `json:"rgba,omitempty"`
	Opacity         float32     `json:"opacity,omitempty"`
	Width           uint32      `json:"width,omitempty"`
	Height          uint32      `json:"height,omitempty"`
	RowStride       uint32      `json:"row_stride,omitempty"`
	Format          PixelFormat `json:"format,omitempty"`
	ColorSpace      ColorSpace  `json:"color_space,omitempty"`
	Premultiplied   bool        `json:"premultiplied,omitempty"`
	Payload         []byte      `json:"payload,omitempty"`
	AssetHash       string      `json:"asset_hash,omitempty"`
	RendererID      string      `json:"renderer_id,omitempty"`
	RendererVersion string      `json:"renderer_version,omitempty"`
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
	// Font fields make the CPU ASS face/weight explicit at the GPU boundary.
	// They are diagnostic metadata until the sidecar supports multi-face atlases.
	FontFamily string `json:"font_family,omitempty"`
	FontWeight uint16 `json:"font_weight,omitempty"`
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
	if s.BaseTexture != nil {
		if err := s.BaseTexture.Validate(); err != nil {
			return fmt.Errorf("scene base texture: %w", err)
		}
	}
	if s.GlyphAtlas != nil {
		if err := s.GlyphAtlas.Validate(); err != nil {
			return fmt.Errorf("scene glyph atlas: %w", err)
		}
	}
	if s.TextOverlay != nil {
		if err := s.TextOverlay.Validate(); err != nil {
			return fmt.Errorf("scene text overlay: %w", err)
		}
	}
	return nil
}

func (o TextOverlayMetadata) Validate() error {
	if len(o.Title)+len(o.Artist)+len(o.Album)+len(o.FontFamily)+len(o.RendererID)+len(o.RendererVersion) > MusicMaxTextBytes || o.SizePx < 0 || math.IsNaN(float64(o.SizePx)) || math.IsInf(float64(o.SizePx), 0) || o.Opacity < 0 || o.Opacity > 1 || math.IsNaN(float64(o.Opacity)) || math.IsInf(float64(o.Opacity), 0) {
		return errors.New("invalid text overlay metadata")
	}
	if o.Width == 0 || o.Height == 0 || o.Width > MusicMaxArtworkDimension || o.Height > MusicMaxArtworkDimension || o.RowStride < o.Width*4 || o.RowStride%GPURowAlignment != 0 {
		return errors.New("invalid text overlay dimensions")
	}
	if uint64(o.RowStride)*uint64(o.Height) > MusicMaxArtworkBytes || (len(o.Payload) != 0 && uint64(len(o.Payload)) != uint64(o.RowStride)*uint64(o.Height)) {
		return errors.New("invalid text overlay payload")
	}
	if o.Format != PixelRGBA8 && o.Format != PixelBGRA8 {
		return errors.New("invalid text overlay format")
	}
	if len(o.AssetHash) != 64 {
		return errors.New("invalid text overlay asset hash")
	}
	if _, err := hex.DecodeString(o.AssetHash); err != nil || o.RendererID == "" || o.RendererVersion == "" {
		return errors.New("invalid text overlay provenance")
	}
	if o.ColorSpace != ColorSRGB && o.ColorSpace != ColorLinear {
		return errors.New("invalid text overlay color space")
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
	Schema             uint16                  `json:"schema"`
	Sequence           uint64                  `json:"sequence"`
	PTSNs              int64                   `json:"pts_ns"`
	Width              uint32                  `json:"width"`
	Height             uint32                  `json:"height"`
	RowStride          uint32                  `json:"row_stride"`
	Format             PixelFormat             `json:"format"`
	ColorSpace         ColorSpace              `json:"color_space"`
	Alpha              bool                    `json:"alpha"`
	Ownership          string                  `json:"ownership"`
	Payload            []byte                  `json:"payload"`
	GlyphAtlasReceipt  *GlyphAtlasReceipt      `json:"glyph_atlas_receipt,omitempty"`
	TextOverlayReceipt *TextOverlayReceipt     `json:"text_overlay_receipt,omitempty"`
	ArtworkReceipt     *ArtworkReceipt         `json:"artwork_receipt,omitempty"`
	GlyphDiagnostics   *GlyphRenderDiagnostics `json:"glyph_diagnostics,omitempty"`
}

// GlyphRenderDiagnostics is bounded, read-only evidence of the instance data
// actually constructed by the GPU sidecar. It is not part of the production
// rendering decision and exists solely to diagnose CPU/GPU text parity.
type GlyphRenderDiagnostics struct {
	Count     int                       `json:"count"`
	Instances []GlyphInstanceDiagnostic `json:"instances,omitempty"`
}
type GlyphInstanceDiagnostic struct {
	ID          string     `json:"id"`
	Screen      [4]float32 `json:"screen"`
	Atlas       [4]float32 `json:"atlas"`
	Color       [4]float32 `json:"color"`
	Scale       float32    `json:"scale"`
	Baseline    float32    `json:"baseline"`
	InkTop      float32    `json:"ink_top"`
	CellPadding [4]float32 `json:"cell_padding"`
}

// GlyphInstanceManifest is the deterministic CPU expansion of text_runs.
// It intentionally mirrors the sidecar diagnostic schema so parity tooling can
// compare geometry without changing the production render path.
type GlyphInstanceManifest struct {
	Count     int                       `json:"count"`
	Instances []GlyphInstanceDiagnostic `json:"instances,omitempty"`
	SHA256    string                    `json:"sha256,omitempty"`
}

type GlyphInstanceParity struct {
	CPUCount       int    `json:"cpuCount"`
	GPUCount       int    `json:"gpuCount"`
	Matched        int    `json:"matched"`
	Mismatches     int    `json:"mismatches"`
	CPUManifestSHA string `json:"cpuManifestSha,omitempty"`
	GPUManifestSHA string `json:"gpuManifestSha,omitempty"`
	FirstMismatch  string `json:"firstMismatch,omitempty"`
}

type GlyphAtlasReceipt struct {
	SHA256       string `json:"sha256"`
	Width        uint32 `json:"width"`
	Height       uint32 `json:"height"`
	RowStride    uint32 `json:"row_stride"`
	GlyphCount   uint32 `json:"glyph_count"`
	TextRunCount uint32 `json:"text_run_count"`
	Format       string `json:"format"`
}

type TextOverlayReceipt struct {
	SHA256          string `json:"sha256"`
	Width           uint32 `json:"width"`
	Height          uint32 `json:"height"`
	RowStride       uint32 `json:"row_stride"`
	Format          string `json:"format"`
	ColorSpace      string `json:"color_space"`
	Premultiplied   bool   `json:"premultiplied"`
	RendererID      string `json:"renderer_id"`
	RendererVersion string `json:"renderer_version"`
}
type ArtworkReceipt struct {
	SHA256       string `json:"sha256"`
	SourceWidth  uint32 `json:"source_width"`
	SourceHeight uint32 `json:"source_height"`
	OutputWidth  uint32 `json:"output_width"`
	OutputHeight uint32 `json:"output_height"`
	CropMode     string `json:"crop_mode"`
	AspectMode   string `json:"aspect_mode"`
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

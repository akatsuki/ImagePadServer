package video

import "fmt"

// MusicRenderV2Provider identifies who owns a layer's final pixels. GoldenUpload
// is intentionally retained for parity diagnostics only; production validation
// rejects it so a CPU-completed screen raster cannot silently become permanent.
type MusicRenderV2Provider string

const (
	MusicRenderV2NativeGPU    MusicRenderV2Provider = "NativeGPU"
	MusicRenderV2GoldenUpload MusicRenderV2Provider = "GoldenUpload"
)

type MusicRenderV2ArtworkMode string

const (
	MusicRenderV2ArtworkSource   MusicRenderV2ArtworkMode = "Source"
	MusicRenderV2ArtworkFallback MusicRenderV2ArtworkMode = "Fallback"
)

type MusicRenderV2LayerReceipt struct {
	Name       string                `json:"name"`
	Provider   MusicRenderV2Provider `json:"provider"`
	InputHash  string                `json:"inputHash,omitempty"`
	ShaderHash string                `json:"shaderHash,omitempty"`
}

type MusicRenderV2Job struct {
	Width       uint32
	Height      uint32
	FPS         uint32
	ArtworkMode MusicRenderV2ArtworkMode
	Layers      []MusicRenderV2LayerReceipt
}

// NewMusicRenderV2Job creates the production ownership manifest before the
// renderer is invoked. It intentionally contains logical layers only; no
// screen-sized CPU texture can be smuggled in through this constructor.
func NewMusicRenderV2Job(input AudioRenderInput, width, height, fps uint32, artworkMode MusicRenderV2ArtworkMode) MusicRenderV2Job {
	_ = input
	layers := make([]MusicRenderV2LayerReceipt, 0, 8)
	for _, name := range []string{"background", "artwork", "spectrum", "waveform", "loudness", "text", "progress", "fade"} {
		layers = append(layers, MusicRenderV2LayerReceipt{Name: name, Provider: MusicRenderV2NativeGPU})
	}
	return MusicRenderV2Job{Width: width, Height: height, FPS: fps, ArtworkMode: artworkMode, Layers: layers}
}

// ValidateProduction rejects the legacy CPU-final-raster contract at the
// boundary, before any sidecar process is started.
func (j MusicRenderV2Job) ValidateProduction() error {
	if j.Width == 0 || j.Height == 0 || j.FPS == 0 {
		return fmt.Errorf("music render v2: invalid output geometry or fps")
	}
	if j.ArtworkMode != MusicRenderV2ArtworkSource && j.ArtworkMode != MusicRenderV2ArtworkFallback {
		return fmt.Errorf("music render v2: artwork mode must be explicit")
	}
	for _, layer := range j.Layers {
		if layer.Name == "screen_rgba" || layer.Provider == MusicRenderV2GoldenUpload {
			return fmt.Errorf("music render v2: CPU final raster is forbidden for layer %q", layer.Name)
		}
		if layer.Provider != MusicRenderV2NativeGPU {
			return fmt.Errorf("music render v2: unknown provider %q for layer %q", layer.Provider, layer.Name)
		}
	}
	return nil
}

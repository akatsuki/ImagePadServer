package video

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
)

// fallbackNoteMaskMetadata rasterizes only the bounded music-note glyph using
// the same FFmpeg/FreeType build as the CPU reference. The GPU still owns the
// tile gradient, fingerprint, blur, shadow, rounded mask and final composite.
func fallbackNoteMaskMetadata(ctx context.Context, ffmpeg string, fonts FontSet, size int) (ArtworkMetadata, error) {
	source, err := renderGlyph(ctx, ffmpeg, fonts, color.RGBA{R: 255, G: 255, B: 255, A: 224}, size)
	if err != nil {
		return ArtworkMetadata{}, fmt.Errorf("rasterize bounded fallback note: %w", err)
	}
	bounds := source.Bounds()
	mask := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, a := source.At(x, y).RGBA()
			alpha := uint8(a >> 8)
			if alpha > 0 {
				mask.SetRGBA(x-bounds.Min.X, y-bounds.Min.Y, color.RGBA{R: 255, G: 255, B: 255, A: alpha})
			}
		}
	}
	return artworkMetadataFromRGBA("gpu-fallback-note-mask", mask)
}

// PrepareGPUMusicSceneInput resolves the immutable artwork input and adaptive
// palette used by every native GPU music route. It never creates a full-frame
// CPU base texture; the sidecar owns crop, blur, shadow and composition.
func PrepareGPUMusicSceneInput(ctx context.Context, ffmpeg string, input AudioRenderInput, width, height int) (AudioRenderInput, error) {
	layout, err := LayoutForSize(width, height)
	if err != nil {
		return AudioRenderInput{}, fmt.Errorf("GPU scene layout: %w", err)
	}
	if strings.TrimSpace(input.ArtworkPath) != "" {
		if _, ok := normalizeArtwork(input.ArtworkPath); !ok {
			return AudioRenderInput{}, fmt.Errorf("GPU artwork input is not decodable: %q", input.ArtworkPath)
		}
	}

	mode := gpuArtworkForegroundMode(input, layout)
	if strings.TrimSpace(input.ArtworkPath) != "" {
		mode, err = AnalyzeVisualizerForeground(ctx, ffmpeg, input.ArtworkPath, nil, layout)
		if err != nil {
			return AudioRenderInput{}, fmt.Errorf("GPU foreground analysis: %w", err)
		}
	}
	// Missing artwork is represented by absent texture payloads. The sidecar
	// synthesizes the gradient, fingerprint and note before its GPU blur pass.
	input.ArtworkTexture = nil
	input.BackgroundArtworkTexture = nil
	if strings.TrimSpace(input.ArtworkPath) == "" {
		fonts, fontErr := VisualizerFonts()
		if fontErr != nil {
			return AudioRenderInput{}, fmt.Errorf("GPU fallback fonts: %w", fontErr)
		}
		noteMask, maskErr := fallbackNoteMaskMetadata(ctx, ffmpeg, fonts, layout.Artwork.W)
		if maskErr != nil {
			return AudioRenderInput{}, fmt.Errorf("GPU fallback note mask: %w", maskErr)
		}
		input.BackgroundArtworkTexture = &noteMask
	}
	input.BaseTexture = nil
	input.PreparedForeground = &mode
	input.PreparedGlyphAtlas, err = prepareExactGlyphAtlas(ctx, ffmpeg, input.Metadata, input.Analysis.Duration, layout, width, height)
	if err != nil {
		return AudioRenderInput{}, fmt.Errorf("GPU bounded text atlas: %w", err)
	}
	return input, nil
}

// RenderVisualizerReferenceBaseForInput is comparison-only. It reproduces the
// canonical CPU static base, including the two-pass fallback accent selection,
// so native GPU base probes are measured against the real reference pixels.
func RenderVisualizerReferenceBaseForInput(ctx context.Context, ffmpeg string, input AudioRenderInput, width, height int) (*image.RGBA, ForegroundMode, error) {
	layout, err := LayoutForSize(width, height)
	if err != nil {
		return nil, ForegroundMode{}, err
	}
	fonts, err := VisualizerFonts()
	if err != nil {
		return nil, ForegroundMode{}, err
	}
	var fallback *image.RGBA
	var rerender func(color.RGBA) (*image.RGBA, error)
	if strings.TrimSpace(input.ArtworkPath) == "" {
		fallback, err = RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, color.RGBA{255, 255, 255, 224}, layout.Artwork.W)
		if err != nil {
			return nil, ForegroundMode{}, err
		}
		rerender = func(accent color.RGBA) (*image.RGBA, error) {
			return RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, accent, layout.Artwork.W)
		}
	}
	return RenderVisualizerBaseCPUWithFallback(ctx, ffmpeg, input.ArtworkPath, fallback, rerender, layout)
}

// RenderVisualizerReferenceBlurredForInput exposes the exact initial CPU
// background pass for diagnostic comparison with the native GPU blur.
func RenderVisualizerReferenceBlurredForInput(ctx context.Context, ffmpeg string, input AudioRenderInput, width, height int) (*image.RGBA, error) {
	layout, err := LayoutForSize(width, height)
	if err != nil {
		return nil, err
	}
	var fallback *image.RGBA
	if strings.TrimSpace(input.ArtworkPath) == "" {
		fonts, fontErr := VisualizerFonts()
		if fontErr != nil {
			return nil, fontErr
		}
		fallback, err = RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, color.RGBA{255, 255, 255, 224}, layout.Artwork.W)
		if err != nil {
			return nil, err
		}
	}
	return RenderVisualizerBlurredBackgroundCPU(ctx, ffmpeg, input.ArtworkPath, fallback, layout)
}

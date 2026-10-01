package nicorender

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
)

// timelineCaptureValidationState owns the collect path's cross-element checks
// and decoded texture accounting while each browser result is consumed.
type timelineCaptureValidationState struct {
	report        *TimelineCaptureReport
	metrics       *TimelineCaptureMetrics
	texturePixels map[uint32]timelineCaptureTexture
	lastTextureID uint32
	rawAssetBytes int64
}

func newTimelineCaptureValidationState(report *TimelineCaptureReport, metrics *TimelineCaptureMetrics) *timelineCaptureValidationState {
	return &timelineCaptureValidationState{
		report: report, metrics: metrics, texturePixels: make(map[uint32]timelineCaptureTexture),
	}
}

func (state *timelineCaptureValidationState) validateElement(meta timelineCommentMeta, probes []int32, element *timelineCaptureElementResult) (timelineCapturedElement, error) {
	if state == nil || state.report == nil || state.metrics == nil || state.texturePixels == nil || element == nil {
		return timelineCapturedElement{}, errors.New("niconico timeline: invalid capture validation state")
	}
	if element.Index != meta.Index || len(element.Samples) != len(probes) {
		return timelineCapturedElement{}, fmt.Errorf("niconico timeline: browser returned mismatched element %d", meta.Index)
	}
	if element.SpriteMetrics != nil {
		browserMetrics := element.SpriteMetrics
		if err := validateTimelineCaptureMetrics(browserMetrics); err != nil {
			return timelineCapturedElement{}, err
		}
		state.metrics.TextureCreations = browserMetrics.TextureCreations
		state.metrics.Mode = browserMetrics.Mode
		state.metrics.DrawWallMs = browserMetrics.DrawWallMs
		state.metrics.ReadbackCalls = browserMetrics.ReadbackCalls
		state.metrics.ReadbackBytes = browserMetrics.ReadbackBytes
		state.metrics.ReadbackWallMs = browserMetrics.ReadbackWallMs
		state.metrics.PBOTextures = browserMetrics.PBOTextures
		state.metrics.PBODrainCalls = browserMetrics.PBODrainCalls
		state.metrics.PBOEnqueueWallMs = browserMetrics.PBOEnqueueWallMs
		state.metrics.PBODrainWallMs = browserMetrics.PBODrainWallMs
		state.metrics.PBOFenceWaitWallMs = browserMetrics.PBOFenceWaitWallMs
		state.metrics.PBOCopyWallMs = browserMetrics.PBOCopyWallMs
		state.metrics.PBOBytes = browserMetrics.PBOBytes
		state.metrics.PBOFallbacks = browserMetrics.PBOFallbacks
		state.metrics.PBOFallbackReason = browserMetrics.PBOFallbackReason
		state.metrics.PBOPeakBytes = browserMetrics.PBOPeakBytes
		state.metrics.PendingPixelPeakBytes = browserMetrics.PendingPixelPeakBytes
		state.metrics.PackWallMs = browserMetrics.PackWallMs
		state.metrics.DeflateWallMs = browserMetrics.DeflateWallMs
		state.metrics.SerializeWallMs = browserMetrics.SerializeWallMs
		state.metrics.Valid = true
	}
	state.report.DrawCanvasCalls = element.DrawCanvasCalls
	state.report.ElementDrawCalls = element.ElementDrawCalls
	if state.report.DrawCanvasCalls != 0 {
		return timelineCapturedElement{}, fmt.Errorf("niconico timeline: drawCanvas was called during element capture")
	}
	if state.report.ElementDrawCalls > state.report.EligibleComments {
		return timelineCapturedElement{}, fmt.Errorf("niconico timeline: element draw bound exceeded")
	}
	for sampleIndex := range element.Samples {
		sample := &element.Samples[sampleIndex]
		if sample.VPos != probes[sampleIndex] {
			return timelineCapturedElement{}, fmt.Errorf("niconico timeline: comment %d probe vpos=%d, want %d", meta.Index, sample.VPos, probes[sampleIndex])
		}
		if len(sample.Commands) > timelineCaptureDrawsPerItem {
			return timelineCapturedElement{}, timelineUnsupported(fmt.Sprintf("comment %d has too many primitives", meta.Index))
		}
		for _, command := range sample.Commands {
			if command.ID == 0 {
				return timelineCapturedElement{}, timelineUnsupported(fmt.Sprintf("comment %d emits a rectangle or unknown primitive", meta.Index))
			}
		}
		if err := decodeAndReleaseTimelineSampleTextures(sample, state.texturePixels, &state.lastTextureID, &state.rawAssetBytes, &state.report.BrowserAssetPayloadBytes); err != nil {
			return timelineCapturedElement{}, fmt.Errorf("niconico timeline: comment %d textures: %w", meta.Index, err)
		}
	}
	return timelineCapturedElement{Meta: meta, Samples: element.Samples}, nil
}

func validateTimelineCaptureMetrics(browserMetrics *browserSpriteCaptureMetrics) error {
	if browserMetrics == nil {
		return errors.New("niconico timeline: missing sprite capture metrics")
	}
	if (browserMetrics.Mode != "sync" && browserMetrics.Mode != "pbo") ||
		browserMetrics.TextureCreations < 0 || browserMetrics.ReadbackCalls < 0 || browserMetrics.ReadbackBytes < 0 ||
		browserMetrics.PBOTextures < 0 || browserMetrics.PBODrainCalls < 0 || browserMetrics.PBOBytes < 0 || browserMetrics.PBOFallbacks < 0 || browserMetrics.PBOPeakBytes < 0 || browserMetrics.PendingPixelPeakBytes < 0 ||
		math.IsNaN(browserMetrics.DrawWallMs) || math.IsInf(browserMetrics.DrawWallMs, 0) || browserMetrics.DrawWallMs < 0 ||
		math.IsNaN(browserMetrics.ReadbackWallMs) || math.IsInf(browserMetrics.ReadbackWallMs, 0) || browserMetrics.ReadbackWallMs < 0 ||
		math.IsNaN(browserMetrics.PBODrainWallMs) || math.IsInf(browserMetrics.PBODrainWallMs, 0) || browserMetrics.PBODrainWallMs < 0 ||
		math.IsNaN(browserMetrics.PBOEnqueueWallMs) || math.IsInf(browserMetrics.PBOEnqueueWallMs, 0) || browserMetrics.PBOEnqueueWallMs < 0 ||
		math.IsNaN(browserMetrics.PBOFenceWaitWallMs) || math.IsInf(browserMetrics.PBOFenceWaitWallMs, 0) || browserMetrics.PBOFenceWaitWallMs < 0 ||
		math.IsNaN(browserMetrics.PBOCopyWallMs) || math.IsInf(browserMetrics.PBOCopyWallMs, 0) || browserMetrics.PBOCopyWallMs < 0 ||
		math.IsNaN(browserMetrics.PackWallMs) || math.IsInf(browserMetrics.PackWallMs, 0) || browserMetrics.PackWallMs < 0 ||
		math.IsNaN(browserMetrics.DeflateWallMs) || math.IsInf(browserMetrics.DeflateWallMs, 0) || browserMetrics.DeflateWallMs < 0 ||
		math.IsNaN(browserMetrics.SerializeWallMs) || math.IsInf(browserMetrics.SerializeWallMs, 0) || browserMetrics.SerializeWallMs < 0 {
		return errors.New("niconico timeline: browser returned invalid sprite capture metrics")
	}
	return nil
}

func decodeAndReleaseTimelineSampleTextures(sample *timelineCaptureSample, texturePixels map[uint32]timelineCaptureTexture, lastTextureID *uint32, rawAssetBytes, browserAssetPayloadBytes *int64) error {
	if sample == nil || texturePixels == nil || lastTextureID == nil || rawAssetBytes == nil || browserAssetPayloadBytes == nil {
		return errors.New("invalid timeline texture capture state")
	}
	newBytes, err := preflightTimelineSpriteTextures(sample.Textures)
	if err != nil {
		return timelineUnsupported(err.Error())
	}
	if *rawAssetBytes > MaxTimelineTotalAssetBytes-newBytes {
		return timelineUnsupported("total browser texture bytes exceed the NCT1 asset bound")
	}
	if newBytes > timelineCapturePerElementMax {
		return timelineUnsupported("one browser capture batch exceeds the bounded CDP payload")
	}
	for _, rawTexture := range sample.Textures {
		if rawTexture.ID == 0 || rawTexture.ID <= *lastTextureID {
			return fmt.Errorf("browser texture IDs are not strictly increasing at %d", rawTexture.ID)
		}
		*lastTextureID = rawTexture.ID
		if _, exists := texturePixels[rawTexture.ID]; exists {
			return fmt.Errorf("duplicate browser texture id %d", rawTexture.ID)
		}
		pixels, payloadBytes, err := decodeSpriteTexture(rawTexture)
		if err != nil {
			return fmt.Errorf("decode browser texture %d: %w", rawTexture.ID, err)
		}
		*browserAssetPayloadBytes += payloadBytes
		digest := sha256.Sum256(pixels)
		texturePixels[rawTexture.ID] = timelineCaptureTexture{Width: rawTexture.Width, Height: rawTexture.Height, RGBA: pixels, SHA256: digest}
		*rawAssetBytes += int64(len(pixels))
	}
	// Keep command samples for timeline assembly, but release the base64 data as
	// soon as it has been decoded instead of retaining it for every comment.
	sample.Textures = nil
	return nil
}

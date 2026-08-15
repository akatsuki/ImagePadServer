package video

import (
	"fmt"
	"math"
	"strings"
)

// MusicSceneDocumentSchema is the version of the bounded JSON handoff used by
// the CPU reference and the Draft GPU exporter. It carries only frame inputs;
// large immutable artwork/glyph assets continue through their existing paths.
const MusicSceneDocumentSchema uint16 = 1

type MusicSceneDocument struct {
	Schema           uint16                        `json:"schema"`
	Width            uint32                        `json:"width"`
	Height           uint32                        `json:"height"`
	FPS              uint32                        `json:"fps"`
	Frames           uint32                        `json:"frames"`
	DurationSeconds  float64                       `json:"duration_seconds"`
	PCMWindowSamples uint32                        `json:"pcm_window_samples"`
	SampleRateHz     uint32                        `json:"sample_rate_hz"`
	Metadata         AudioMetadata                 `json:"metadata"`
	LayoutRects      [MusicMaxLayoutRects][4]int32 `json:"layout_rects"`
	Palette          [4][4]uint8                   `json:"palette"`
	FramesPayload    []MusicSceneDocumentFrame     `json:"frames_payload"`
}

type MusicSceneDocumentFrame struct {
	FrameIndex  uint32     `json:"frame_index"`
	PTSNs       int64      `json:"pts_ns"`
	SpectrumQ16 [24]uint16 `json:"spectrum_q16"`
	WaveformQ16 []uint16   `json:"waveform_q16,omitempty"`
	LoudnessQ16 []uint16   `json:"loudness_q16"`
	ProgressQ16 uint32     `json:"progress_q16"`
	FadeInQ16   uint32     `json:"fade_in_q16"`
	FadeOutQ16  uint32     `json:"fade_out_q16"`
	RMSQ15      uint16     `json:"rms_q15"`
	PeakQ15     uint16     `json:"peak_q15"`
	TextLines   [4]string  `json:"text_lines"`
	Fingerprint string     `json:"fingerprint,omitempty"`
}

// NewMusicSceneDocument snapshots the canonical Go scene normalization into a
// bounded, language-neutral frame document. Both renderers can consume this
// without re-running different FFT/loudness/quantization code.
func NewMusicSceneDocument(input AudioRenderInput, width, height, fps, frames, pcmWindowSamples, sampleRateHz uint32) (MusicSceneDocument, error) {
	if width == 0 || height == 0 || fps == 0 || frames == 0 {
		return MusicSceneDocument{}, fmt.Errorf("invalid scene document dimensions or frame clock")
	}
	if pcmWindowSamples == 0 || sampleRateHz == 0 {
		return MusicSceneDocument{}, fmt.Errorf("invalid scene document PCM contract")
	}
	if len(input.Analysis.Frames) == 0 {
		return MusicSceneDocument{}, fmt.Errorf("scene document requires analyzed frames")
	}

	doc := MusicSceneDocument{
		Schema:           MusicSceneDocumentSchema,
		Width:            width,
		Height:           height,
		FPS:              fps,
		Frames:           frames,
		DurationSeconds:  input.Analysis.Duration,
		PCMWindowSamples: pcmWindowSamples,
		SampleRateHz:     sampleRateHz,
		Metadata:         input.Metadata,
		FramesPayload:    make([]MusicSceneDocumentFrame, frames),
	}
	if doc.DurationSeconds < 0 || math.IsNaN(doc.DurationSeconds) || math.IsInf(doc.DurationSeconds, 0) {
		doc.DurationSeconds = float64(frames) / float64(fps)
	}

	sceneBuilder := NewCanonicalMusicSceneBuilder(input)
	for index := uint32(0); index < frames; index++ {
		ptsNS := int64(index) * int64(1_000_000_000/fps)
		scene := sceneBuilder.Scene(uint64(index), ptsNS)
		if index == 0 {
			doc.LayoutRects = sceneLayoutRects(scene.Layout)
			doc.Palette = scene.Palette.PrimaryPalette()
		}
		var spectrum [24]uint16
		copy(spectrum[:], scene.Feature.SpectrumQ16)
		loudness := append([]uint16(nil), scene.Dynamics.LoudnessEnvelope...)
		if len(loudness) != MusicMaxLoudnessSamples {
			return MusicSceneDocument{}, fmt.Errorf("frame %d loudness payload has %d samples, want %d", index, len(loudness), MusicMaxLoudnessSamples)
		}
		text := [4]string{}
		if scene.GlyphAtlas != nil {
			for i, run := range scene.GlyphAtlas.TextRuns {
				if i >= len(text) {
					break
				}
				text[i] = run.Text
			}
		}
		doc.FramesPayload[index] = MusicSceneDocumentFrame{
			FrameIndex:  index,
			PTSNs:       scene.Feature.PTSNs,
			SpectrumQ16: spectrum,
			WaveformQ16: normalizeSceneWaveformQ16(scene.Feature.WaveformQ16),
			LoudnessQ16: loudness,
			ProgressQ16: uint32(math.Round(sceneDocumentClamp01(scene.Dynamics.ProgressRatio) * 65535)),
			FadeInQ16:   uint32(math.Round(sceneDocumentClamp01(float64(scene.Dynamics.EdgeFadeAlpha)) * 65535)),
			FadeOutQ16:  uint32(math.Round(sceneDocumentClamp01(float64(scene.Dynamics.EndFadeAlpha)) * 65535)),
			RMSQ15:      scene.Feature.RMSQ15,
			PeakQ15:     scene.Feature.PeakQ15,
			TextLines:   text,
			Fingerprint: scene.Fingerprint,
		}
	}
	return doc, doc.Validate()
}

// ApplyMusicSceneDocument replaces the analysis values used by the CPU
// reference with the exact Q16 frame payload from the shared document.
func ApplyMusicSceneDocument(input AudioRenderInput, doc MusicSceneDocument) (AudioRenderInput, error) {
	if err := doc.Validate(); err != nil {
		return AudioRenderInput{}, err
	}
	input.Metadata = doc.Metadata
	input.Analysis.FPS = int(doc.FPS)
	input.Analysis.Duration = doc.DurationSeconds
	input.Analysis.Frames = make([]AudioFrame, len(doc.FramesPayload))
	input.Analysis.WaveformFrames = make([][]uint16, len(doc.FramesPayload))
	for i, frame := range doc.FramesPayload {
		for band, value := range frame.SpectrumQ16 {
			input.Analysis.Frames[i].Spectrum24[band] = float64(value) / 65535.0
		}
		input.Analysis.WaveformFrames[i] = append([]uint16(nil), frame.WaveformQ16...)
	}
	for i := range input.Analysis.Features.LoudnessEnvelope {
		if len(doc.FramesPayload) == 0 || i >= len(doc.FramesPayload[0].LoudnessQ16) {
			break
		}
		input.Analysis.Features.LoudnessEnvelope[i] = float64(doc.FramesPayload[0].LoudnessQ16[i]) / 65535.0
	}
	return input, nil
}

func (d MusicSceneDocument) Validate() error {
	if d.Schema != MusicSceneDocumentSchema || d.Width == 0 || d.Height == 0 || d.FPS == 0 || d.Frames == 0 {
		return fmt.Errorf("invalid music scene document header")
	}
	if d.PCMWindowSamples == 0 || d.SampleRateHz == 0 || len(d.FramesPayload) != int(d.Frames) {
		return fmt.Errorf("invalid music scene document frame contract")
	}
	if d.DurationSeconds < 0 || math.IsNaN(d.DurationSeconds) || math.IsInf(d.DurationSeconds, 0) {
		return fmt.Errorf("invalid music scene document duration")
	}
	for i, frame := range d.FramesPayload {
		if frame.FrameIndex != uint32(i) || len(frame.LoudnessQ16) != MusicMaxLoudnessSamples || len(frame.WaveformQ16) > MusicMaxWaveformSamples || len(frame.WaveformQ16)%2 != 0 {
			return fmt.Errorf("invalid music scene document frame %d", i)
		}
		for _, text := range frame.TextLines {
			if len(strings.TrimSpace(text)) > MusicMaxTextBytes {
				return fmt.Errorf("music scene document text is too large")
			}
		}
	}
	return nil
}

func normalizeSceneWaveformQ16(waveform []uint16) []uint16 {
	if len(waveform) == 0 {
		return nil
	}
	normalized := append([]uint16(nil), waveform...)
	if len(normalized)%2 != 0 {
		normalized = append(normalized, normalized[len(normalized)-1])
	}
	return normalized
}

func sceneLayoutRects(layout MusicSceneLayout) [MusicMaxLayoutRects][4]int32 {
	return [MusicMaxLayoutRects][4]int32{
		{int32(layout.Artwork.X), int32(layout.Artwork.Y), int32(layout.Artwork.W), int32(layout.Artwork.H)},
		{int32(layout.Title.X), int32(layout.Title.Y), int32(layout.Title.W), int32(layout.Title.H)},
		{int32(layout.Artist.X), int32(layout.Artist.Y), int32(layout.Artist.W), int32(layout.Artist.H)},
		{int32(layout.Album.X), int32(layout.Album.Y), int32(layout.Album.W), int32(layout.Album.H)},
		{int32(layout.Spectrum.X), int32(layout.Spectrum.Y), int32(layout.Spectrum.W), int32(layout.Spectrum.H)},
		{int32(layout.Loudness.X), int32(layout.Loudness.Y), int32(layout.Loudness.W), int32(layout.Loudness.H)},
		{int32(layout.Progress.X), int32(layout.Progress.Y), int32(layout.Progress.W), int32(layout.Progress.H)},
		{int32(layout.Time.X), int32(layout.Time.Y), int32(layout.Time.W), int32(layout.Time.H)},
	}
}

func (p MusicScenePalette) PrimaryPalette() [4][4]uint8 {
	return [4][4]uint8{p.Primary, p.Accent, p.Background, p.Overlay}
}

func sceneDocumentClamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

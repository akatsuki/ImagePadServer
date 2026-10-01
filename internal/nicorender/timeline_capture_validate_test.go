package nicorender

import (
	"strings"
	"testing"
)

func TestTimelineCaptureValidationRejectsInvalidElementResults(t *testing.T) {
	valid := func() (*timelineCaptureValidationState, timelineCommentMeta, []int32, timelineCaptureElementResult) {
		report := &TimelineCaptureReport{EligibleComments: 1}
		metrics := &TimelineCaptureMetrics{}
		state := newTimelineCaptureValidationState(report, metrics)
		meta := timelineCommentMeta{Index: 4, OwnerOrder: 2}
		probes := []int32{10}
		element := timelineCaptureElementResult{
			Index:   4,
			Samples: []timelineCaptureSample{timelineTestSample(10, 20, []spriteJSONCommand{timelineTestCommand(1, 20)})},
		}
		return state, meta, probes, element
	}

	tests := []struct {
		name   string
		mutate func(*timelineCaptureValidationState, *timelineCommentMeta, *[]int32, *timelineCaptureElementResult)
		want   string
	}{
		{
			name: "element identity",
			mutate: func(_ *timelineCaptureValidationState, _ *timelineCommentMeta, _ *[]int32, element *timelineCaptureElementResult) {
				element.Index++
			},
			want: "mismatched element 4",
		},
		{
			name: "sample count",
			mutate: func(_ *timelineCaptureValidationState, _ *timelineCommentMeta, _ *[]int32, element *timelineCaptureElementResult) {
				element.Samples = nil
			},
			want: "mismatched element 4",
		},
		{
			name: "sprite metrics",
			mutate: func(_ *timelineCaptureValidationState, _ *timelineCommentMeta, _ *[]int32, element *timelineCaptureElementResult) {
				element.SpriteMetrics = &browserSpriteCaptureMetrics{Mode: "invalid"}
			},
			want: "invalid sprite capture metrics",
		},
		{
			name: "draw canvas calls",
			mutate: func(_ *timelineCaptureValidationState, _ *timelineCommentMeta, _ *[]int32, element *timelineCaptureElementResult) {
				element.DrawCanvasCalls = 1
			},
			want: "drawCanvas was called during element capture",
		},
		{
			name: "element draw bound",
			mutate: func(_ *timelineCaptureValidationState, _ *timelineCommentMeta, _ *[]int32, element *timelineCaptureElementResult) {
				element.ElementDrawCalls = 2
			},
			want: "element draw bound exceeded",
		},
		{
			name: "probe position",
			mutate: func(_ *timelineCaptureValidationState, _ *timelineCommentMeta, _ *[]int32, element *timelineCaptureElementResult) {
				element.Samples[0].VPos++
			},
			want: "comment 4 probe vpos=11, want 10",
		},
		{
			name: "unknown primitive",
			mutate: func(_ *timelineCaptureValidationState, _ *timelineCommentMeta, _ *[]int32, element *timelineCaptureElementResult) {
				element.Samples[0].Commands[0].ID = 0
			},
			want: "comment 4 emits a rectangle or unknown primitive",
		},
		{
			name: "primitive count",
			mutate: func(_ *timelineCaptureValidationState, _ *timelineCommentMeta, _ *[]int32, element *timelineCaptureElementResult) {
				commands := make([]spriteJSONCommand, timelineCaptureDrawsPerItem+1)
				for i := range commands {
					commands[i] = timelineTestCommand(1, 20)
				}
				element.Samples[0].Commands = commands
			},
			want: "comment 4 has too many primitives",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, meta, probes, element := valid()
			tt.mutate(state, &meta, &probes, &element)
			_, err := state.validateElement(meta, probes, &element)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateElement error=%v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestTimelineCaptureValidationPreservesMetricOverwriteAndTextureRelease(t *testing.T) {
	report := &TimelineCaptureReport{EligibleComments: 2}
	metrics := &TimelineCaptureMetrics{}
	state := newTimelineCaptureValidationState(report, metrics)
	makeElement := func(index uint32, textureID uint32, drawCalls int, metrics *browserSpriteCaptureMetrics) (timelineCommentMeta, []int32, timelineCaptureElementResult) {
		meta := timelineCommentMeta{Index: index, OwnerOrder: 1}
		probes := []int32{10}
		sample := timelineTestSample(10, 20, []spriteJSONCommand{timelineTestCommand(textureID, 20)})
		sample.Textures = []spriteJSONTexture{{ID: textureID, Width: 1, Height: 1, Encoding: "rgba", Data: "AQIDBA=="}}
		return meta, probes, timelineCaptureElementResult{
			Index: index, Samples: []timelineCaptureSample{sample}, ElementDrawCalls: drawCalls,
			SpriteMetrics: metrics,
		}
	}

	firstMeta, probes, first := makeElement(3, 1, 1, &browserSpriteCaptureMetrics{Mode: "sync", TextureCreations: 1})
	if _, err := state.validateElement(firstMeta, probes, &first); err != nil {
		t.Fatalf("validate first element: %v", err)
	}
	if first.Samples[0].Textures != nil {
		t.Fatal("encoded first-element texture payload was not released")
	}

	secondMeta, probes, second := makeElement(4, 2, 2, &browserSpriteCaptureMetrics{Mode: "pbo", TextureCreations: 5})
	second.DrawCanvasCalls = 0
	if _, err := state.validateElement(secondMeta, probes, &second); err != nil {
		t.Fatalf("validate second element: %v", err)
	}
	if second.Samples[0].Textures != nil {
		t.Fatal("encoded second-element texture payload was not released")
	}
	if got := metrics.TextureCreations; got != 5 || metrics.Mode != "pbo" {
		t.Fatalf("sprite metrics were not overwritten by the latest element: mode=%q texture_creations=%d", metrics.Mode, got)
	}
	if got := report.ElementDrawCalls; got != 2 || got == first.ElementDrawCalls+second.ElementDrawCalls || report.DrawCanvasCalls != 0 {
		t.Fatalf("report draw counters after second element = canvas %d element %d, want last-element overwrite 2 (not sum %d)", report.DrawCanvasCalls, got, first.ElementDrawCalls+second.ElementDrawCalls)
	}
	if state.lastTextureID != 2 || state.rawAssetBytes != 8 || report.BrowserAssetPayloadBytes != 8 {
		t.Fatalf("texture state = lastID %d rawBytes %d payloadBytes %d", state.lastTextureID, state.rawAssetBytes, report.BrowserAssetPayloadBytes)
	}
	if len(state.texturePixels) != 2 || string(state.texturePixels[1].RGBA) != string([]byte{1, 2, 3, 4}) || string(state.texturePixels[2].RGBA) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("decoded texture map has unexpected contents: %+v", state.texturePixels)
	}
}

func TestTimelineCaptureValidationRejectsTextureIDRegression(t *testing.T) {
	report := &TimelineCaptureReport{EligibleComments: 2}
	state := newTimelineCaptureValidationState(report, &TimelineCaptureMetrics{})
	makeElement := func(index uint32) (timelineCommentMeta, timelineCaptureElementResult) {
		meta := timelineCommentMeta{Index: index}
		sample := timelineTestSample(10, 20, []spriteJSONCommand{timelineTestCommand(1, 20)})
		sample.Textures = []spriteJSONTexture{{ID: 1, Width: 1, Height: 1, Encoding: "rgba", Data: "AQIDBA=="}}
		return meta, timelineCaptureElementResult{Index: index, Samples: []timelineCaptureSample{sample}}
	}
	meta, first := makeElement(1)
	if _, err := state.validateElement(meta, []int32{10}, &first); err != nil {
		t.Fatalf("validate first element: %v", err)
	}
	meta, second := makeElement(2)
	if _, err := state.validateElement(meta, []int32{10}, &second); err == nil || !strings.Contains(err.Error(), "texture IDs are not strictly increasing at 1") {
		t.Fatalf("regressing texture ID error=%v", err)
	}
}

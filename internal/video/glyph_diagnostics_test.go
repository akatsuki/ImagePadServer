package video

import (
	"encoding/json"
	"testing"
)

func TestGlyphRenderDiagnosticsWireRoundTrip(t *testing.T) {
	f := GpuFrame{Schema: GPUContractVersion, Sequence: 1, PTSNs: 0,
		Width: 64, Height: 1, RowStride: 256, Format: PixelRGBA8,
		ColorSpace: ColorSRGB, Ownership: "OwnedByTransport", Payload: make([]byte, 256),
		GlyphDiagnostics: &GlyphRenderDiagnostics{Count: 1, Instances: []GlyphInstanceDiagnostic{{
			ID: "A", Screen: [4]float32{.1, .2, .3, .4}, Atlas: [4]float32{.01, .02, .03, .04},
			Color: [4]float32{1, 0, 0, 1}, Scale: .8, Baseline: 42, InkTop: 7, CellPadding: [4]float32{16, 24, 0, 0},
		}}},
	}
	b, err := json.Marshal(f)
	if err != nil { t.Fatal(err) }
	var got GpuFrame
	if err := json.Unmarshal(b, &got); err != nil { t.Fatal(err) }
	if got.GlyphDiagnostics == nil || got.GlyphDiagnostics.Count != 1 || got.GlyphDiagnostics.Instances[0].ID != "A" {
		t.Fatalf("glyph diagnostics lost: %+v", got.GlyphDiagnostics)
	}
}

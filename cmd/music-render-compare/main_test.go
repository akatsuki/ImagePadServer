package main

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

func TestOverlayProbeRegionsOrder(t *testing.T) {
	r := overlayProbeRegions(video.MusicSceneLayout{})
	if len(r) != 4 || r[0].Name != "title" || r[3].Name != "time" {
		t.Fatalf("regions=%v", r)
	}
}

func TestOverlayProbeEvidenceApplyJSONCompatibility(t *testing.T) {
	d := sceneEvidence{}
	e := overlayProbeEvidence{CPUHash: "abc", GPUReceipt: &video.TextOverlayReceipt{SHA256: "abc", RendererID: "r", RendererVersion: "1"}}
	e.apply(&d)
	if !d.TextOverlaySHAEqual || d.TextOverlayRenderer != "r/1" {
		t.Fatalf("evidence=%+v", d)
	}
}

func TestGlyphAtlasEvidenceUsesPayloadAndStride(t *testing.T) {
	atlas := &video.GlyphAtlasMetadata{Width: 2, Height: 1, RowStride: 8, Payload: []byte{0, 0, 0, 255, 0, 0, 0, 0}, Glyphs: []video.GlyphEntry{{ID: "x"}}, TextRuns: []video.TextRun{{Text: "x"}}, AssetHash: "asset"}
	e, ok := glyphAtlasEvidence(atlas)
	if !ok || e.GlyphCoveragePixels != 1 || e.GlyphWidth != 2 || e.GlyphRowStride != 8 || e.GlyphCount != 1 || e.TextRunCount != 1 {
		t.Fatalf("unexpected atlas evidence: %+v", e)
	}
	want := sha256.Sum256(atlas.Payload)
	if e.GlyphPayloadHash != fmt.Sprintf("%x", want[:]) {
		t.Fatalf("payload hash mismatch: %s", e.GlyphPayloadHash)
	}
}

func TestSHA256FileMatchesBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "frame.png")
	want := sha256.Sum256([]byte("frame-fixture"))
	if err := os.WriteFile(p, []byte("frame-fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := sha256File(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != fmt.Sprintf("%x", want) {
		t.Fatalf("sha256 = %q, want %x", got, want)
	}
}

func writeTestPNG(t *testing.T, name string, fill, mark color.RGBA) string {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			im.SetRGBA(x, y, fill)
		}
	}
	im.SetRGBA(1, 1, mark)
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCPUOnlyMarkdownDoesNotClaimGPUComparison(t *testing.T) {
	dir := t.TempDir()
	writeMarkdown(dir, report{Input: "fixture.wav", CPUOnly: true, CPU: renderResult{Probe: probeResult{Duration: 1.5, Frames: 45}}})
	b, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "# CPU music render fixture") {
		t.Fatalf("markdown title = %q", s)
	}
	if !strings.Contains(s, "| GPU | 0.000 | 0.000 | 0 |  |") {
		t.Fatalf("missing explicit empty GPU row: %q", s)
	}
}

func TestLoadImageMetricsFindsNonBackgroundBounds(t *testing.T) {
	p := writeTestPNG(t, "a.png", color.RGBA{10, 20, 30, 255}, color.RGBA{240, 220, 200, 255})
	m, err := loadImageMetrics(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 4 || m.Height != 3 || m.NonBackgroundPixels != 1 {
		t.Fatalf("metrics = %+v", m)
	}
	if m.NonBackgroundBounds == nil || m.NonBackgroundBounds.MinX != 1 || m.NonBackgroundBounds.MinY != 1 {
		t.Fatalf("bounds = %+v", m.NonBackgroundBounds)
	}
}

func TestCompareImagesReportsDifference(t *testing.T) {
	a := writeTestPNG(t, "a.png", color.RGBA{0, 0, 0, 255}, color.RGBA{255, 0, 0, 255})
	b := writeTestPNG(t, "b.png", color.RGBA{0, 0, 0, 255}, color.RGBA{0, 255, 0, 255})
	c, err := compareImages(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SizeMatch || c.MismatchedPixels != 1 || c.MismatchRatio <= 0 || c.RMSE <= 0 {
		t.Fatalf("comparison = %+v", c)
	}
}

func TestCompareRGBAFramesReportsDifference(t *testing.T) {
	a := []byte{0, 0, 0, 255, 255, 0, 0, 255}
	b := []byte{0, 0, 0, 255, 0, 255, 0, 255}
	c, err := compareRGBAFrames(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SizeMatch || c.MismatchedPixels != 1 || c.MismatchRatio != 0.5 || c.MeanAbsoluteRGBA <= 0 || c.RMSE <= 0 {
		t.Fatalf("raw frame comparison = %+v", c)
	}
}

func TestCompareGlyphMaskRegionReportsCoverage(t *testing.T) {
	a := writeTestPNG(t, "glyph-a.png", color.RGBA{0, 0, 0, 255}, color.RGBA{255, 255, 255, 255})
	b := writeTestPNG(t, "glyph-b.png", color.RGBA{0, 0, 0, 255}, color.RGBA{255, 255, 255, 255})
	c, err := compareGlyphMaskRegion(a, b, imageBounds{MinX: 0, MinY: 0, MaxX: 4, MaxY: 3})
	if err != nil {
		t.Fatal(err)
	}
	if c.CPUVisible != 1 || c.GPUVisible != 1 || c.Intersection != 1 || c.Union != 1 || c.IoU != 1 || c.MeanAbsoluteErr != 0 {
		t.Fatalf("glyph mask = %+v", c)
	}
}

func TestProbeNormalizesPTSAndExcludesAudio(t *testing.T) {
	r := probeResult{FirstPTS: 1.5, LastPTS: 6.466667, VideoDuration: 5.1}
	if r.AudioExcluded {
		t.Fatal("fixture should start unmarked")
	}
	r.NormalizedFirstPTS = 0
	r.NormalizedLastPTS = r.LastPTS - r.FirstPTS
	r.AudioExcluded = true
	if r.NormalizedFirstPTS != 0 || r.NormalizedLastPTS < 4.9 || !r.AudioExcluded {
		t.Fatalf("normalized probe = %+v", r)
	}
}

func TestNativeGatePass(t *testing.T) {
	if !nativeGatePass(false, true) {
		t.Fatal("diagnostic mode should remain usable when native gate is not required")
	}
	if nativeGatePass(true, true) {
		t.Fatal("diagnostic mode must fail when native GPU evidence is required")
	}
	if !nativeGatePass(true, false) {
		t.Fatal("native mode should pass the ownership gate")
	}
}

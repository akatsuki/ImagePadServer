package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
)

// ExpandMusicGlyphManifest applies the same bounded expansion contract as the
// GPU sidecar. Coordinates are normalized to the requested render target.
func ExpandMusicGlyphManifest(scene *MusicScenePayload, width, height uint32) GlyphInstanceManifest {
	m := GlyphInstanceManifest{}
	if scene == nil || scene.GlyphAtlas == nil || width == 0 || height == 0 {
		return m
	}
	a := scene.GlyphAtlas
	sx, sy := float32(width)/1280, float32(height)/720
	for _, run := range a.TextRuns {
		if len(m.Instances) >= 256 {
			break
		}
		cursor := run.X * sx
		for _, ch := range []rune(run.Text) {
			if len(m.Instances) >= 256 {
				break
			}
			id := string(ch)
			var g *GlyphEntry
			for i := range a.Glyphs {
				if a.Glyphs[i].ID == id {
					g = &a.Glyphs[i]
					break
				}
			}
			if g == nil {
				for i := range a.Glyphs {
					if a.Glyphs[i].ID == a.MissingGlyphID {
						g = &a.Glyphs[i]
						break
					}
				}
			}
			if g == nil {
				continue
			}
			const atlasFaceSize float32 = 48
			const atlasInkTop float32 = 7
			scale := run.SizePx / atlasFaceSize
			sw := float32(g.Width) * scale
			if sw < 1 {
				sw = 1
			}
			sh := float32(g.Height) * scale * sy
			if sh < 1 {
				sh = 1
			}
			screenY := run.Y - atlasInkTop*scale
			if screenY < 0 {
				screenY = 0
			}
			rgba := [4]float32{float32(run.RGBA[0]) / 255, float32(run.RGBA[1]) / 255, float32(run.RGBA[2]) / 255, float32(run.RGBA[3]) / 255 * clampGlyph(run.Opacity)}
			m.Instances = append(m.Instances, GlyphInstanceDiagnostic{
				ID: id, Screen: [4]float32{cursor / float32(width), screenY * sy / float32(height), sw * sx / float32(width), sh / float32(height)},
				Atlas: [4]float32{float32(g.X) / float32(a.Width), float32(g.Y) / float32(a.Height), float32(g.Width) / float32(a.Width), float32(g.Height) / float32(a.Height)},
				Color: rgba, Scale: scale, Baseline: run.Y * sy, InkTop: atlasInkTop * scale * sy,
				CellPadding: [4]float32{maxf(0, 64-float32(g.Width)), maxf(0, 64-float32(g.Height)), 0, 0},
			})
			adv := g.Advance
			if adv < float32(g.Width) {
				adv = float32(g.Width)
			}
			cursor += adv * scale * sx
		}
	}
	m.Count = len(m.Instances)
	if b, err := json.Marshal(m.Instances); err == nil {
		h := sha256.Sum256(b)
		m.SHA256 = hex.EncodeToString(h[:])
	}
	return m
}

func clampGlyph(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// CompareMusicGlyphManifest compares sidecar readback values with a CPU
// manifest. It reports every positional/value mismatch, bounded to avoid huge
// diagnostics in a report.
func CompareMusicGlyphManifest(cpu GlyphInstanceManifest, gpu *GlyphRenderDiagnostics) GlyphInstanceParity {
	p := GlyphInstanceParity{CPUCount: cpu.Count}
	if gpu == nil {
		p.GPUCount = -1
		p.Mismatches = cpu.Count
		p.FirstMismatch = "missing GPU diagnostics"
		p.CPUManifestSHA = cpu.SHA256
		return p
	}
	p.GPUCount = gpu.Count
	p.CPUManifestSHA = cpu.SHA256
	if b, e := json.Marshal(gpu.Instances); e == nil {
		h := sha256.Sum256(b)
		p.GPUManifestSHA = hex.EncodeToString(h[:])
	}
	n := cpu.Count
	if gpu.Count < n {
		n = gpu.Count
	}
	p.Matched = n
	for i := 0; i < n; i++ {
		a, b := cpu.Instances[i], gpu.Instances[i]
		if a.ID != b.ID || !sameFloats(a.Screen, b.Screen) || !sameFloats(a.Atlas, b.Atlas) || !sameFloats(a.Color, b.Color) || !closeGlyph(a.Scale, b.Scale) || !closeGlyph(a.Baseline, b.Baseline) || !closeGlyph(a.InkTop, b.InkTop) || !sameFloats(a.CellPadding, b.CellPadding) {
			p.Mismatches++
			if p.FirstMismatch == "" {
				p.FirstMismatch = fmt.Sprintf("index=%d cpu=%s gpu=%s", i, a.ID, b.ID)
			}
		}
	}
	if cpu.Count != gpu.Count {
		p.Mismatches += int(math.Abs(float64(cpu.Count - gpu.Count)))
		if p.FirstMismatch == "" {
			p.FirstMismatch = fmt.Sprintf("count cpu=%d gpu=%d", cpu.Count, gpu.Count)
		}
	}
	return p
}
func closeGlyph(a, b float32) bool { return float32(math.Abs(float64(a-b))) <= 1e-5 }
func sameFloats(a, b [4]float32) bool {
	for i := range a {
		if !closeGlyph(a[i], b[i]) {
			return false
		}
	}
	return true
}

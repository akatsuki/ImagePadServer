package video

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Palette holds two RGBA colors for a gradient fill.
type Palette struct {
	Start, End color.RGBA
}

// PaletteForFeatures selects a mood palette using the first-match order
// defined in the visualizer design specification section 14.1.
//
//  1. High energy:      BPM >= 130 or integrated loudness >= -11 LUFS
//  2. Bass-focused:     energy ratio at 20..250 Hz  >= 0.45
//  3. Bright:           spectral centroid >= 3500 Hz
//  4. Calm:             BPM < 95 and integrated loudness <= -16 LUFS
//  5. Balanced/default: all other tracks
func PaletteForFeatures(features AudioFeatures) Palette {
	switch {
	case features.BPM >= 130 || features.IntegratedLUFS >= -11:
		return Palette{
			Start: color.RGBA{122, 29, 79, 255},  // #7A1D4F
			End:   color.RGBA{255, 107, 53, 255}, // #FF6B35
		}
	case features.LowFrequencyRatio >= 0.45:
		return Palette{
			Start: color.RGBA{36, 16, 63, 255},   // #24103F
			End:   color.RGBA{124, 58, 237, 255}, // #7C3AED
		}
	case features.SpectralCentroid >= 3500:
		return Palette{
			Start: color.RGBA{11, 85, 99, 255},   // #0B5563
			End:   color.RGBA{32, 199, 201, 255}, // #20C7C9
		}
	case features.BPM < 95 && features.IntegratedLUFS <= -16:
		return Palette{
			Start: color.RGBA{31, 42, 68, 255},  // #1F2A44
			End:   color.RGBA{94, 92, 230, 255}, // #5E5CE6
		}
	default:
		return Palette{
			Start: color.RGBA{23, 59, 87, 255},   // #173B57
			End:   color.RGBA{58, 134, 255, 255}, // #3A86FF
		}
	}
}

// RenderFallbackArtwork produces a deterministic fallback artwork tile.
//
//  1. Creates a square RGBA image of the given size.
//  2. Fills with a top-to-bottom gradient from Palette.Start to Palette.End.
//  3. Draws 64 radial fingerprint lines (width 3, foreground RGB at 26 % opacity).
//  4. Renders the music note glyph "♪" using FFmpeg drawtext and composites it
//     at the center with 88 % opacity of the caller-provided foreground color.
func RenderFallbackArtwork(ctx context.Context, ffmpeg string, fonts FontSet, features AudioFeatures, foreground color.RGBA, size int) (*image.RGBA, error) {
	if size <= 0 {
		return nil, fmt.Errorf("invalid size %d", size)
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// 2. Top-to-bottom gradient fill.
	palette := PaletteForFeatures(features)
	fillGradient(img, palette.Start, palette.End)

	// 3. Radial fingerprint at 26% opacity.
	fgFingerprint := color.RGBA{
		R: foreground.R,
		G: foreground.G,
		B: foreground.B,
		A: uint8(math.Round(0.26 * 255)), // 26% opacity
	}
	drawFingerprint(img, features.Fingerprint64, fgFingerprint, size)

	// 4. Music note glyph via FFmpeg drawtext, composited at centre.
	glyph, err := renderGlyph(ctx, ffmpeg, fonts, foreground, size)
	if err != nil {
		return nil, fmt.Errorf("render glyph: %w", err)
	}
	draw.Draw(img, img.Bounds(), glyph, image.Point{}, draw.Over)

	return img, nil
}

// ---------------------------------------------------------------------------
// Gradient
// ---------------------------------------------------------------------------

// fillGradient fills img with a vertical linear gradient from start (top)
// to end (bottom).
func fillGradient(img *image.RGBA, start, end color.RGBA) {
	b := img.Bounds()
	h := b.Dy()
	if h < 2 {
		// Single row or empty – just fill with start.
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				img.SetRGBA(x, y, start)
			}
		}
		return
	}
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h-1)
		r := uint8(float64(start.R)*(1-t) + float64(end.R)*t)
		g := uint8(float64(start.G)*(1-t) + float64(end.G)*t)
		b_ := uint8(float64(start.B)*(1-t) + float64(end.B)*t)
		rowColor := color.RGBA{R: r, G: g, B: b_, A: 255}
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetRGBA(x, y+b.Min.Y, rowColor)
		}
	}
}

// ---------------------------------------------------------------------------
// Fingerprint
// ---------------------------------------------------------------------------

// drawFingerprint draws 64 round-capped radial lines representing the
// per-band energy averages.  Lines radiate from the image centre using the
// same angles as spec section 14.2.
func drawFingerprint(img *image.RGBA, fp [64]float64, fg color.RGBA, size int) {
	cx := float64(size) / 2
	cy := float64(size) / 2
	baseRadius := 54.0 * float64(size) / 288.0
	lengthScale := 58.0 * float64(size) / 288.0

	for i := 0; i < 64; i++ {
		angle := (-90.0 + float64(i)*360.0/64.0) * math.Pi / 180.0
		v := fp[i]
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}

		innerR := baseRadius
		outerR := baseRadius + v*lengthScale

		x1 := cx + innerR*math.Cos(angle)
		y1 := cy + innerR*math.Sin(angle)
		x2 := cx + outerR*math.Cos(angle)
		y2 := cy + outerR*math.Sin(angle)

		drawRoundCappedLine(img, x1, y1, x2, y2, fg, 3)
	}
}

// drawRoundCappedLine draws a thick line with round end caps between
// (x1,y1) and (x2,y2) with the given width and colour.
func drawRoundCappedLine(img *image.RGBA, x1, y1, x2, y2 float64, c color.RGBA, width int) {
	dx := x2 - x1
	dy := y2 - y1
	dist := math.Sqrt(dx*dx + dy*dy)
	if dist < 0.5 {
		drawDot(img, int(math.Round(x1)), int(math.Round(y1)), width, c)
		return
	}
	halfW := width / 2
	steps := int(dist) * 2
	if steps < 1 {
		steps = 1
	}
	// Allowed squared distance from the pixel centre for a round brush.
	rSq := halfW*halfW + halfW
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		px := int(math.Round(x1 + t*dx))
		py := int(math.Round(y1 + t*dy))
		for dy := -halfW; dy <= halfW; dy++ {
			for dx := -halfW; dx <= halfW; dx++ {
				if dx*dx+dy*dy <= rSq {
					blendPixel(img, px+dx, py+dy, c)
				}
			}
		}
	}
}

// drawDot draws a filled square block of the given width at (cx, cy).
func drawDot(img *image.RGBA, cx, cy, width int, c color.RGBA) {
	halfW := width / 2
	for dy := -halfW; dy <= halfW; dy++ {
		for dx := -halfW; dx <= halfW; dx++ {
			blendPixel(img, cx+dx, cy+dy, c)
		}
	}
}

// blendPixel composites colour c over the existing pixel at (x, y) using
// standard over-compositing.
func blendPixel(img *image.RGBA, x, y int, c color.RGBA) {
	b := img.Bounds()
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	if c.A == 255 {
		img.SetRGBA(x, y, c)
		return
	}
	if c.A == 0 {
		return
	}
	dst := img.RGBAAt(x, y)
	srcA := float64(c.A) / 255.0
	dstA := float64(dst.A) / 255.0
	outA := srcA + dstA*(1-srcA)
	if outA < 0.001 {
		return
	}
	r := uint8((float64(c.R)*srcA + float64(dst.R)*dstA*(1-srcA)) / outA)
	g := uint8((float64(c.G)*srcA + float64(dst.G)*dstA*(1-srcA)) / outA)
	bv := uint8((float64(c.B)*srcA + float64(dst.B)*dstA*(1-srcA)) / outA)
	img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: bv, A: uint8(outA * 255)})
}

// ---------------------------------------------------------------------------
// Glyph
// ---------------------------------------------------------------------------

// renderGlyph renders the music note "♪" to a transparent RGBA image using
// FFmpeg drawtext with the SemiBold 600-weight font.  The returned image is
// tinted with the provided foreground colour and its visual bounding box is
// centred on the tile.
func renderGlyph(ctx context.Context, ffmpeg string, fonts FontSet, fg color.RGBA, size int) (*image.RGBA, error) {
	glyph, err := renderGlyphWithFFmpeg(ctx, ffmpeg, fonts, fg, size)
	if err == nil {
		return glyph, nil
	}
	if !isMissingDrawtextError(err) {
		return nil, err
	}
	return renderGlyphWithGo(fonts, fg, size)
}

func renderGlyphWithFFmpeg(ctx context.Context, ffmpeg string, fonts FontSet, fg color.RGBA, size int) (*image.RGBA, error) {
	tmpDir, err := os.MkdirTemp("", "fallback-artwork-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	outPath := filepath.Join(tmpDir, "glyph.png")
	fontFile := fonts.SemiBold600
	fontFileFilter := strings.ReplaceAll(fontFile, "\\", "/")
	fontFileFilter = strings.ReplaceAll(fontFileFilter, ":", "\\:")

	fontSize := float64(size) * 0.58
	glyphColor := "white@0.88"
	args := []string{
		"-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("color=c=black@0:size=%dx%d:d=0.04,format=rgba", size, size),
		"-vf", fmt.Sprintf("drawtext=text='♪':fontfile='%s':fontsize=%.0f:fontcolor=%s:x=(w-text_w)/2:y=(h-text_h)/2",
			fontFileFilter, fontSize, glyphColor),
		"-frames:v", "1",
		outPath,
	}

	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	combOutput, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg drawtext: %w\n%s", err, string(combOutput))
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read glyph png: %w", err)
	}

	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode glyph png: %w", err)
	}

	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, a := src.At(x, y).RGBA()
			alpha := uint8(a >> 8)
			if alpha > 0 {
				dst.SetRGBA(x, y, color.RGBA{
					R: fg.R,
					G: fg.G,
					B: fg.B,
					A: alpha,
				})
			}
		}
	}

	return dst, nil
}

func renderGlyphWithGo(fonts FontSet, fg color.RGBA, size int) (*image.RGBA, error) {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	note := color.RGBA{R: fg.R, G: fg.G, B: fg.B, A: 224}
	armX := size * 58 / 100
	armY := size * 28 / 100
	stemX := size * 42 / 100
	stemTop := size * 22 / 100
	stemBottom := size * 68 / 100
	head1X := size * 38 / 100
	head1Y := size * 72 / 100
	head2X := size * 56 / 100
	head2Y := size * 64 / 100
	drawRoundCappedLine(dst, float64(stemX), float64(stemTop), float64(stemX), float64(stemBottom), note, max(3, size/28))
	drawRoundCappedLine(dst, float64(stemX), float64(stemTop), float64(armX), float64(armY), note, max(3, size/28))
	drawFilledCircle(dst, head1X, head1Y, max(6, size/9), note)
	drawFilledCircle(dst, head2X, head2Y, max(6, size/9), note)
	return dst, nil
}

func isMissingDrawtextError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "No such filter: 'drawtext'") ||
		strings.Contains(s, "No such filter: drawtext") ||
		strings.Contains(s, "Filter not found")
}

func drawFilledCircle(img *image.RGBA, cx, cy, radius int, c color.RGBA) {
	r2 := radius * radius
	for y := cy - radius; y <= cy+radius; y++ {
		for x := cx - radius; x <= cx+radius; x++ {
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy <= r2 {
				blendPixel(img, x, y, c)
			}
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

package video

import (
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

	xdraw "golang.org/x/image/draw"
)

// ---------------------------------------------------------------------------
// ForegroundMode
// ---------------------------------------------------------------------------

// ForegroundMode holds the global foreground colour and the readability-overlay
// colour (including its final alpha) that the renderer applies for the entire
// frame.
type ForegroundMode struct {
	Color   color.RGBA
	Overlay color.RGBA
}

// ---------------------------------------------------------------------------
// SelectForegroundMode
// ---------------------------------------------------------------------------

// SelectForegroundMode examines the blurred full-frame background and the two
// text/graph regions, then picks the global foreground mode that provides at
// least WCAG 4.5:1 contrast.
//
//  1. Average pixel luminance is measured in the metadata and graph regions.
//  2. If both regions average below 128 → light mode (white text, black
//     overlay starting at 36 %). Otherwise → dark mode (black text, white
//     overlay starting at 28 %).
//  3. The readability-overlay opacity is increased in 5‑percentage‑point
//     steps, up to 60 %, until both regions reach 4.5:1.
func SelectForegroundMode(background image.Image, metadataRect, graphRect image.Rectangle) ForegroundMode {
	metaLum := averageLuminance(background, metadataRect)
	graphLum := averageLuminance(background, graphRect)

	var fgColor, overlayColor color.RGBA
	var startOpacity float64

	if metaLum < 128 && graphLum < 128 {
		// Light mode — white text, black overlay.
		fgColor = color.RGBA{R: 255, G: 255, B: 255, A: 255}
		overlayColor = color.RGBA{R: 0, G: 0, B: 0, A: 255}
		startOpacity = 0.36
	} else {
		// Dark mode — black text, white overlay.
		fgColor = color.RGBA{R: 0, G: 0, B: 0, A: 255}
		overlayColor = color.RGBA{R: 255, G: 255, B: 255, A: 255}
		startOpacity = 0.28
	}

	// Find the lowest overlay opacity that passes 4.5:1.
	opacity := startOpacity
	fgLum := srgbLuminance(fgColor)

	for opacity <= 0.60 {
		metaOK := regionContrastOK(background, metadataRect, overlayColor, opacity, fgLum)
		graphOK := regionContrastOK(background, graphRect, overlayColor, opacity, fgLum)
		if metaOK && graphOK {
			break
		}
		opacity += 0.05
	}
	if opacity > 0.60 {
		opacity = 0.60
	}

	return ForegroundMode{
		Color:   fgColor,
		Overlay: color.RGBA{R: overlayColor.R, G: overlayColor.G, B: overlayColor.B, A: uint8(math.Round(opacity * 255))},
	}
}

// ---------------------------------------------------------------------------
// Luminance helpers (pixel-average and WCAG)
// ---------------------------------------------------------------------------

// averageLuminance returns the mean of (R+G+B)/3 across all pixels in the
// given rectangle of img.  Values are 0–255.
func averageLuminance(img image.Image, rect image.Rectangle) float64 {
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() {
		return 0
	}

	var sum float64
	n := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// RGBA() returns 16-bit values; shift right 8 to get 8‑bit.
			sum += float64((r>>8)+(g>>8)+(b>>8)) / 3.0
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// srgbLuminance computes the WCAG 2.1 relative luminance of an 8‑bit sRGB
// colour.
func srgbLuminance(c color.RGBA) float64 {
	r := srgbLinearize(float64(c.R) / 255.0)
	g := srgbLinearize(float64(c.G) / 255.0)
	b := srgbLinearize(float64(c.B) / 255.0)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

func srgbLinearize(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// wcagContrast returns the WCAG 2.1 contrast ratio between two luminances.
func wcagContrast(l1, l2 float64) float64 {
	if l1 > l2 {
		return (l1 + 0.05) / (l2 + 0.05)
	}
	return (l2 + 0.05) / (l1 + 0.05)
}

// regionContrastOK reports whether every pixel in the given rectangle, after
// the semi‑transparent overlay is applied, achieves at least 4.5:1 contrast
// against the foreground colour.
func regionContrastOK(img image.Image, rect image.Rectangle, overlayColor color.RGBA, overlayAlpha, fgLum float64) bool {
	rect = rect.Intersect(img.Bounds())
	if rect.Empty() {
		return true
	}

	// For light mode (white text, black overlay) the worst case is the
	// pixel with the highest effective luminance; for dark mode it is the
	// pixel with the lowest effective luminance.
	whiteFg := fgLum > 0.5 // true for white (1.0), false for black (0.0)

	oa := overlayAlpha
	or := float64(overlayColor.R)
	og := float64(overlayColor.G)
	ob := float64(overlayColor.B)

	extreme := -1.0 // sentinel

	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// 16‑bit → 8‑bit.
			br := float64(r>>8) * (1 - oa)
			bg := float64(g>>8) * (1 - oa)
			bb := float64(b>>8) * (1 - oa)

			effR := or*oa + br
			effG := og*oa + bg
			effB := ob*oa + bb

			lum := srgbLuminance8(effR, effG, effB)

			if whiteFg {
				// Need the highest effective luminance.
				if lum > extreme {
					extreme = lum
				}
			} else {
				// Need the lowest effective luminance.
				if extreme < 0 || lum < extreme {
					extreme = lum
				}
			}
		}
	}

	if extreme < 0 {
		return true
	}

	ratio := wcagContrast(fgLum, extreme)
	return ratio >= 4.5
}

// srgbLuminance8 is a fast path that accepts 8‑bit R/G/B directly.
func srgbLuminance8(r, g, b float64) float64 {
	r = srgbLinearize(r / 255.0)
	g = srgbLinearize(g / 255.0)
	b = srgbLinearize(b / 255.0)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// ---------------------------------------------------------------------------
// PrepareVisualizerBase
// ---------------------------------------------------------------------------

// PrepareVisualizerBase renders the blurred full-frame background and the
// foreground artwork tile with its shadow, then saves the result as a PNG.
//
//  1. If artworkPath is non-empty, FFmpeg scales it with a cover operation to
//     fill the canvas and applies gblur=sigma=64.
//  2. If artworkPath is empty or invalid, the fallback *image.RGBA is written
//     to a temporary file and processed the same way.
//  3. The foreground artwork is scaled/cropped to fill the artwork rectangle,
//     masked with rounded corners and composited over the background together
//     with its shadow.
//  4. The result is saved to outPath and the selected ForegroundMode returned.
func PrepareVisualizerBase(ctx context.Context, ffmpeg, artworkPath string, fallback *image.RGBA, layout VisualizerLayout, outPath string) (ForegroundMode, error) {
	// Derive canvas dimensions from the layout.
	canvasW := int(math.Round(float64(layout.Artwork.W) * 1280.0 / 288.0))
	canvasH := int(math.Round(float64(layout.Artwork.H) * 720.0 / 288.0))

	// -----------------------------------------------------------------------
	// 1. Blurred background
	// -----------------------------------------------------------------------
	tmpDir, err := os.MkdirTemp("", "imagepad-visualizer-bg-*")
	if err != nil {
		return ForegroundMode{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	sourcePath := artworkPath
	cleanupSource := false
	if sourcePath == "" {
		// Write the fallback tile to a temp PNG so FFmpeg can process it.
		sourcePath = filepath.Join(tmpDir, "fallback.png")
		if err := savePNG(sourcePath, fallback); err != nil {
			return ForegroundMode{}, fmt.Errorf("save fallback: %w", err)
		}
		cleanupSource = true
	}
	if cleanupSource {
		defer os.Remove(sourcePath)
	}

	blurredPath := filepath.Join(tmpDir, "blurred.png")

	// Use ffmpeg to scale with cover, center crop, then Gaussian blur.
	filter := fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,gblur=sigma=64",
		canvasW, canvasH, canvasW, canvasH,
	)
	args := []string{
		"-y",
		"-i", sourcePath,
		"-vf", filter,
		"-frames:v", "1",
		blurredPath,
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	if output, err := CombinedOutputTrackedFFmpeg(cmd); err != nil {
		return ForegroundMode{}, fmt.Errorf("ffmpeg background blur failed: %w\n%s", err, string(output))
	}

	bg, err := loadPNG(blurredPath)
	if err != nil {
		return ForegroundMode{}, fmt.Errorf("load blurred background: %w", err)
	}
	bgRGBA := toRGBA(bg)

	// -----------------------------------------------------------------------
	// 2. Foreground artwork — scale to fill artwork rect
	// -----------------------------------------------------------------------
	artRect := image.Rect(
		layout.Artwork.X, layout.Artwork.Y,
		layout.Artwork.X+layout.Artwork.W, layout.Artwork.Y+layout.Artwork.H,
	)

	var fgSrc image.Image
	if artworkPath != "" {
		fgSrc, err = loadAnyPNG(artworkPath)
		if err != nil {
			return ForegroundMode{}, fmt.Errorf("load artwork: %w", err)
		}
	} else {
		fgSrc = fallback
	}

	fgScaled := scaleCover(fgSrc, artRect.Dx(), artRect.Dy())

	// -----------------------------------------------------------------------
	// 3. Shadow
	// -----------------------------------------------------------------------
	cr := int(math.Round(24.0 * float64(layout.Artwork.W) / 288.0))
	shadowBlur := int(math.Round(24.0 * float64(layout.Artwork.W) / 288.0))
	shadowOffY := int(math.Round(8.0 * float64(layout.Artwork.W) / 288.0))

	renderShadow(bgRGBA, artRect, cr, shadowBlur, shadowOffY)

	// -----------------------------------------------------------------------
	// 4. Rounded-corner mask for foreground artwork
	// -----------------------------------------------------------------------
	masked := applyRoundedCorners(fgScaled, cr)
	draw.Draw(bgRGBA, artRect, masked, image.Point{}, draw.Over)

	// -----------------------------------------------------------------------
	// 5. Select foreground mode and save
	// -----------------------------------------------------------------------
	metaRect := image.Rect(
		layout.Title.X, layout.Title.Y,
		layout.Title.X+layout.Title.W, layout.Title.Y+layout.Title.H,
	)
	graphRect := image.Rect(
		layout.Loudness.X, layout.Loudness.Y,
		layout.Loudness.X+layout.Loudness.W, layout.Loudness.Y+layout.Loudness.H,
	)
	mode := SelectForegroundMode(bgRGBA, metaRect, graphRect)

	if err := savePNG(outPath, bgRGBA); err != nil {
		return ForegroundMode{}, fmt.Errorf("save output: %w", err)
	}

	return mode, nil
}

// ---------------------------------------------------------------------------
// Image helpers
// ---------------------------------------------------------------------------

// savePNG writes img as a PNG file to path.
func savePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// loadPNG reads a PNG file from disk.
func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode PNG: %w", err)
	}
	return img, nil
}

// loadAnyPNG reads a PNG file (or other registered format) from disk.
func loadAnyPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return img, nil
}

// toRGBA converts any image.Image to an *image.RGBA, copying pixels.
func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

// scaleCover scales src to cover dstW×dstH while preserving aspect ratio,
// then center-crops to exactly dstW×dstH.  Uses Catmull‑Rom interpolation.
func scaleCover(src image.Image, dstW, dstH int) *image.RGBA {
	sb := src.Bounds()
	srcW := sb.Dx()
	srcH := sb.Dy()

	if srcW == 0 || srcH == 0 {
		return image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	}

	sf := math.Max(float64(dstW)/float64(srcW), float64(dstH)/float64(srcH))
	interW := int(math.Round(float64(srcW) * sf))
	interH := int(math.Round(float64(srcH) * sf))

	// Scale to intermediate size.
	interImg := image.NewRGBA(image.Rect(0, 0, interW, interH))
	xdraw.CatmullRom.Scale(interImg, interImg.Bounds(), src, sb, xdraw.Src, nil)

	// Center crop.
	cx := (interW - dstW) / 2
	cy := (interH - dstH) / 2
	cropRect := image.Rect(cx, cy, cx+dstW, cy+dstH)
	cropped := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.Draw(cropped, cropped.Bounds(), interImg, cropRect.Min, draw.Src)

	return cropped
}

// ---------------------------------------------------------------------------
// Shadow rendering
// ---------------------------------------------------------------------------

// renderShadow draws a drop shadow for the artwork onto bg.  The shadow is a
// black rounded rectangle at (artRect.Min+(0,offY), artRect.Size()-(0,0))
// blurred by the given radius and scaled to 20 % opacity.
func renderShadow(bg *image.RGBA, artRect image.Rectangle, cornerRadius, blurRadius, offsetY int) {
	if blurRadius <= 0 {
		return
	}
	pad := blurRadius
	sw := artRect.Dx() + 2*pad
	sh := artRect.Dy() + 2*pad

	shadowImg := image.NewRGBA(image.Rect(0, 0, sw, sh))

	// Draw filled rounded rectangle (black, full opacity) at the offset
	// position inside the padded shadow image.
	rr := image.Rect(pad, pad+offsetY, pad+artRect.Dx(), pad+artRect.Dy()+offsetY)
	drawFilledRoundedRect(shadowImg, rr, cornerRadius, color.RGBA{R: 0, G: 0, B: 0, A: 255})

	// Box blur.
	blurred := boxBlurRGBA(shadowImg, blurRadius)

	// Scale to 20 % opacity.
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			c := blurred.RGBAAt(x, y)
			if c.A > 0 {
				newA := uint8(float64(c.A) * 0.20)
				blurred.SetRGBA(x, y, color.RGBA{R: c.R, G: c.G, B: c.B, A: newA})
			}
		}
	}

	// Composite onto background.
	dstRect := image.Rect(
		artRect.Min.X-pad, artRect.Min.Y-pad,
		artRect.Max.X+pad, artRect.Max.Y+pad,
	)
	draw.Draw(bg, dstRect, blurred, image.Point{}, draw.Over)
}

// ---------------------------------------------------------------------------
// Box blur
// ---------------------------------------------------------------------------

// boxBlurRGBA applies two separable box-blur passes (horizontal then vertical)
// with the given radius.
func boxBlurRGBA(src *image.RGBA, radius int) *image.RGBA {
	b := src.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w == 0 || h == 0 || radius <= 0 {
		return src
	}

	// Horizontal pass.
	hBuf := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sumR, sumG, sumB, sumA int64
			cnt := 0
			for dx := -radius; dx <= radius; dx++ {
				px := x + dx
				if px >= 0 && px < w {
					c := src.RGBAAt(px, y)
					sumR += int64(c.R)
					sumG += int64(c.G)
					sumB += int64(c.B)
					sumA += int64(c.A)
					cnt++
				}
			}
			hBuf.SetRGBA(x, y, color.RGBA{
				R: uint8(sumR / int64(cnt)),
				G: uint8(sumG / int64(cnt)),
				B: uint8(sumB / int64(cnt)),
				A: uint8(sumA / int64(cnt)),
			})
		}
	}

	// Vertical pass.
	dst := image.NewRGBA(b)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sumR, sumG, sumB, sumA int64
			cnt := 0
			for dy := -radius; dy <= radius; dy++ {
				py := y + dy
				if py >= 0 && py < h {
					c := hBuf.RGBAAt(x, py)
					sumR += int64(c.R)
					sumG += int64(c.G)
					sumB += int64(c.B)
					sumA += int64(c.A)
					cnt++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(sumR / int64(cnt)),
				G: uint8(sumG / int64(cnt)),
				B: uint8(sumB / int64(cnt)),
				A: uint8(sumA / int64(cnt)),
			})
		}
	}

	return dst
}

// ---------------------------------------------------------------------------
// Rounded-corner mask
// ---------------------------------------------------------------------------

// applyRoundedCorners returns a copy of src with the given corner radius
// applied via an alpha mask.  Pixels outside the rounded rect become fully
// transparent.
func applyRoundedCorners(src *image.RGBA, radius int) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)

	if radius <= 0 {
		return dst
	}

	// Zero out alpha for pixels outside the rounded rectangle.
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if !inRoundedRect(x, y, image.Rect(0, 0, b.Dx(), b.Dy()), radius) {
				dst.SetRGBA(x, y, color.RGBA{})
			}
		}
	}
	return dst
}

// inRoundedRect reports whether (x, y) falls inside the rectangle with rounded
// corners of the given radius.
//
//nolint:cyclop
func inRoundedRect(x, y int, rect image.Rectangle, radius int) bool {
	if x < rect.Min.X || x >= rect.Max.X || y < rect.Min.Y || y >= rect.Max.Y {
		return false
	}
	r := radius
	if r <= 0 {
		return true
	}

	// Top-left corner.
	if x < rect.Min.X+r && y < rect.Min.Y+r {
		dx := x - (rect.Min.X + r - 1)
		dy := y - (rect.Min.Y + r - 1)
		return dx*dx+dy*dy <= r*r
	}
	// Top-right corner.
	if x >= rect.Max.X-r && y < rect.Min.Y+r {
		dx := x - (rect.Max.X - r)
		dy := y - (rect.Min.Y + r - 1)
		return dx*dx+dy*dy <= r*r
	}
	// Bottom-left corner.
	if x < rect.Min.X+r && y >= rect.Max.Y-r {
		dx := x - (rect.Min.X + r - 1)
		dy := y - (rect.Max.Y - r)
		return dx*dx+dy*dy <= r*r
	}
	// Bottom-right corner.
	if x >= rect.Max.X-r && y >= rect.Max.Y-r {
		dx := x - (rect.Max.X - r)
		dy := y - (rect.Max.Y - r)
		return dx*dx+dy*dy <= r*r
	}
	return true
}

// drawFilledRoundedRect fills a rectangle with rounded corners using the given
// colour.
func drawFilledRoundedRect(img *image.RGBA, rect image.Rectangle, radius int, c color.RGBA) {
	if radius <= 0 {
		draw.Draw(img, rect, &image.Uniform{c}, image.Point{}, draw.Src)
		return
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if inRoundedRect(x, y, rect, radius) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

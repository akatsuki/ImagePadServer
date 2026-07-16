package video

import "image"

// RenderArtworkCoverCPU applies the canonical aspect-cover crop into an RGBA
// output. It is a pure diagnostic reference; production renderers are untouched.
func RenderArtworkCoverCPU(src image.Image, width, height int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	if src == nil || width <= 0 || height <= 0 {
		return out
	}
	sb := src.Bounds()
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	scale := float64(width) / sw
	if v := float64(height) / sh; v > scale {
		scale = v
	}
	cw, ch := float64(width)/scale, float64(height)/scale
	ox, oy := (sw-cw)/2, (sh-ch)/2
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sx := sb.Min.X + int((float64(x)+.5)/scale+ox)
			sy := sb.Min.Y + int((float64(y)+.5)/scale+oy)
			if sx >= sb.Max.X {
				sx = sb.Max.X - 1
			}
			if sy >= sb.Max.Y {
				sy = sb.Max.Y - 1
			}
			out.Set(x, y, src.At(sx, sy))
		}
	}
	return out
}

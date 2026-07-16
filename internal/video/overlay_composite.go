package video

import (
	"image"
	"image/color"
	"math"
	"os"
)

type OverlayParityMetric struct {
	Threshold    uint8   `json:"threshold"`
	CPUVisible   int     `json:"cpuVisible"`
	GPUVisible   int     `json:"gpuVisible"`
	Intersection int     `json:"intersection"`
	Union        int     `json:"union"`
	IoU          float64 `json:"iou"`
	MAE          float64 `json:"mae"`
	RMSE         float64 `json:"rmse"`
	CPUBounds    [4]int  `json:"cpuBounds"`
	GPUBounds    [4]int  `json:"gpuBounds"`
}

func CompareOverlayParityCPUImageGPUImage(cpu, gpu image.Image, crop image.Rectangle) OverlayParityMetric {
	r := OverlayParityMetric{Threshold: 8}
	cm, gm := map[int]bool{}, map[int]bool{}
	var sum, sq float64
	n := 0
	for y := crop.Min.Y; y < crop.Max.Y; y++ {
		for x := crop.Min.X; x < crop.Max.X; x++ {
			cr, cg, cb, _ := cpu.At(x, y).RGBA()
			gr, gg, gb, _ := gpu.At(x, y).RGBA()
			cv := uint8((299*(cr/257) + 587*(cg/257) + 114*(cb/257)) / 1000)
			gv := uint8((299*(gr/257) + 587*(gg/257) + 114*(gb/257)) / 1000)
			p := y*100000 + x
			if cv >= 8 {
				cm[p] = true
				r.CPUVisible++
			}
			if gv >= 8 {
				gm[p] = true
				r.GPUVisible++
			}
			d := float64(cv) - float64(gv)
			if d < 0 {
				d = -d
			}
			sum += d
			sq += d * d
			n++
		}
	}
	for p := range cm {
		if gm[p] {
			r.Intersection++
		}
	}
	for p := range cm {
		x, y := p%100000, p/100000
		if r.CPUBounds == [4]int{} {
			r.CPUBounds = [4]int{x, y, x + 1, y + 1}
		} else {
			if x < r.CPUBounds[0] {
				r.CPUBounds[0] = x
			}
			if y < r.CPUBounds[1] {
				r.CPUBounds[1] = y
			}
			if x+1 > r.CPUBounds[2] {
				r.CPUBounds[2] = x + 1
			}
			if y+1 > r.CPUBounds[3] {
				r.CPUBounds[3] = y + 1
			}
		}
	}
	for p := range gm {
		x, y := p%100000, p/100000
		if r.GPUBounds == [4]int{} {
			r.GPUBounds = [4]int{x, y, x + 1, y + 1}
		} else {
			if x < r.GPUBounds[0] {
				r.GPUBounds[0] = x
			}
			if y < r.GPUBounds[1] {
				r.GPUBounds[1] = y
			}
			if x+1 > r.GPUBounds[2] {
				r.GPUBounds[2] = x + 1
			}
			if y+1 > r.GPUBounds[3] {
				r.GPUBounds[3] = y + 1
			}
		}
	}
	r.Union = len(cm) + len(gm) - r.Intersection
	if r.Union > 0 {
		r.IoU = float64(r.Intersection) / float64(r.Union)
	}
	if n > 0 {
		r.MAE = sum / float64(n)
		r.RMSE = math.Sqrt(sq / float64(n))
	}
	return r
}

// RenderTextOverlayScreenRGBA places the canonical overlay raster into a
// deterministic screen-space crop using nearest sampling.
func RenderTextOverlayScreenRGBA(overlay *TextOverlayMetadata, width, height uint32, crop SceneRect) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
	if overlay == nil || len(overlay.Payload) == 0 || crop.W <= 0 || crop.H <= 0 {
		return out
	}
	for y := 0; y < crop.H && crop.Y+y < int(height); y++ {
		for x := 0; x < crop.W && crop.X+x < int(width); x++ {
			sx := x * int(overlay.Width) / crop.W
			sy := y * int(overlay.Height) / crop.H
			off := sy*int(overlay.RowStride) + sx*4
			if off+3 < len(overlay.Payload) {
				out.SetRGBA(crop.X+x, crop.Y+y, color.RGBA{overlay.Payload[off], overlay.Payload[off+1], overlay.Payload[off+2], overlay.Payload[off+3]})
			}
		}
	}
	return out
}

// CompositeTextOverlayCPU composites a premultiplied RGBA overlay over base
// using SrcOver. Transparent overlay pixels are forced to RGB=0, preventing
// hidden color from leaking into later blends. The input image is never
// mutated; callers can use the returned image as the parity reference.
func CompositeTextOverlayCPU(base image.Image, overlay *TextOverlayMetadata) *image.RGBA {
	if base == nil {
		return nil
	}
	b := base.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.Set(x, y, base.At(x, y))
		}
	}
	if overlay == nil || overlay.Width == 0 || overlay.Height == 0 || len(overlay.Payload) == 0 {
		return out
	}
	stride := int(overlay.RowStride)
	for y := 0; y < int(overlay.Height) && y < b.Dy(); y++ {
		for x := 0; x < int(overlay.Width) && x < b.Dx(); x++ {
			off := y*stride + x*4
			if off+3 >= len(overlay.Payload) {
				continue
			}
			r, g, bl, a := overlay.Payload[off], overlay.Payload[off+1], overlay.Payload[off+2], overlay.Payload[off+3]
			if a == 0 {
				r, g, bl = 0, 0, 0
				continue
			}
			br, bg, bb, ba := out.At(b.Min.X+x, b.Min.Y+y).RGBA()
			da := uint8(ba / 257)
			// Convert straight destination to premultiplied, SrcOver, then back.
			dr, dg, db := uint32(br/257), uint32(bg/257), uint32(bb/257)
			inv := uint32(255 - a)
			oa := uint32(a) + uint32(da)*inv/255
			if oa == 0 {
				out.SetRGBA(b.Min.X+x, b.Min.Y+y, color.RGBA{})
				continue
			}
			pr := uint32(r) + dr*inv/255
			pg := uint32(g) + dg*inv/255
			pb := uint32(bl) + db*inv/255
			out.SetRGBA(b.Min.X+x, b.Min.Y+y, color.RGBA{R: uint8(pr * 255 / oa), G: uint8(pg * 255 / oa), B: uint8(pb * 255 / oa), A: uint8(oa)})
		}
	}
	return out
}

// CompositeTextOverlayCPUParity is explicitly opt-in and leaves the normal
// ASS/production path untouched.
func CompositeTextOverlayCPUParity(base image.Image, overlay *TextOverlayMetadata) image.Image {
	if os.Getenv("IMAGEPAD_TEXT_OVERLAY_PARITY") != "1" {
		return base
	}
	return CompositeTextOverlayCPU(base, overlay)
}

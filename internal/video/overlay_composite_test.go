package video

import (
	"image"
	"image/color"
	"os"
	"testing"
)

func TestCompositeTextOverlayCPUCropAndTransparentRGB(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			base.SetRGBA(x, y, color.RGBA{20, 20, 20, 255})
		}
	}
	o := &TextOverlayMetadata{Width: 2, Height: 1, RowStride: 256, Payload: make([]byte, 256), Premultiplied: true}
	o.Payload[0], o.Payload[1], o.Payload[2], o.Payload[3] = 255, 0, 0, 128
	o.Payload[4], o.Payload[5], o.Payload[6], o.Payload[7] = 99, 88, 77, 0
	r := CompositeTextOverlayCPU(base, o)
	if r.Bounds() != base.Bounds() {
		t.Fatal("crop changed output bounds")
	}
	if got := r.RGBAAt(0, 0); got.A == 0 || got.R == 20 {
		t.Fatalf("overlay not blended: %#v", got)
	}
	if got := r.RGBAAt(1, 0); got != (color.RGBA{R: 20, G: 20, B: 20, A: 255}) {
		t.Fatalf("transparent RGB leaked: %#v", got)
	}
	os.Setenv("IMAGEPAD_TEXT_OVERLAY_PARITY", "0")
	if CompositeTextOverlayCPUParity(base, o) != base {
		t.Fatal("parity unexpectedly enabled")
	}
}

func TestRenderTextOverlayScreenRGBABounds(t *testing.T) {
	o := &TextOverlayMetadata{Width: 2, Height: 2, RowStride: 256, Payload: make([]byte, 512)}
	o.Payload[3], o.Payload[7], o.Payload[259], o.Payload[263] = 255, 255, 255, 255
	r := RenderTextOverlayScreenRGBA(o, 640, 360, SceneRect{X: 10, Y: 20, W: 4, H: 4})
	b := r.Bounds()
	if b.Dx() != 640 || b.Dy() != 360 {
		t.Fatal(b)
	}
	if r.RGBAAt(10, 20).A != 255 || r.RGBAAt(13, 23).A != 255 {
		t.Fatal("overlay bbox not rasterized")
	}
}

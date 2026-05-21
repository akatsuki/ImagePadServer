package imageproc

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
)

func TestProcessResizesToVRChatLimit(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3000, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 3000; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}

	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	result, err := Process(&input, "large.png", dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Width != 2048 {
		t.Fatalf("width = %d, want 2048", result.Width)
	}
	if result.Height <= 0 || result.Height > 2048 {
		t.Fatalf("height = %d, want within VRChat limit", result.Height)
	}
	if result.ContentType != "image/jpeg" {
		t.Fatalf("content type = %s, want image/jpeg", result.ContentType)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatal(err)
	}
}

func TestProcessRejectsPNGOverMaxBytes(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))

	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.Format = "png"
	opts.MaxBytes = 1

	_, err := Process(&input, "tiny.png", t.TempDir(), opts)
	if err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestProcessRejectsJPEGOverMaxBytes(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))

	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.Format = "jpeg"
	opts.MaxBytes = 1

	_, err := Process(&input, "tiny.png", t.TempDir(), opts)
	if err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestProcessAppliesEXIFOrientation(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 24, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 24; x++ {
			src.Set(x, y, color.RGBA{R: 220, G: uint8(y), B: uint8(x), A: 255})
		}
	}

	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	input := jpegWithOrientation(jpegData.Bytes(), 6)

	opts := DefaultOptions()
	opts.MaxDimension = 2048
	result, err := Process(bytes.NewReader(input), "iphone.jpg", t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Width != 48 || result.Height != 24 {
		t.Fatalf("size = %d x %d, want 48 x 24 after orientation", result.Width, result.Height)
	}
}

func TestProcessRasterizesSVG(t *testing.T) {
	input := strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg" width="80" height="40" viewBox="0 0 80 40"><rect width="80" height="40" fill="#00aaff"/></svg>`)

	opts := DefaultOptions()
	opts.Format = "png"
	result, err := Process(input, "remote.svg", t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Width != 80 || result.Height != 40 {
		t.Fatalf("size = %d x %d, want 80 x 40", result.Width, result.Height)
	}
	if result.ContentType != "image/png" {
		t.Fatalf("content type = %s, want image/png", result.ContentType)
	}
}

func jpegWithOrientation(jpegBytes []byte, orientation byte) []byte {
	exif := []byte{
		'E', 'x', 'i', 'f', 0, 0,
		'M', 'M', 0, 42,
		0, 0, 0, 8,
		0, 1,
		0x01, 0x12,
		0, 3,
		0, 0, 0, 1,
		0, orientation, 0, 0,
		0, 0, 0, 0,
	}
	segmentLen := len(exif) + 2
	app1 := []byte{0xff, 0xe1, byte(segmentLen >> 8), byte(segmentLen)}
	app1 = append(app1, exif...)

	out := append([]byte{}, jpegBytes[:2]...)
	out = append(out, app1...)
	out = append(out, jpegBytes[2:]...)
	return out
}

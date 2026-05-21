package imageproc

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
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

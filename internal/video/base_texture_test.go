package video

import (
	"image"
	"image/color"
	"testing"
)

func TestNewBaseTextureMetadataCopiesAndPadsRows(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.SetRGBA(2, 1, color.RGBA{R: 7, G: 8, B: 9, A: 255})
	b, err := NewBaseTextureMetadata("base", src, ColorSRGB)
	if err != nil {
		t.Fatal(err)
	}
	if b.RowStride != GPURowAlignment {
		t.Fatalf("stride=%d, want %d", b.RowStride, GPURowAlignment)
	}
	if b.Payload[(1*int(b.RowStride))+2*4] != 7 {
		t.Fatal("pixel payload was not copied")
	}
	if b.AssetHash == "" {
		t.Fatal("missing asset hash")
	}
}

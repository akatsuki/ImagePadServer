package imageproc

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Options struct {
	MaxDimension int
	Format       string
	JPEGQuality  int
	MaxBytes     int64
}

type Result struct {
	Path        string
	PublicName  string
	ContentType string
	Width       int
	Height      int
}

func DefaultOptions() Options {
	return Options{
		MaxDimension: 2048,
		Format:       "jpeg",
		JPEGQuality:  88,
		MaxBytes:     30 << 20,
	}
}

func Process(reader io.Reader, _ string, outDir string, opts Options) (Result, error) {
	if opts.MaxDimension <= 0 || opts.MaxDimension > 2048 {
		opts.MaxDimension = 2048
	}
	if opts.JPEGQuality <= 0 || opts.JPEGQuality > 100 {
		opts.JPEGQuality = 88
	}
	opts.Format = strings.ToLower(opts.Format)
	if opts.Format != "png" {
		opts.Format = "jpeg"
	}

	img, format, err := image.Decode(reader)
	if err != nil {
		return Result{}, fmt.Errorf("unsupported or invalid image: %w", err)
	}
	_ = format

	resized := resizeToFit(img, opts.MaxDimension)
	width := resized.Bounds().Dx()
	height := resized.Bounds().Dy()

	ext := ".jpg"
	contentType := "image/jpeg"
	if opts.Format == "png" {
		ext = ".png"
		contentType = "image/png"
	}

	path := filepath.Join(outDir, "processed"+ext)
	var data []byte
	if opts.Format == "png" {
		var buf bytes.Buffer
		if err := png.Encode(&buf, resized); err != nil {
			return Result{}, err
		}
		data = buf.Bytes()
	} else {
		encoded, err := encodeJPEGWithinLimit(flatten(resized), opts.JPEGQuality, opts.MaxBytes)
		if err != nil {
			return Result{}, err
		}
		data = encoded
	}

	file, err := os.Create(path)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return Result{}, err
	}

	return Result{
		Path:        path,
		PublicName:  "current" + ext,
		ContentType: contentType,
		Width:       width,
		Height:      height,
	}, nil
}

func encodeJPEGWithinLimit(img image.Image, quality int, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 30 << 20
	}
	for q := quality; q >= 50; q -= 8 {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
		if int64(buf.Len()) <= maxBytes || q == 50 {
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("failed to encode jpeg")
}

func resizeToFit(src image.Image, maxDim int) image.Image {
	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= maxDim && h <= maxDim {
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Src)
		return dst
	}

	scale := float64(maxDim) / float64(w)
	if h > w {
		scale = float64(maxDim) / float64(h)
	}
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewNRGBA(image.Rect(0, 0, newW, newH))
	for y := 0; y < newH; y++ {
		sy := bounds.Min.Y + int(float64(y)*float64(h)/float64(newH))
		for x := 0; x < newW; x++ {
			sx := bounds.Min.X + int(float64(x)*float64(w)/float64(newW))
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func flatten(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Over)
	return dst
}

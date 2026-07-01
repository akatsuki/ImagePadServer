package imageproc

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("IMAGEPAD_SKIP_IMAGE_TOOL_DOWNLOAD", "1")
	os.Exit(m.Run())
}

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
	opts := DefaultOptions()
	opts.Format = "jpeg"
	opts.MaxDimension = 2048
	opts.MaxBytes = 30 << 20
	result, err := Process(&input, "large.png", dir, opts)
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

func TestDefaultOptionsAllow8KUploads(t *testing.T) {
	opts := DefaultOptions()
	if opts.MaxDimension != 2048 {
		t.Fatalf("MaxDimension = %d, want 2048", opts.MaxDimension)
	}
	if opts.MaxInputBytes != maxImageBytes {
		t.Fatalf("MaxInputBytes = %d, want %d", opts.MaxInputBytes, maxImageBytes)
	}
	if opts.MaxBytes != 30<<20 {
		t.Fatalf("MaxBytes = %d, want %d", opts.MaxBytes, int64(30<<20))
	}
	if opts.Format != "webp" {
		t.Fatalf("Format = %q, want webp", opts.Format)
	}
	if opts.WebPQuality != 80 {
		t.Fatalf("WebPQuality = %d, want 80", opts.WebPQuality)
	}
	if opts.PNGQuality != "lossless" {
		t.Fatalf("PNGQuality = %q, want lossless", opts.PNGQuality)
	}
}

func TestProcessSupportsLargeMaxDimension(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 5000, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 5000; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}

	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.Format = "jpeg"
	opts.MaxDimension = 5000
	opts.MaxBytes = 60 << 20
	result, err := Process(&input, "wide.png", t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Width != 5000 {
		t.Fatalf("width = %d, want 5000", result.Width)
	}
	if result.Height != 1200 {
		t.Fatalf("height = %d, want 1200", result.Height)
	}
}

func TestProcessRejectsInputOverMaxBytes(t *testing.T) {
	payload := bytes.Repeat([]byte{0xff}, 2048)
	opts := DefaultOptions()
	opts.MaxInputBytes = 1024

	_, err := Process(bytes.NewReader(payload), "big.bin", t.TempDir(), opts)
	if err == nil {
		t.Fatal("expected size limit error for raw input")
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
	opts.Format = "jpeg"
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

func TestProcessWebP(t *testing.T) {
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg not available for WebP encode test")
		}
		t.Setenv("IMAGEPAD_FFMPEG", ffmpeg)
	}

	src := image.NewNRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			src.Set(x, y, color.NRGBA{R: 20, G: 160, B: 80, A: 255})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.Format = "webp"
	opts.WebPQuality = 80
	result, err := Process(&input, "sample.png", t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "image/webp" {
		t.Fatalf("content type = %s, want image/webp", result.ContentType)
	}
	if !strings.HasSuffix(result.PublicName, ".webp") {
		t.Fatalf("public name = %q, want .webp suffix", result.PublicName)
	}
	if stat, err := os.Stat(result.Path); err != nil || stat.Size() == 0 {
		t.Fatalf("webp output stat = %v, err = %v", stat, err)
	}
}

func TestOptimizePNGNoTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("IMAGEPAD_PNGQUANT", "")
	t.Setenv("IMAGEPAD_OXIPNG", "")
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	path := t.TempDir() + string(os.PathSeparator) + "sample.png"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, src); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OptimizePNG(path, "lossless")
	if err != nil {
		t.Fatal(err)
	}
	if got != before.Size() {
		t.Fatalf("size = %d, want unchanged %d", got, before.Size())
	}
}

func TestIsSVGDoesNotMatchEmbeddedMetadata(t *testing.T) {
	input := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rc2pa icon image/svg+xml "), []byte(`<svg width="16" height="16"></svg>`)...)
	if isSVG(input) {
		t.Fatal("expected binary PNG metadata containing SVG text not to be treated as an SVG file")
	}
}

func TestIsSVGMatchesXMLWrappedSVG(t *testing.T) {
	input := []byte(`<?xml version="1.0" encoding="UTF-8"?><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"></svg>`)
	if !isSVG(input) {
		t.Fatal("expected XML-wrapped SVG to be treated as an SVG file")
	}
}

func TestIsCameraRAWName(t *testing.T) {
	rawNames := []string{
		"sony.ARW",
		"sony.srf",
		"sony.sr2",
		"canon.crw",
		"canon.cr2",
		"canon.cr3",
		"panasonic.rw2",
		"olympus.orf",
		"fujifilm.raf",
		"nikon.nef",
		"nikon.nrw",
		"sigma.x3f",
		"adobe.dng",
	}
	for _, name := range rawNames {
		if !IsCameraRAWName(name) {
			t.Fatalf("expected %s to be treated as camera RAW", name)
		}
	}

	for _, name := range []string{"photo.jpg", "image.png", "movie.mp4", "raw.txt"} {
		if IsCameraRAWName(name) {
			t.Fatalf("expected %s not to be treated as camera RAW", name)
		}
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

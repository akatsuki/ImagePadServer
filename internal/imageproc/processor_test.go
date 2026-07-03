package imageproc

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/tiff"
)

func TestDefaultOptionsUsesWebP(t *testing.T) {
	opts := DefaultOptions()
	if opts.Format != "webp" {
		t.Fatalf("Format = %q, want webp", opts.Format)
	}
	if opts.JPEGQuality != 85 {
		t.Fatalf("JPEGQuality = %d, want 85", opts.JPEGQuality)
	}
	if opts.WebPQuality != 80 {
		t.Fatalf("WebPQuality = %d, want 80", opts.WebPQuality)
	}
	if opts.PNGQuality != "lossless" {
		t.Fatalf("PNGQuality = %q, want lossless", opts.PNGQuality)
	}
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
	if result.ContentType != "image/webp" {
		t.Fatalf("content type = %s, want image/webp", result.ContentType)
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
}

func TestProcessWebPOutput(t *testing.T) {
	requireFFmpeg(t)
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewRGBA(image.Rect(0, 0, 32, 24))); err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.Format = "webp"
	result, err := Process(&input, "input.png", t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "image/webp" {
		t.Fatalf("ContentType = %q, want image/webp", result.ContentType)
	}
	if result.PublicName != "current.webp" {
		t.Fatalf("PublicName = %q, want current.webp", result.PublicName)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("expected non-empty webp output")
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

func TestProcessCameraRAWUsesLargestEmbeddedJPEGPreview(t *testing.T) {
	thumb := image.NewRGBA(image.Rect(0, 0, 160, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			thumb.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	var input bytes.Buffer
	if err := tiff.Encode(&input, thumb, nil); err != nil {
		t.Fatal(err)
	}
	input.Write(bytes.Repeat([]byte{0}, 64))
	input.Write(makeJPEG(t, 3936, 2624))

	opts := DefaultOptions()
	opts.Format = "png"
	opts.MaxDimension = 4096
	opts.MaxBytes = 120 << 20
	result, err := Process(bytes.NewReader(input.Bytes()), "DSC_4750.NEF", t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Width != 3936 || result.Height != 2624 {
		t.Fatalf("result size = %dx%d, want 3936x2624", result.Width, result.Height)
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

func makeJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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

	for _, name := range []string{"photo.jpg", "image.png", "movie.mp4", "raw.txt", "sample.avif", "sample.heic", "sample.heif", "sample.jxl"} {
		if IsCameraRAWName(name) {
			t.Fatalf("expected %s not to be treated as camera RAW", name)
		}
	}
}

func TestIsModernCompressedImageName(t *testing.T) {
	for _, name := range []string{"sample.avif", "sample.heic", "sample.heif", "sample.jxl"} {
		if !isModernCompressedImageName(name) {
			t.Fatalf("expected %s to be treated as a modern compressed image", name)
		}
	}
	for _, name := range []string{"sample.jpg", "sample.png", "sample.raw", "sample.docx"} {
		if isModernCompressedImageName(name) {
			t.Fatalf("expected %s not to be treated as a modern compressed image", name)
		}
	}
}

func TestDecodeModernCompressedImageInvalidInput(t *testing.T) {
	requireFFmpeg(t)
	_, err := decodeModernCompressedImage([]byte("not an image"), "bad.avif", t.TempDir(), 2048)
	if err == nil {
		t.Fatal("expected invalid modern compressed image to fail")
	}
}

func TestProcessGeneratedAVIFInput(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	avifPath := filepath.Join(t.TempDir(), "input.avif")
	generateModernFixture(t, ffmpeg, avifPath)

	in, err := os.Open(avifPath)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()

	opts := DefaultOptions()
	opts.Format = "png"
	result, err := Process(in, "input.avif", t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "image/png" {
		t.Fatalf("ContentType = %q, want image/png", result.ContentType)
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

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		ffmpeg = os.Getenv("IMAGEPAD_FFMPEG")
	}
	if ffmpeg == "" {
		t.Skip("ffmpeg not available")
	}
	return ffmpeg
}

func generateModernFixture(t *testing.T, ffmpeg, outPath string) {
	t.Helper()
	cmd := exec.Command(ffmpeg,
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-f", "lavfi",
		"-i", "color=c=red:s=16x12:d=1",
		"-frames:v", "1",
		outPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot generate %s fixture: %v: %s", filepath.Ext(outPath), err, strings.TrimSpace(string(output)))
	}
}

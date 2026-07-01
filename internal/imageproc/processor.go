package imageproc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"imagepadserver/internal/toolchain"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	maxImageDimension = 8192
	maxImageBytes     = 120 << 20
)

type Options struct {
	MaxDimension  int
	Format        string
	JPEGQuality   int
	WebPQuality   int
	PNGQuality    string
	MaxInputBytes int64
	MaxBytes      int64
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
		MaxDimension:  2048,
		Format:        "webp",
		JPEGQuality:   85,
		WebPQuality:   80,
		PNGQuality:    "lossless",
		MaxInputBytes: maxImageBytes,
		MaxBytes:      30 << 20,
	}
}

func Process(reader io.Reader, name string, outDir string, opts Options) (Result, error) {
	if opts.MaxDimension <= 0 || opts.MaxDimension > maxImageDimension {
		opts.MaxDimension = 2048
	}
	if opts.JPEGQuality <= 0 || opts.JPEGQuality > 100 {
		opts.JPEGQuality = 85
	}
	if opts.WebPQuality <= 0 || opts.WebPQuality > 100 {
		opts.WebPQuality = 80
	}
	if opts.PNGQuality == "" {
		opts.PNGQuality = "lossless"
	}
	if opts.MaxBytes <= 0 || opts.MaxBytes > maxImageBytes {
		opts.MaxBytes = maxImageBytes
	}
	if opts.MaxInputBytes <= 0 || opts.MaxInputBytes > maxImageBytes {
		opts.MaxInputBytes = maxImageBytes
	}
	opts.Format = strings.ToLower(opts.Format)
	switch opts.Format {
	case "jpeg", "jpg":
		opts.Format = "jpeg"
	case "png", "webp":
	default:
		opts.Format = "webp"
	}

	limited := io.LimitReader(reader, opts.MaxInputBytes+1)
	input, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, err
	}
	if int64(len(input)) > opts.MaxInputBytes {
		return Result{}, fmt.Errorf("image exceeds size limit of %d bytes", opts.MaxInputBytes)
	}
	orientation := exifOrientation(input)

	img, format, err := decodeImage(input, name, outDir, opts.MaxDimension)
	if err != nil {
		return Result{}, fmt.Errorf("unsupported or invalid image: %w", err)
	}
	_ = format

	img = applyOrientation(img, orientation)
	resized := resizeToFit(img, opts.MaxDimension)
	width := resized.Bounds().Dx()
	height := resized.Bounds().Dy()

	ext := ".jpg"
	contentType := "image/jpeg"
	if opts.Format == "png" {
		ext = ".png"
		contentType = "image/png"
	} else if opts.Format == "webp" {
		ext = ".webp"
		contentType = "image/webp"
	}

	path := filepath.Join(outDir, "processed"+ext)
	switch opts.Format {
	case "png":
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, resized); err != nil {
			return Result{}, err
		}
		if int64(buf.Len()) > opts.MaxBytes {
			return Result{}, fmt.Errorf("encoded image exceeds size limit of %d bytes", opts.MaxBytes)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
			return Result{}, err
		}
		if _, err := OptimizePNG(path, opts.PNGQuality); err != nil {
			return Result{}, err
		}
	case "webp":
		if err := EncodeWebP(resized, path, opts.WebPQuality); err != nil {
			return Result{}, fmt.Errorf("webp encode: %w", err)
		}
		stat, err := os.Stat(path)
		if err != nil {
			return Result{}, err
		}
		if stat.Size() > opts.MaxBytes {
			_ = os.Remove(path)
			return Result{}, fmt.Errorf("encoded image exceeds size limit of %d bytes", opts.MaxBytes)
		}
	default:
		encoded, err := encodeJPEGWithinLimit(flatten(resized), opts.JPEGQuality, opts.MaxBytes)
		if err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			return Result{}, err
		}
	}

	return Result{
		Path:        path,
		PublicName:  "current" + ext,
		ContentType: contentType,
		Width:       width,
		Height:      height,
	}, nil
}

func decodeImage(input []byte, name, outDir string, maxDimension int) (image.Image, string, error) {
	if isSVG(input) {
		img, err := rasterizeSVG(input)
		return img, "svg", err
	}
	img, format, err := image.Decode(bytes.NewReader(input))
	if err == nil {
		return img, format, nil
	}
	if !IsCameraRAWName(name) {
		return nil, "", err
	}
	rawImg, rawErr := decodeCameraRAW(input, name, outDir, maxDimension)
	if rawErr != nil {
		return nil, "", fmt.Errorf("%w; camera RAW fallback failed: %v", err, rawErr)
	}
	return rawImg, "raw", nil
}

func IsCameraRAWName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	_, ok := cameraRAWExtensions[ext]
	return ok
}

var cameraRAWExtensions = map[string]struct{}{
	".arw": {},
	".srf": {},
	".sr2": {},
	".crw": {},
	".cr2": {},
	".cr3": {},
	".rw2": {},
	".raw": {},
	".orf": {},
	".raf": {},
	".nef": {},
	".nrw": {},
	".x3f": {},
	".dng": {},
}

func decodeCameraRAW(input []byte, name, outDir string, maxDimension int) (image.Image, error) {
	ffmpeg, err := toolchain.EnsureFFmpeg()
	if err != nil {
		return nil, fmt.Errorf("FFmpeg is required to convert camera RAW files: %w", err)
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".raw"
	}
	in, err := os.CreateTemp(outDir, "raw-source-*"+ext)
	if err != nil {
		return nil, err
	}
	inPath := in.Name()
	defer os.Remove(inPath)
	if _, err := in.Write(input); err != nil {
		_ = in.Close()
		return nil, err
	}
	if err := in.Close(); err != nil {
		return nil, err
	}

	out, err := os.CreateTemp(outDir, "raw-decoded-*.png")
	if err != nil {
		return nil, err
	}
	outPath := out.Name()
	_ = out.Close()
	defer os.Remove(outPath)

	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-i", inPath,
		"-frames:v", "1",
	}
	if maxDimension > 0 {
		args = append(args, "-vf", rawScaleFilter(maxDimension))
	}
	args = append(args, outPath)

	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	output, err := toolchain.CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, trimCommandOutput(output))
	}

	file, err := os.Open(outPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, err := png.Decode(file)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func rawScaleFilter(maxDimension int) string {
	if maxDimension <= 0 || maxDimension > maxImageDimension {
		maxDimension = 2048
	}
	max := fmt.Sprintf("%d", maxDimension)
	return "scale=w='min(" + max + ",iw)':h='min(" + max + ",ih)':force_original_aspect_ratio=decrease"
}

func trimCommandOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 700 {
		return text[len(text)-700:]
	}
	if text == "" {
		return "no output"
	}
	return text
}

func isSVG(input []byte) bool {
	head := bytes.TrimSpace(input[:min(len(input), 1024)])
	head = bytes.TrimPrefix(head, []byte{0xef, 0xbb, 0xbf})
	head = bytes.ToLower(head)
	if bytes.HasPrefix(head, []byte("<svg")) {
		return true
	}
	if bytes.HasPrefix(head, []byte("<?xml")) || bytes.HasPrefix(head, []byte("<!doctype")) {
		return bytes.Contains(head, []byte("<svg"))
	}
	return false
}

func rasterizeSVG(input []byte) (image.Image, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	width := int(math.Ceil(icon.ViewBox.W))
	height := int(math.Ceil(icon.ViewBox.H))
	if width <= 0 || height <= 0 {
		width, height = 1024, 1024
	}
	if width > 4096 || height > 4096 {
		scale := math.Min(4096/float64(width), 4096/float64(height))
		width = int(math.Max(1, math.Round(float64(width)*scale)))
		height = int(math.Max(1, math.Round(float64(height)*scale)))
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	scanner := rasterx.NewScannerGV(width, height, img, img.Bounds())
	raster := rasterx.NewDasher(width, height, scanner)
	icon.SetTarget(0, 0, float64(width), float64(height))
	icon.Draw(raster, 1)
	return img, nil
}

func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	offset := 2
	for offset+4 <= len(data) {
		if data[offset] != 0xff {
			return 1
		}
		marker := data[offset+1]
		offset += 2
		for marker == 0xff && offset < len(data) {
			marker = data[offset]
			offset++
		}
		if marker == 0xda || marker == 0xd9 {
			return 1
		}
		if offset+2 > len(data) {
			return 1
		}
		segmentLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if segmentLen < 2 || offset+segmentLen > len(data) {
			return 1
		}
		segment := data[offset+2 : offset+segmentLen]
		if marker == 0xe1 && len(segment) > 6 && bytes.Equal(segment[:6], []byte("Exif\x00\x00")) {
			return tiffOrientation(segment[6:])
		}
		offset += segmentLen
	}
	return 1
}

func tiffOrientation(data []byte) int {
	if len(data) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(data[2:4]) != 42 {
		return 1
	}
	ifdOffset := int(order.Uint32(data[4:8]))
	if ifdOffset < 0 || ifdOffset+2 > len(data) {
		return 1
	}
	count := int(order.Uint16(data[ifdOffset : ifdOffset+2]))
	entryOffset := ifdOffset + 2
	for i := 0; i < count; i++ {
		entry := entryOffset + i*12
		if entry+12 > len(data) {
			return 1
		}
		tag := order.Uint16(data[entry : entry+2])
		if tag != 0x0112 {
			continue
		}
		fieldType := order.Uint16(data[entry+2 : entry+4])
		values := order.Uint32(data[entry+4 : entry+8])
		if fieldType != 3 || values < 1 {
			return 1
		}
		value := int(order.Uint16(data[entry+8 : entry+10]))
		if value >= 1 && value <= 8 {
			return value
		}
		return 1
	}
	return 1
}

func applyOrientation(src image.Image, orientation int) image.Image {
	switch orientation {
	case 2:
		return flipHorizontal(src)
	case 3:
		return rotate180(src)
	case 4:
		return flipVertical(src)
	case 5:
		return rotate90CW(flipHorizontal(src))
	case 6:
		return rotate90CW(src)
	case 7:
		return rotate90CW(flipVertical(src))
	case 8:
		return rotate90CCW(src)
	default:
		return src
	}
}

func flipHorizontal(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func flipVertical(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, h-1-y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func rotate180(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, h-1-y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func rotate90CW(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func rotate90CCW(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(y, w-1-x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func encodeJPEGWithinLimit(img image.Image, quality int, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = maxImageBytes
	}
	triedMinimum := false
	for q := quality; q >= 50; q -= 8 {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
		if int64(buf.Len()) <= maxBytes {
			return buf.Bytes(), nil
		}
		triedMinimum = q == 50
	}
	if !triedMinimum {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50}); err != nil {
			return nil, err
		}
		if int64(buf.Len()) <= maxBytes {
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("failed to encode jpeg within size limit of %d bytes", maxBytes)
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
	return flattenWithBackground(src, color.White)
}

func flattenWithBackground(src image.Image, bg color.Color) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Over)
	return dst
}

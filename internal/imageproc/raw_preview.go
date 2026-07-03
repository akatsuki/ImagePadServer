package imageproc

import (
	"bytes"
	"image"
	"image/jpeg"
)

const maxEmbeddedRAWPreviewPixels = 200_000_000

func decodeEmbeddedRAWPreview(input []byte) (image.Image, bool, error) {
	start, ok := largestEmbeddedJPEGStart(input)
	if !ok {
		return nil, false, nil
	}
	img, err := jpeg.Decode(bytes.NewReader(input[start:]))
	if err != nil {
		return nil, false, err
	}
	return img, true, nil
}

func largestEmbeddedJPEGStart(input []byte) (int, bool) {
	bestStart := -1
	bestArea := 0
	for offset := 0; offset < len(input)-2; {
		idx := bytes.Index(input[offset:], []byte{0xff, 0xd8, 0xff})
		if idx < 0 {
			break
		}
		start := offset + idx
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(input[start:]))
		if err == nil && plausibleEmbeddedJPEGSize(cfg.Width, cfg.Height) {
			area := cfg.Width * cfg.Height
			if area > bestArea {
				bestArea = area
				bestStart = start
			}
		}
		offset = start + 3
	}
	return bestStart, bestStart >= 0
}

func plausibleEmbeddedJPEGSize(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	area := width * height
	if area <= 0 || area > maxEmbeddedRAWPreviewPixels {
		return false
	}
	if width > height*8 || height > width*8 {
		return false
	}
	return true
}

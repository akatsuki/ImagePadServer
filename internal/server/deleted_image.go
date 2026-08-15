package server

import (
	"image"
	"image/color"
)

func deletedImage() image.Image {
	return placeholderImage("IMAGE", "DELETED", "CLEARED", 20, 14)
}

// inactiveImage は非公開（unpublished）アドレス到達時のプレースホルダ画像
// （"ERROR INACTIVE ADDRESS"）を返す。
func inactiveImage() image.Image {
	return placeholderImage("ERROR", "INACTIVE", "ADDRESS", 16, 14)
}

// placeholderImage は「タイトル＋主文言＋副文言」のプレースホルダ画像を生成する。
// deletedImage（削除済）と inactiveImage（非公開）で共用し、プレースホルダ機構を
// 一元化する（重複禁止）。
func placeholderImage(title, primary, secondary string, primaryScale, secondaryScale int) image.Image {
	const width = 1024
	const height = 1024
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, 0, 0, width, height, color.RGBA{R: 18, G: 23, B: 28, A: 255})
	fillRect(img, 0, 444, width, 580, color.RGBA{R: 180, G: 48, B: 48, A: 255})
	drawBlockText(img, title, centeredX(title, 16), 294, 16, color.RGBA{R: 176, G: 188, B: 198, A: 255})
	drawBlockText(img, primary, centeredX(primary, primaryScale), 462, primaryScale, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	drawBlockText(img, secondary, centeredX(secondary, secondaryScale), 640, secondaryScale, color.RGBA{R: 176, G: 188, B: 198, A: 255})
	return img
}

func centeredX(text string, scale int) int {
	x := (1024 - len(text)*6*scale) / 2
	if x < 0 {
		return 0
	}
	return x
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func drawBlockText(img *image.RGBA, text string, x, y, scale int, c color.RGBA) {
	cursor := x
	for _, r := range text {
		if r == ' ' {
			cursor += 4 * scale
			continue
		}
		glyph, ok := blockGlyphs[r]
		if !ok {
			cursor += 4 * scale
			continue
		}
		for row, bits := range glyph {
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) == 0 {
					continue
				}
				fillRect(img, cursor+col*scale, y+row*scale, cursor+(col+1)*scale, y+(row+1)*scale, c)
			}
		}
		cursor += 6 * scale
	}
}

var blockGlyphs = map[rune][7]byte{
	'A': {0x0e, 0x11, 0x11, 0x1f, 0x11, 0x11, 0x11},
	'C': {0x0f, 0x10, 0x10, 0x10, 0x10, 0x10, 0x0f},
	'D': {0x1e, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1e},
	'E': {0x1f, 0x10, 0x10, 0x1e, 0x10, 0x10, 0x1f},
	'G': {0x0f, 0x10, 0x10, 0x13, 0x11, 0x11, 0x0f},
	'I': {0x1f, 0x04, 0x04, 0x04, 0x04, 0x04, 0x1f},
	'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1f},
	'M': {0x11, 0x1b, 0x15, 0x15, 0x11, 0x11, 0x11},
	'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O': {0x0e, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0e},
	'R': {0x1e, 0x11, 0x11, 0x1e, 0x14, 0x12, 0x11},
	'S': {0x0f, 0x10, 0x10, 0x0e, 0x01, 0x01, 0x1e},
	'T': {0x1f, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0a, 0x04},
}

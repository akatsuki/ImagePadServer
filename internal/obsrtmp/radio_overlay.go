package obsrtmp

import (
	"image"
	"image/color"
	"image/draw"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

type OverlayMode string

const (
	OverlayModeOff           OverlayMode = "off"
	OverlayModeNotifications OverlayMode = "notifications"
)

type OverlayTrack struct {
	ID     string
	Title  string
	Artist string
}

type OverlaySnapshot struct {
	Mode          OverlayMode
	Current       *OverlayTrack
	Next          *OverlayTrack
	ShowNextFrom  time.Duration
	ShowNextUntil time.Duration
	Revision      uint64
}

type OverlayRenderer struct {
	mu              sync.Mutex
	lastRevision    uint64
	revisionStarted time.Duration
}

func NewOverlayRenderer() *OverlayRenderer {
	return &OverlayRenderer{}
}

func (r *OverlayRenderer) RenderRGBA(width, height int, elapsed time.Duration, snapshot OverlaySnapshot) []byte {
	if width <= 0 || height <= 0 {
		return nil
	}
	frame := make([]byte, width*height*4)
	if snapshot.Mode != OverlayModeNotifications || snapshot.Next == nil || elapsed < snapshot.ShowNextFrom {
		return frame
	}
	if snapshot.ShowNextUntil > snapshot.ShowNextFrom && elapsed >= snapshot.ShowNextUntil {
		return frame
	}

	r.mu.Lock()
	if r.lastRevision != snapshot.Revision {
		r.lastRevision = snapshot.Revision
		r.revisionStarted = elapsed
	}
	animationElapsed := elapsed - r.revisionStarted
	r.mu.Unlock()

	alpha := uint8(220)
	if animationElapsed < 300*time.Millisecond {
		progress := float64(animationElapsed) / float64(300*time.Millisecond)
		if progress < 0 {
			progress = 0
		}
		// Keep the first frame visible while still making revision restarts
		// deterministic and observable.
		alpha = uint8(36 + progress*184)
	}

	img := &image.RGBA{Pix: frame, Stride: width * 4, Rect: image.Rect(0, 0, width, height)}
	panelHeight := height / 4
	if panelHeight < 38 {
		panelHeight = 38
	}
	if panelHeight > height {
		panelHeight = height
	}
	panelWidth := width * 3 / 4
	if panelWidth < 120 {
		panelWidth = width
	}
	x0 := (width - panelWidth) / 2
	y0 := height - panelHeight - maxInt(8, height/24)
	if y0 < 0 {
		y0 = 0
	}
	draw.Draw(img, image.Rect(x0, y0, x0+panelWidth, y0+panelHeight), &image.Uniform{C: color.RGBA{R: 12, G: 18, B: 24, A: alpha}}, image.Point{}, draw.Over)

	face := basicfont.Face7x13
	textAlpha := alpha
	d := &font.Drawer{Dst: img, Src: &image.Uniform{C: color.RGBA{R: 245, G: 248, B: 250, A: textAlpha}}, Face: face}
	d.Dot = fixed.P(x0+12, y0+18)
	d.DrawString(trimOverlayText(snapshot.Next.Title, 42))
	if snapshot.Next.Artist != "" && panelHeight >= 38 {
		d.Src = &image.Uniform{C: color.RGBA{R: 170, G: 205, B: 214, A: textAlpha}}
		d.Dot = fixed.P(x0+12, y0+34)
		d.DrawString(trimOverlayText(snapshot.Next.Artist, 42))
	}
	return frame
}

func trimOverlayText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

package video

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Rect
// ---------------------------------------------------------------------------

// Rect represents a positioned rectangle in the visualizer layout.
type Rect struct {
	X, Y, W, H int
}

// ---------------------------------------------------------------------------
// VisualizerLayout
// ---------------------------------------------------------------------------

// VisualizerLayout holds the canonical positions of every visualizer element.
type VisualizerLayout struct {
	Artwork  Rect
	Title    Rect
	Artist   Rect
	Album    Rect
	Spectrum Rect
	Loudness Rect
	Progress Rect
	Time     Rect
}

// ---------------------------------------------------------------------------
// TextMetrics
// ---------------------------------------------------------------------------

// TextMetrics holds the measured pixel dimensions of rendered text.
type TextMetrics struct {
	Width, Height int
}

// ---------------------------------------------------------------------------
// Base layout at 1280x720 (spec section 6)
// ---------------------------------------------------------------------------

var baseLayout = VisualizerLayout{
	Artwork:  Rect{X: 96, Y: 152, W: 288, H: 288},
	Title:    Rect{X: 432, Y: 152, W: 752, H: 58},
	Artist:   Rect{X: 432, Y: 224, W: 752, H: 34},
	Album:    Rect{X: 432, Y: 264, W: 752, H: 30},
	Spectrum: Rect{X: 432, Y: 320, W: 752, H: 168},
	Loudness: Rect{X: 64, Y: 548, W: 1000, H: 80},
	Progress: Rect{X: 64, Y: 650, W: 1000, H: 8},
	Time:     Rect{X: 1088, Y: 632, W: 128, H: 32},
}

// LayoutForSize computes the canonical visualizer layout for the given output
// dimensions. It scales the 1280x720 base layout uniformly. Returns an error
// if width or height is zero or negative.
func LayoutForSize(width, height int) (VisualizerLayout, error) {
	if width <= 0 {
		return VisualizerLayout{}, errors.New("width must be positive")
	}
	if height <= 0 {
		return VisualizerLayout{}, errors.New("height must be positive")
	}

	sx := float64(width) / 1280.0
	sy := float64(height) / 720.0

	scale := func(v int, s float64) int {
		return int(math.Round(float64(v) * s))
	}

	return VisualizerLayout{
		Artwork:  Rect{X: scale(baseLayout.Artwork.X, sx), Y: scale(baseLayout.Artwork.Y, sy), W: scale(baseLayout.Artwork.W, sx), H: scale(baseLayout.Artwork.H, sy)},
		Title:    Rect{X: scale(baseLayout.Title.X, sx), Y: scale(baseLayout.Title.Y, sy), W: scale(baseLayout.Title.W, sx), H: scale(baseLayout.Title.H, sy)},
		Artist:   Rect{X: scale(baseLayout.Artist.X, sx), Y: scale(baseLayout.Artist.Y, sy), W: scale(baseLayout.Artist.W, sx), H: scale(baseLayout.Artist.H, sy)},
		Album:    Rect{X: scale(baseLayout.Album.X, sx), Y: scale(baseLayout.Album.Y, sy), W: scale(baseLayout.Album.W, sx), H: scale(baseLayout.Album.H, sy)},
		Spectrum: Rect{X: scale(baseLayout.Spectrum.X, sx), Y: scale(baseLayout.Spectrum.Y, sy), W: scale(baseLayout.Spectrum.W, sx), H: scale(baseLayout.Spectrum.H, sy)},
		Loudness: Rect{X: scale(baseLayout.Loudness.X, sx), Y: scale(baseLayout.Loudness.Y, sy), W: scale(baseLayout.Loudness.W, sx), H: scale(baseLayout.Loudness.H, sy)},
		Progress: Rect{X: scale(baseLayout.Progress.X, sx), Y: scale(baseLayout.Progress.Y, sy), W: scale(baseLayout.Progress.W, sx), H: scale(baseLayout.Progress.H, sy)},
		Time:     Rect{X: scale(baseLayout.Time.X, sx), Y: scale(baseLayout.Time.Y, sy), W: scale(baseLayout.Time.W, sx), H: scale(baseLayout.Time.H, sy)},
	}, nil
}

// ---------------------------------------------------------------------------
// ScrollOffset
// ---------------------------------------------------------------------------

// ScrollOffset returns the horizontal scroll offset for a text field at the
// given elapsed time.  It implements the spec section 9.1 rolling behaviour.
//
//   - If textWidth <= viewportWidth, the offset is always 0.
//   - Otherwise the text pauses at the left for 3 seconds, scrolls left at
//     40 canonical pixels per second until the right edge reaches the viewport
//     right edge, then resets immediately.
func ScrollOffset(elapsed, textWidth, viewportWidth float64) float64 {
	if textWidth <= viewportWidth {
		return 0
	}

	overflow := textWidth - viewportWidth // D
	cycleDuration := 3.0 + overflow/40.0  // pause + scroll time

	phase := math.Mod(elapsed, cycleDuration)
	if phase < 3.0 {
		return 0 // initial pause
	}

	scrollPhase := phase - 3.0
	offset := -40.0 * scrollPhase

	// Clamp so we never scroll past the text's right edge
	if offset < -overflow {
		offset = -overflow
	}

	return offset
}

// ---------------------------------------------------------------------------
// FormatMediaTime
// ---------------------------------------------------------------------------

// FormatMediaTime formats an integer seconds value into a display string.
// Durations under one hour produce "M:SS"; one hour or more produce "H:MM:SS".
func FormatMediaTime(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 3600 {
		m := seconds / 60
		s := seconds % 60
		return fmt.Sprintf("%d:%02d", m, s)
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

// ---------------------------------------------------------------------------
// MeasureTextWithFFmpeg
// ---------------------------------------------------------------------------

// MeasureTextWithFFmpeg measures the rendered pixel dimensions of a text
// string by rendering it onto a transparent PNG with FFmpeg's drawtext filter
// and scanning the alpha channel for non-transparent bounds.
func MeasureTextWithFFmpeg(ctx context.Context, ffmpeg, fontPath, text string, fontSize int) (TextMetrics, error) {
	if len(text) == 0 {
		return TextMetrics{}, errors.New("cannot measure empty text")
	}

	tmpDir, err := os.MkdirTemp("", "imagepad-text-measure-*")
	if err != nil {
		return TextMetrics{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	outPath := filepath.Join(tmpDir, "measure.png")

	// Render at 2x resolution with a large canvas, then scan alpha bounds.
	// Use a white font on transparent background (alpha-only detection).
	canvasW := fontSize * len([]rune(text)) * 2
	if canvasW < 100 {
		canvasW = 100
	}
	canvasH := fontSize * 4
	if canvasH < 100 {
		canvasH = 100
	}

	// Build filter: create transparent canvas, draw text.
	// Use forward-slash paths for FFmpeg Windows compatibility.
	// Escape any colons in the path with \: so the filtergraph parser
	// treats them as literal colons (e.g. C:\... -> C\:/...).
	fontSizeStr := strconv.Itoa(fontSize)
	escText := escapeDrawText(text)
	escFont := strings.ReplaceAll(fontPath, "\\", "/")
	escFont = strings.ReplaceAll(escFont, ":", "\\:")
	filter := fmt.Sprintf(
		"color=c=0x00000000:s=%dx%d:d=1,drawtext=text='%s':fontfile='%s':fontsize=%s:fontcolor=white:x=(w-text_w)/2:y=(h-text_h)/2[out]",
		canvasW, canvasH, escText, escFont, fontSizeStr,
	)

	args := []string{
		"-v", "error",
		"-filter_complex", filter,
		"-map", "[out]",
		"-frames:v", "1",
		"-y", outPath,
	}

	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return TextMetrics{}, fmt.Errorf("ffmpeg drawtext measure failed: %w\n%s", err, string(output))
	}

	f, err := os.Open(outPath)
	if err != nil {
		return TextMetrics{}, fmt.Errorf("open measure output: %w", err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return TextMetrics{}, fmt.Errorf("decode measure PNG: %w", err)
	}

	bounds := alphaBounds(img)
	if bounds == nil {
		// If no non-transparent pixel was found, try a different approach
		// Just return reasonable defaults based on font size
		return TextMetrics{Width: fontSize * len([]rune(text)), Height: fontSize}, nil
	}

	return TextMetrics{
		Width:  bounds.Dx(),
		Height: bounds.Dy(),
	}, nil
}

// alphaBounds returns the smallest rectangle containing all non-transparent
// pixels in the image. Returns nil when the image is fully transparent.
func alphaBounds(img image.Image) *image.Rectangle {
	bounds := img.Bounds()
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X, bounds.Min.Y
	hasPixel := false

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a > 0 {
				hasPixel = true
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}

	if !hasPixel {
		return nil
	}

	r := image.Rect(minX, minY, maxX+1, maxY+1)
	return &r
}

// escapeDrawText escapes a string for use in an FFmpeg drawtext filter
// expression.  The drawtext filter uses single-quote quoting rules in the
// filter graph: literal single quotes are written as '\'' (close, escaped
// literal, reopen).
func escapeDrawText(s string) string {
	// Replace backslashes first
	s = strings.ReplaceAll(s, "\\", "\\\\")
	// Replace single quotes with the drawtext escape sequence
	s = strings.ReplaceAll(s, "'", "'\\\\''")
	return s
}

// ---------------------------------------------------------------------------
// Color utilities (used by ASS generation)
// ---------------------------------------------------------------------------

// assColor converts a color.RGBA to an ASS-format colour string (&HAABBGGRR).
func assColor(c color.RGBA) string {
	return fmt.Sprintf("&H%02X%02X%02X%02X", c.A, c.B, c.G, c.R)
}

// ---------------------------------------------------------------------------
// Generated-temp-dir helpers
// ---------------------------------------------------------------------------

// hideWindow hides the console window for a command (Windows no-op on other OS).
// Declared here so the package can compile against the existing hide_windows.go.

// CombinedOutputTrackedFFmpeg is declared in toolchain.go.

// ---------------------------------------------------------------------------
// Binary read helpers for ASS
// ---------------------------------------------------------------------------

func float64ToBytes(f float64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, math.Float64bits(f))
	return b
}

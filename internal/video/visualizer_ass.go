package video

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BuildVisualizerASS generates a complete ASS subtitle file for the audio
// visualizer.  It produces [Script Info], [V4+ Styles], and [Events] sections.
//
// The styles use the resolved ASSFamily names from the font files.  Font
// resolution errors are returned rather than silently falling back to
// filesystem paths (AV-824).
func BuildVisualizerASS(metadata AudioMetadata, duration float64, layout VisualizerLayout, fonts FontSet, metrics map[string]TextMetrics) (string, error) {
	return BuildVisualizerASSWithMode(metadata, duration, layout, fonts, metrics, ForegroundMode{Color: color.RGBA{255, 255, 255, 255}}, 1280, 720)
}

// ASSClipPadding returns the vertical clip padding for ASS clip rectangles.
// At 1280px width the padding is 2 canonical pixels; it scales with output
// width and has a minimum of 1.
func ASSClipPadding(width int) int {
	return max(1, int(math.Round(2.0*float64(width)/1280.0)))
}

func BuildVisualizerASSWithMode(metadata AudioMetadata, duration float64, layout VisualizerLayout, fonts FontSet, metrics map[string]TextMetrics, mode ForegroundMode, width, height int) (string, error) {
	var b strings.Builder

	clipPad := ASSClipPadding(width)

	// Resolve font identities for ASS family names (AV-824).
	// Require resolved font faces; return an error when font files cannot be
	// read or have invalid name tables. Never emit filesystem paths in ASS
	// Fontname.
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		return "", fmt.Errorf("resolve font faces for ASS: %w", err)
	}
	titleFontName := faces.SemiBold600.ASSFamily
	artistFontName := faces.Medium500.ASSFamily
	albumFontName := faces.Regular400.ASSFamily
	timeFontName := faces.Medium500.ASSFamily

	// --- [Script Info] ---
	b.WriteString("[Script Info]\n")
	b.WriteString("ScriptType: v4.00+\n")
	b.WriteString(fmt.Sprintf("PlayResX: %d\n", width))
	b.WriteString(fmt.Sprintf("PlayResY: %d\n", height))
	b.WriteString("ScaledBorderAndShadow: yes\n")
	b.WriteString("WrapStyle: 2\n")
	b.WriteString("\n")

	// --- [V4+ Styles] ---
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")

	titleSize := scaledFontSize(48, width)
	artistSize := scaledFontSize(28, width)
	albumSize := scaledFontSize(24, width)
	timeSize := scaledFontSize(22, width)

	primary := assForegroundColor(mode.PrimaryColor, 0.88)
	// Use alignment 4 (middle-left) for title, artist, album. Title and artist
	// styles are only emitted when their text is present; an empty field is
	// skipped entirely (matching album) so it is never measured or rendered.
	if metadata.Title != "" {
		writeStyle(&b, "Title", titleFontName, titleSize, 600, 4, primary)
	}
	if metadata.Artist != "" {
		writeStyle(&b, "Artist", artistFontName, artistSize, 500, 4, primary)
	}
	// Use alignment 5 (middle-center) for time text.
	writeStyle(&b, "TimeText", timeFontName, timeSize, 500, 5, primary)

	if metadata.Album != "" {
		writeStyle(&b, "Album", albumFontName, albumSize, 400, 4, primary)
	}
	b.WriteString("\n")

	// --- [Events] ---
	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	totalSeconds := int(math.Ceil(duration))
	if totalSeconds < 1 {
		totalSeconds = 1
	}
	totalDuration := duration

	// Time event for each whole second
	for s := 0; s < totalSeconds; s++ {
		start := float64(s)
		end := start + 1.0
		if end > totalDuration {
			end = totalDuration
		}
		timeStr := FormatMediaTime(s) + " / " + FormatMediaTime(int(math.Floor(totalDuration)))

		// Position time text at the Time rect; center alignment (style 5)
		timeX := layout.Time.X + layout.Time.W/2
		timeY := layout.Time.Y + layout.Time.H/2

		writeDialogue(&b, assTimestamp(start), assTimestamp(end),
			"TimeText", fmt.Sprintf("\\pos(%d,%d)", timeX, timeY), timeStr)
	}

	// Title event: either stationary or scrolling
	titleMetrics := metrics["title"]
	titleWidth := titleMetrics.Width
	viewportW := layout.Title.W
	viewportX := layout.Title.X
	viewportY := layout.Title.Y
	viewportH := layout.Title.H

	if metadata.Title != "" {
		titleText := escapeASSText(metadata.Title)
		if titleWidth <= viewportW {
			// Stationary, left-aligned
			posX := viewportX
			posY := viewportY + viewportH/2
			clip := fmt.Sprintf("\\clip(%d,%d,%d,%d)", viewportX, viewportY-clipPad, viewportX+viewportW, viewportY+viewportH+clipPad)
			writeDialogue(&b, "0:00:00.00", assTimestamp(totalDuration),
				"Title", fmt.Sprintf("%s\\q2\\pos(%d,%d)", clip, posX, posY), titleText)
		} else {
			buildScrollingDialogue(&b, totalDuration, "Title", titleText, float64(titleWidth), float64(viewportW), float64(viewportX), float64(viewportY), float64(viewportH), clipPad, float64(width))
		}
	}

	// Artist event (only when non-empty)
	if metadata.Artist != "" {
		artistMetrics := metrics["artist"]
		artistWidth := artistMetrics.Width
		artistText := escapeASSText(metadata.Artist)

		if artistWidth <= layout.Artist.W {
			posX := layout.Artist.X
			posY := layout.Artist.Y + layout.Artist.H/2
			clip := fmt.Sprintf("\\clip(%d,%d,%d,%d)", layout.Artist.X, layout.Artist.Y-clipPad, layout.Artist.X+layout.Artist.W, layout.Artist.Y+layout.Artist.H+clipPad)
			writeDialogue(&b, "0:00:00.00", assTimestamp(totalDuration),
				"Artist", fmt.Sprintf("%s\\pos(%d,%d)", clip, posX, posY), artistText)
		} else {
			buildScrollingDialogue(&b, totalDuration, "Artist", artistText, float64(artistWidth), float64(layout.Artist.W), float64(layout.Artist.X), float64(layout.Artist.Y), float64(layout.Artist.H), clipPad, float64(width))
		}
	}

	// Album event (only when non-empty)
	if metadata.Album != "" {
		albumMetrics := metrics["album"]
		albumWidth := albumMetrics.Width
		albumText := escapeASSText(metadata.Album)

		if albumWidth <= layout.Album.W {
			posX := layout.Album.X
			posY := layout.Album.Y + layout.Album.H/2
			clip := fmt.Sprintf("\\clip(%d,%d,%d,%d)", layout.Album.X, layout.Album.Y-clipPad, layout.Album.X+layout.Album.W, layout.Album.Y+layout.Album.H+clipPad)
			writeDialogue(&b, "0:00:00.00", assTimestamp(totalDuration),
				"Album", fmt.Sprintf("%s\\pos(%d,%d)", clip, posX, posY), albumText)
		} else {
			buildScrollingDialogue(&b, totalDuration, "Album", albumText, float64(albumWidth), float64(layout.Album.W), float64(layout.Album.X), float64(layout.Album.Y), float64(layout.Album.H), clipPad, float64(width))
		}
	}

	return b.String(), nil
}

// ---------------------------------------------------------------------------
// ASS helpers
// ---------------------------------------------------------------------------

func writeStyle(b *strings.Builder, name, fontName string, fontSize, fontWeight, alignment int, primary string) {
	if alignment == 0 {
		alignment = 1 // left-aligned by default
	}
	b.WriteString(fmt.Sprintf("Style: %s,%s,%d,%s,&H000000FF,&H00000000,&H00000000,%d,0,0,0,100,100,0,0,1,0,0,%d,0,0,0,1\n",
		name, fontName, fontSize, primary, fontWeight, alignment))
}

func assForegroundColor(c color.RGBA, opacity float64) string {
	alpha := uint8(math.Round((1 - opacity) * 255))
	return fmt.Sprintf("&H%02X%02X%02X%02X", alpha, c.B, c.G, c.R)
}

// assTimestamp formats a float64 seconds value as ASS timestamp H:MM:SS.cc
func assTimestamp(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	h := int(seconds) / 3600
	m := (int(seconds) % 3600) / 60
	s := int(seconds) % 60
	cs := int(math.Round((seconds - float64(int(seconds))) * 100))
	if cs >= 100 {
		cs = 99
	}
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}

func writeDialogue(b *strings.Builder, start, end, style, override, text string) {
	b.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,%s,,0,0,0,,%s%s\n",
		start, end, style, "{"+override+"}", text))
}

// escapeASSText escapes special ASS characters in a text string.
func escapeASSText(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "{", "\\{")
	s = strings.ReplaceAll(s, "}", "\\}")
	s = strings.ReplaceAll(s, "\n", "\\N")
	return s
}

const (
	scrollExtraMoveSeconds = 2.0
	scrollBlankSeconds     = 0.5
	scrollFadeMilliseconds = 300
)

// scrollCycle computes the overflow distance, hold duration, extended move duration,
// and total cycle duration for scrolling metadata text.
//
// When text fits within the viewport (textWidth <= viewportWidth), all returned
// values are zero — no scrolling is needed.
//
// When overflow exists, hold is always 3.0 seconds. The scroll speed is
// 40 * outputWidth / 1280 (canonical pixels per second scaled to output
// resolution). Movement continues for two extra seconds at the same speed, then
// the viewport stays blank for 500 ms before the next cycle.
func scrollCycle(textWidth, viewportWidth, outputWidth float64) (overflow, hold, move, total float64) {
	if textWidth <= viewportWidth {
		return 0, 0, 0, 0
	}
	overflow = textWidth - viewportWidth
	speed := 40.0 * outputWidth / 1280.0
	hold = 3.0
	move = overflow/speed + scrollExtraMoveSeconds
	total = hold + move + scrollBlankSeconds
	return
}

// buildScrollingDialogue adds ASS events for a scrolling text field.
// clipPad is the vertical clip expansion (AV-821). outputWidth is the PlayResX
// value used to scale the scroll speed (AV-822).
func buildScrollingDialogue(b *strings.Builder, totalDuration float64, style, text string, textWidth, viewportW, viewportX, viewportY, viewportH float64, clipPad int, outputWidth float64) {
	overflow, hold, moveDuration, cycleDuration := scrollCycle(textWidth, viewportW, outputWidth)
	scaledSpeed := 40.0 * outputWidth / 1280.0

	posY := viewportY + viewportH/2.0

	// Clip rectangle for the viewport, expanded vertically by clipPad
	clipStr := fmt.Sprintf("\\clip(%d,%d,%d,%d)", int(viewportX), int(viewportY)-clipPad, int(viewportX+viewportW), int(viewportY+viewportH)+clipPad)

	if cycleDuration <= 0 {
		return
	}

	currentTime := 0.0
	for currentTime < totalDuration {
		cycleStart := currentTime
		cycleEnd := currentTime + cycleDuration

		// Pause phase
		pauseEnd := cycleStart + hold
		if pauseEnd > totalDuration {
			pauseEnd = totalDuration
		}

		pausePosX := int(viewportX)
		override := fmt.Sprintf("%s\\q2\\fad(%d,0)\\pos(%d,%d)", clipStr, scrollFadeMilliseconds, pausePosX, int(posY))
		writeDialogue(b, assTimestamp(cycleStart), assTimestamp(pauseEnd), style, override, text)

		if pauseEnd >= totalDuration {
			break
		}

		// Scroll phase
		scrollStart := pauseEnd
		scrollEnd := scrollStart + moveDuration
		if scrollEnd > totalDuration {
			scrollEnd = totalDuration
		}

		// Use \move for smooth scrolling
		startX := int(viewportX)
		endX := int(math.Round(viewportX - overflow - scaledSpeed*scrollExtraMoveSeconds))

		moveOverride := fmt.Sprintf("%s\\q2\\fad(0,%d)\\move(%d,%d,%d,%d)", clipStr, scrollFadeMilliseconds, startX, int(posY), endX, int(posY))
		writeDialogue(b, assTimestamp(scrollStart), assTimestamp(scrollEnd), style, moveOverride, text)

		currentTime = cycleEnd
	}
}

// fontSizeForHeight returns an appropriate font size for a viewport of the
// given height, assuming 1.2x line height and vertical centering.
func scaledFontSize(canonical, width int) int {
	return max(1, int(math.Round(float64(canonical)*float64(width)/1280.0)))
}

// ---------------------------------------------------------------------------
// MeasureASSEncodedWidth — libass-based text measurement (AV-824)
// ---------------------------------------------------------------------------

// MeasureASSEncodedWidth renders text through FFmpeg's ass filter (libass)
// and returns the pixel width of the rendered text's bounding box.
//
// Unlike MeasureTextWithFFmpeg (which uses drawtext/FreeType), this function
// measures width using the same libass rendering pipeline that encodes the
// actual video.  On Windows, drawtext and libass can disagree by 22–100 px;
// using libass for both measurement and encoding guarantees self-consistent
// scroll-or-stationary decisions.
//
// fontName and fontWeight are the same ASS family and numeric weight used by
// the production style. fontDir is the directory containing the font files.
func MeasureASSEncodedWidth(ctx context.Context, ffmpeg, fontName string, fontWeight int, fontDir, text string, fontSize int) (int, error) {
	tmpDir, err := os.MkdirTemp("", "imagepad-ass-width-*")
	if err != nil {
		return 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	assPath := filepath.Join(tmpDir, "measure.ass")
	outPath := filepath.Join(tmpDir, "measure.png")

	const maxCanvasWidth = 32768
	runeCount := len([]rune(text))
	canvasWidth := max(256, runeCount*fontSize*2+fontSize*4)
	if canvasWidth > maxCanvasWidth {
		return 0, fmt.Errorf("ASS text measurement canvas width %d exceeds limit %d", canvasWidth, maxCanvasWidth)
	}
	canvasHeight := max(200, fontSize*4)

	// Build a minimal ASS file using the exact production family and weight.
	var assContent strings.Builder
	assContent.WriteString("[Script Info]\n")
	assContent.WriteString("ScriptType: v4.00+\n")
	assContent.WriteString("WrapStyle: 2\n")
	assContent.WriteString(fmt.Sprintf("PlayResX: %d\n", canvasWidth))
	assContent.WriteString(fmt.Sprintf("PlayResY: %d\n", canvasHeight))
	assContent.WriteString("ScaledBorderAndShadow: no\n")
	assContent.WriteString("\n")
	assContent.WriteString("[V4+ Styles]\n")
	assContent.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	// Alignment 7 = top-left. Primary=white, no outline/shadow.
	assContent.WriteString(fmt.Sprintf("Style: Default,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,%d,0,0,0,100,100,0,0,1,0,0,7,0,0,0,1\n", fontName, fontSize, fontWeight))
	assContent.WriteString("\n")
	assContent.WriteString("[Events]\n")
	assContent.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	assContent.WriteString(fmt.Sprintf("Dialogue: 0,0:00:00.00,0:00:01.00,Default,,0,0,0,,{\\q2}%s\n", escapeASSText(text)))

	if err := os.WriteFile(assPath, []byte(assContent.String()), 0644); err != nil {
		return 0, fmt.Errorf("write ass: %w", err)
	}

	// Build FFmpeg filter using the same escaping as the production pipeline.
	escAss := escapeFilterPath(assPath)
	escFontDir := escapeFilterPath(fontDir)

	filter := fmt.Sprintf(
		"color=c=black:s=%dx%d:d=1,ass=filename='%s':fontsdir='%s'",
		canvasWidth, canvasHeight, escAss, escFontDir,
	)

	args := []string{
		"-v", "error",
		"-filter_complex", filter,
		"-frames:v", "1",
		"-y", outPath,
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffmpeg ass render: %w\n%s", err, string(output))
	}

	f, err := os.Open(outPath)
	if err != nil {
		return 0, fmt.Errorf("open ass output: %w", err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return 0, fmt.Errorf("decode ass PNG: %w", err)
	}

	// Scan for non-black pixels (white text on black background).
	bounds := nonBlackBounds(img)
	if bounds == nil {
		return 0, fmt.Errorf("no text pixels found in ass rendered frame for %q", text)
	}
	if bounds.Max.X >= canvasWidth {
		return 0, fmt.Errorf("ASS text measurement clipped at canvas width %d", canvasWidth)
	}
	return bounds.Dx(), nil
}

// nonBlackBounds returns the smallest rectangle containing all non-black
// pixels in the image. A pixel is considered "non-black" if any of its R, G,
// or B components exceeds a small threshold (to include antialiased edges).
// Returns nil when the image is entirely black.
func nonBlackBounds(img image.Image) *image.Rectangle {
	const threshold uint32 = 128
	bounds := img.Bounds()
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X, bounds.Min.Y
	hasPixel := false

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > threshold || g > threshold || b > threshold {
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

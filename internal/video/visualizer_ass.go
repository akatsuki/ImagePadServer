package video

import (
	"fmt"
	"image/color"
	"math"
	"strings"
)

// ASS colour constants
const (
	assWhite = "&H00FFFFFF"
)

// BuildVisualizerASS generates a complete ASS subtitle file for the audio
// visualizer.  It produces [Script Info], [V4+ Styles], and [Events] sections.
//
// The styles use the exact font paths from fonts, and the events include
// positioned/clipped text for title, artist, album, and per-second time text.
func BuildVisualizerASS(metadata AudioMetadata, duration float64, layout VisualizerLayout, fonts FontSet, metrics map[string]TextMetrics) string {
	return BuildVisualizerASSWithMode(metadata, duration, layout, fonts, metrics, ForegroundMode{Color: color.RGBA{255, 255, 255, 255}}, 1280, 720)
}

// ASSClipPadding returns the vertical clip padding for ASS clip rectangles.
// At 1280px width the padding is 2 canonical pixels; it scales with output
// width and has a minimum of 1.
func ASSClipPadding(width int) int {
	return max(1, int(math.Round(2.0*float64(width)/1280.0)))
}

func BuildVisualizerASSWithMode(metadata AudioMetadata, duration float64, layout VisualizerLayout, fonts FontSet, metrics map[string]TextMetrics, mode ForegroundMode, width, height int) string {
	var b strings.Builder

	clipPad := ASSClipPadding(width)

	// --- [Script Info] ---
	b.WriteString("[Script Info]\n")
	b.WriteString("ScriptType: v4.00+\n")
	b.WriteString(fmt.Sprintf("PlayResX: %d\n", width))
	b.WriteString(fmt.Sprintf("PlayResY: %d\n", height))
	b.WriteString("ScaledBorderAndShadow: yes\n")
	b.WriteString("\n")

	// --- [V4+ Styles] ---
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")

	titleSize := scaledFontSize(48, width)
	artistSize := scaledFontSize(28, width)
	albumSize := scaledFontSize(24, width)
	timeSize := scaledFontSize(22, width)

	primary := assForegroundColor(mode.Color, 0.88)
	// Use alignment 4 (middle-left) for title, artist, album.
	writeStyle(&b, "Title", fonts.SemiBold600, titleSize, 4, primary)
	writeStyle(&b, "Artist", fonts.Medium500, artistSize, 4, primary)
	// Use alignment 5 (middle-center) for time text.
	writeStyle(&b, "TimeText", fonts.Medium500, timeSize, 5, primary)

	if metadata.Album != "" {
		writeStyle(&b, "Album", fonts.Regular400, albumSize, 4, primary)
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

	titleText := escapeASSText(metadata.Title)
	if titleWidth <= viewportW {
		// Stationary, left-aligned
		posX := viewportX
		posY := viewportY + viewportH/2
		clip := fmt.Sprintf("\\clip(%d,%d,%d,%d)", viewportX, viewportY-clipPad, viewportX+viewportW, viewportY+viewportH+clipPad)
		writeDialogue(&b, "0:00:00.00", assTimestamp(totalDuration),
			"Title", fmt.Sprintf("%s\\pos(%d,%d)", clip, posX, posY), titleText)
	} else {
		buildScrollingDialogue(&b, totalDuration, "Title", titleText, float64(titleWidth), float64(viewportW), float64(viewportX), float64(viewportY), float64(viewportH), clipPad, float64(width))
	}

	// Artist event
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

	return b.String()
}

// ---------------------------------------------------------------------------
// ASS helpers
// ---------------------------------------------------------------------------

func writeStyle(b *strings.Builder, name, fontName string, fontSize, alignment int, primary string) {
	if alignment == 0 {
		alignment = 1 // left-aligned by default
	}
	b.WriteString(fmt.Sprintf("Style: %s,%s,%d,%s,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,0,0,%d,0,0,0,1\n",
		name, fontName, fontSize, primary, alignment))
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

// scrollCycle computes the overflow distance, hold duration, move duration,
// and total cycle duration for scrolling metadata text.
//
// When text fits within the viewport (textWidth <= viewportWidth), all returned
// values are zero — no scrolling is needed.
//
// When overflow exists, hold is always 3.0 seconds. The scroll speed is
// 40 * outputWidth / 1280 (canonical pixels per second scaled to output
// resolution). Move duration is overflow / speed. Total cycle is hold + move.
func scrollCycle(textWidth, viewportWidth, outputWidth float64) (overflow, hold, move, total float64) {
	if textWidth <= viewportWidth {
		return 0, 0, 0, 0
	}
	overflow = textWidth - viewportWidth
	speed := 40.0 * outputWidth / 1280.0
	hold = 3.0
	move = overflow / speed
	total = hold + move
	return
}

// buildScrollingDialogue adds ASS events for a scrolling text field.
// clipPad is the vertical clip expansion (AV-821). outputWidth is the PlayResX
// value used to scale the scroll speed (AV-822).
func buildScrollingDialogue(b *strings.Builder, totalDuration float64, style, text string, textWidth, viewportW, viewportX, viewportY, viewportH float64, clipPad int, outputWidth float64) {
	overflow, hold, _, cycleDuration := scrollCycle(textWidth, viewportW, outputWidth)

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
		override := fmt.Sprintf("%s\\pos(%d,%d)", clipStr, pausePosX, int(posY))
		writeDialogue(b, assTimestamp(cycleStart), assTimestamp(pauseEnd), style, override, text)

		if pauseEnd >= totalDuration {
			break
		}

		// Scroll phase
		scrollStart := pauseEnd
		scrollEnd := cycleStart + cycleDuration
		if scrollEnd > totalDuration {
			scrollEnd = totalDuration
		}

		// Use \move for smooth scrolling
		startX := int(viewportX)
		endX := int(viewportX - overflow)

		moveOverride := fmt.Sprintf("%s\\move(%d,%d,%d,%d)", clipStr, startX, int(posY), endX, int(posY))
		writeDialogue(b, assTimestamp(scrollStart), assTimestamp(scrollEnd), style, moveOverride, text)

		currentTime = cycleEnd
	}
}

// fontSizeForHeight returns an appropriate font size for a viewport of the
// given height, assuming 1.2x line height and vertical centering.
func scaledFontSize(canonical, width int) int {
	return max(1, int(math.Round(float64(canonical)*float64(width)/1280.0)))
}

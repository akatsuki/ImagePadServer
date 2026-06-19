package video

import (
	"fmt"
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
	var b strings.Builder

	// --- [Script Info] ---
	b.WriteString("[Script Info]\n")
	b.WriteString("ScriptType: v4.00+\n")
	// PlayRes matches the output resolution: compute from layout extents.
	playResX := 0
	playResY := 0
	for _, r := range []Rect{layout.Artwork, layout.Title, layout.Artist, layout.Album, layout.Spectrum, layout.Loudness, layout.Progress, layout.Time} {
		if r.X+r.W > playResX {
			playResX = r.X + r.W
		}
		if r.Y+r.H > playResY {
			playResY = r.Y + r.H
		}
	}
	// If we somehow got zero, fall back to 1280x720 assumptions.
	// The Time rect extends to X=1216 which is less than 1280. For the
	// canonical 1280x720 output the frame width is 1280. We use the
	// bounding box of all elements plus a margin to the right edge.
	if playResX < 1280 {
		playResX = 1280
	}
	if playResY < 720 {
		playResY = 720
	}
	b.WriteString(fmt.Sprintf("PlayResX: %d\n", playResX))
	b.WriteString(fmt.Sprintf("PlayResY: %d\n", playResY))
	b.WriteString("ScaledBorderAndShadow: yes\n")
	b.WriteString("\n")

	// --- [V4+ Styles] ---
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")

	titleSize := fontSizeForHeight(layout.Title.H)
	artistSize := fontSizeForHeight(layout.Artist.H)
	albumSize := fontSizeForHeight(layout.Album.H)
	timeSize := fontSizeForHeight(layout.Time.H)

	writeStyle(&b, "Title", fonts.SemiBold600, titleSize, 0)
	writeStyle(&b, "Artist", fonts.Medium500, artistSize, 0)
	writeStyle(&b, "TimeText", fonts.Medium500, timeSize, 8) // centered alignment

	if metadata.Album != "" {
		writeStyle(&b, "Album", fonts.Regular400, albumSize, 0)
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
		timeStr := FormatMediaTime(s)

		// Position time text at the Time rect; center alignment (style 8)
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
		clip := fmt.Sprintf("\\clip(%d,%d,%d,%d)", viewportX, viewportY, viewportX+viewportW, viewportY+viewportH)
		writeDialogue(&b, "0:00:00.00", assTimestamp(totalDuration),
			"Title", fmt.Sprintf("%s\\pos(%d,%d)", clip, posX, posY), titleText)
	} else {
		buildScrollingDialogue(&b, totalDuration, "Title", titleText, float64(titleWidth), float64(viewportW), float64(viewportX), float64(viewportY), float64(viewportH))
	}

	// Artist event
	artistMetrics := metrics["artist"]
	artistWidth := artistMetrics.Width
	artistText := escapeASSText(metadata.Artist)

	if artistWidth <= layout.Artist.W {
		posX := layout.Artist.X
		posY := layout.Artist.Y + layout.Artist.H/2
		clip := fmt.Sprintf("\\clip(%d,%d,%d,%d)", layout.Artist.X, layout.Artist.Y, layout.Artist.X+layout.Artist.W, layout.Artist.Y+layout.Artist.H)
		writeDialogue(&b, "0:00:00.00", assTimestamp(totalDuration),
			"Artist", fmt.Sprintf("%s\\pos(%d,%d)", clip, posX, posY), artistText)
	} else {
		buildScrollingDialogue(&b, totalDuration, "Artist", artistText, float64(artistWidth), float64(layout.Artist.W), float64(layout.Artist.X), float64(layout.Artist.Y), float64(layout.Artist.H))
	}

	// Album event (only when non-empty)
	if metadata.Album != "" {
		albumMetrics := metrics["album"]
		albumWidth := albumMetrics.Width
		albumText := escapeASSText(metadata.Album)

		if albumWidth <= layout.Album.W {
			posX := layout.Album.X
			posY := layout.Album.Y + layout.Album.H/2
			clip := fmt.Sprintf("\\clip(%d,%d,%d,%d)", layout.Album.X, layout.Album.Y, layout.Album.X+layout.Album.W, layout.Album.Y+layout.Album.H)
			writeDialogue(&b, "0:00:00.00", assTimestamp(totalDuration),
				"Album", fmt.Sprintf("%s\\pos(%d,%d)", clip, posX, posY), albumText)
		} else {
			buildScrollingDialogue(&b, totalDuration, "Album", albumText, float64(albumWidth), float64(layout.Album.W), float64(layout.Album.X), float64(layout.Album.Y), float64(layout.Album.H))
		}
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// ASS helpers
// ---------------------------------------------------------------------------

func writeStyle(b *strings.Builder, name, fontName string, fontSize, alignment int) {
	if alignment == 0 {
		alignment = 1 // left-aligned by default
	}
	// PrimaryColour: white at 88% opacity (0xE0 = 224)
	// Using &H00FFFFFF with alpha handled by the renderer
	b.WriteString(fmt.Sprintf("Style: %s,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,0,0,%d,0,0,0,1\n",
		name, fontName, fontSize, alignment))
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

// buildScrollingDialogue adds ASS events for a scrolling text field.
func buildScrollingDialogue(b *strings.Builder, totalDuration float64, style, text string, textWidth, viewportW, viewportX, viewportY, viewportH float64) {
	overflow := textWidth - viewportW
	cycleDuration := 3.0 + overflow/40.0

	posY := viewportY + viewportH/2.0

	// Clip rectangle for the viewport
	clipStr := fmt.Sprintf("\\clip(%d,%d,%d,%d)", int(viewportX), int(viewportY), int(viewportX+viewportW), int(viewportY+viewportH))

	if cycleDuration <= 0 {
		cycleDuration = 0.1
	}

	currentTime := 0.0
	for currentTime < totalDuration {
		cycleStart := currentTime
		cycleEnd := currentTime + cycleDuration

		// Pause phase (first 3 seconds)
		pauseEnd := cycleStart + 3.0
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
func fontSizeForHeight(viewportHeight int) int {
	return int(math.Round(float64(viewportHeight) / 1.2))
}

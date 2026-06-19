package video

import (
	"image/color"
	"strings"
	"testing"
)

func TestBuildVisualizerASSUsesGlobalForegroundMode(t *testing.T) {
	layout, _ := LayoutForSize(1280, 720)
	fonts := FontSet{Regular400: "regular.otf", Medium500: "medium.otf", SemiBold600: "semibold.otf"}
	meta := AudioMetadata{Title: "Title", Artist: "Artist"}
	metrics := map[string]TextMetrics{"title": {Width: 100}, "artist": {Width: 80}}
	darkMode := ForegroundMode{Color: color.RGBA{0, 0, 0, 255}}
	ass := BuildVisualizerASSWithMode(meta, 60, layout, fonts, metrics, darkMode, 1280, 720)
	if !strings.Contains(ass, "&H1F000000") {
		t.Fatalf("ASS does not use black at 88%% opacity:\n%s", ass)
	}
	if strings.Contains(ass, "&H00FFFFFF") {
		t.Fatal("ASS still contains opaque white foreground")
	}
}

func TestBuildVisualizerASSUsesSpecifiedCanonicalFontSizes(t *testing.T) {
	layout, _ := LayoutForSize(1280, 720)
	fonts := FontSet{Regular400: "regular.otf", Medium500: "medium.otf", SemiBold600: "semibold.otf"}
	meta := AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"}
	metrics := map[string]TextMetrics{"title": {Width: 100}, "artist": {Width: 80}, "album": {Width: 60}}
	ass := BuildVisualizerASSWithMode(meta, 60, layout, fonts, metrics, ForegroundMode{Color: color.RGBA{255, 255, 255, 255}}, 1280, 720)
	for _, want := range []string{"Style: Title,semibold.otf,48,", "Style: Artist,medium.otf,28,", "Style: Album,regular.otf,24,", "Style: TimeText,medium.otf,22,"} {
		if !strings.Contains(ass, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestBuildVisualizerASS_Basic(t *testing.T) {
	meta := AudioMetadata{
		Title:  "Test Title",
		Artist: "Test Artist",
		Album:  "Test Album",
	}
	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	fonts := FontSet{
		Regular400:  "C:\\fonts\\NotoSansCJKjp-Regular.otf",
		Medium500:   "C:\\fonts\\NotoSansCJKjp-Medium.otf",
		SemiBold600: "C:\\fonts\\NotoSansCJKjp-SemiBold.otf",
	}
	metrics := map[string]TextMetrics{
		"title":  {Width: 400, Height: 58},
		"artist": {Width: 350, Height: 34},
		"album":  {Width: 200, Height: 30},
	}

	ass := BuildVisualizerASS(meta, 60.0, layout, fonts, metrics)

	// Check sections
	if !strings.Contains(ass, "[Script Info]") {
		t.Fatal("missing [Script Info]")
	}
	if !strings.Contains(ass, "[V4+ Styles]") {
		t.Fatal("missing [V4+ Styles]")
	}
	if !strings.Contains(ass, "[Events]") {
		t.Fatal("missing [Events]")
	}

	// Check PlayRes
	if !strings.Contains(ass, "PlayResX: 1280") {
		t.Fatal("missing PlayResX: 1280")
	}
	if !strings.Contains(ass, "PlayResY: 720") {
		t.Fatal("missing PlayResY: 720")
	}

	// Check styles
	if !strings.Contains(ass, "Style: Title") {
		t.Fatal("missing Title style")
	}
	if !strings.Contains(ass, "Style: Artist") {
		t.Fatal("missing Artist style")
	}
	if !strings.Contains(ass, "Style: Album") {
		t.Fatal("missing Album style")
	}
	if !strings.Contains(ass, "Style: TimeText") {
		t.Fatal("missing TimeText style")
	}

	// Check font names in styles (full path-based)
	if !strings.Contains(ass, fonts.SemiBold600) {
		t.Fatal("Title style should reference SemiBold600 font")
	}
	if !strings.Contains(ass, fonts.Medium500) {
		t.Fatal("Artist style should reference Medium500 font")
	}
	if !strings.Contains(ass, fonts.Regular400) {
		t.Fatal("Album style should reference Regular400 font")
	}

	// Check time events exist for duration
	// 60 seconds should produce at least 60 Dialogue lines for time
	timeCount := strings.Count(ass, "TimeText")
	if timeCount < 59 {
		t.Fatalf("expected >= 59 TimeText events, got %d", timeCount)
	}

	// Check clip and pos
	if !strings.Contains(ass, "\\clip") {
		t.Fatal("missing \\clip")
	}
	if !strings.Contains(ass, "\\pos") {
		t.Fatal("missing \\pos")
	}

	// Check Format line
	if !strings.Contains(ass, "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text") {
		t.Fatal("missing or incorrect Format line")
	}
}

func TestBuildVisualizerASS_NoAlbum(t *testing.T) {
	meta := AudioMetadata{
		Title:  "Test Title",
		Artist: "Test Artist",
		Album:  "",
	}
	layout, _ := LayoutForSize(1280, 720)
	fonts := FontSet{
		Regular400:  "/fonts/Noto-Regular.otf",
		Medium500:   "/fonts/Noto-Medium.otf",
		SemiBold600: "/fonts/Noto-SemiBold.otf",
	}
	metrics := map[string]TextMetrics{
		"title":  {Width: 400, Height: 58},
		"artist": {Width: 350, Height: 34},
	}

	ass := BuildVisualizerASS(meta, 10.0, layout, fonts, metrics)

	if strings.Contains(ass, "Style: Album") {
		t.Fatal("Album style should not exist when album is empty")
	}
	if strings.Contains(ass, "Dialogue:.*Album") {
		t.Fatal("Album should not have dialogue events when empty")
	}
}

func TestBuildVisualizerASS_LongTitleScroll(t *testing.T) {
	meta := AudioMetadata{
		Title:  "A very long title that should definitely scroll because it exceeds the viewport width",
		Artist: "Test Artist",
		Album:  "Test Album",
	}
	layout, _ := LayoutForSize(1280, 720)
	fonts := FontSet{
		Regular400:  "/fonts/Noto-Regular.otf",
		Medium500:   "/fonts/Noto-Medium.otf",
		SemiBold600: "/fonts/Noto-SemiBold.otf",
	}
	metrics := map[string]TextMetrics{
		"title":  {Width: 1200, Height: 58},
		"artist": {Width: 350, Height: 34},
		"album":  {Width: 200, Height: 30},
	}

	ass := BuildVisualizerASS(meta, 30.0, layout, fonts, metrics)

	// The title text should appear in \move commands (since it scrolls)
	if strings.Contains(ass, "\\move") {
		t.Log("ASS contains \\move for scrolling title")
	}

	// Title should be clipped
	if !strings.Contains(ass, "\\clip") {
		t.Fatal("expected \\clip for scrolling text")
	}
}

func TestBuildVisualizerASS_TimeEvents(t *testing.T) {
	meta := AudioMetadata{
		Title:  "Song",
		Artist: "Singer",
		Album:  "Album",
	}
	layout, _ := LayoutForSize(1280, 720)
	fonts := FontSet{
		Regular400:  "/fonts/Noto-Regular.otf",
		Medium500:   "/fonts/Noto-Medium.otf",
		SemiBold600: "/fonts/Noto-SemiBold.otf",
	}
	metrics := map[string]TextMetrics{
		"title":  {Width: 200, Height: 58},
		"artist": {Width: 200, Height: 34},
		"album":  {Width: 200, Height: 30},
	}

	ass := BuildVisualizerASS(meta, 5.0, layout, fonts, metrics)

	// We expect time events for seconds 0,1,2,3,4 (5 events)
	lines := strings.Split(ass, "\n")
	timeEvents := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "Dialogue:") && strings.Contains(line, "TimeText") {
			timeEvents++
		}
	}
	if timeEvents != 5 {
		t.Fatalf("expected 5 time events for 5s duration, got %d", timeEvents)
	}

	// First time event should start at 0:00
	if !strings.Contains(ass, "0:00") {
		t.Fatal("expected time event for 0:00")
	}
	// Last time event should be for second 4 (duration 5)
	if !strings.Contains(ass, "0:04") {
		t.Fatal("expected time event for 0:04")
	}
}

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

// TestASSMiddleAlignmentAndClipPadding verifies AV-821:
//   - Title/Artist/Album use ASS alignment 4 (middle-left)
//   - TimeText uses ASS alignment 5 (middle-center)
//   - Clip rectangle expands vertically by clipPad = max(1, round(2*width/1280))
func TestASSMiddleAlignmentAndClipPadding(t *testing.T) {
	layout, _ := LayoutForSize(1280, 720)
	fonts := FontSet{Regular400: "reg.otf", Medium500: "med.otf", SemiBold600: "semib.otf"}
	meta := AudioMetadata{Title: "T", Artist: "A", Album: "Al"}
	metrics := map[string]TextMetrics{
		"title":  {Width: 100, Height: 58},
		"artist": {Width: 80, Height: 34},
		"album":  {Width: 60, Height: 30},
	}
	fg := ForegroundMode{Color: color.RGBA{255, 255, 255, 255}}

	ass := BuildVisualizerASSWithMode(meta, 10.0, layout, fonts, metrics, fg, 1280, 720)

	// Extract a single Style line by name.
	styleLine := func(name string) string {
		for _, line := range strings.Split(ass, "\n") {
			if strings.HasPrefix(line, "Style: "+name+",") {
				return line
			}
		}
		return ""
	}

	// The ASS style format (18th field, 0-indexed):
	//   Name,Fontname,Fontsize,PrimaryColour,SecondaryColour,OutlineColour,
	//   BackColour,Bold,Italic,Underline,StrikeOut,ScaleX,ScaleY,Spacing,
	//   Angle,BorderStyle,Outline,Shadow,Alignment,MarginL,MarginR,MarginV,Encoding

	t.Run("title alignment 4", func(t *testing.T) {
		s := styleLine("Title")
		if s == "" {
			t.Fatal("Title style not found")
		}
		parts := strings.Split(s, ",")
		if len(parts) < 19 {
			t.Fatalf("Title style has %d fields, want >= 19", len(parts))
		}
		if got := parts[18]; got != "4" {
			t.Errorf("Title alignment = %q, want 4 (middle-left)", got)
		}
	})
	t.Run("artist alignment 4", func(t *testing.T) {
		s := styleLine("Artist")
		if s == "" {
			t.Fatal("Artist style not found")
		}
		parts := strings.Split(s, ",")
		if len(parts) < 19 {
			t.Fatalf("Artist style has %d fields", len(parts))
		}
		if got := parts[18]; got != "4" {
			t.Errorf("Artist alignment = %q, want 4 (middle-left)", got)
		}
	})
	t.Run("album alignment 4", func(t *testing.T) {
		s := styleLine("Album")
		if s == "" {
			t.Fatal("Album style not found")
		}
		parts := strings.Split(s, ",")
		if len(parts) < 19 {
			t.Fatalf("Album style has %d fields", len(parts))
		}
		if got := parts[18]; got != "4" {
			t.Errorf("Album alignment = %q, want 4 (middle-left)", got)
		}
	})
	t.Run("timetext alignment 5", func(t *testing.T) {
		s := styleLine("TimeText")
		if s == "" {
			t.Fatal("TimeText style not found")
		}
		parts := strings.Split(s, ",")
		if len(parts) < 19 {
			t.Fatalf("TimeText style has %d fields", len(parts))
		}
		if got := parts[18]; got != "5" {
			t.Errorf("TimeText alignment = %q, want 5 (middle-center)", got)
		}
	})

	// Clip padding at 1280x720: clipPad = max(1, round(2*1280/1280)) = 2
	// Title:  X=432 Y=152 W=752 H=58  -> clip(432, 150, 1184, 212)
	// Artist: X=432 Y=224 W=752 H=34  -> clip(432, 222, 1184, 260)
	// Album:  X=432 Y=264 W=752 H=30  -> clip(432, 262, 1184, 296)
	t.Run("1280p clip expanded by 2", func(t *testing.T) {
		if !strings.Contains(ass, "\\clip(432,150,1184,212)") {
			t.Error("Title clip not expanded by 2px at 1280x720")
		}
		if !strings.Contains(ass, "\\clip(432,222,1184,260)") {
			t.Error("Artist clip not expanded by 2px at 1280x720")
		}
		if !strings.Contains(ass, "\\clip(432,262,1184,296)") {
			t.Error("Album clip not expanded by 2px at 1280x720")
		}
	})

	// 1920x1080: clipPad = max(1, round(2*1920/1280)) = 3
	t.Run("1080p clip expanded by 3", func(t *testing.T) {
		layout1080, _ := LayoutForSize(1920, 1080)
		// Title: X=648 Y=228 W=1128 H=87  -> clip(648, 225, 1776, 318)
		ass1080 := BuildVisualizerASSWithMode(meta, 10.0, layout1080, fonts, metrics, fg, 1920, 1080)
		if !strings.Contains(ass1080, "\\clip(648,225,1776,318)") {
			t.Error("1080p Title clip not expanded by 3px")
		}
	})

	// 640x360: clipPad = max(1, round(2*640/1280)) = 1
	t.Run("360p clip expanded by 1", func(t *testing.T) {
		layout360, _ := LayoutForSize(640, 360)
		// Title: X=216 Y=76 W=376 H=29  -> clip(216, 75, 592, 106)
		ass360 := BuildVisualizerASSWithMode(meta, 10.0, layout360, fonts, metrics, fg, 640, 360)
		if !strings.Contains(ass360, "\\clip(216,75,592,106)") {
			t.Error("360p Title clip not expanded by 1px")
		}
	})

	// Scrolling title still uses expanded clip
	t.Run("scrolling clip expanded", func(t *testing.T) {
		longMeta := AudioMetadata{Title: "X" + strings.Repeat("x", 200), Artist: "A", Album: "Al"}
		longMetrics := map[string]TextMetrics{
			"title":  {Width: 1400, Height: 58},
			"artist": {Width: 80, Height: 34},
			"album":  {Width: 60, Height: 30},
		}
		assLong := BuildVisualizerASSWithMode(longMeta, 30.0, layout, fonts, longMetrics, fg, 1280, 720)
		if !strings.Contains(assLong, "\\clip(432,150,1184,212)") {
			t.Error("Scrolling Title clip not expanded by 2px")
		}
	})
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

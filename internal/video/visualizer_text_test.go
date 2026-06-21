package video

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveVisualizerFontFacesFromActualFonts(t *testing.T) {
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Fatal(err)
	}
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		t.Fatal(err)
	}

	// Check each named field
	checkFace := func(name string, f VisualizerFontFace) {
		if f.FilePath == "" {
			t.Errorf("%s.FilePath is empty", name)
		}
		if f.ASSFamily == "" {
			t.Errorf("%s.ASSFamily is empty", name)
		}
		if strings.ContainsAny(f.ASSFamily, `/\`) {
			t.Errorf("%s.ASSFamily %q looks like a file path", name, f.ASSFamily)
		}
		if !strings.Contains(f.ASSFamily, "Noto Sans CJK") {
			t.Errorf("%s.ASSFamily %q does not contain Noto Sans CJK", name, f.ASSFamily)
		}
	}
	checkFace("Regular400", faces.Regular400)
	checkFace("Medium500", faces.Medium500)
	checkFace("SemiBold600", faces.SemiBold600)
}

func TestResolveVisualizerFontFacesInvalidPath(t *testing.T) {
	// Non-existent font file
	_, err := ResolveVisualizerFontFaces(FontSet{
		Regular400:  "nonexistent.otf",
		Medium500:   "nonexistent.otf",
		SemiBold600: "nonexistent.otf",
	})
	if err == nil {
		t.Fatal("expected error for non-existent font file")
	}
}

func TestResolveVisualizerFontFacesFromTempFont(t *testing.T) {
	// Write a minimal OTF-like file and verify it fails gracefully
	// (too small to be a valid font)
	tmp := t.TempDir()
	bad := tmp + "\\bad.otf"
	if err := os.WriteFile(bad, []byte("not a font"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveVisualizerFontFaces(FontSet{
		Regular400:  bad,
		Medium500:   bad,
		SemiBold600: bad,
	})
	if err == nil {
		t.Fatal("expected error for invalid font data")
	}
}

func TestResolveVisualizerFontFacesEmptyNameRejectedInFontData(t *testing.T) {
	// We can't easily create a font with empty family name,
	// but verify that we test by reading an actual font and
	// ensuring all returned names are non-empty
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Skip("fonts not available:", err)
	}
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []VisualizerFontFace{faces.Regular400, faces.Medium500, faces.SemiBold600} {
		if f.ASSFamily == "" {
			t.Error("a face has empty ASSFamily")
		}
	}
}

func TestResolveVisualizerFontFacesConsistentResults(t *testing.T) {
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Skip("fonts not available:", err)
	}
	// Run twice and compare results
	faces1, err1 := ResolveVisualizerFontFaces(fonts)
	if err1 != nil {
		t.Fatal(err1)
	}
	faces2, err2 := ResolveVisualizerFontFaces(fonts)
	if err2 != nil {
		t.Fatal(err2)
	}
	checkEqual := func(name string, a, b VisualizerFontFace) {
		if a.ASSFamily != b.ASSFamily {
			t.Errorf("%s ASSFamily differs: %q vs %q", name, a.ASSFamily, b.ASSFamily)
		}
		if a.FilePath != b.FilePath {
			t.Errorf("%s FilePath differs: %q vs %q", name, a.FilePath, b.FilePath)
		}
	}
	checkEqual("Regular400", faces1.Regular400, faces2.Regular400)
	checkEqual("Medium500", faces1.Medium500, faces2.Medium500)
	checkEqual("SemiBold600", faces1.SemiBold600, faces2.SemiBold600)
}

func TestVisualizerFontFaceTypes(t *testing.T) {
	// Compile-time check: VisualizerFontFace exists with expected fields
	var f VisualizerFontFace
	if f.FilePath != "" || f.ASSFamily != "" || f.PostScriptName != "" {
		t.Fatal("zero value should have empty fields")
	}
}

func TestResolveVisualizerFontFacesWeightIdentification(t *testing.T) {
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Skip("fonts not available:", err)
	}
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		t.Fatal(err)
	}
	// Verify each field was populated (the PostScript name mapping worked)
	if faces.Regular400.FilePath == "" {
		t.Error("Regular400 not populated")
	}
	if faces.Medium500.FilePath == "" {
		t.Error("Medium500 not populated")
	}
	if faces.SemiBold600.FilePath == "" {
		t.Error("SemiBold600 not populated")
	}
	// Verify distinct paths (no duplicate weight assignment)
	m := map[string]bool{
		faces.Regular400.FilePath:  true,
		faces.Medium500.FilePath:   true,
		faces.SemiBold600.FilePath: true,
	}
	if len(m) != 3 {
		t.Error("weights mapped to the same file — PostScript name parsing may have failed")
	}
}

// TestASSWidthsConsistent verifies AV-824: ASS-rendered text measurements are
// reproducible and scale with text length.  On Windows, drawtext and libass
// measure widths differently (22-100 px gap), so we use ASS/libass
// measurement exclusively — the same engine that will actually encode text.
//
// The test:
//   - Measures the same text twice via \fn PostScript-name override to verify
//     consistency (identical results from repeated measurements).
//   - Measures a short and long version of the same text to verify that longer
//     text produces a wider measurement.
//
// This test requires a working FFmpeg with the ass filter and the bundled
// Noto Sans CJK JP font files.  It skips when either is unavailable.
func TestASSWidthsConsistent(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}

	if !hasLibass(t, ffmpeg) {
		t.Skip("FFmpeg was not built with libass support")
	}

	fonts, err := VisualizerFonts()
	if err != nil {
		t.Skipf("Noto fonts not available: %v", err)
	}

	// Use ResolveVisualizerFontFaces to get PostScript names (AV-824).
	// This mirrors the production pipeline and validates that PostScriptName
	// is correctly populated.
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		t.Fatalf("ResolveVisualizerFontFaces: %v", err)
	}
	psSemiBold := faces.SemiBold600.PostScriptName
	psMedium := faces.Medium500.PostScriptName
	psRegular := faces.Regular400.PostScriptName
	if psSemiBold == "" || psMedium == "" || psRegular == "" {
		t.Fatalf("PostScript names not resolved: SemiBold=%q Medium=%q Regular=%q",
			psSemiBold, psMedium, psRegular)
	}

	ctx := context.Background()
	fontDir := filepath.Dir(fonts.Regular400)

	// Test cases that verify consistency (same text measured twice).
	// Each entry is (label, text, psName, fontSize).
	type consistencyCase struct {
		label    string
		text     string
		fontName string
		weight   int
		fontSize int
	}
	consistencyCases := []consistencyCase{
		{label: "Latin/title", text: "Hello World ABC", fontName: faces.SemiBold600.ASSFamily, weight: 600, fontSize: 48},
		{label: "Latin/artist", text: "Hello World ABC", fontName: faces.Medium500.ASSFamily, weight: 500, fontSize: 28},
		{label: "Japanese/title", text: "日本語のテキスト表示", fontName: faces.SemiBold600.ASSFamily, weight: 600, fontSize: 48},
		{label: "Japanese/artist", text: "日本語のテキスト表示", fontName: faces.Medium500.ASSFamily, weight: 500, fontSize: 28},
		{label: "Mixed/title", text: "Hello 世界 テスト ABC", fontName: faces.SemiBold600.ASSFamily, weight: 600, fontSize: 48},
		{label: "Mixed/artist", text: "Hello 世界 テスト ABC", fontName: faces.Medium500.ASSFamily, weight: 500, fontSize: 28},
	}

	for _, c := range consistencyCases {
		t.Run("consistency/"+c.label, func(t *testing.T) {
			w1, err := MeasureASSEncodedWidth(ctx, ffmpeg, c.fontName, c.weight, fontDir, c.text, c.fontSize)
			if err != nil {
				t.Fatalf("MeasureASSEncodedWidth #1: %v", err)
			}
			w2, err := MeasureASSEncodedWidth(ctx, ffmpeg, c.fontName, c.weight, fontDir, c.text, c.fontSize)
			if err != nil {
				t.Fatalf("MeasureASSEncodedWidth #2: %v", err)
			}
			if w1 != w2 {
				t.Errorf("ASS measurement inconsistent: %d vs %d", w1, w2)
			}
			t.Logf("width=%d (consistent)", w1)
		})
	}

	// Verify that longer text at the same font size produces a wider
	// measurement (basic sanity check).
	t.Run("longer-text-is-wider", func(t *testing.T) {
		short := "A"
		long := "AAAAA"
		shortW, err := MeasureASSEncodedWidth(ctx, ffmpeg, faces.Regular400.ASSFamily, 400, fontDir, short, 48)
		if err != nil {
			t.Fatalf("measure short: %v", err)
		}
		longW, err := MeasureASSEncodedWidth(ctx, ffmpeg, faces.Regular400.ASSFamily, 400, fontDir, long, 48)
		if err != nil {
			t.Fatalf("measure long: %v", err)
		}
		if longW <= shortW {
			t.Errorf("long text width (%d) should exceed short text width (%d)", longW, shortW)
		}
		t.Logf("short=%d long=%d", shortW, longW)
	})
}

func TestMeasureASSEncodedWidthDoesNotClipBeyond2000Pixels(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	if !hasLibass(t, ffmpeg) {
		t.Skip("FFmpeg was not built with libass support")
	}
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Skipf("Noto fonts not available: %v", err)
	}
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		t.Fatal(err)
	}

	width, err := MeasureASSEncodedWidth(
		context.Background(), ffmpeg, faces.SemiBold600.ASSFamily, 600,
		filepath.Dir(fonts.Regular400), strings.Repeat("漢", 100), 48,
	)
	if err != nil {
		t.Fatal(err)
	}
	if width <= 2000 {
		t.Fatalf("long ASS text width was clipped to %d; want > 2000", width)
	}
}

func TestBuildVisualizerASSUsesExactFontWeights(t *testing.T) {
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Skipf("Noto fonts not available: %v", err)
	}
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	ass, err := BuildVisualizerASS(
		AudioMetadata{Title: "Title", Artist: "Artist", Album: "Album"},
		10, layout, fonts,
		map[string]TextMetrics{"title": {Width: 100}, "artist": {Width: 100}, "album": {Width: 100}},
	)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]struct {
		family string
		size   string
		weight string
	}{
		"Title":    {faces.SemiBold600.ASSFamily, "48", "600"},
		"Artist":   {faces.Medium500.ASSFamily, "28", "500"},
		"Album":    {faces.Regular400.ASSFamily, "24", "400"},
		"TimeText": {faces.Medium500.ASSFamily, "22", "500"},
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(ass, "\n") {
		if !strings.HasPrefix(line, "Style: ") {
			continue
		}
		fields := strings.Split(strings.TrimPrefix(line, "Style: "), ",")
		if len(fields) < 8 {
			continue
		}
		want, ok := wants[fields[0]]
		if !ok {
			continue
		}
		seen[fields[0]] = true
		if fields[1] != want.family || fields[2] != want.size || fields[7] != want.weight {
			t.Errorf("%s style family/size/weight = %q/%q/%q, want %q/%q/%q", fields[0], fields[1], fields[2], fields[7], want.family, want.size, want.weight)
		}
	}
	for name := range wants {
		if !seen[name] {
			t.Errorf("missing %s style", name)
		}
	}
}

func TestMeasuredAndEncodedTextWidthsDifferByAtMostOnePixel(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		// libass sub-pixel rendering varies by build; this measure-vs-render
		// consistency check is validated on the shipping platforms only.
		t.Skip("libass pixel comparison is platform/build-sensitive; skip on non-shipping OS")
	}
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	if !hasLibass(t, ffmpeg) {
		t.Skip("FFmpeg was not built with libass support")
	}
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Skipf("Noto fonts not available: %v", err)
	}
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	const title = "Hello 世界 ABC"
	measured, err := MeasureASSEncodedWidth(context.Background(), ffmpeg, faces.SemiBold600.ASSFamily, 600, filepath.Dir(fonts.Regular400), title, 48)
	if err != nil {
		t.Fatal(err)
	}
	ass, err := BuildVisualizerASS(
		AudioMetadata{Title: title}, 1, layout, fonts,
		map[string]TextMetrics{"title": {Width: measured}},
	)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	assPath := filepath.Join(tmp, "encoded.ass")
	pngPath := filepath.Join(tmp, "encoded.png")
	if err := os.WriteFile(assPath, []byte(ass), 0644); err != nil {
		t.Fatal(err)
	}
	filter := fmt.Sprintf("color=c=black:s=1280x720:d=1,ass=filename='%s':fontsdir='%s'", escapeFilterPath(assPath), escapeFilterPath(filepath.Dir(fonts.Regular400)))
	cmd := exec.Command(ffmpeg, "-v", "error", "-filter_complex", filter, "-frames:v", "1", "-y", pngPath)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("render production ASS: %v\n%s", err, out)
	}
	f, err := os.Open(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	rect := imageRect(layout.Title.X, layout.Title.Y, layout.Title.W, layout.Title.H).Intersect(img.Bounds())
	minX, maxX := rect.Max.X, rect.Min.X-1
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r > 128 || g > 128 || b > 128 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
			}
		}
	}
	if maxX < minX {
		t.Fatal("production ASS title produced no visible pixels")
	}
	encoded := maxX - minX + 1
	if diff := encoded - measured; diff < -1 || diff > 1 {
		t.Fatalf("measured width %d and encoded width %d differ by more than one pixel", measured, encoded)
	}
}

func imageRect(x, y, w, h int) image.Rectangle {
	return image.Rect(x, y, x+w, y+h)
}

// hasLibass checks whether the given FFmpeg binary was built with the ass
// (libass) filter.
func hasLibass(t *testing.T, ffmpeg string) bool {
	t.Helper()
	cmd := exec.Command(ffmpeg, "-filters")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// Look for a line containing "ass" in the filter listing.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, " ass ") && strings.HasPrefix(line, " ") {
			return true
		}
	}
	return false
}

func TestParsePostScriptWeight(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"NotoSansCJKjp-Regular", "Regular"},
		{"NotoSansCJKjp-Medium", "Medium"},
		{"NotoSansCJKjp-SemiBold", "SemiBold"},
		{"NoDash", ""},
		{"", ""},
		{"Multiple-Dashes-Here", "Here"},
	}
	for _, tc := range tests {
		got := parsePostScriptWeight(tc.input)
		if got != tc.want {
			t.Errorf("parsePostScriptWeight(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

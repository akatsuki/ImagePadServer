package video

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
		psName   string
		fontSize int
	}
	consistencyCases := []consistencyCase{
		{label: "Latin/title", text: "Hello World ABC", psName: psSemiBold, fontSize: 48},
		{label: "Latin/artist", text: "Hello World ABC", psName: psMedium, fontSize: 28},
		{label: "Japanese/title", text: "日本語のテキスト表示", psName: psSemiBold, fontSize: 48},
		{label: "Japanese/artist", text: "日本語のテキスト表示", psName: psMedium, fontSize: 28},
		{label: "Mixed/title", text: "Hello 世界 テスト ABC", psName: psSemiBold, fontSize: 48},
		{label: "Mixed/artist", text: "Hello 世界 テスト ABC", psName: psMedium, fontSize: 28},
	}

	for _, c := range consistencyCases {
		t.Run("consistency/"+c.label, func(t *testing.T) {
			w1, err := MeasureASSEncodedWidth(ctx, ffmpeg, c.psName, fontDir, c.text, c.fontSize)
			if err != nil {
				t.Fatalf("MeasureASSEncodedWidth #1: %v", err)
			}
			w2, err := MeasureASSEncodedWidth(ctx, ffmpeg, c.psName, fontDir, c.text, c.fontSize)
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
		shortW, err := MeasureASSEncodedWidth(ctx, ffmpeg, psRegular, fontDir, short, 48)
		if err != nil {
			t.Fatalf("measure short: %v", err)
		}
		longW, err := MeasureASSEncodedWidth(ctx, ffmpeg, psRegular, fontDir, long, 48)
		if err != nil {
			t.Fatalf("measure long: %v", err)
		}
		if longW <= shortW {
			t.Errorf("long text width (%d) should exceed short text width (%d)", longW, shortW)
		}
		t.Logf("short=%d long=%d", shortW, longW)
	})
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

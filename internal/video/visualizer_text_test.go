package video

import (
	"os"
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
	if f.FilePath != "" || f.ASSFamily != "" {
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

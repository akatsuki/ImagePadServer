package video

import "testing"

func TestVisualizerFontPathsExist(t *testing.T) {
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Fatalf("VisualizerFonts() = %v", err)
	}
	for name, path := range map[string]string{"Regular": fonts.Regular400, "Medium": fonts.Medium500, "SemiBold": fonts.SemiBold600} {
		if path == "" {
			t.Fatalf("%s path is empty", name)
		}
	}
}

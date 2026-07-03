package video

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// The visualizer fonts are embedded in the binary so they travel with it
// regardless of the working directory or whether the binary was built with
// -trimpath. FFmpeg/libass require real files on disk, so VisualizerFonts
// materializes them to a per-content cache directory before returning paths.
//
//go:embed fonts/NotoSansJP-Regular.ttf fonts/NotoSansJP-Medium.ttf fonts/NotoSansJP-SemiBold.ttf
var embeddedFonts embed.FS

var errFontNotFound = errors.New("font not found: Noto Sans JP")

var embeddedFontFiles = []string{
	"fonts/NotoSansJP-Regular.ttf",
	"fonts/NotoSansJP-Medium.ttf",
	"fonts/NotoSansJP-SemiBold.ttf",
}

// VisualizerFonts extracts the bundled Noto Sans JP fonts to a cache
// directory on disk and returns their paths. All three fonts live in the same
// directory, which callers rely on when passing a single fontsdir to FFmpeg.
func VisualizerFonts() (FontSet, error) {
	dir, err := extractEmbeddedFonts()
	if err != nil {
		return FontSet{}, errFontNotFound
	}
	return FontSet{
		Regular400:  filepath.Join(dir, "NotoSansJP-Regular.ttf"),
		Medium500:   filepath.Join(dir, "NotoSansJP-Medium.ttf"),
		SemiBold600: filepath.Join(dir, "NotoSansJP-SemiBold.ttf"),
	}, nil
}

// extractEmbeddedFonts writes the embedded fonts to a content-addressed
// directory under the user cache dir and returns that directory. Existing,
// correctly sized files are reused, so the cost is paid only on first run (or
// after a font update, which lands in a fresh directory).
func extractEmbeddedFonts() (string, error) {
	contents := make(map[string][]byte, len(embeddedFontFiles))
	h := sha256.New()
	for _, f := range embeddedFontFiles {
		b, err := embeddedFonts.ReadFile(f)
		if err != nil {
			return "", err
		}
		contents[f] = b
		h.Write(b)
	}
	version := hex.EncodeToString(h.Sum(nil))[:16]

	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "imagepadserver", "fonts", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	for _, f := range embeddedFontFiles {
		dest := filepath.Join(dir, filepath.Base(f))
		want := contents[f]
		if info, statErr := os.Stat(dest); statErr == nil && info.Size() == int64(len(want)) {
			continue
		}
		if err := writeFileAtomic(dest, want); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// writeFileAtomic writes data to dest via a temp file and rename so a partially
// written font is never observed, even with concurrent extractors.
func writeFileAtomic(dest string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".font-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

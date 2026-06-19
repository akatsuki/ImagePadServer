package video

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

var errFontNotFound = errors.New("Noto Sans CJK JP font not found")

func projectRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot determine source file path")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("project root not found")
		}
		dir = parent
	}
}

func VisualizerFonts() (FontSet, error) {
	root, err := projectRoot()
	if err != nil {
		return FontSet{}, errFontNotFound
	}
	base := filepath.Join(root, "assets", "fonts")
	paths := FontSet{
		Regular400:  filepath.Join(base, "NotoSansCJKjp-Regular.otf"),
		Medium500:   filepath.Join(base, "NotoSansCJKjp-Medium.otf"),
		SemiBold600: filepath.Join(base, "NotoSansCJKjp-SemiBold.otf"),
	}
	for _, p := range []string{paths.Regular400, paths.Medium500, paths.SemiBold600} {
		if _, err := os.Stat(p); err != nil {
			return FontSet{}, errFontNotFound
		}
	}
	return paths, nil
}

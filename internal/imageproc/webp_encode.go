package imageproc

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"imagepadserver/internal/toolchain"
)

// EncodeWebP flattens alpha to black, then encodes a lossy WebP via FFmpeg.
func EncodeWebP(src image.Image, outPath string, quality int) error {
	if quality <= 0 || quality > 100 {
		quality = 80
	}
	ffmpeg, err := toolchain.EnsureFFmpeg()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(outPath), "webp-source-*.png")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := png.Encode(tmp, flattenWithBackground(src, color.Black)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-i", tmpPath,
		"-frames:v", "1",
		"-c:v", "libwebp",
		"-quality", strconv.Itoa(quality),
		outPath,
	}
	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	output, err := toolchain.CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimCommandOutput(output))
	}
	stat, err := os.Stat(outPath)
	if err != nil {
		return err
	}
	if stat.Size() == 0 {
		return fmt.Errorf("ffmpeg produced an empty WebP")
	}
	return nil
}

package imageproc

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"imagepadserver/internal/video"
)

// EncodeWebP flattens alpha consistently with JPEG output and encodes a still WebP via FFmpeg.
func EncodeWebP(src image.Image, outPath string, quality int) error {
	if quality <= 0 || quality > 100 {
		quality = 80
	}
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "webp-source-*.png")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := png.Encode(tmp, flatten(src)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return err
	}
	cmd := exec.Command(ffmpeg,
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-i", tmpPath,
		"-c:v", "libwebp",
		"-quality", strconv.Itoa(quality),
		outPath,
	)
	hideWindow(cmd)
	output, err := video.CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimCommandOutput(output))
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return fmt.Errorf("webp output is empty")
	}
	return nil
}

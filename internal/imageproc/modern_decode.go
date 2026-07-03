package imageproc

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"imagepadserver/internal/video"
)

func isModernCompressedImageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".avif", ".heic", ".heif", ".jxl":
		return true
	default:
		return false
	}
}

func decodeModernCompressedImage(input []byte, name, outDir string, maxDimension int) (image.Image, error) {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return nil, fmt.Errorf("FFmpeg is required to decode %s: %w", filepath.Ext(name), err)
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".img"
	}
	in, err := os.CreateTemp(outDir, "modern-source-*"+ext)
	if err != nil {
		return nil, err
	}
	inPath := in.Name()
	defer os.Remove(inPath)
	if _, err := in.Write(input); err != nil {
		_ = in.Close()
		return nil, err
	}
	if err := in.Close(); err != nil {
		return nil, err
	}

	out, err := os.CreateTemp(outDir, "modern-decoded-*.png")
	if err != nil {
		return nil, err
	}
	outPath := out.Name()
	_ = out.Close()
	defer os.Remove(outPath)

	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-i", inPath,
		"-frames:v", "1",
	}
	if maxDimension > 0 {
		args = append(args, "-vf", rawScaleFilter(maxDimension))
	}
	args = append(args, outPath)

	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	output, err := video.CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, trimCommandOutput(output))
	}
	file, err := os.Open(outPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, err := png.Decode(file)
	if err != nil {
		return nil, err
	}
	return img, nil
}

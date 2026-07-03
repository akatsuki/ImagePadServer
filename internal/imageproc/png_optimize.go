package imageproc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

var pngQualityRanges = map[string][2]int{
	"highest": {90, 100},
	"high":    {75, 90},
	"medium":  {60, 75},
	"low":     {45, 60},
	"lowest":  {30, 45},
}

func OptimizePNG(path string, quality string) (int64, error) {
	if quality != "" && quality != "lossless" {
		if err := runPngquantIfAvailable(path, quality); err != nil {
			size, _ := fileSize(path)
			return size, err
		}
	}
	if err := runOxipngIfAvailable(path); err != nil {
		size, _ := fileSize(path)
		return size, err
	}
	return fileSize(path)
}

func runPngquantIfAvailable(path string, quality string) error {
	bin, err := EnsurePngquant()
	if err != nil || bin == "" {
		return err
	}
	r, ok := pngQualityRanges[quality]
	if !ok {
		r = pngQualityRanges["high"]
	}
	tmp := filepath.Join(filepath.Dir(path), "pngquant-"+filepath.Base(path))
	defer os.Remove(tmp)
	cmd := exec.Command(bin,
		"--quality="+strconv.Itoa(r[0])+"-"+strconv.Itoa(r[1]),
		"--speed", "3",
		"--strip",
		"--force",
		"--output", tmp,
		"--", path,
	)
	hideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pngquant: %w: %s", err, trimCommandOutput(output))
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}

func runOxipngIfAvailable(path string) error {
	bin, err := EnsureOxipng()
	if err != nil || bin == "" {
		return err
	}
	cmd := exec.Command(bin, "--opt", "3", "--strip", "safe", path)
	hideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("oxipng: %w: %s", err, trimCommandOutput(output))
	}
	return nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

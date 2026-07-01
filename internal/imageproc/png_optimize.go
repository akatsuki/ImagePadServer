package imageproc

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

var qualityPresetToPNGRange = map[string][2]int{
	"highest": {90, 100},
	"high":    {75, 90},
	"medium":  {60, 75},
	"low":     {45, 60},
	"lowest":  {30, 45},
}

func OptimizePNG(path string, quality string) (int64, error) {
	if quality == "" {
		quality = "lossless"
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if quality != "lossless" {
		if pngquant, err := EnsurePngquant(); err != nil {
			log.Printf("pngquant unavailable: %v", err)
		} else if pngquant != "" {
			if err := runPngquant(pngquant, path, quality); err != nil {
				log.Printf("pngquant skipped: %v", err)
			}
		}
	}
	if oxipng, err := EnsureOxipng(); err != nil {
		log.Printf("oxipng unavailable: %v", err)
	} else if oxipng != "" {
		if err := runOxipng(oxipng, path); err != nil {
			log.Printf("oxipng skipped: %v", err)
		}
	}
	stat, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if stat.Size() > int64(len(original)) {
		if err := os.WriteFile(path, original, 0600); err != nil {
			return 0, err
		}
		return int64(len(original)), nil
	}
	return stat.Size(), nil
}

func runPngquant(exe, path, quality string) error {
	rng, ok := qualityPresetToPNGRange[quality]
	if !ok {
		return fmt.Errorf("unknown PNG quality preset %q", quality)
	}
	tmp := filepath.Join(filepath.Dir(path), "pngquant-"+filepath.Base(path))
	_ = os.Remove(tmp)
	cmd := exec.Command(exe,
		"--quality="+strconv.Itoa(rng[0])+"-"+strconv.Itoa(rng[1]),
		"--speed", "3",
		"--strip",
		"--force",
		"--output", tmp,
		"--",
		path,
	)
	hideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, trimCommandOutput(output))
	}
	return os.Rename(tmp, path)
}

func runOxipng(exe, path string) error {
	cmd := exec.Command(exe, "--opt", "3", "--strip", "safe", path)
	hideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, trimCommandOutput(output))
	}
	return nil
}

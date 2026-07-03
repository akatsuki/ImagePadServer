package imageproc

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func EnsurePngquant() (string, error) {
	return resolveImageTool("IMAGEPAD_PNGQUANT", "pngquant")
}

func EnsureOxipng() (string, error) {
	return resolveImageTool("IMAGEPAD_OXIPNG", "oxipng")
}

func ValidateImageTools() {
	if path, err := EnsurePngquant(); err != nil {
		log.Printf("pngquant unavailable: %v", err)
	} else if path != "" {
		log.Printf("pngquant ready: %s", path)
	}
	if path, err := EnsureOxipng(); err != nil {
		log.Printf("oxipng unavailable: %v", err)
	} else if path != "" {
		log.Printf("oxipng ready: %s", path)
	}
}

func resolveImageTool(envName, base string) (string, error) {
	if configured := strings.TrimSpace(os.Getenv(envName)); configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", err
		}
		return configured, nil
	}
	for _, name := range imageToolNames(base) {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", nil
}

func imageToolNames(base string) []string {
	if filepath.Ext(base) != "" {
		return []string{base}
	}
	return []string{base, base + ".exe"}
}

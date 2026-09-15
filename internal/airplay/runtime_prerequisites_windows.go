//go:build windows

package airplay

import (
	"os"
	"path/filepath"
)

const envAllowInheritedGStreamer = "IMAGEPAD_AIRPLAY_ALLOW_INHERITED_GSTREAMER"

func gstreamerScannerPath(root string, runtimeRoots []string) string {
	candidates := []string{filepath.Join(root, "libexec", "gstreamer-1.0", "gst-plugin-scanner.exe")}
	for _, runtimeRoot := range runtimeRoots {
		candidates = append(candidates,
			filepath.Join(runtimeRoot, "libexec", "gstreamer-1.0", "gst-plugin-scanner.exe"),
			filepath.Join(filepath.Dir(runtimeRoot), "libexec", "gstreamer-1.0", "gst-plugin-scanner.exe"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			if absolute, err := filepath.Abs(candidate); err == nil {
				return absolute
			}
		}
	}
	return ""
}

func gstreamerRegistryPath() string {
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		return ""
	}
	root := filepath.Join(cache, "ImagePadServer", "gstreamer")
	if err := os.MkdirAll(root, 0700); err != nil {
		return ""
	}
	return filepath.Join(root, "registry.bin")
}

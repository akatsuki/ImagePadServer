//go:build windows
// +build windows

package steamvr

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"imagepadserver/internal/about"
	"imagepadserver/internal/settings"
)

const appConfigPath = `C:\Program Files (x86)\Steam\config\appconfig.json`
const appKey = "steam.overlay.imagepadserver"

//go:embed imagepad-icon-256.png
var steamVRIconPNG []byte

// RegistrationStatus describes whether ImagePadServer is registered with SteamVR.
type RegistrationStatus struct {
	Available    bool   `json:"available"`
	Enabled      bool   `json:"enabled"`
	ManifestPath string `json:"manifestPath"`
	ConfigPath   string `json:"configPath"`
	Message      string `json:"message"`
}

// Registration returns the current SteamVR manifest registration status.
func Registration() RegistrationStatus {
	manifestPath, err := manifestPath()
	if err != nil {
		return RegistrationStatus{Available: false, Message: err.Error()}
	}
	cfg, err := readAppConfig()
	if err != nil {
		return RegistrationStatus{
			Available:    false,
			ManifestPath: manifestPath,
			ConfigPath:   appConfigPath,
			Message:      err.Error(),
		}
	}
	return RegistrationStatus{
		Available:    true,
		Enabled:      containsPath(cfg.ManifestPaths(), manifestPath),
		ManifestPath: manifestPath,
		ConfigPath:   appConfigPath,
	}
}

// SetRegistration enables or disables ImagePadServer in SteamVR's app config.
func SetRegistration(enabled bool) (RegistrationStatus, error) {
	manifestPath, err := manifestPath()
	if err != nil {
		return Registration(), err
	}
	cfg, err := readAppConfig()
	if err != nil {
		return Registration(), err
	}

	paths := cfg.ManifestPaths()
	paths = removeImagePadManifestPaths(paths, manifestPath)
	changed := len(paths) != len(cfg.ManifestPaths())
	if enabled {
		if !containsPath(paths, manifestPath) {
			paths = append(paths, manifestPath)
			changed = true
		}
	} else {
		next := make([]string, 0, len(paths))
		for _, path := range paths {
			if !samePath(path, manifestPath) {
				next = append(next, path)
			} else {
				changed = true
			}
		}
		paths = next
	}

	if changed {
		cfg.SetManifestPaths(paths)
		if err := writeAppConfig(cfg); err != nil {
			return Registration(), err
		}
	}
	return Registration(), nil
}

type steamVRAppConfig map[string]interface{}

func readAppConfig() (steamVRAppConfig, error) {
	data, err := os.ReadFile(appConfigPath)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var cfg steamVRAppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func writeAppConfig(cfg steamVRAppConfig) error {
	backupPath := appConfigPath + ".imagepadserver.bak"
	data, err := os.ReadFile(appConfigPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return err
	}
	next, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	next = append(next, '\n')
	return os.WriteFile(appConfigPath, next, 0644)
}

func (cfg steamVRAppConfig) ManifestPaths() []string {
	raw, ok := cfg["manifest_paths"]
	if !ok || raw == nil {
		return nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if path, ok := item.(string); ok && path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func (cfg steamVRAppConfig) SetManifestPaths(paths []string) {
	items := make([]interface{}, 0, len(paths))
	for _, path := range paths {
		items = append(items, path)
	}
	cfg["manifest_paths"] = items
}

func manifestPath() (string, error) {
	exe, err := installExecutableForSteamVR()
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(settings.Dir(), "steamvr", "imagepadserver.vrmanifest")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err != nil {
		return "", err
	}
	if err := writeSteamVRIcon(filepath.Dir(manifestPath)); err != nil {
		return "", err
	}
	data, err := manifestData(exe)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		return "", err
	}
	return manifestPath, nil
}

func writeSteamVRIcon(dir string) error {
	return os.WriteFile(filepath.Join(dir, "imagepad-icon-256.png"), steamVRIconPNG, 0644)
}

func installExecutableForSteamVR() (string, error) {
	src, err := os.Executable()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(settings.Dir(), "bin", "ImagePadServer.exe")
	srcAbs, _ := filepath.Abs(src)
	dstAbs, _ := filepath.Abs(dst)
	if strings.EqualFold(filepath.Clean(srcAbs), filepath.Clean(dstAbs)) {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return "", err
	}
	if sameFileVersion(src, dst) {
		return dst, nil
	}
	if err := copyFile(dst, src); err != nil {
		return "", err
	}
	return dst, nil
}

func sameFileVersion(src, dst string) bool {
	srcInfo, srcErr := os.Stat(src)
	dstInfo, dstErr := os.Stat(dst)
	if srcErr != nil || dstErr != nil {
		return false
	}
	return srcInfo.Size() == dstInfo.Size() && !srcInfo.ModTime().After(dstInfo.ModTime())
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}

func manifestData(exePath string) ([]byte, error) {
	doc := map[string]interface{}{
		"source": "imagepadserver",
		"applications": []map[string]interface{}{
			{
				"app_key":              appKey,
				"launch_type":          "binary",
				"binary_path_windows":  exePath,
				"arguments":            "--steamvr-launch",
				"is_dashboard_overlay": true,
				"image_path":           "imagepad-icon-256.png",
				"strings": map[string]interface{}{
					"en_us": map[string]string{
						"name":        about.AppName,
						"description": "Open the ImagePadServer browser UI for VRChat ImagePad uploads.",
					},
					"ja_jp": map[string]string{
						"name":        about.AppName,
						"description": "Open ImagePadServer for VRChat ImagePad uploads.",
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if samePath(path, target) {
			return true
		}
	}
	return false
}

func removeImagePadManifestPaths(paths []string, keep string) []string {
	next := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.ToSlash(strings.ToLower(filepath.Clean(path)))
		if strings.Contains(clean, "/imagepadserver/") && strings.HasSuffix(clean, "imagepadserver.vrmanifest") && !samePath(path, keep) {
			continue
		}
		next = append(next, path)
	}
	return next
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil {
		a = absA
	}
	if errB == nil {
		b = absB
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

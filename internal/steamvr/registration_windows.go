//go:build windows
// +build windows

package steamvr

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const appConfigPath = `C:\Program Files (x86)\Steam\config\appconfig.json`

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
	changed := false
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
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "steamvr", "imagepadserver.vrmanifest"),
			filepath.Join(filepath.Dir(dir), "steamvr", "imagepadserver.vrmanifest"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "steamvr", "imagepadserver.vrmanifest"))
	}
	for _, candidate := range candidates {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}
	return "", errors.New("SteamVR manifest file was not found")
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if samePath(path, target) {
			return true
		}
	}
	return false
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

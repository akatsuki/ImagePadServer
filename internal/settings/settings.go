package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Settings struct {
	SteamVRExplicitlyDisabled bool `json:"steamvrExplicitlyDisabled"`
}

func Load() (Settings, error) {
	var settings Settings
	data, err := os.ReadFile(path())
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return settings, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func Save(settings Settings) error {
	settingsPath := path()
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(settingsPath, data, 0644)
}

func Dir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "ImagePadServer")
}

func path() string {
	return filepath.Join(Dir(), "settings.json")
}

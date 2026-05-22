package settings

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
)

type Settings struct {
	SteamVRExplicitlyDisabled bool   `json:"steamvrExplicitlyDisabled"`
	VideoPlayerEnabled        bool   `json:"videoPlayerEnabled"`
	VideoQualityMode          string `json:"videoQualityMode,omitempty"`
	NetworkMbps               int    `json:"networkMbps,omitempty"`
	NetworkUploadMbps         int    `json:"networkUploadMbps,omitempty"`
	AdminToken                string `json:"adminToken,omitempty"`
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
	return os.WriteFile(settingsPath, data, 0600)
}

func EnsureAdminToken() (string, error) {
	settings, err := Load()
	if err != nil {
		return "", err
	}
	if settings.AdminToken != "" {
		return settings.AdminToken, nil
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	settings.AdminToken = token
	if err := Save(settings); err != nil {
		return "", err
	}
	return token, nil
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

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

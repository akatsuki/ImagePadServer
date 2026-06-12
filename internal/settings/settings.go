package settings

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type Settings struct {
	SteamVRExplicitlyDisabled bool          `json:"steamvrExplicitlyDisabled"`
	VideoPlayerEnabled        bool          `json:"videoPlayerEnabled"`
	VideoQualityMode          string        `json:"videoQualityMode,omitempty"`
	NetworkMbps               int           `json:"networkMbps,omitempty"`
	NetworkUploadMbps         int           `json:"networkUploadMbps,omitempty"`
	AdminToken                string        `json:"adminToken,omitempty"`
	OBSStreamKey              string        `json:"obsStreamKey,omitempty"`
	OBSLatencyMode            string        `json:"obsLatencyMode,omitempty"`
	OBSDVREnabled             bool          `json:"obsDVREnabled,omitempty"`
	RelayDevices              []RelayDevice `json:"relayDevices,omitempty"`
}

type RelayDevice struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	DeviceName   string `json:"deviceName"`
	Scope        string `json:"scope"`
	CreatedAt    string `json:"createdAt"`
	LastSeenAt   string `json:"lastSeenAt,omitempty"`
	RevokedAt    string `json:"revokedAt,omitempty"`
}

var fileMu sync.Mutex

func Load() (Settings, error) {
	fileMu.Lock()
	defer fileMu.Unlock()
	return loadUnlocked()
}

func Save(settings Settings) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	return saveUnlocked(settings)
}

// Update loads settings, applies fn, and saves atomically.
func Update(fn func(*Settings) error) error {
	fileMu.Lock()
	defer fileMu.Unlock()

	settings, err := loadUnlocked()
	if err != nil {
		return err
	}
	if err := fn(&settings); err != nil {
		return err
	}
	return saveUnlocked(settings)
}

func EnsureAdminToken() (string, error) {
	fileMu.Lock()
	defer fileMu.Unlock()

	settings, err := loadUnlocked()
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
	if err := saveUnlocked(settings); err != nil {
		return "", err
	}
	return token, nil
}

func EnsureOBSStreamKey() (string, error) {
	fileMu.Lock()
	defer fileMu.Unlock()

	settings, err := loadUnlocked()
	if err != nil {
		return "", err
	}
	if settings.OBSStreamKey != "" {
		return settings.OBSStreamKey, nil
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	settings.OBSStreamKey = token
	if err := saveUnlocked(settings); err != nil {
		return "", err
	}
	return token, nil
}

func RotateOBSStreamKey() (string, error) {
	fileMu.Lock()
	defer fileMu.Unlock()

	settings, err := loadUnlocked()
	if err != nil {
		return "", err
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	settings.OBSStreamKey = token
	if err := saveUnlocked(settings); err != nil {
		return "", err
	}
	return token, nil
}

func loadUnlocked() (Settings, error) {
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

func saveUnlocked(settings Settings) error {
	settingsPath := path()
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmpPath := settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, settingsPath)
}

func Dir() string {
	if configured := os.Getenv("IMAGEPAD_DATA_DIR"); configured != "" {
		return configured
	}
	if legacy := os.Getenv("APPDATA"); legacy != "" && runtime.GOOS == "windows" {
		return filepath.Join(legacy, "ImagePadServer")
	}
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		return filepath.Join(base, "ImagePadServer")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		switch runtime.GOOS {
		case "darwin":
			return filepath.Join(home, "Library", "Application Support", "ImagePadServer")
		default:
			return filepath.Join(home, ".config", "ImagePadServer")
		}
	}
	return filepath.Join(os.TempDir(), "ImagePadServer")
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

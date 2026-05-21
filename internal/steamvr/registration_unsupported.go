//go:build !windows
// +build !windows

package steamvr

import "errors"

// RegistrationStatus describes whether ImagePadServer is registered with SteamVR.
type RegistrationStatus struct {
	Available    bool   `json:"available"`
	Enabled      bool   `json:"enabled"`
	ManifestPath string `json:"manifestPath"`
	ConfigPath   string `json:"configPath"`
	Message      string `json:"message"`
}

// Registration returns an unsupported status outside Windows.
func Registration() RegistrationStatus {
	return RegistrationStatus{Available: false, Message: "SteamVR registration is only supported on Windows"}
}

// SetRegistration is unsupported outside Windows.
func SetRegistration(enabled bool) (RegistrationStatus, error) {
	_ = enabled
	return Registration(), errors.New("SteamVR registration is only supported on Windows")
}

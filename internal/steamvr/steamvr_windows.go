//go:build windows
// +build windows

package steamvr

// Config contains the local ImagePadServer endpoint that optional SteamVR
// integrations should use instead of duplicating server state.
type Config struct {
	ServerURL string
}

// Start initializes the optional Windows SteamVR integration.
//
// The first milestone keeps this as a no-op hook so the core server lifecycle
// and cross-platform builds can be validated before choosing an OpenVR binding
// or helper executable.
func Start(cfg Config) error {
	_ = cfg
	return nil
}

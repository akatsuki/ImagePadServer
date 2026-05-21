//go:build !windows
// +build !windows

package steamvr

// Config contains the local ImagePadServer endpoint that optional SteamVR
// integrations should use instead of duplicating server state.
type Config struct {
	ServerURL string
}

// Start is a no-op on platforms without SteamVR support.
func Start(cfg Config) error {
	_ = cfg
	return nil
}

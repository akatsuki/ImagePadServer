//go:build !windows
// +build !windows

package tray

// Tray represents a no-op notification-area icon on unsupported platforms.
type Tray struct{}

// Start is a no-op on platforms without the Windows notification area.
func Start(serverURL string) (*Tray, error) {
	_ = serverURL
	return &Tray{}, nil
}

// Stop is a no-op on unsupported platforms.
func (t *Tray) Stop() {}

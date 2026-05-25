//go:build !windows && !(darwin && cgo)
// +build !windows
// +build !darwin !cgo

package tray

// Tray represents a no-op notification-area icon on unsupported platforms.
type Tray struct{}

// Start is a no-op on platforms without the Windows notification area.
func Start(serverURL string, onExit func(), onResume func(), onReconnect func()) (*Tray, error) {
	_ = serverURL
	_ = onExit
	_ = onResume
	_ = onReconnect
	return &Tray{}, nil
}

// Stop is a no-op on unsupported platforms.
func (t *Tray) Stop() {}

// MustRunOnMainThread reports whether Start owns the main application loop.
func MustRunOnMainThread() bool {
	return false
}

// StopCurrent is used by platforms whose tray owns the main application loop.
func StopCurrent() {}

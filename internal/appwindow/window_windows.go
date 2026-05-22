//go:build windows
// +build windows

package appwindow

import "imagepadserver/internal/browser"

// Show opens the browser UI from SteamVR launches.
//
// The previous native Win32 helper window could become unresponsive when
// launched by SteamVR. Keep SteamVR launches reliable and let the browser UI
// remain the single full-featured control surface.
func Show(serverURL string) error {
	browser.Open(serverURL)
	return nil
}

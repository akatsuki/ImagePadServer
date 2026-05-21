//go:build !windows
// +build !windows

package appwindow

import "imagepadserver/internal/browser"

func Show(serverURL string) error {
	browser.Open(serverURL)
	return nil
}

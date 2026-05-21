//go:build !windows
// +build !windows

package clipboard

import "errors"

var errUnsupported = errors.New("clipboard copy is only supported on Windows")

// CopyText is unsupported on non-Windows platforms.
func CopyText(text string) error {
	_ = text
	return errUnsupported
}

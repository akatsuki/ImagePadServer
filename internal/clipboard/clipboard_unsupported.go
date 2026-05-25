//go:build !windows && !darwin
// +build !windows,!darwin

package clipboard

import "errors"

var errUnsupported = errors.New("clipboard copy is not supported on this platform")

// CopyText is unsupported on platforms without a local clipboard bridge.
func CopyText(text string) error {
	_ = text
	return errUnsupported
}

//go:build windows
// +build windows

package clipboard

import (
	"os/exec"
	"strings"
)

// CopyText writes text to the current Windows user's clipboard.
func CopyText(text string) error {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "Set-Clipboard -Value ([Console]::In.ReadToEnd())")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

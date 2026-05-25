//go:build darwin
// +build darwin

package clipboard

import (
	"io"
	"os/exec"
	"strings"
)

// CopyText writes text to the current macOS user's clipboard.
func CopyText(text string) error {
	cmd := exec.Command("pbcopy")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, copyErr := io.Copy(stdin, strings.NewReader(text))
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return waitErr
}

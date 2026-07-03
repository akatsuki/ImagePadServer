//go:build !windows

package ytdlpauth

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}

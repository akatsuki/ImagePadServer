//go:build !windows

package nicorender

import "os/exec"

func hideNativeWindow(cmd *exec.Cmd) {}

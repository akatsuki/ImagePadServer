//go:build !windows

package toolchain

import "os/exec"

func hideWindow(*exec.Cmd) {}

func HideWindow(cmd *exec.Cmd) {
	hideWindow(cmd)
}

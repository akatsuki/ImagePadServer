//go:build windows

package xpostimage

import (
	"os/exec"
	"syscall"
)

func hideVideoFetchWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

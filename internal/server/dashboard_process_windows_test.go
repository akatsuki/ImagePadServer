//go:build windows

package server

import (
	"os/exec"
	"syscall"
)

func hideDashboardTestWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

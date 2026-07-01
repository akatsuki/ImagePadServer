//go:build windows

package toolchain

import (
	"os/exec"
	"syscall"
)

// createNoWindow is the Windows CREATE_NO_WINDOW process creation flag. It
// prevents the child console process from allocating a console at all, which
// HideWindow alone does not reliably suppress (the console can still flash).
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func HideWindow(cmd *exec.Cmd) {
	hideWindow(cmd)
}

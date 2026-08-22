//go:build windows

package airplay

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func hideProcessWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

//go:build windows

package nicorender

import (
	"os/exec"
	"syscall"
)

func hideNativeWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

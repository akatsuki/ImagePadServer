//go:build !windows

package server

import (
	"context"
	"imagepadserver/internal/nicoexportbudget"
	"os/exec"
	"syscall"
)

func runXPostPortable(ctx context.Context, s nicoexportbudget.ProcessSpec) error {
	cmd := exec.CommandContext(ctx, s.Exe, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	cmd.Stdin = s.Stdin
	cmd.Stdout = s.Stdout
	cmd.Stderr = s.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd.Run()
}

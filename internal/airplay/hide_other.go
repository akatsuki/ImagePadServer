//go:build !windows

package airplay

import "os/exec"

func hideProcessWindow(cmd *exec.Cmd) {}

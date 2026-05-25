//go:build !windows

package imageproc

import "os/exec"

func hideWindow(*exec.Cmd) {}

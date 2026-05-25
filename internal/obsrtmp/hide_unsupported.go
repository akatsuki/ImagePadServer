//go:build !windows

package obsrtmp

import "os/exec"

func hideWindow(*exec.Cmd) {}

//go:build !windows

package tunnel

import "os/exec"

func hideWindow(*exec.Cmd) {}

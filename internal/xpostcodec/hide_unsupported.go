//go:build !windows

package xpostcodec

import "os/exec"

func hideWindow(*exec.Cmd) {}

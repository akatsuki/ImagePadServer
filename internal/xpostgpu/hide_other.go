//go:build !windows

package xpostgpu

import "os/exec"

func hideWindow(*exec.Cmd) {}

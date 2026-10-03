//go:build !windows

package xpostimage

import "os/exec"

func hideVideoFetchWindow(*exec.Cmd) {}

//go:build !windows

package server

import "os/exec"

func hideDashboardTestWindow(*exec.Cmd) {}

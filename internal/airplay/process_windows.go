//go:build windows

package airplay

import "os/exec"

func configureProcess(cmd *exec.Cmd, output *limitedBuffer) {
	cmd.Stdout = output
	cmd.Stderr = output
	hideProcessWindow(cmd)
}

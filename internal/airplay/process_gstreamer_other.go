//go:build !windows

package airplay

import "os/exec"

func configureGStreamerBridgeProcess(cmd *exec.Cmd, output *limitedBuffer, bridgePath string) {
	// Stdout is the binary Matroska transport and is attached by
	// startGStreamerBridgeProcess with StdoutPipe. Only stderr belongs in the
	// diagnostic buffer.
	cmd.Stderr = output
}

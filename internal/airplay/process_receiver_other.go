//go:build !windows

package airplay

import (
	"os/exec"
	"path/filepath"
)

func configureReceiverProcess(cmd *exec.Cmd, output *limitedBuffer, receiverPath, sessionDir string) (func() error, error) {
	configureProcess(cmd, output)
	cmd.Dir = filepath.Dir(receiverPath)
	return nil, nil
}

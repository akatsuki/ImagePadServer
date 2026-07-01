package video

import (
	"os/exec"

	"imagepadserver/internal/toolchain"
)

func hideWindow(cmd *exec.Cmd) {
	toolchain.HideWindow(cmd)
}

//go:build !windows && !darwin && !linux

package voicevoxruntime

import (
	"errors"
	"os"
)

func acquireInstallLock(string) (*os.File, error) {
	return nil, errors.New("VOICEVOX runtime is unsupported on this platform")
}

//go:build !windows && !darwin && !linux

package voicevoxruntime

import "errors"

func checkDiskSpace(string, uint64) error {
	return errors.New("VOICEVOX runtime installation is unsupported on this platform")
}

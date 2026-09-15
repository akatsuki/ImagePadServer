//go:build !windows

package video

import "os"

func assignStartedFFmpegToKillOnCloseJob(*os.Process) (func(), error) {
	return func() {}, nil
}

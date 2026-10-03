//go:build darwin || linux

package voicevoxruntime

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func checkDiskSpace(path string, required uint64) error {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return err
	}
	if uint64(s.Bavail)*uint64(s.Bsize) < required {
		return fmt.Errorf("VOICEVOXの準備には約%d GiBの空き容量が必要です", (required+(1<<30)-1)>>30)
	}
	return nil
}

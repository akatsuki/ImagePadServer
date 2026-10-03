//go:build windows

package voicevoxruntime

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func checkDiskSpace(path string, required uint64) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &available, &total, &free); err != nil {
		return err
	}
	if available < required {
		return fmt.Errorf("VOICEVOXの準備には約%d GiBの空き容量が必要です", (required+(1<<30)-1)>>30)
	}
	return nil
}

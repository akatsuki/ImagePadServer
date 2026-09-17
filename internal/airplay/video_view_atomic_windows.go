//go:build windows

package airplay

import "golang.org/x/sys/windows"

func atomicReplaceVideoViewFile(tmp, dst string) error {
	// MoveFileEx replaces an existing destination in one filesystem operation.
	return windows.MoveFileEx(windows.StringToUTF16Ptr(tmp), windows.StringToUTF16Ptr(dst), windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

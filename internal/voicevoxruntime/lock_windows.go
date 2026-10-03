//go:build windows

package voicevoxruntime

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

// Sharing disabled: the OS releases ownership even if the installer crashes.
func acquireInstallLock(root string) (*os.File, error) {
	path := filepath.Join(root, ".install.lock")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("VOICEVOXの準備ロックを取得できません（別のアプリが準備中の可能性があります）: %w", err)
	}
	return os.NewFile(uintptr(handle), path), nil
}

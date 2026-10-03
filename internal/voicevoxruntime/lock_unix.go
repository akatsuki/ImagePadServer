//go:build darwin || linux

package voicevoxruntime

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

func acquireInstallLock(root string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(root, ".install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("VOICEVOXの準備は別のアプリが実行中です: %w", err)
	}
	return f, nil
}

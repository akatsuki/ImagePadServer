//go:build !windows

package video

import (
	"errors"
	"os"
	"syscall"
)

// openFileBackedMapping opens (or creates) a file at path and maps it with
// shared read/write access so another process that maps the same file sees the
// same pages.
func openFileBackedMapping(path string, size int) ([]byte, func() error, error) {
	if path == "" || size <= 0 {
		return nil, nil, errors.New("invalid file mapping geometry")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := f.Truncate(int64(size)); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	b, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	release := func() error {
		e1 := syscall.Munmap(b)
		e2 := f.Close()
		if e1 != nil {
			return e1
		}
		return e2
	}
	return b, release, nil
}

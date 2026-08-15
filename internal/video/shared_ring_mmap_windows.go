//go:build windows

package video

import (
	"errors"
	"os"
	"unsafe"
)

// openFileBackedMapping opens (or creates) a file at path and maps it with
// shared read/write access so another process that maps the same file sees the
// same pages. The kernel32 procs are declared in native_mapping_windows.go.
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
	h, _, e := createFileMappingW.Call(
		f.Fd(), 0, 0x04, uintptr(uint64(size)>>32), uintptr(size), 0,
	)
	if h == 0 {
		_ = f.Close()
		return nil, nil, e
	}
	v, _, e := mapViewOfFile.Call(h, 0x0006, 0, 0, uintptr(size))
	if v == 0 {
		closeHandle.Call(h)
		_ = f.Close()
		return nil, nil, e
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(v)), size)
	release := func() error {
		flushViewOfFile.Call(v, uintptr(size))
		unmapViewOfFile.Call(v)
		closeHandle.Call(h)
		return f.Close()
	}
	return b, release, nil
}

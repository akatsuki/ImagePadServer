//go:build windows

package video

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

type windowsMapping struct { handle syscall.Handle; view uintptr; bytes []byte }

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	createFileMappingW = kernel32.NewProc("CreateFileMappingW")
	mapViewOfFile = kernel32.NewProc("MapViewOfFile")
	flushViewOfFile = kernel32.NewProc("FlushViewOfFile")
	unmapViewOfFile = kernel32.NewProc("UnmapViewOfFile")
	closeHandle = kernel32.NewProc("CloseHandle")
)

func openNativeMapping(name string, size int) (NativeMapping, error) {
	if name == "" || size <= 0 { return nil, ErrNativeMappingUnavailable }
	// Named kernel objects cannot contain path separators; callers may pass a
	// session path for parity with POSIX, so normalize it to a stable object id.
	name = "ImagePadServer." + strings.NewReplacer("\\", ".", "/", ".", ":", ".").Replace(name)
	wname, _ := syscall.UTF16PtrFromString(name)
	h, _, e := createFileMappingW.Call(^uintptr(0), 0, 0x04, uintptr(uint64(size)>>32), uintptr(size), uintptr(unsafe.Pointer(wname)))
	if h == 0 { return nil, fmt.Errorf("CreateFileMappingW: %w", e) }
	v, _, e := mapViewOfFile.Call(h, 0x0006, 0, 0, uintptr(size))
	if v == 0 { closeHandle.Call(h); return nil, fmt.Errorf("MapViewOfFile: %w", e) }
	b := unsafe.Slice((*byte)(unsafe.Pointer(v)), size)
	return &windowsMapping{handle: syscall.Handle(h), view: v, bytes: b}, nil
}
func (m *windowsMapping) Bytes() []byte { return m.bytes }
func (m *windowsMapping) Close() error {
	if m.view != 0 { flushViewOfFile.Call(m.view, uintptr(len(m.bytes))); unmapViewOfFile.Call(m.view); m.view = 0 }
	if m.handle != 0 { closeHandle.Call(uintptr(m.handle)); m.handle = 0 }
	return nil
}

//go:build windows
// +build windows

package clipboard

import (
	"syscall"
	"unsafe"
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
)

// CopyText writes text to the current Windows user's clipboard.
func CopyText(text string) error {
	data, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}

	if ok, _, err := procOpenClipboard.Call(0); ok == 0 {
		return err
	}
	defer procCloseClipboard.Call()

	if ok, _, err := procEmptyClipboard.Call(); ok == 0 {
		return err
	}

	mem, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)*2))
	if mem == 0 {
		return err
	}

	ptr, _, err := procGlobalLock.Call(mem)
	if ptr == 0 {
		procGlobalFree.Call(mem)
		return err
	}
	for i, v := range data {
		*(*uint16)(unsafe.Pointer(ptr + uintptr(i*2))) = v
	}
	procGlobalUnlock.Call(mem)

	if ok, _, err := procSetClipboardData.Call(cfUnicodeText, mem); ok == 0 {
		procGlobalFree.Call(mem)
		return err
	}
	return nil
}

//go:build windows
// +build windows

package tray

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"imagepadserver/internal/about"
	"imagepadserver/internal/appicon"
	"imagepadserver/internal/browser"
)

const (
	trayUID      = 1
	trayCallback = 0x0400 + 77
	menuOpenID   = 1001
	menuExitID   = 1002

	wmDestroy      = 0x0002
	wmClose        = 0x0010
	wmCommand      = 0x0111
	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205
	nimAdd         = 0x00000000
	nimDelete      = 0x00000002
	nifMessage     = 0x00000001
	nifIcon        = 0x00000002
	nifTip         = 0x00000004
	mfString       = 0x00000000
	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040
	tpmRightButton = 0x0002
)

// Tray represents the Windows notification-area icon.
type Tray struct {
	hwnd   uintptr
	done   chan struct{}
	once   sync.Once
	onExit func()
}

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	shell32              = syscall.NewLazyDLL("shell32.dll")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyIcon      = user32.NewProc("DestroyIcon")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procSetForegroundW   = user32.NewProc("SetForegroundWindow")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	currentTray          *Tray
	currentTrayURL       string
	currentTrayIcon      uintptr
	currentTrayIconOwned bool
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type point struct {
	X int32
	Y int32
}

type notifyIconData struct {
	Size        uint32
	HWnd        uintptr
	ID          uint32
	Flags       uint32
	CallbackMsg uint32
	Icon        uintptr
	Tip         [128]uint16
	State       uint32
	StateMask   uint32
	Info        [256]uint16
	Version     uint32
	InfoTitle   [64]uint16
	InfoFlags   uint32
	GUID        [16]byte
	BalloonIcon uintptr
}

// Start shows a Windows notification-area icon that opens serverURL when clicked.
func Start(serverURL string, onExit func()) (*Tray, error) {
	ready := make(chan error, 1)
	tray := &Tray{done: make(chan struct{}), onExit: onExit}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		runTray(tray, serverURL, ready)
	}()

	if err := <-ready; err != nil {
		return nil, err
	}
	return tray, nil
}

// Stop removes the notification-area icon.
func (t *Tray) Stop() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.hwnd != 0 {
			procPostMessageW.Call(t.hwnd, wmClose, 0, 0)
		}
		<-t.done
	})
}

func runTray(t *Tray, serverURL string, ready chan<- error) {
	currentTray = t
	currentTrayURL = serverURL

	instance, _, _ := procGetModuleHandleW.Call(0)
	className := utf16Ptr("ImagePadServerTrayWindow")
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   syscall.NewCallback(trayWindowProc),
		Instance:  instance,
		ClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("ImagePadServer Tray"))),
		0,
		0, 0, 0, 0,
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		close(t.done)
		ready <- err
		return
	}
	t.hwnd = hwnd

	icon := loadTrayIcon(instance)
	currentTrayIcon = icon
	if ok := addNotifyIcon(hwnd, icon); !ok {
		procDestroyWindow.Call(hwnd)
		close(t.done)
		ready <- syscall.GetLastError()
		return
	}
	ready <- nil

	var message msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
	close(t.done)
}

func trayWindowProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	switch message {
	case trayCallback:
		switch uint32(lparam) {
		case wmLButtonUp:
			browser.Open(currentTrayURL)
			return 0
		case wmRButtonUp:
			showTrayMenu(hwnd)
			return 0
		}
	case wmCommand:
		switch uint32(wparam) & 0xffff {
		case menuOpenID:
			browser.Open(currentTrayURL)
			return 0
		case menuExitID:
			if currentTray != nil && currentTray.onExit != nil {
				go currentTray.onExit()
			}
			return 0
		}
	case wmClose:
		deleteNotifyIcon(hwnd)
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if currentTrayIconOwned && currentTrayIcon != 0 {
			procDestroyIcon.Call(currentTrayIcon)
			currentTrayIcon = 0
			currentTrayIconOwned = false
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wparam, lparam)
	return ret
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	procAppendMenuW.Call(menu, mfString, menuOpenID, uintptr(unsafe.Pointer(utf16Ptr("開く"))))
	procAppendMenuW.Call(menu, mfString, menuExitID, uintptr(unsafe.Pointer(utf16Ptr("終了"))))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundW.Call(hwnd)
	procTrackPopupMenu.Call(menu, tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
}

func addNotifyIcon(hwnd, icon uintptr) bool {
	data := newNotifyIconData(hwnd, icon)
	ok, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
	return ok != 0
}

func deleteNotifyIcon(hwnd uintptr) {
	data := notifyIconData{
		Size: uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd: hwnd,
		ID:   trayUID,
	}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

func newNotifyIconData(hwnd, icon uintptr) notifyIconData {
	data := notifyIconData{
		Size:        uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:        hwnd,
		ID:          trayUID,
		Flags:       nifMessage | nifIcon | nifTip,
		CallbackMsg: trayCallback,
		Icon:        icon,
	}
	copy(data.Tip[:], syscall.StringToUTF16(about.AppName+" "+about.Version))
	return data
}

func loadTrayIcon(instance uintptr) uintptr {
	if path, err := ensureTrayIcon(); err == nil {
		if icon, _, _ := procLoadImageW.Call(
			0,
			uintptr(unsafe.Pointer(utf16Ptr(path))),
			imageIcon,
			0,
			0,
			lrLoadFromFile|lrDefaultSize,
		); icon != 0 {
			currentTrayIconOwned = true
			return icon
		}
	}
	if icon, _, _ := procLoadIconW.Call(instance, 1); icon != 0 {
		currentTrayIconOwned = false
		return icon
	}
	icon, _, _ := procLoadIconW.Call(0, 32512)
	currentTrayIconOwned = false
	return icon
}

func ensureTrayIcon() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "ImagePadServer")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "imagepad-tray.ico")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, appicon.IconICO) {
		return path, nil
	}
	if err := os.WriteFile(path, appicon.IconICO, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func utf16Ptr(text string) *uint16 {
	ptr, _ := syscall.UTF16PtrFromString(text)
	return ptr
}

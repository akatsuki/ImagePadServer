//go:build darwin && cgo
// +build darwin,cgo

package tray

/*
#cgo darwin CFLAGS: -fblocks
#cgo darwin LDFLAGS: -framework Cocoa

#include <stdlib.h>

void imagepadStartStatusItem(char *title, char *version, char *copyright, void *imageBytes, int imageLen);
void imagepadStopStatusItem(void);

extern void imagepadDarwinOpen(void);
extern void imagepadDarwinReconnect(void);
extern void imagepadDarwinExit(void);
*/
import "C"

import (
	"runtime"
	"sync"
	"unsafe"

	"imagepadserver/internal/about"
	"imagepadserver/internal/appicon"
	"imagepadserver/internal/browser"
)

// Tray represents the macOS menu bar status item.
type Tray struct {
	done        chan struct{}
	once        sync.Once
	serverURL   string
	onExit      func()
	onReconnect func()
}

var currentDarwinTray *Tray

func init() {
	runtime.LockOSThread()
}

// Start shows a macOS menu bar item for ImagePadServer.
func Start(serverURL string, onExit func(), onResume func(), onReconnect func()) (*Tray, error) {
	_ = onResume
	tray := &Tray{
		done:        make(chan struct{}),
		serverURL:   serverURL,
		onExit:      onExit,
		onReconnect: onReconnect,
	}
	currentDarwinTray = tray

	title := C.CString("ImagePad")
	defer C.free(unsafe.Pointer(title))
	version := C.CString(about.Version)
	defer C.free(unsafe.Pointer(version))
	copyright := C.CString(about.Copyright)
	defer C.free(unsafe.Pointer(copyright))
	var imagePtr unsafe.Pointer
	if len(appicon.MenuBarTemplatePNG) > 0 {
		imagePtr = unsafe.Pointer(&appicon.MenuBarTemplatePNG[0])
	}
	C.imagepadStartStatusItem(title, version, copyright, imagePtr, C.int(len(appicon.MenuBarTemplatePNG)))
	close(tray.done)
	return tray, nil
}

// Stop removes the macOS menu bar item.
func (t *Tray) Stop() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		C.imagepadStopStatusItem()
		<-t.done
	})
}

//export imagepadDarwinOpen
func imagepadDarwinOpen() {
	if currentDarwinTray != nil {
		browser.Open(currentDarwinTray.serverURL)
	}
}

//export imagepadDarwinReconnect
func imagepadDarwinReconnect() {
	if currentDarwinTray != nil && currentDarwinTray.onReconnect != nil {
		go currentDarwinTray.onReconnect()
	}
}

//export imagepadDarwinExit
func imagepadDarwinExit() {
	if currentDarwinTray != nil && currentDarwinTray.onExit != nil {
		go currentDarwinTray.onExit()
	}
}

// MustRunOnMainThread reports whether Start owns the main application loop.
func MustRunOnMainThread() bool {
	return true
}

// StopCurrent stops the active macOS menu bar event loop.
func StopCurrent() {
	if currentDarwinTray != nil {
		currentDarwinTray.Stop()
	}
}

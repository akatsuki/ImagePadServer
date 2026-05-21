//go:build windows
// +build windows

package appwindow

import (
	"bytes"
	"encoding/json"
	"errors"
	"image/color"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"imagepadserver/internal/clipboard"
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	swShow             = 5
	cwUseDefault       = 0x80000000
	wmDestroy          = 0x0002
	wmClose            = 0x0010
	wmPaint            = 0x000f
	wmSetIcon          = 0x0080
	wmLButtonDown      = 0x0201
	wmMouseMove        = 0x0200
	iconSmall          = 0
	iconBig            = 1
	ofnFileMustExist   = 0x00001000
	ofnPathMustExist   = 0x00000800
)

type rect struct {
	left, top, right, bottom int32
}

type point struct {
	x, y int32
}

type button struct {
	id     int
	label  string
	bounds rect
	accent color.RGBA
}

const (
	actionRefresh = iota + 1
	actionCopy
	actionUpload
	actionClear
	actionClose
)

type stateResponse struct {
	ImageURL string `json:"imageURL"`
	Current  *struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"current"`
	Tunnel struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		URL     string `json:"url"`
	} `json:"tunnel"`
}

type nativeWindow struct {
	serverURL string
	hwnd      uintptr
	client    http.Client
	imageURL  string
	status    string
	tunnelURL string
	hoverID   int
	buttons   []button
}

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	gdi32                = syscall.NewLazyDLL("gdi32.dll")
	comdlg32             = syscall.NewLazyDLL("comdlg32.dll")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procCreatePen        = gdi32.NewProc("CreatePen")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procDrawTextW        = user32.NewProc("DrawTextW")
	procEndPaint         = user32.NewProc("EndPaint")
	procFillRect         = user32.NewProc("FillRect")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procRectangle        = gdi32.NewProc("Rectangle")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procRoundRect        = gdi32.NewProc("RoundRect")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procShowWindow       = user32.NewProc("ShowWindow")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	currentWindow        *nativeWindow
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

type paintStruct struct {
	Hdc         uintptr
	Erase       int32
	Paint       rect
	Restore     int32
	IncUpdate   int32
	RgbReserved [32]byte
}

type openFileName struct {
	StructSize    uint32
	Owner         uintptr
	Instance      uintptr
	Filter        *uint16
	CustomFilter  *uint16
	MaxCustFilter uint32
	FilterIndex   uint32
	File          *uint16
	MaxFile       uint32
	FileTitle     *uint16
	MaxFileTitle  uint32
	InitialDir    *uint16
	Title         *uint16
	Flags         uint32
	FileOffset    uint16
	FileExtension uint16
	DefExt        *uint16
	CustData      uintptr
	FnHook        uintptr
	TemplateName  *uint16
}

func Show(serverURL string) error {
	w := &nativeWindow{
		serverURL: strings.TrimRight(serverURL, "/") + "/",
		client:    http.Client{Timeout: 20 * time.Second},
		status:    "起動中...",
	}
	w.buttons = []button{
		{actionRefresh, "更新", rect{40, 456, 236, 536}, color.RGBA{44, 107, 219, 255}},
		{actionCopy, "コピー", rect{260, 456, 456, 536}, color.RGBA{23, 146, 126, 255}},
		{actionUpload, "アップロード", rect{480, 456, 744, 536}, color.RGBA{48, 128, 90, 255}},
		{actionClear, "画像クリア", rect{768, 456, 1036, 536}, color.RGBA{190, 72, 62, 255}},
		{actionClose, "閉じる", rect{840, 578, 1036, 638}, color.RGBA{92, 103, 116, 255}},
	}
	currentWindow = w

	instance, _, _ := procGetModuleHandleW.Call(0)
	appIcon, _, _ := procLoadIconW.Call(instance, 1)
	className := utf16Ptr("ImagePadServerCustomWindow")
	wc := wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:    syscall.NewCallback(windowProc),
		Instance:   instance,
		Icon:       appIcon,
		Background: 0,
		ClassName:  className,
		IconSm:     appIcon,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("ImagePadServer"))),
		wsOverlappedWindow|wsVisible,
		cwUseDefault, cwUseDefault, 1100, 720,
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		return err
	}
	w.hwnd = hwnd
	if appIcon != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, appIcon)
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, appIcon)
	}
	w.refresh()
	procShowWindow.Call(hwnd, swShow)

	var message msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
	return nil
}

func windowProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	switch message {
	case wmPaint:
		if currentWindow != nil {
			currentWindow.paint()
			return 0
		}
	case wmLButtonDown:
		if currentWindow != nil {
			x, y := lparamPoint(lparam)
			currentWindow.click(x, y)
			return 0
		}
	case wmMouseMove:
		if currentWindow != nil {
			x, y := lparamPoint(lparam)
			currentWindow.setHover(x, y)
			return 0
		}
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wparam, lparam)
	return ret
}

func (w *nativeWindow) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(w.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(w.hwnd, uintptr(unsafe.Pointer(&ps)))

	fill(hdc, rect{0, 0, 1200, 800}, rgb(13, 18, 25))
	roundFill(hdc, rect{24, 24, 1056, 660}, 24, rgb(28, 36, 47), rgb(61, 78, 96))
	drawText(hdc, "ImagePadServer", rect{48, 48, 700, 96}, 34, 700, rgb(241, 246, 250), 0)
	drawText(hdc, "VR Upload Console", rect{52, 94, 700, 128}, 20, 500, rgb(139, 157, 175), 0)

	statusColor := rgb(92, 224, 171)
	if w.imageURL == "" {
		statusColor = rgb(233, 183, 92)
	}
	roundFill(hdc, rect{48, 146, 1032, 250}, 18, rgb(18, 24, 32), rgb(53, 68, 83))
	drawText(hdc, statusLabel(w.imageURL), rect{72, 166, 1008, 202}, 28, 700, statusColor, 0)
	drawText(hdc, w.status, rect{72, 206, 1008, 242}, 21, 500, rgb(198, 209, 220), 0)

	roundFill(hdc, rect{48, 282, 1032, 410}, 18, rgb(12, 17, 24), rgb(46, 60, 74))
	drawText(hdc, urlDisplay(w.imageURL), rect{72, 306, 1008, 386}, 24, 600, rgb(238, 243, 247), 0x00000010)

	for _, btn := range w.buttons {
		w.drawButton(hdc, btn)
	}

	drawText(hdc, "SteamVRから起動したら、このウインドウでアップロードとコピーを操作できます。", rect{52, 600, 812, 640}, 20, 500, rgb(140, 157, 176), 0)
}

func (w *nativeWindow) drawButton(hdc uintptr, btn button) {
	c := btn.accent
	if w.hoverID == btn.id {
		c = lighten(c, 24)
	}
	roundFill(hdc, btn.bounds, 18, colorRef(c), colorRef(lighten(c, 38)))
	drawText(hdc, btn.label, inset(btn.bounds, 10, 10), 28, 700, rgb(255, 255, 255), 0x00000001|0x00000004)
}

func (w *nativeWindow) click(x, y int32) {
	for _, btn := range w.buttons {
		if inRect(x, y, btn.bounds) {
			w.handle(btn.id)
			return
		}
	}
}

func (w *nativeWindow) setHover(x, y int32) {
	next := 0
	for _, btn := range w.buttons {
		if inRect(x, y, btn.bounds) {
			next = btn.id
			break
		}
	}
	if next != w.hoverID {
		w.hoverID = next
		w.invalidate()
	}
}

func (w *nativeWindow) handle(id int) {
	switch id {
	case actionRefresh:
		w.refresh()
	case actionCopy:
		if strings.HasPrefix(w.imageURL, "http") && clipboard.CopyText(w.imageURL) == nil {
			w.status = "URLをPCクリップボードへコピーしました"
		} else {
			w.status = "コピーできるURLがありません"
		}
	case actionUpload:
		if path, ok := pickImageFile(w.hwnd); ok {
			w.status = "アップロード中..."
			w.invalidate()
			if err := w.upload(path); err != nil {
				w.status = "アップロード失敗: " + err.Error()
				w.invalidate()
				return
			}
			w.refresh()
		}
	case actionClear:
		if err := w.post("api/clear"); err != nil {
			w.status = "クリア失敗: " + err.Error()
			w.invalidate()
			return
		}
		w.refresh()
	case actionClose:
		procDestroyWindow.Call(w.hwnd)
	}
	w.invalidate()
}

func (w *nativeWindow) refresh() {
	var state stateResponse
	resp, err := w.client.Get(w.serverURL + "api/state")
	if err != nil {
		w.status = "状態取得失敗: " + err.Error()
		w.invalidate()
		return
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		w.status = "状態取得失敗: " + err.Error()
		w.invalidate()
		return
	}
	w.imageURL = state.ImageURL
	w.tunnelURL = state.Tunnel.URL
	if state.Current != nil {
		w.status = "画像 " + itoa(state.Current.Width) + " x " + itoa(state.Current.Height)
	} else {
		w.status = "画像未設定"
	}
	if state.Tunnel.OK {
		w.status += " / 公開HTTPS接続済み"
	} else if state.Tunnel.Message != "" {
		w.status += " / " + state.Tunnel.Message
	}
	w.invalidate()
}

func (w *nativeWindow) upload(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	_ = writer.WriteField("format", "jpeg")
	if err := writer.Close(); err != nil {
		return err
	}

	resp, err := w.client.Post(w.serverURL+"api/upload", writer.FormDataContentType(), &body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		if len(data) == 0 {
			return errors.New(resp.Status)
		}
		return errors.New(string(data))
	}
	return nil
}

func (w *nativeWindow) post(path string) error {
	resp, err := w.client.Post(w.serverURL+path, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New(resp.Status)
	}
	return nil
}

func (w *nativeWindow) invalidate() {
	procInvalidateRect.Call(w.hwnd, 0, 1)
}

func pickImageFile(owner uintptr) (string, bool) {
	buffer := make([]uint16, 4096)
	filter := syscall.StringToUTF16("画像ファイル\x00*.png;*.jpg;*.jpeg;*.gif\x00すべてのファイル\x00*.*\x00\x00")
	title := utf16Ptr("アップロードする画像を選択")
	ofn := openFileName{
		StructSize: uint32(unsafe.Sizeof(openFileName{})),
		Owner:      owner,
		Filter:     &filter[0],
		File:       &buffer[0],
		MaxFile:    uint32(len(buffer)),
		Title:      title,
		Flags:      ofnFileMustExist | ofnPathMustExist,
	}
	ok, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ok == 0 {
		return "", false
	}
	return syscall.UTF16ToString(buffer), true
}

func fill(hdc uintptr, r rect, c uintptr) {
	brush, _, _ := procCreateSolidBrush.Call(c)
	defer procDeleteObject.Call(brush)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
}

func roundFill(hdc uintptr, r rect, radius int32, fillColor, borderColor uintptr) {
	brush, _, _ := procCreateSolidBrush.Call(fillColor)
	pen, _, _ := procCreatePen.Call(0, 1, borderColor)
	defer procDeleteObject.Call(brush)
	defer procDeleteObject.Call(pen)
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procRoundRect.Call(hdc, uintptr(r.left), uintptr(r.top), uintptr(r.right), uintptr(r.bottom), uintptr(radius), uintptr(radius))
	procSelectObject.Call(hdc, oldBrush)
	procSelectObject.Call(hdc, oldPen)
}

func drawText(hdc uintptr, text string, r rect, size, weight int, c uintptr, flags uintptr) {
	font := createFont(size, weight)
	defer procDeleteObject.Call(font)
	oldFont, _, _ := procSelectObject.Call(hdc, font)
	procSetBkMode.Call(hdc, 1)
	procSetTextColor.Call(hdc, c)
	if flags == 0 {
		flags = 0x00000000 | 0x00000004
	}
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(utf16Ptr(text))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), flags)
	procSelectObject.Call(hdc, oldFont)
}

func createFont(height, weight int) uintptr {
	font, _, _ := procCreateFontW.Call(
		uintptr(-height),
		0, 0, 0,
		uintptr(weight),
		0, 0, 0,
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))),
	)
	return font
}

func statusLabel(url string) string {
	if strings.HasPrefix(url, "http") {
		return "READY"
	}
	return "WAITING FOR IMAGE"
}

func urlDisplay(url string) string {
	if url == "" {
		return "画像をアップロードすると、ここにImagePad用URLが表示されます。"
	}
	if len(url) > 92 {
		return url[:52] + "..." + url[len(url)-34:]
	}
	return url
}

func rgb(r, g, b byte) uintptr {
	return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16
}

func colorRef(c color.RGBA) uintptr {
	return rgb(c.R, c.G, c.B)
}

func lighten(c color.RGBA, amount byte) color.RGBA {
	return color.RGBA{R: add(c.R, amount), G: add(c.G, amount), B: add(c.B, amount), A: 255}
}

func add(v, amount byte) byte {
	if int(v)+int(amount) > 255 {
		return 255
	}
	return v + amount
}

func inRect(x, y int32, r rect) bool {
	return x >= r.left && x <= r.right && y >= r.top && y <= r.bottom
}

func inset(r rect, x, y int32) rect {
	return rect{r.left + x, r.top + y, r.right - x, r.bottom - y}
}

func lparamPoint(lparam uintptr) (int32, int32) {
	x := int16(lparam & 0xffff)
	y := int16((lparam >> 16) & 0xffff)
	return int32(x), int32(y)
}

func utf16Ptr(text string) *uint16 {
	ptr, _ := syscall.UTF16PtrFromString(text)
	return ptr
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}

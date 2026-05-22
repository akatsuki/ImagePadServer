//go:build windows
// +build windows

package steamvr

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"imagepadserver/internal/browser"
	"imagepadserver/internal/clipboard"
	"imagepadserver/internal/settings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Config contains the local ImagePadServer endpoint that optional SteamVR
// integrations should use instead of duplicating server state.
type Config struct {
	ServerURL string
}

const (
	openVRApplicationOverlay = 2
	openVRInitErrorNone      = 0
	ivrOverlayVersion        = "FnTable:IVROverlay_028"

	overlayWidth  = 1024
	overlayHeight = 576

	fnFindOverlay                  = 0
	fnSetOverlayWidthInMeters      = 22
	fnShowOverlay                  = 43
	fnPollNextOverlayEvent         = 48
	fnSetOverlayInputMethod        = 50
	fnSetOverlayMouseScale         = 52
	fnSetOverlayFromFile           = 63
	fnCreateDashboardOverlay       = 67
	fnSetDashboardOverlaySceneProc = 70

	overlayInputMethodMouse = 1

	eventMouseMove     = 300
	eventMouseButtonDn = 301
	eventMouseButtonUp = 302
)

type overlaySession struct {
	cfg             Config
	table           *[96]uintptr
	mainHandle      uint64
	thumbnailHandle uint64
	panelPath       string
	iconPath        string
	client          http.Client
	state           panelState
	buttons         []panelButton
	hoverID         string
	lastPanelHash   [sha1.Size]byte
	hasPanelHash    bool
}

type panelState struct {
	Status       string
	Kind         string
	ShareURL     string
	ShareLabel   string
	PreviewURL   string
	Preview      image.Image
	LastAction   string
	LastActionAt time.Time
	UpdatedAt    time.Time
	LastPointer  string
}

type panelButton struct {
	ID    string
	Label string
	Rect  image.Rectangle
}

type stateResponse struct {
	ShareURL      string `json:"shareURL"`
	ShareURLLabel string `json:"shareURLLabel"`
	PreviewURL    string `json:"previewImageURL"`
	Current       *struct {
		Kind      string `json:"kind"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		SizeBytes int64  `json:"sizeBytes"`
	} `json:"current"`
	VideoStatus struct {
		Active  bool   `json:"active"`
		Message string `json:"message"`
	} `json:"videoStatus"`
}

type vrEvent struct {
	EventType       uint32
	TrackedDevice   uint32
	EventAgeSeconds float32
	MouseX          float32
	MouseY          float32
	MouseButton     uint32
	MouseCursor     uint32
	Reserved        [64]byte
}

var overlayFont *opentype.Font

// Start initializes the optional Windows SteamVR dashboard overlay.
func Start(cfg Config) error {
	dllPath, err := openVRDLLPath()
	if err != nil {
		return err
	}
	dll, err := syscall.LoadDLL(dllPath)
	if err != nil {
		return err
	}
	initProc, err := dll.FindProc("VR_InitInternal")
	if err != nil {
		return err
	}
	getInterfaceProc, err := dll.FindProc("VR_GetGenericInterface")
	if err != nil {
		return err
	}

	var initErr uint32
	initProc.Call(uintptr(unsafe.Pointer(&initErr)), openVRApplicationOverlay)
	if initErr != openVRInitErrorNone {
		return fmt.Errorf("OpenVR init failed: %d", initErr)
	}

	version, err := syscall.BytePtrFromString(ivrOverlayVersion)
	if err != nil {
		return err
	}
	var interfaceErr uint32
	overlayTablePtr, _, _ := getInterfaceProc.Call(uintptr(unsafe.Pointer(version)), uintptr(unsafe.Pointer(&interfaceErr)))
	if interfaceErr != openVRInitErrorNone {
		return fmt.Errorf("OpenVR IVROverlay interface failed: %d", interfaceErr)
	}
	if overlayTablePtr == 0 {
		return errors.New("OpenVR IVROverlay interface was nil")
	}

	table := (*[96]uintptr)(unsafe.Pointer(overlayTablePtr))
	session, err := newOverlaySession(cfg, table)
	if err != nil {
		return err
	}
	go session.run()
	return nil
}

func newOverlaySession(cfg Config, table *[96]uintptr) (*overlaySession, error) {
	key, err := syscall.BytePtrFromString(appKey)
	if err != nil {
		return nil, err
	}
	name, err := syscall.BytePtrFromString("ImagePadServer")
	if err != nil {
		return nil, err
	}

	var mainHandle uint64
	var thumbnailHandle uint64
	if table[fnFindOverlay] != 0 {
		_, _, _ = syscall.SyscallN(
			table[fnFindOverlay],
			uintptr(unsafe.Pointer(key)),
			uintptr(unsafe.Pointer(&mainHandle)),
		)
	}
	if mainHandle == 0 {
		if table[fnCreateDashboardOverlay] == 0 {
			return nil, errors.New("OpenVR CreateDashboardOverlay was unavailable")
		}
		overlayErr, _, _ := syscall.SyscallN(
			table[fnCreateDashboardOverlay],
			uintptr(unsafe.Pointer(key)),
			uintptr(unsafe.Pointer(name)),
			uintptr(unsafe.Pointer(&mainHandle)),
			uintptr(unsafe.Pointer(&thumbnailHandle)),
		)
		if overlayErr != 0 {
			return nil, fmt.Errorf("OpenVR CreateDashboardOverlay failed: %d", overlayErr)
		}
	}

	if table[fnSetDashboardOverlaySceneProc] != 0 && mainHandle != 0 {
		_, _, _ = syscall.SyscallN(table[fnSetDashboardOverlaySceneProc], uintptr(mainHandle), uintptr(uint32(os.Getpid())))
	}

	steamVRDir := filepath.Join(settings.Dir(), "steamvr")
	if err := os.MkdirAll(steamVRDir, 0755); err != nil {
		return nil, err
	}
	session := &overlaySession{
		cfg:             cfg,
		table:           table,
		mainHandle:      mainHandle,
		thumbnailHandle: thumbnailHandle,
		panelPath:       filepath.Join(steamVRDir, "dashboard-panel.png"),
		iconPath:        filepath.Join(steamVRDir, "imagepad-icon-256.png"),
		client:          http.Client{Timeout: 3 * time.Second},
		buttons: []panelButton{
			{ID: "copy", Label: "Copy URL", Rect: image.Rect(60, 406, 286, 496)},
			{ID: "clear", Label: "Clear", Rect: image.Rect(306, 406, 492, 496)},
			{ID: "open", Label: "Open UI", Rect: image.Rect(512, 406, 718, 496)},
			{ID: "refresh", Label: "Refresh", Rect: image.Rect(738, 406, 964, 496)},
		},
	}

	if _, err := os.Stat(session.iconPath); err == nil && thumbnailHandle != 0 {
		if err := session.setOverlayImage(thumbnailHandle, session.iconPath); err != nil {
			return nil, err
		}
	}
	session.configureOverlay()
	return session, nil
}

func (s *overlaySession) configureOverlay() {
	if s.table[fnSetOverlayWidthInMeters] != 0 && s.mainHandle != 0 {
		_, _, _ = syscall.SyscallN(s.table[fnSetOverlayWidthInMeters], uintptr(s.mainHandle), uintptr(math.Float32bits(2.0)))
	}
	if s.table[fnSetOverlayInputMethod] != 0 && s.mainHandle != 0 {
		_, _, _ = syscall.SyscallN(s.table[fnSetOverlayInputMethod], uintptr(s.mainHandle), overlayInputMethodMouse)
	}
	if s.table[fnSetOverlayMouseScale] != 0 && s.mainHandle != 0 {
		scale := struct {
			X float32
			Y float32
		}{overlayWidth, overlayHeight}
		_, _, _ = syscall.SyscallN(s.table[fnSetOverlayMouseScale], uintptr(s.mainHandle), uintptr(unsafe.Pointer(&scale)))
	}
}

func (s *overlaySession) run() {
	s.state.Status = "Connecting to ImagePadServer..."
	s.renderAndApply()
	s.refresh()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		s.pollEvents()
		select {
		case <-ticker.C:
			s.refresh()
		default:
			time.Sleep(16 * time.Millisecond)
		}
	}
}

func (s *overlaySession) pollEvents() {
	if s.table[fnPollNextOverlayEvent] == 0 || s.mainHandle == 0 {
		return
	}
	for {
		var event vrEvent
		ok, _, _ := syscall.SyscallN(
			s.table[fnPollNextOverlayEvent],
			uintptr(s.mainHandle),
			uintptr(unsafe.Pointer(&event)),
			unsafe.Sizeof(event),
		)
		if ok == 0 {
			return
		}
		switch event.EventType {
		case eventMouseMove:
			next := s.buttonAt(event.MouseX, event.MouseY)
			if next != s.hoverID {
				s.hoverID = next
				s.renderAndApply()
			}
		case eventMouseButtonDn, eventMouseButtonUp:
			px, py := overlayPoint(event.MouseX, event.MouseY)
			s.state.LastPointer = fmt.Sprintf("pointer %d,%d", px, py)
			if id := s.buttonAt(event.MouseX, event.MouseY); id != "" {
				s.handleButton(id)
			} else {
				s.flash("No button at " + s.state.LastPointer)
			}
		}
	}
}

func (s *overlaySession) handleButton(id string) {
	switch id {
	case "copy":
		if strings.HasPrefix(s.state.ShareURL, "http") {
			if err := clipboard.CopyText(s.state.ShareURL); err == nil {
				s.flash("Copied URL")
			} else {
				s.flash("Copy failed")
			}
		} else {
			s.flash("No URL yet")
		}
	case "clear":
		req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(s.cfg.ServerURL, "/")+"/api/clear", nil)
		resp, err := s.client.Do(req)
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if resp != nil {
				resp.Body.Close()
			}
			s.flash("Clear failed")
			return
		}
		resp.Body.Close()
		s.flash("Cleared")
		s.refresh()
	case "open":
		browser.Open(s.cfg.ServerURL)
		s.flash("Opened desktop UI")
	case "refresh":
		s.refresh()
		s.flash("Refreshed")
	}
}

func (s *overlaySession) refresh() {
	stateURL := strings.TrimRight(s.cfg.ServerURL, "/") + "/api/state"
	resp, err := s.client.Get(stateURL)
	if err != nil {
		s.state.Status = "Server unreachable"
		s.renderAndApply()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.state.Status = "Server rejected state request"
		s.renderAndApply()
		return
	}
	var data stateResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		s.state.Status = "State decode failed"
		s.renderAndApply()
		return
	}

	next := s.state
	next.ShareURL = data.ShareURL
	next.ShareLabel = data.ShareURLLabel
	next.PreviewURL = data.PreviewURL
	next.Kind = "No media"
	next.Status = "Ready"
	if data.Current != nil {
		if data.Current.Kind == "video" {
			next.Kind = "Video"
		} else {
			next.Kind = fmt.Sprintf("Image %d x %d", data.Current.Width, data.Current.Height)
		}
	}
	if data.VideoStatus.Active {
		next.Status = "Streaming"
		if data.VideoStatus.Message != "" {
			next.Status = data.VideoStatus.Message
		}
	}
	if data.PreviewURL == "" {
		next.Preview = nil
	} else if data.PreviewURL != s.state.PreviewURL || s.state.Preview == nil {
		next.Preview = s.fetchPreview(data.PreviewURL)
	}
	changed := next.Status != s.state.Status ||
		next.Kind != s.state.Kind ||
		next.ShareURL != s.state.ShareURL ||
		next.ShareLabel != s.state.ShareLabel ||
		next.PreviewURL != s.state.PreviewURL ||
		(next.Preview == nil) != (s.state.Preview == nil)
	if changed {
		next.UpdatedAt = time.Now()
	} else {
		next.UpdatedAt = s.state.UpdatedAt
	}
	s.state = next
	if changed {
		s.renderAndApply()
	}
}

func (s *overlaySession) fetchPreview(rawURL string) image.Image {
	resp, err := s.client.Get(rawURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil
	}
	if img, err := png.Decode(bytes.NewReader(data)); err == nil {
		return img
	}
	if img, err := jpeg.Decode(bytes.NewReader(data)); err == nil {
		return img
	}
	return nil
}

func (s *overlaySession) flash(message string) {
	s.state.LastAction = message
	s.state.LastActionAt = time.Now()
	s.renderAndApply()
}

func (s *overlaySession) renderAndApply() {
	changed, err := s.renderPanel()
	if err == nil && changed {
		_ = s.setOverlayImage(s.mainHandle, s.panelPath)
	}
}

func (s *overlaySession) renderPanel() (bool, error) {
	img := image.NewRGBA(image.Rect(0, 0, overlayWidth, overlayHeight))
	fill(img, img.Bounds(), color.RGBA{10, 13, 18, 255})
	fill(img, image.Rect(0, 0, overlayWidth, 96), color.RGBA{26, 39, 58, 255})
	fill(img, image.Rect(0, 94, overlayWidth, 96), color.RGBA{58, 157, 134, 255})

	text(img, 44, 46, "ImagePadServer", 30, color.RGBA{246, 250, 253, 255})
	text(img, 44, 74, s.state.Status, 18, color.RGBA{181, 225, 213, 255})
	text(img, 744, 74, shortClock(s.state.UpdatedAt), 18, color.RGBA{164, 179, 196, 255})

	previewRect := image.Rect(44, 132, 432, 360)
	fill(img, previewRect, color.RGBA{18, 24, 32, 255})
	stroke(img, previewRect, color.RGBA{52, 68, 84, 255})
	if s.state.Preview != nil {
		drawPreview(img, previewRect.Inset(10), s.state.Preview)
	} else {
		text(img, 156, 252, "No Preview", 22, color.RGBA{116, 132, 150, 255})
	}

	infoRect := image.Rect(462, 132, 980, 360)
	fill(img, infoRect, color.RGBA{18, 24, 32, 255})
	stroke(img, infoRect, color.RGBA{52, 68, 84, 255})
	text(img, 492, 172, s.state.Kind, 24, color.RGBA{242, 247, 251, 255})
	label := s.state.ShareLabel
	if label == "" {
		label = "URL"
	}
	text(img, 492, 214, label, 18, color.RGBA{96, 211, 183, 255})
	text(img, 492, 254, elide(s.state.ShareURL, 48), 18, color.RGBA{207, 218, 229, 255})
	if s.state.LastAction != "" && time.Since(s.state.LastActionAt) < 4*time.Second {
		fill(img, image.Rect(492, 296, 930, 336), color.RGBA{32, 82, 75, 255})
		text(img, 514, 324, s.state.LastAction, 18, color.RGBA{239, 252, 248, 255})
	}
	if s.state.LastPointer != "" && time.Since(s.state.LastActionAt) < 4*time.Second {
		text(img, 744, 548, s.state.LastPointer, 14, color.RGBA{116, 132, 150, 255})
	}

	for _, button := range s.buttons {
		active := button.ID == s.hoverID
		buttonColor := color.RGBA{38, 106, 170, 255}
		if button.ID == "copy" {
			buttonColor = color.RGBA{22, 145, 126, 255}
		}
		if button.ID == "clear" {
			buttonColor = color.RGBA{175, 69, 82, 255}
		}
		if active {
			buttonColor = lighten(buttonColor, 24)
		}
		fill(img, button.Rect, buttonColor)
		stroke(img, button.Rect, lighten(buttonColor, 44))
		text(img, button.Rect.Min.X+24, button.Rect.Min.Y+56, button.Label, 22, color.White)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return false, err
	}
	hash := sha1.Sum(buf.Bytes())
	if s.hasPanelHash && hash == s.lastPanelHash {
		return false, nil
	}
	file, err := os.Create(s.panelPath)
	if err != nil {
		return false, err
	}
	if _, err := file.Write(buf.Bytes()); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	s.lastPanelHash = hash
	s.hasPanelHash = true
	return true, nil
}

func (s *overlaySession) setOverlayImage(handle uint64, path string) error {
	if s.table[fnSetOverlayFromFile] == 0 || handle == 0 {
		return nil
	}
	filePath, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	overlayErr, _, _ := syscall.SyscallN(
		s.table[fnSetOverlayFromFile],
		uintptr(handle),
		uintptr(unsafe.Pointer(filePath)),
	)
	if overlayErr != 0 {
		return fmt.Errorf("OpenVR SetOverlayFromFile failed: %d", overlayErr)
	}
	return nil
}

func (s *overlaySession) buttonAt(x, y float32) string {
	px, py := overlayPoint(x, y)
	for _, button := range s.buttons {
		if image.Pt(px, py).In(button.Rect) {
			return button.ID
		}
	}
	return ""
}

func overlayPoint(x, y float32) (int, int) {
	px := int(x)
	py := int(y)
	if x >= 0 && x <= 1 && y >= 0 && y <= 1 {
		px = int(x * overlayWidth)
		py = int(y * overlayHeight)
	}
	if py < 0 {
		py = 0
	}
	if py > overlayHeight {
		py = overlayHeight
	}
	return px, py
}

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(img, r, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

func stroke(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+2), c)
	fill(img, image.Rect(r.Min.X, r.Max.Y-2, r.Max.X, r.Max.Y), c)
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+2, r.Max.Y), c)
	fill(img, image.Rect(r.Max.X-2, r.Min.Y, r.Max.X, r.Max.Y), c)
}

func text(img *image.RGBA, x, y int, value string, size float64, c color.Color) {
	face := textFace(size)
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(value)
}

func textFace(size float64) font.Face {
	if overlayFont == nil {
		parsed, err := opentype.Parse(goregular.TTF)
		if err == nil {
			overlayFont = parsed
		}
	}
	if overlayFont != nil {
		face, err := opentype.NewFace(overlayFont, &opentype.FaceOptions{
			Size:    size,
			DPI:     72,
			Hinting: font.HintingFull,
		})
		if err == nil {
			return face
		}
	}
	return basicFallbackFace()
}

func basicFallbackFace() font.Face {
	face, _ := opentype.NewFace(mustFallbackFont(), &opentype.FaceOptions{Size: 16, DPI: 72})
	return face
}

func mustFallbackFont() *opentype.Font {
	if overlayFont != nil {
		return overlayFont
	}
	parsed, _ := opentype.Parse(goregular.TTF)
	overlayFont = parsed
	return overlayFont
}

func drawPreview(dst *image.RGBA, r image.Rectangle, src image.Image) {
	b := src.Bounds()
	if b.Empty() {
		return
	}
	scale := math.Min(float64(r.Dx())/float64(b.Dx()), float64(r.Dy())/float64(b.Dy()))
	w := int(float64(b.Dx()) * scale)
	h := int(float64(b.Dy()) * scale)
	target := image.Rect(0, 0, w, h).Add(image.Pt(r.Min.X+(r.Dx()-w)/2, r.Min.Y+(r.Dy()-h)/2))
	xdraw.BiLinear.Scale(dst, target, src, b, draw.Over, nil)
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

func elide(value string, max int) string {
	if value == "" {
		return "No URL yet"
	}
	if len(value) <= max {
		return value
	}
	if max < 12 {
		return value[:max]
	}
	return value[:max/2] + "..." + value[len(value)-(max/2):]
}

func shortClock(t time.Time) string {
	if t.IsZero() {
		return "--:--"
	}
	return t.Format("15:04:05")
}

func openVRDLLPath() (string, error) {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Steam", "steamapps", "common", "SteamVR", "bin", "win64", "openvr_api.dll"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Steam", "openvr_api.dll"),
		filepath.Join(os.Getenv("ProgramFiles"), "Steam", "openvr_api.dll"),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("openvr_api.dll"); err == nil {
		return path, nil
	}
	return "", errors.New("openvr_api.dll was not found")
}

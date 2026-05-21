package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"imagepadserver/internal/appicon"
	"imagepadserver/internal/clipboard"
	"imagepadserver/internal/config"
	"imagepadserver/internal/imageproc"
	"imagepadserver/internal/library"
	"imagepadserver/internal/network"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/steamvr"
	"imagepadserver/internal/upnp"
)

type Server struct {
	cfg   config.Config
	store *library.Store

	mu             sync.RWMutex
	upnp           upnp.Result
	tmpl           *template.Template
	lanURL         string
	imageURLBase   string
	previewURLBase string
	tunnelStatus   map[string]interface{}
	tunnelURLBase  string
}

func New(cfg config.Config, store *library.Store, imageURLBase string) *Server {
	lanURL := cfg.URLForHost(network.BestLANIP())
	if imageURLBase == "" {
		imageURLBase = lanURL
	}
	return &Server{
		cfg:            cfg,
		store:          store,
		upnp:           upnp.Result{Message: "Checking router UPnP support..."},
		tmpl:           template.Must(template.New("index").Parse(indexHTML)),
		lanURL:         lanURL,
		imageURLBase:   imageURLBase,
		previewURLBase: lanURL,
		tunnelStatus:   map[string]interface{}{"ok": false, "message": "Cloudflare Tunnel starting..."},
	}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/upload-url", s.handleUploadURL)
	mux.HandleFunc("/api/clear", s.handleClear)
	mux.HandleFunc("/api/copy-url", s.handleCopyURL)
	mux.HandleFunc("/api/steamvr", s.handleSteamVR)
	mux.HandleFunc("/qr/phone.png", s.handlePhoneQR)
	mux.HandleFunc("/image/current", s.handleCurrentImage)
	mux.HandleFunc("/image/current.png", s.handleCurrentImage)
	mux.HandleFunc("/image/current.jpg", s.handleCurrentImage)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/healthz", s.handleHealth)
}

func (s *Server) SetUPnPResult(result upnp.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upnp = result
}

func (s *Server) SetTunnelStatus(ok bool, baseURL, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.tunnelURLBase = strings.TrimRight(baseURL, "/") + "/"
	} else {
		s.tunnelURLBase = ""
	}
	s.tunnelStatus = map[string]interface{}{
		"ok":      ok,
		"url":     s.tunnelURLBase,
		"message": message,
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := s.state(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.Execute(w, data)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.state(r))
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "failed to parse upload", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "image field is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	state, err := s.processAndPublish(r, file, header.Filename, optionsFromValues(r.FormValue))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, state)
}

func (s *Server) handleUploadURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL          string `json:"url"`
		Format       string `json:"format"`
		Quality      string `json:"quality"`
		MaxDimension string `json:"maxDimension"`
		MaxMB        string `json:"maxMB"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid URL upload request", http.StatusBadRequest)
		return
	}

	values := map[string]string{
		"format":       req.Format,
		"quality":      req.Quality,
		"maxDimension": req.MaxDimension,
		"maxMB":        req.MaxMB,
	}
	opts := optionsFromValues(func(key string) string { return values[key] })
	remote, name, err := downloadRemoteImage(req.URL, opts.MaxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer remote.Close()

	state, err := s.processAndPublish(r, remote, name, opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, state)
}

func (s *Server) processAndPublish(r *http.Request, reader io.Reader, name string, opts imageproc.Options) (map[string]interface{}, error) {
	result, err := imageproc.Process(reader, name, s.store.Dir(), opts)
	if err != nil {
		return nil, err
	}
	info := library.CurrentImage{
		PublicName:   result.PublicName,
		ContentType:  result.ContentType,
		Width:        result.Width,
		Height:       result.Height,
		OriginalName: name,
	}
	if err := s.store.SetCurrent(result.Path, info); err != nil {
		return nil, fmt.Errorf("failed to save image")
	}
	_ = os.Remove(result.Path)

	state := s.state(r)
	copiedURL := urlForClipboard(state)
	clipboardCopied := false
	if copiedURL != "" {
		if err := clipboard.CopyText(copiedURL); err == nil {
			clipboardCopied = true
		}
	}
	state["copiedURL"] = copiedURL
	state["clipboardCopied"] = clipboardCopied
	return state, nil
}

func optionsFromValues(value func(string) string) imageproc.Options {
	opts := imageproc.DefaultOptions()
	if v := value("format"); v != "" {
		opts.Format = v
	}
	if v := value("quality"); v != "" {
		if q, err := strconv.Atoi(v); err == nil {
			opts.JPEGQuality = q
		}
	}
	if v := value("maxDimension"); v != "" {
		if maxDim, err := strconv.Atoi(v); err == nil {
			opts.MaxDimension = maxDim
		}
	}
	if v := value("maxMB"); v != "" {
		if maxMB, err := strconv.Atoi(v); err == nil && maxMB > 0 {
			if maxMB > 30 {
				maxMB = 30
			}
			opts.MaxBytes = int64(maxMB) << 20
		}
	}
	return opts
}

func downloadRemoteImage(rawURL string, maxBytes int64) (io.ReadCloser, string, error) {
	parsed, err := validatePublicURL(rawURL)
	if err != nil {
		return nil, "", err
	}
	if maxBytes <= 0 || maxBytes > 30<<20 {
		maxBytes = 30 << 20
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			_, err := validatePublicURL(req.URL.String())
			return err
		},
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}

	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	req.Header.Set("Accept", "image/webp,image/svg+xml,image/png,image/jpeg,image/gif,image/bmp,image/tiff,image/*;q=0.8,*/*;q=0.2")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download failed: %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return nil, "", fmt.Errorf("remote image exceeds size limit of %d bytes", maxBytes)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !remoteContentTypeAllowed(ct) {
		return nil, "", fmt.Errorf("remote content is not an image: %s", ct)
	}

	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("remote image exceeds size limit of %d bytes", maxBytes)
	}
	name := remoteFileName(resp.Request.URL, resp.Header.Get("Content-Type"))
	return io.NopCloser(bytes.NewReader(data)), name, nil
}

func validatePublicURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid image URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https image URLs are allowed")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("invalid image URL host")
	}
	if isBlockedHost(host) {
		return nil, fmt.Errorf("local or private network URLs are not allowed")
	}
	return parsed, nil
}

func isBlockedHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return true
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return true
		}
	}
	return false
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 10:
			return true
		case v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31:
			return true
		case v4[0] == 192 && v4[1] == 168:
			return true
		case v4[0] == 169 && v4[1] == 254:
			return true
		}
		return false
	}
	return ip.IsPrivate()
}

func remoteContentTypeAllowed(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return strings.HasPrefix(mediaType, "image/") || mediaType == "application/octet-stream"
}

func remoteFileName(u *url.URL, contentType string) string {
	name := filepath.Base(u.Path)
	if name == "." || name == "/" || name == "" {
		name = "remote-image"
	}
	if filepath.Ext(name) != "" {
		return name
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(mediaType) {
	case "image/jpeg":
		return name + ".jpg"
	case "image/png":
		return name + ".png"
	case "image/gif":
		return name + ".gif"
	case "image/webp":
		return name + ".webp"
	case "image/bmp":
		return name + ".bmp"
	case "image/tiff":
		return name + ".tiff"
	case "image/svg+xml":
		return name + ".svg"
	default:
		return name
	}
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.store.Clear(); err != nil {
		http.Error(w, "failed to clear image", http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.state(r))
}

func (s *Server) handleCopyURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid copy request", http.StatusBadRequest)
		return
	}

	state := s.state(r)
	copiedURL := urlForCopyTarget(state, req.Target)
	if copiedURL == "" {
		http.Error(w, "no URL available to copy", http.StatusBadRequest)
		return
	}

	clipboardCopied := clipboard.CopyText(copiedURL) == nil
	writeJSON(w, map[string]interface{}{
		"copiedURL":         copiedURL,
		"pcClipboardCopied": clipboardCopied,
	})
}

func (s *Server) handleSteamVR(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, steamvr.Registration())
	case http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid SteamVR request", http.StatusBadRequest)
			return
		}
		status, err := steamvr.SetRegistration(req.Enabled)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, status)
			return
		}
		appSettings, loadErr := settings.Load()
		if loadErr == nil {
			appSettings.SteamVRExplicitlyDisabled = !req.Enabled
			_ = settings.Save(appSettings)
		}
		writeJSON(w, status)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePhoneQR(w http.ResponseWriter, r *http.Request) {
	png, err := qrcode.Encode(s.lanURL, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, "failed to generate QR", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(appicon.IconICO)
}

func (s *Server) handleCurrentImage(w http.ResponseWriter, r *http.Request) {
	path, img, ok := s.store.CurrentPath()
	if !ok {
		s.serveDeletedImage(w, r)
		return
	}
	if requestedID := r.URL.Query().Get("v"); requestedID != "" && requestedID != img.ID {
		s.serveDeletedImage(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		s.serveDeletedImage(w, r)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", img.ContentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeFileName(img.PublicName)))
	if r.URL.Query().Get("preview") != "1" {
		s.recordImageRequest(r)
	}
	http.ServeContent(w, r, img.PublicName, img.UpdatedAt, file)
}

func (s *Server) serveDeletedImage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("preview") != "1" {
		s.recordImageRequest(r)
	}
	contentType := deletedContentType(r.URL.Path)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", `inline; filename="deleted.jpg"`)
	if contentType == "image/png" {
		_ = png.Encode(w, deletedImage())
		return
	}
	_ = jpeg.Encode(w, deletedImage(), &jpeg.Options{Quality: 90})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) state(r *http.Request) map[string]interface{} {
	s.mu.RLock()
	upnpResult := s.upnp
	tunnelURLBase := s.tunnelURLBase
	tunnelStatus := s.tunnelStatus
	s.mu.RUnlock()

	localImageURL := ""
	imageURLBase := s.imageURLBase
	if tunnelURLBase != "" {
		imageURLBase = tunnelURLBase
	}
	imageURL := ""
	previewImageURL := ""
	publicImageURL := ""
	if current := s.store.Current(); current != nil {
		imagePath := imageURLPath(current)
		localImageURL = s.previewURLBase + imagePath + "?v=" + current.ID
		if tunnelURLBase != "" {
			imageURL = imageURLBase + imagePath + "?v=" + current.ID
		}
		previewImageURL = s.previewURLBase + imagePath + "?v=" + current.ID
		if tunnelURLBase != "" {
			publicImageURL = tunnelURLBase + imagePath + "?v=" + current.ID
		}
	}
	if imageURL == "" {
		imageURL = ""
	}

	return map[string]interface{}{
		"appName":         "ImagePadServer",
		"phoneURL":        s.lanURL,
		"imageURL":        imageURL,
		"publicImageURL":  publicImageURL,
		"localImageURL":   localImageURL,
		"previewImageURL": previewImageURL,
		"qrURL":           "/qr/phone.png",
		"upnp":            upnpResult,
		"tunnel":          tunnelStatus,
		"current":         s.store.Current(),
		"remoteAddr":      r.RemoteAddr,
	}
}

func (s *Server) recordImageRequest(r *http.Request) {
	line := fmt.Sprintf("%s\t%s\t%s\t%s\n",
		time.Now().Format(time.RFC3339),
		r.RemoteAddr,
		r.URL.RequestURI(),
		strings.ReplaceAll(r.UserAgent(), "\t", " "),
	)
	go appendAccessLog(line)
}

func appendAccessLog(line string) {
	logPath := filepath.Join(settings.Dir(), "image-access.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return
	}
	file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line)
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func urlForClipboard(state map[string]interface{}) string {
	return urlForCopyTarget(state, "imageURL")
}

func urlForCopyTarget(state map[string]interface{}, target string) string {
	switch target {
	case "phoneURL", "phoneURLMobile":
		if phoneURL, ok := state["phoneURL"].(string); ok {
			return phoneURL
		}
	case "localImageURL":
		if localURL, ok := state["localImageURL"].(string); ok {
			return localURL
		}
	case "publicImageURL":
		if publicURL, ok := state["publicImageURL"].(string); ok {
			return publicURL
		}
	default:
		if imageURL, ok := state["imageURL"].(string); ok && strings.HasPrefix(imageURL, "http") {
			return imageURL
		}
		if publicURL, ok := state["publicImageURL"].(string); ok && publicURL != "" {
			return publicURL
		}
		if localURL, ok := state["localImageURL"].(string); ok {
			return localURL
		}
	}
	return ""
}

func safeFileName(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, `\`, "")
	if name == "" {
		return "current"
	}
	return name
}

func imageURLPath(img *library.CurrentImage) string {
	if img == nil {
		return "image/current"
	}
	switch img.ContentType {
	case "image/png":
		return "image/current.png"
	case "image/jpeg":
		return "image/current.jpg"
	default:
		ext := strings.ToLower(filepath.Ext(img.PublicName))
		if ext == ".png" {
			return "image/current.png"
		}
		if ext == ".jpg" || ext == ".jpeg" {
			return "image/current.jpg"
		}
		return "image/current"
	}
}

func deletedContentType(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".png") {
		return "image/png"
	}
	return "image/jpeg"
}

func deletedImage() image.Image {
	const width = 1024
	const height = 1024
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, 0, 0, width, height, color.RGBA{R: 18, G: 23, B: 28, A: 255})
	fillRect(img, 0, 444, width, 580, color.RGBA{R: 180, G: 48, B: 48, A: 255})
	drawBlockText(img, "IMAGE", 282, 294, 16, color.RGBA{R: 176, G: 188, B: 198, A: 255})
	drawBlockText(img, "DELETED", 166, 462, 20, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	drawBlockText(img, "CLEARED", 214, 640, 14, color.RGBA{R: 176, G: 188, B: 198, A: 255})
	return img
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func drawBlockText(img *image.RGBA, text string, x, y, scale int, c color.RGBA) {
	cursor := x
	for _, r := range text {
		if r == ' ' {
			cursor += 4 * scale
			continue
		}
		glyph, ok := blockGlyphs[r]
		if !ok {
			cursor += 4 * scale
			continue
		}
		for row, bits := range glyph {
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) == 0 {
					continue
				}
				fillRect(img, cursor+col*scale, y+row*scale, cursor+(col+1)*scale, y+(row+1)*scale, c)
			}
		}
		cursor += 6 * scale
	}
}

var blockGlyphs = map[rune][7]byte{
	'A': {0x0e, 0x11, 0x11, 0x1f, 0x11, 0x11, 0x11},
	'C': {0x0f, 0x10, 0x10, 0x10, 0x10, 0x10, 0x0f},
	'D': {0x1e, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1e},
	'E': {0x1f, 0x10, 0x10, 0x1e, 0x10, 0x10, 0x1f},
	'G': {0x0f, 0x10, 0x10, 0x13, 0x11, 0x11, 0x0f},
	'I': {0x1f, 0x04, 0x04, 0x04, 0x04, 0x04, 0x1f},
	'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1f},
	'M': {0x11, 0x1b, 0x15, 0x15, 0x11, 0x11, 0x11},
	'R': {0x1e, 0x11, 0x11, 0x1e, 0x14, 0x12, 0x11},
	'T': {0x1f, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
}

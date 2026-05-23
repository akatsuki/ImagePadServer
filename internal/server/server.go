package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log"
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

	"imagepadserver/internal/about"
	"imagepadserver/internal/appicon"
	"imagepadserver/internal/clipboard"
	"imagepadserver/internal/config"
	"imagepadserver/internal/imageproc"
	"imagepadserver/internal/library"
	"imagepadserver/internal/network"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/upnp"
	"imagepadserver/internal/video"
)

const (
	// maxMultipartMemory is kept low so large uploads spill to temp files instead of RAM.
	maxMultipartMemory  = 32 << 20
	maxVideoUploadBytes = 2 << 30 // matches yt-dlp --max-filesize 2G
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
	adminToken     string
}

func New(cfg config.Config, store *library.Store, imageURLBase string) *Server {
	advertisedHost := cfg.AdvertisedHost(network.BestReachableIP(cfg.PreferTailscale))
	lanURL := cfg.URLForHost(advertisedHost)
	if imageURLBase == "" {
		imageURLBase = lanURL
	}
	adminToken, err := settings.EnsureAdminToken()
	if err != nil {
		adminToken = ""
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
		adminToken:     adminToken,
	}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", s.admin(s.handleIndex))
	mux.HandleFunc("/api/state", s.admin(s.handleState))
	mux.HandleFunc("/api/upload", s.admin(s.handleUpload))
	mux.HandleFunc("/api/upload-url", s.admin(s.handleUploadURL))
	mux.HandleFunc("/api/clear", s.admin(s.handleClear))
	mux.HandleFunc("/api/copy-url", s.admin(s.handleCopyURL))
	mux.HandleFunc("/api/about", s.admin(s.handleAbout))
	mux.HandleFunc("/api/update-check", s.admin(s.handleUpdateCheck))
	// SteamVR integration is frozen indefinitely. Keep internal/steamvr as an
	// archived asset, but do not expose its management API.
	mux.HandleFunc("/api/video-player", s.admin(s.handleVideoPlayer))
	mux.HandleFunc("/api/video-quality", s.admin(s.handleVideoQuality))
	mux.HandleFunc("/api/network-check", s.admin(s.handleNetworkCheck))
	mux.HandleFunc("/qr/phone.png", s.admin(s.handlePhoneQR))
	mux.HandleFunc("/image/current", s.handleCurrentImage)
	mux.HandleFunc("/image/current.png", s.handleCurrentImage)
	mux.HandleFunc("/image/current.jpg", s.handleCurrentImage)
	mux.HandleFunc("/video/current.mp4", s.handleCurrentVideo)
	mux.HandleFunc("/stream/current.m3u8", s.handleCurrentHLS)
	mux.HandleFunc("/stream/", s.handleStream)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/healthz", s.handleHealth)
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminAllowed(r) {
			http.Error(w, "admin access requires the local app or the password QR link", http.StatusForbidden)
			return
		}
		s.rememberAdminToken(w, r)
		next(w, r)
	}
}

func (s *Server) adminAllowed(r *http.Request) bool {
	if isLoopbackRequest(r) && isLocalAdminHost(r.Host) {
		return true
	}
	if !isPrivateRemoteRequest(r) || !isAllowedAdminHost(r.Host) {
		return false
	}
	return s.validAdminToken(r)
}

func (s *Server) validAdminToken(r *http.Request) bool {
	if s.adminToken == "" {
		return false
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-ImagePad-Token")
	}
	if token == "" {
		if cookie, err := r.Cookie("imagepad_admin"); err == nil {
			token = cookie.Value
		}
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.adminToken)) == 1
}

func (s *Server) rememberAdminToken(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" || r.URL.Query().Get("token") == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "imagepad_admin",
		Value:    s.adminToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 365,
	})
}

func isLoopbackRequest(r *http.Request) bool {
	ip := remoteIP(r)
	return ip != nil && ip.IsLoopback()
}

func isLocalAdminHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || host == "0.0.0.0" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isAllowedAdminHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return !strings.Contains(host, ".")
	}
	return isAllowedAdminIP(ip)
}

func isPrivateRemoteRequest(r *http.Request) bool {
	ip := remoteIP(r)
	return isAllowedAdminIP(ip)
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func isAllowedAdminIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

func (s *Server) SetUPnPResult(result upnp.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upnp = result
}

func (s *Server) SetPublicNetworkMessage(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upnp = upnp.Result{Message: message}
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
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("handleState panic: %v", recovered)
			http.Error(w, "failed to read application state", http.StatusInternalServerError)
		}
	}()
	writeJSON(w, s.state(r))
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(uploadMemoryLimit(s.videoPlayerEnabled())); err != nil {
		http.Error(w, "failed to parse upload", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "image field is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	state, err := s.processAndPublish(r, file, header.Filename, header.Header.Get("Content-Type"), optionsFromValues(r.FormValue))
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
	if s.videoPlayerEnabled() {
		if err := validateHTTPURL(req.URL); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.clearPublication()
		sourcePath, name, err := video.DownloadURL(req.URL, s.store.Dir())
		if err != nil {
			http.Error(w, videoURLDownloadError(err), http.StatusBadRequest)
			return
		}
		state, err := s.processVideoFileAndPublish(r, sourcePath, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, state)
		return
	}
	remote, name, err := downloadRemoteImage(req.URL, opts.MaxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer remote.Close()

	state, err := s.processAndPublish(r, remote, name, "", opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, state)
}

func (s *Server) processAndPublish(r *http.Request, reader io.Reader, name, contentType string, opts imageproc.Options) (map[string]interface{}, error) {
	if s.videoPlayerEnabled() && isVideoUpload(name, contentType) {
		return s.processVideoAndPublish(r, reader, name)
	}

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
	if s.videoPlayerEnabled() {
		if imagePath, current, ok := s.store.CurrentPath(); ok {
			video.PublishStillImageAsyncForID(imagePath, s.store.Dir(), current.ID, s.videoQualityPreset())
		}
	} else {
		video.RemoveGenerated(s.store.Dir())
	}

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

func (s *Server) processVideoAndPublish(r *http.Request, reader io.Reader, name string) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
		return nil, err
	}
	s.clearPublication()

	sourcePath := filepath.Join(s.store.Dir(), "source"+safeVideoExt(name))
	source, err := os.Create(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to save video upload")
	}
	written, err := io.Copy(source, io.LimitReader(reader, maxVideoUploadBytes+1))
	if err != nil {
		_ = source.Close()
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("failed to save video upload")
	}
	if err := source.Close(); err != nil {
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("failed to save video upload")
	}
	if written > maxVideoUploadBytes {
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("video exceeds size limit of %d bytes", maxVideoUploadBytes)
	}
	return s.processVideoFileAndPublish(r, sourcePath, name)
}

func (s *Server) processVideoFileAndPublish(r *http.Request, sourcePath, name string) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
		return nil, err
	}
	info := library.CurrentImage{
		Kind:         "video",
		FileName:     filepath.Base(sourcePath),
		PublicName:   "current-video" + filepath.Ext(sourcePath),
		ContentType:  videoContentType(sourcePath),
		OriginalName: name,
	}
	if stat, err := os.Stat(sourcePath); err == nil {
		info.SizeBytes = stat.Size()
	}
	if err := s.store.SetCurrentInfo(info); err != nil {
		return nil, fmt.Errorf("failed to save video")
	}
	current := s.store.Current()
	currentID := ""
	if current != nil {
		currentID = current.ID
	}
	video.PublishUploadedVideoAsyncForID(sourcePath, s.store.Dir(), currentID, s.videoQualityPreset())

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

func (s *Server) clearPublication() {
	video.RemoveGenerated(s.store.Dir())
	_ = s.store.Clear()
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

func uploadMemoryLimit(_ bool) int64 {
	return maxMultipartMemory
}

func videoURLDownloadError(err error) string {
	if err == nil {
		return "動画URLの取得に失敗しました"
	}
	return fmt.Sprintf(
		"動画URLの取得に失敗しました: %v。yt-dlp で取得できないURLの場合は動画ファイルを直接アップロードするか、ビデオプレーヤーモードをオフにして画像URLとして指定してください。",
		err,
	)
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
	parsed, err := validateRemoteHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func validateHTTPURL(rawURL string) error {
	_, err := validateRemoteHTTPURL(rawURL)
	return err
}

func validateRemoteHTTPURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https URLs are allowed")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("invalid URL host")
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
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
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
	video.RemoveGenerated(s.store.Dir())
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

func (s *Server) handleVideoPlayer(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.videoPlayerState())
	case http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid video player request", http.StatusBadRequest)
			return
		}
		if req.Enabled {
			if _, err := video.EnsureFFmpeg(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.VideoPlayerEnabled = req.Enabled
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		if req.Enabled {
			if imagePath, current, ok := s.store.CurrentPath(); ok {
				video.PublishStillImageAsyncForID(imagePath, s.store.Dir(), current.ID, s.videoQualityPreset())
			}
		} else {
			video.RemoveGenerated(s.store.Dir())
		}
		writeJSON(w, s.videoPlayerState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVideoQuality(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.videoQualityState())
	case http.MethodPost:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid video quality request", http.StatusBadRequest)
			return
		}
		mode := normalizeQualityMode(req.Mode)
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.VideoQualityMode = mode
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		writeJSON(w, s.videoQualityState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleNetworkCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	measurement := video.MeasureNetwork()
	_ = settings.Update(func(appSettings *settings.Settings) error {
		appSettings.NetworkUploadMbps = measurement.UploadMbps
		return nil
	})
	writeJSON(w, s.videoQualityState())
}

func (s *Server) handlePhoneQR(w http.ResponseWriter, r *http.Request) {
	png, err := qrcode.Encode(s.adminURL(s.lanURL), qrcode.Medium, 512)
	if err != nil {
		http.Error(w, "failed to generate QR", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) adminURL(baseURL string) string {
	if s.adminToken == "" {
		return baseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	query := parsed.Query()
	query.Set("token", s.adminToken)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Server) adminPath(path string) string {
	if s.adminToken == "" {
		return path
	}
	if strings.Contains(path, "?") {
		return path + "&token=" + url.QueryEscape(s.adminToken)
	}
	return path + "?token=" + url.QueryEscape(s.adminToken)
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(appicon.IconICO)
}

func (s *Server) handleCurrentImage(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	path, img, ok := s.store.CurrentPath()
	if !ok {
		s.serveDeletedImage(w, r)
		return
	}
	if img.Kind == "video" {
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

func (s *Server) handleCurrentVideo(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	if !s.videoPlayerEnabled() {
		http.NotFound(w, r)
		return
	}
	img := s.store.Current()
	if img == nil {
		http.NotFound(w, r)
		return
	}
	if requestedID := r.URL.Query().Get("v"); requestedID != "" && requestedID != img.ID {
		http.NotFound(w, r)
		return
	}
	if img.Kind == "video" {
		path, current, ok := s.store.CurrentPath()
		if !ok {
			http.NotFound(w, r)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		contentType := current.ContentType
		if contentType == "" {
			contentType = videoContentType(current.FileName)
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeFileName(current.PublicName)))
		s.recordImageRequest(r)
		http.ServeContent(w, r, current.PublicName, current.UpdatedAt, file)
		return
	}
	s.serveGeneratedFile(w, r, video.MP4File, "video/mp4", "current.mp4", img.UpdatedAt)
}

func (s *Server) handleCurrentHLS(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	if !s.videoPlayerEnabled() {
		http.NotFound(w, r)
		return
	}
	img := s.store.Current()
	if img == nil {
		http.NotFound(w, r)
		return
	}
	if requestedID := streamRequestID(r); requestedID != "" && requestedID != img.ID {
		http.NotFound(w, r)
		return
	}
	s.serveGeneratedFile(w, r, video.PlaylistName(img.ID), "application/vnd.apple.mpegurl", "current.m3u8", img.UpdatedAt)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(filepath.Base(r.URL.Path), ".m3u8") {
		s.handleCurrentHLS(w, r)
		return
	}
	s.handleCurrentHLSSegment(w, r)
}

func (s *Server) handleCurrentHLSSegment(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	if !s.videoPlayerEnabled() {
		http.NotFound(w, r)
		return
	}
	img := s.store.Current()
	if img == nil {
		http.NotFound(w, r)
		return
	}
	if requestedID := streamRequestID(r); requestedID != "" && requestedID != img.ID {
		http.NotFound(w, r)
		return
	}
	fileName := filepath.Base(r.URL.Path)
	if !isHLSSegmentName(fileName) {
		http.NotFound(w, r)
		return
	}
	s.serveGeneratedFile(w, r, fileName, "video/mp2t", fileName, img.UpdatedAt)
}

func publicReadAllowed(r *http.Request) bool {
	return isAllowedAdminIP(remoteIP(r))
}

func (s *Server) serveGeneratedFile(w http.ResponseWriter, r *http.Request, fileName, contentType, publicName string, modTime time.Time) {
	path := filepath.Join(s.store.Dir(), fileName)
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeFileName(publicName)))
	s.recordImageRequest(r)
	http.ServeContent(w, r, publicName, modTime, file)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"appName":     about.AppName,
		"version":     about.Version,
		"author":      about.Author,
		"license":     about.License,
		"copyright":   about.Copyright,
		"description": about.Description,
		"openSource":  about.OpenSourceNotices,
	})
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/akatsuki/ImagePadServer/releases/latest", nil)
	if err != nil {
		http.Error(w, "failed to create update request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", about.AppName+"/"+about.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"ok":      false,
			"current": about.Version,
			"message": "更新確認に失敗しました",
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, map[string]interface{}{
			"ok":      false,
			"current": about.Version,
			"message": fmt.Sprintf("更新確認に失敗しました: HTTP %d", resp.StatusCode),
		})
		return
	}
	var latest struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&latest); err != nil {
		writeJSON(w, map[string]interface{}{
			"ok":      false,
			"current": about.Version,
			"message": "更新情報を読めませんでした",
		})
		return
	}
	newer := versionGreater(latest.TagName, about.Version)
	message := "最新版です"
	if newer {
		message = "新しいバージョンがあります"
	}
	writeJSON(w, map[string]interface{}{
		"ok":      true,
		"current": about.Version,
		"latest":  latest.TagName,
		"name":    latest.Name,
		"url":     latest.HTMLURL,
		"newer":   newer,
		"message": message,
	})
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
	videoURL := ""
	hlsURL := ""
	previewImageURL := ""
	publicImageURL := ""
	publicVideoURL := ""
	publicHLSURL := ""
	videoPlayer := s.videoPlayerState()
	if current := s.store.Current(); current != nil {
		if current.Kind != "video" {
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
		videoEnabled, _ := videoPlayer["enabled"].(bool)
		videoStatus, _ := videoPlayer["status"].(video.Result)
		if videoEnabled && tunnelURLBase != "" && current.Kind == "video" && videoStatus.MP4 {
			videoURL = imageURLBase + "video/current.mp4?v=" + current.ID
			publicVideoURL = tunnelURLBase + "video/current.mp4?v=" + current.ID
		}
		if videoEnabled && tunnelURLBase != "" && (videoStatus.HLS || videoStatus.Active || current.Kind != "video") {
			streamPath := hlsURLPath(current.ID)
			hlsURL = imageURLBase + streamPath
			publicHLSURL = tunnelURLBase + streamPath
		}
	}
	if imageURL == "" {
		imageURL = ""
	}
	shareURL, shareURLLabel := primaryShareURL(map[string]interface{}{
		"imageURL":      imageURL,
		"videoURL":      videoURL,
		"hlsURL":        hlsURL,
		"localImageURL": localImageURL,
		"videoPlayer":   videoPlayer,
	})

	return map[string]interface{}{
		"appName":         about.AppName,
		"version":         about.Version,
		"author":          about.Author,
		"license":         about.License,
		"copyright":       about.Copyright,
		"openSource":      about.OpenSourceNotices,
		"phoneURL":        s.adminURL(s.lanURL),
		"imageURL":        imageURL,
		"videoURL":        videoURL,
		"hlsURL":          hlsURL,
		"shareURL":        shareURL,
		"shareURLLabel":   shareURLLabel,
		"publicImageURL":  publicImageURL,
		"publicVideoURL":  publicVideoURL,
		"publicHLSURL":    publicHLSURL,
		"localImageURL":   localImageURL,
		"previewImageURL": previewImageURL,
		"qrURL":           s.adminPath("/qr/phone.png"),
		"upnp":            upnpResult,
		"tunnel":          tunnelStatus,
		"video":           videoPlayer["status"],
		"videoPlayer":     videoPlayer,
		"videoQuality":    s.videoQualityState(),
		"current":         s.store.Current(),
		"remoteAddr":      r.RemoteAddr,
	}
}

func (s *Server) videoPlayerEnabled() bool {
	appSettings, err := settings.Load()
	if err != nil {
		return false
	}
	return appSettings.VideoPlayerEnabled
}

func (s *Server) videoPlayerState() map[string]interface{} {
	enabled := s.videoPlayerEnabled()
	status := video.CurrentStatus(s.store.Dir())
	if !enabled {
		status = video.Result{Message: "VRChat video player support is disabled."}
	}
	return map[string]interface{}{
		"enabled": enabled,
		"status":  status,
		"quality": s.videoQualityPreset(),
	}
}

func (s *Server) videoQualityPreset() video.QualityPreset {
	appSettings, err := settings.Load()
	if err != nil {
		return video.ResolveQuality("auto", 0)
	}
	preset := video.ResolveQualityForUpload(appSettings.VideoQualityMode, appSettings.NetworkMbps, appSettings.NetworkUploadMbps)
	if active, ok := video.ActiveQuality(s.store.Dir()); ok {
		return video.BitrateOnlyPreset(preset, active)
	}
	return preset
}

func (s *Server) videoQualityState() map[string]interface{} {
	preset := s.videoQualityPreset()
	return map[string]interface{}{
		"mode":       preset.Mode,
		"effective":  preset.Effective,
		"height":     preset.Height,
		"uploadMbps": preset.UploadMbps,
		"preset":     preset,
	}
}

func normalizeQualityMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "1080", "720", "360":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "auto"
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

func versionGreater(candidate, current string) bool {
	candidateParts := versionParts(candidate)
	currentParts := versionParts(current)
	for i := 0; i < len(candidateParts) && i < len(currentParts); i++ {
		if candidateParts[i] > currentParts[i] {
			return true
		}
		if candidateParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

func versionParts(version string) [3]int {
	version = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(version)), "v")
	version = strings.Split(version, "-")[0]
	parts := strings.Split(version, ".")
	var result [3]int
	for i := 0; i < len(parts) && i < len(result); i++ {
		n, _ := strconv.Atoi(parts[i])
		result[i] = n
	}
	return result
}

func urlForClipboard(state map[string]interface{}) string {
	if shareURL, _ := state["shareURL"].(string); strings.HasPrefix(shareURL, "http") {
		return shareURL
	}
	shareURL, _ := primaryShareURL(state)
	if strings.HasPrefix(shareURL, "http") {
		return shareURL
	}
	return urlForCopyTarget(state, "imageURL")
}

func primaryShareURL(state map[string]interface{}) (string, string) {
	if videoPlayer, ok := state["videoPlayer"].(map[string]interface{}); ok {
		if enabled, _ := videoPlayer["enabled"].(bool); enabled {
			if hlsURL, ok := state["hlsURL"].(string); ok && strings.HasPrefix(hlsURL, "http") {
				return hlsURL, "HLS URL"
			}
			if videoURL, ok := state["videoURL"].(string); ok && strings.HasPrefix(videoURL, "http") {
				return videoURL, "MP4 URL"
			}
		}
	}
	if imageURL, ok := state["imageURL"].(string); ok && strings.HasPrefix(imageURL, "http") {
		return imageURL, "ImagePad URL"
	}
	if publicURL, ok := state["publicImageURL"].(string); ok && strings.HasPrefix(publicURL, "http") {
		return publicURL, "ImagePad URL"
	}
	if localURL, ok := state["localImageURL"].(string); ok && strings.HasPrefix(localURL, "http") {
		return localURL, "Local URL"
	}
	return "", "URL"
}

func urlForCopyTarget(state map[string]interface{}, target string) string {
	switch target {
	case "shareURL":
		if shareURL, ok := state["shareURL"].(string); ok {
			return shareURL
		}
		shareURL, _ := primaryShareURL(state)
		return shareURL
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
	case "videoURL":
		if videoURL, ok := state["videoURL"].(string); ok {
			return videoURL
		}
	case "hlsURL":
		if hlsURL, ok := state["hlsURL"].(string); ok {
			return hlsURL
		}
	case "publicVideoURL":
		if publicURL, ok := state["publicVideoURL"].(string); ok {
			return publicURL
		}
	case "publicHLSURL":
		if publicURL, ok := state["publicHLSURL"].(string); ok {
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

func isVideoUpload(name, contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(strings.ToLower(mediaType), "video/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi":
		return true
	default:
		return false
	}
}

func safeVideoExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi":
		return strings.ToLower(filepath.Ext(name))
	default:
		return ".mp4"
	}
}

func videoContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	default:
		return "video/mp4"
	}
}

func isHLSSegmentName(name string) bool {
	if !strings.HasPrefix(name, "current") || !strings.HasSuffix(name, ".ts") {
		return false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(name, "current"), ".ts")
	if middle == "" {
		return false
	}
	if !strings.HasPrefix(middle, "-") {
		first := rune(middle[0])
		if first < '0' || first > '9' {
			return false
		}
		for _, r := range middle {
			if (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
		return true
	}
	for _, r := range middle {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func hlsURLPath(id string) string {
	if id == "" {
		return "stream/current.m3u8"
	}
	return "stream/" + url.PathEscape(id) + "/" + video.PlaylistName(id)
}

func streamRequestID(r *http.Request) string {
	if requestedID := r.URL.Query().Get("v"); requestedID != "" {
		return requestedID
	}
	path := strings.TrimPrefix(r.URL.Path, "/stream/")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 && parts[0] != "" {
		if id, err := url.PathUnescape(parts[0]); err == nil {
			return id
		}
		return parts[0]
	}
	return ""
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

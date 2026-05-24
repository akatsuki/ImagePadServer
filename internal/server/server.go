package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"image/jpeg"
	"image/png"
	"io"
	"log"
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

	mu              sync.RWMutex
	upnp            upnp.Result
	tmpl            *template.Template
	lanURL          string
	imageURLBase    string
	previewURLBase  string
	tunnelStatus    map[string]interface{}
	tunnelURLBase   string
	tunnelReconnect chan<- struct{}
	adminToken      string
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
	mux.HandleFunc("/api/tunnel/reconnect", s.admin(s.handleTunnelReconnect))
	mux.HandleFunc("/api/upload", s.admin(s.handleUpload))
	mux.HandleFunc("/api/upload-queue", s.admin(s.handleUploadQueue))
	mux.HandleFunc("/api/upload-url", s.admin(s.handleUploadURL))
	mux.HandleFunc("/api/upload-url-queue", s.admin(s.handleUploadURLQueue))
	mux.HandleFunc("/api/clear", s.admin(s.handleClear))
	mux.HandleFunc("/api/history", s.admin(s.handleHistory))
	mux.HandleFunc("/api/history/favorite", s.admin(s.handleHistoryFavorite))
	mux.HandleFunc("/api/history/queue", s.admin(s.handleHistoryQueue))
	mux.HandleFunc("/api/history/select", s.admin(s.handleHistorySelect))
	mux.HandleFunc("/api/copy-url", s.admin(s.handleCopyURL))
	mux.HandleFunc("/api/about", s.admin(s.handleAbout))
	mux.HandleFunc("/api/update-check", s.admin(s.handleUpdateCheck))
	// SteamVR integration is frozen indefinitely. Keep internal/steamvr as an
	// archived asset, but do not expose its management API.
	mux.HandleFunc("/api/video-player", s.admin(s.handleVideoPlayer))
	mux.HandleFunc("/api/video-quality", s.admin(s.handleVideoQuality))
	mux.HandleFunc("/api/network-check", s.admin(s.handleNetworkCheck))
	mux.HandleFunc("/qr/phone.png", s.admin(s.handlePhoneQR))
	mux.HandleFunc("/history/", s.admin(s.handleHistoryMedia))
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

func (s *Server) SetTunnelReconnect(ch chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tunnelReconnect = ch
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

func (s *Server) handleTunnelReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	reconnect := s.tunnelReconnect
	s.mu.RUnlock()
	if reconnect == nil {
		http.Error(w, "tunnel reconnect unavailable", http.StatusServiceUnavailable)
		return
	}

	select {
	case reconnect <- struct{}{}:
		writeJSON(w, map[string]interface{}{"ok": true, "message": "再接続を要求しました"})
	default:
		writeJSON(w, map[string]interface{}{"ok": true, "message": "再接続要求は保留中です"})
	}
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(uploadMemoryLimit()); err != nil {
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

func (s *Server) handleUploadQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(uploadMemoryLimit()); err != nil {
		http.Error(w, "failed to parse upload", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "image field is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	state, err := s.processAndQueue(r, file, header.Filename, header.Header.Get("Content-Type"), optionsFromValues(r.FormValue))
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

func (s *Server) handleUploadURLQueue(w http.ResponseWriter, r *http.Request) {
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
		sourcePath, name, err := video.DownloadURL(req.URL, s.store.Dir())
		if err != nil {
			http.Error(w, videoURLDownloadError(err), http.StatusBadRequest)
			return
		}
		state, err := s.processVideoFileAndQueue(r, sourcePath, name)
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

	state, err := s.processAndQueue(r, remote, name, "", opts)
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
			s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
		}
	}

	state := s.state(r)
	return s.withClipboardResult(state), nil
}

func (s *Server) processAndQueue(r *http.Request, reader io.Reader, name, contentType string, opts imageproc.Options) (map[string]interface{}, error) {
	if !s.videoPlayerEnabled() {
		return nil, fmt.Errorf("video player support is disabled")
	}
	if isVideoUpload(name, contentType) {
		return s.processVideoAndQueue(r, reader, name)
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
	historyItem, err := s.store.AddHistory(result.Path, info)
	if err != nil {
		return nil, fmt.Errorf("failed to add image to history")
	}
	_ = os.Remove(result.Path)
	if path, _, ok := s.store.HistoryPath(historyItem.ID); ok {
		s.enqueueStillConversion(path, historyItem.ID, historyItem.OriginalName)
	}
	return s.state(r), nil
}

func (s *Server) processVideoAndPublish(r *http.Request, reader io.Reader, name string) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
		return nil, err
	}

	sourcePath := filepath.Join(s.store.Dir(), "source-"+randomSuffix()+safeVideoExt(name))
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

func (s *Server) processVideoAndQueue(r *http.Request, reader io.Reader, name string) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
		return nil, err
	}
	sourcePath := filepath.Join(s.store.Dir(), "queued-source-"+randomSuffix()+safeVideoExt(name))
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
	return s.processVideoFileAndQueue(r, sourcePath, name)
}

func (s *Server) processVideoFileAndPublish(r *http.Request, sourcePath, name string) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
		return nil, err
	}
	thumbnail := s.createVideoThumbnail(sourcePath)
	info := library.CurrentImage{
		Kind:         "video",
		FileName:     filepath.Base(sourcePath),
		PublicName:   "current-video" + filepath.Ext(sourcePath),
		ContentType:  videoContentType(sourcePath),
		OriginalName: name,
		Thumbnail:    thumbnail,
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
	s.enqueueUploadedConversion(sourcePath, currentID, name)

	state := s.state(r)
	return s.withClipboardResult(state), nil
}

func (s *Server) processVideoFileAndQueue(r *http.Request, sourcePath, name string) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
		return nil, err
	}
	thumbnail := s.createVideoThumbnail(sourcePath)
	info := library.CurrentImage{
		Kind:         "video",
		FileName:     filepath.Base(sourcePath),
		PublicName:   "queued-video" + filepath.Ext(sourcePath),
		ContentType:  videoContentType(sourcePath),
		OriginalName: name,
		Thumbnail:    thumbnail,
	}
	if stat, err := os.Stat(sourcePath); err == nil {
		info.SizeBytes = stat.Size()
	}
	historyItem, err := s.store.AddHistory(sourcePath, info)
	if err != nil {
		return nil, fmt.Errorf("failed to add video to history")
	}
	if path, _, ok := s.store.HistoryPath(historyItem.ID); ok {
		s.enqueueUploadedConversion(path, historyItem.ID, historyItem.OriginalName)
	}
	return s.state(r), nil
}

func (s *Server) createVideoThumbnail(sourcePath string) string {
	name := "video-thumb-" + randomSuffix() + ".jpg"
	path := filepath.Join(s.store.Dir(), name)
	if err := video.GenerateThumbnail(sourcePath, path); err != nil {
		_ = os.Remove(path)
		return ""
	}
	return name
}

func (s *Server) withClipboardResult(state map[string]interface{}) map[string]interface{} {
	copiedURL := urlForClipboard(state)
	clipboardCopied := false
	if copiedURL != "" {
		if err := clipboard.CopyText(copiedURL); err == nil {
			clipboardCopied = true
		}
	}
	state["copiedURL"] = copiedURL
	state["clipboardCopied"] = clipboardCopied
	return state
}

func (s *Server) clearPublication() {
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
			if maxMB > 120 {
				maxMB = 120
			}
			opts.MaxBytes = int64(maxMB) << 20
		}
	}
	return opts
}

func uploadMemoryLimit() int64 {
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

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.historyState())
}

func (s *Server) handleHistoryFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID       string `json:"id"`
		Favorite bool   `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "invalid history favorite request", http.StatusBadRequest)
		return
	}
	if err := s.store.SetFavorite(req.ID, req.Favorite); err != nil {
		http.Error(w, "history item not found", http.StatusNotFound)
		return
	}
	writeJSON(w, s.historyState())
}

func (s *Server) handleHistoryQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "invalid history queue request", http.StatusBadRequest)
		return
	}
	if !s.videoPlayerEnabled() {
		http.Error(w, "video player support is disabled", http.StatusBadRequest)
		return
	}
	if err := s.enqueueHistoryItem(req.ID); err != nil {
		http.Error(w, "history item not found", http.StatusNotFound)
		return
	}
	writeJSON(w, s.state(r))
}

func (s *Server) handleHistorySelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "invalid history select request", http.StatusBadRequest)
		return
	}
	if err := s.store.SetCurrentFromHistory(req.ID); err != nil {
		http.Error(w, "history item not found", http.StatusNotFound)
		return
	}
	if path, current, ok := s.store.CurrentPath(); ok && s.videoPlayerEnabled() {
		if current.Kind == "video" {
			s.enqueueUploadedConversion(path, current.ID, current.OriginalName)
		} else {
			s.enqueueStillConversion(path, current.ID, current.OriginalName)
		}
	}
	writeJSON(w, s.state(r))
}

func (s *Server) enqueueHistoryItem(id string) error {
	path, item, ok := s.store.HistoryPath(id)
	if !ok {
		return os.ErrNotExist
	}
	if item.Kind == "video" {
		s.enqueueUploadedConversion(path, item.ID, item.OriginalName)
		return nil
	}
	s.enqueueStillConversion(path, item.ID, item.OriginalName)
	return nil
}

func (s *Server) enqueueStillConversion(path, id, title string) {
	jobID := video.EnqueueStillImageForID(path, s.store.Dir(), id, title, s.videoQualityPreset())
	s.watchConversion(jobID, id)
}

func (s *Server) enqueueUploadedConversion(path, id, title string) {
	jobID := video.EnqueueUploadedVideoForID(path, s.store.Dir(), id, title, s.videoQualityPreset())
	s.watchConversion(jobID, id)
}

func (s *Server) watchConversion(jobID, mediaID string) {
	if jobID == "" || mediaID == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.After(6 * time.Hour)
		for {
			select {
			case <-deadline:
				return
			case <-ticker.C:
				for _, item := range video.QueueStatus(s.store.Dir()) {
					if item.ID != jobID {
						continue
					}
					switch item.Status {
					case "done":
						files := video.GeneratedFiles(s.store.Dir(), mediaID)
						if len(files) > 0 {
							_ = s.store.MarkConverted(mediaID, files)
						}
						return
					case "error", "canceled":
						return
					}
				}
			}
		}
	}()
}

func (s *Server) handleHistoryMedia(w http.ResponseWriter, r *http.Request) {
	pathPart := strings.TrimPrefix(r.URL.Path, "/history/")
	thumbnail := false
	if strings.HasSuffix(pathPart, "/thumbnail") {
		thumbnail = true
		pathPart = strings.TrimSuffix(pathPart, "/thumbnail")
	}
	id, err := url.PathUnescape(strings.Trim(pathPart, "/"))
	if err != nil || id == "" {
		http.NotFound(w, r)
		return
	}
	var path string
	var item library.HistoryItem
	var ok bool
	if thumbnail {
		path, item, ok = s.store.HistoryThumbnailPath(id)
	} else {
		path, item, ok = s.store.HistoryPath(id)
	}
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
	contentType := item.ContentType
	if thumbnail {
		contentType = "image/jpeg"
	}
	if contentType == "" && item.Kind == "video" {
		contentType = videoContentType(item.FileName)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	http.ServeContent(w, r, safeFileName(item.PublicName), item.UpdatedAt, file)
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
				s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
			}
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
	if current := s.store.Current(); current != nil {
		videoPlayer := s.videoPlayerStateForID(current.ID)
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
		if videoEnabled && tunnelURLBase != "" && ((current.Kind == "video" && (videoStatus.HLS || videoStatus.Active)) || (current.Kind != "video" && videoStatus.HLS)) {
			streamPath := hlsURLPath(current.ID)
			hlsURL = imageURLBase + streamPath
			publicHLSURL = tunnelURLBase + streamPath
		}
	} else {
		videoPlayer := s.videoPlayerEmptyState()
		shareURL, shareURLLabel := primaryShareURL(map[string]interface{}{
			"imageURL":      imageURL,
			"videoURL":      videoURL,
			"hlsURL":        hlsURL,
			"localImageURL": localImageURL,
			"videoPlayer":   videoPlayer,
		})
		return s.stateWithMedia(r, upnpResult, tunnelStatus, videoPlayer, imageURL, videoURL, hlsURL, shareURL, shareURLLabel, publicImageURL, publicVideoURL, publicHLSURL, localImageURL, previewImageURL)
	}
	if imageURL == "" {
		imageURL = ""
	}
	videoPlayer := s.videoPlayerStateForID("")
	if current := s.store.Current(); current != nil {
		videoPlayer = s.videoPlayerStateForID(current.ID)
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
		"videoQueue":      s.videoQueueState(),
		"current":         s.store.Current(),
		"history":         s.historyState(),
		"remoteAddr":      r.RemoteAddr,
	}
}

func (s *Server) stateWithMedia(r *http.Request, upnpResult upnp.Result, tunnelStatus map[string]interface{}, videoPlayer map[string]interface{}, imageURL, videoURL, hlsURL, shareURL, shareURLLabel, publicImageURL, publicVideoURL, publicHLSURL, localImageURL, previewImageURL string) map[string]interface{} {
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
		"videoQueue":      s.videoQueueState(),
		"current":         s.store.Current(),
		"history":         s.historyState(),
		"remoteAddr":      r.RemoteAddr,
	}
}

func (s *Server) historyState() []map[string]interface{} {
	items := s.store.History()
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		title := item.OriginalName
		if title == "" {
			title = item.PublicName
		}
		if title == "" {
			title = item.ID
		}
		thumbnailURL := s.adminPath("/history/" + url.PathEscape(item.ID))
		if item.Thumbnail != "" {
			thumbnailURL = s.adminPath("/history/" + url.PathEscape(item.ID) + "/thumbnail")
		}
		result = append(result, map[string]interface{}{
			"id":           item.ID,
			"kind":         item.Kind,
			"title":        title,
			"width":        item.Width,
			"height":       item.Height,
			"sizeBytes":    item.SizeBytes,
			"updatedAt":    item.UpdatedAt,
			"favorite":     item.Favorite,
			"persistent":   item.Persistent,
			"thumbnailURL": thumbnailURL,
			"hasThumbnail": item.Thumbnail != "",
		})
	}
	return result
}

func (s *Server) videoQueueState() []map[string]interface{} {
	queueItems := video.QueueStatus(s.store.Dir())
	historyItems := s.store.History()
	thumbnails := map[string]string{}
	for _, item := range historyItems {
		if item.Thumbnail != "" {
			thumbnails[item.ID] = s.adminPath("/history/" + url.PathEscape(item.ID) + "/thumbnail")
		}
	}
	result := make([]map[string]interface{}, 0, len(queueItems))
	for _, item := range queueItems {
		result = append(result, map[string]interface{}{
			"id":              item.ID,
			"mediaID":         item.MediaID,
			"title":           item.Title,
			"kind":            item.Kind,
			"status":          item.Status,
			"message":         item.Message,
			"progressPercent": item.ProgressPercent,
			"progressText":    item.ProgressText,
			"quality":         item.Quality,
			"createdAt":       item.CreatedAt,
			"startedAt":       item.StartedAt,
			"finishedAt":      item.FinishedAt,
			"thumbnailURL":    thumbnails[item.MediaID],
		})
	}
	return result
}

func (s *Server) videoPlayerEnabled() bool {
	appSettings, err := settings.Load()
	if err != nil {
		return false
	}
	return appSettings.VideoPlayerEnabled
}

func (s *Server) videoPlayerState() map[string]interface{} {
	return s.videoPlayerStateForID("")
}

func (s *Server) videoPlayerStateForID(id string) map[string]interface{} {
	enabled := s.videoPlayerEnabled()
	status := video.CurrentStatusForID(s.store.Dir(), id)
	if !enabled {
		status = video.Result{Message: "VRChat video player support is disabled."}
	}
	return map[string]interface{}{
		"enabled": enabled,
		"status":  status,
		"quality": s.videoQualityPreset(),
	}
}

func (s *Server) videoPlayerEmptyState() map[string]interface{} {
	enabled := s.videoPlayerEnabled()
	status := video.Result{Message: "VRChat video outputs have not been generated yet."}
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

func randomSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

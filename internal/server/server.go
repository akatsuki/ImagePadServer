package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/skip2/go-qrcode"

	"imagepadserver/internal/config"
	"imagepadserver/internal/imageproc"
	"imagepadserver/internal/library"
	"imagepadserver/internal/network"
	"imagepadserver/internal/upnp"
)

type Server struct {
	cfg   config.Config
	store *library.Store

	mu     sync.RWMutex
	upnp   upnp.Result
	tmpl   *template.Template
	lanURL string
}

func New(cfg config.Config, store *library.Store) *Server {
	lanURL := cfg.URLForHost(network.BestLANIP())
	return &Server{
		cfg:    cfg,
		store:  store,
		upnp:   upnp.Result{Message: "Checking router UPnP support..."},
		tmpl:   template.Must(template.New("index").Parse(indexHTML)),
		lanURL: lanURL,
	}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/qr/phone.png", s.handlePhoneQR)
	mux.HandleFunc("/image/current", s.handleCurrentImage)
	mux.HandleFunc("/image/current.png", s.handleCurrentImage)
	mux.HandleFunc("/image/current.jpg", s.handleCurrentImage)
	mux.HandleFunc("/healthz", s.handleHealth)
}

func (s *Server) SetUPnPResult(result upnp.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upnp = result
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

	opts := imageproc.DefaultOptions()
	if v := r.FormValue("format"); v != "" {
		opts.Format = v
	}
	if v := r.FormValue("quality"); v != "" {
		if q, err := strconv.Atoi(v); err == nil {
			opts.JPEGQuality = q
		}
	}
	if v := r.FormValue("maxDimension"); v != "" {
		if maxDim, err := strconv.Atoi(v); err == nil {
			opts.MaxDimension = maxDim
		}
	}
	if v := r.FormValue("maxMB"); v != "" {
		if maxMB, err := strconv.Atoi(v); err == nil && maxMB > 0 {
			if maxMB > 30 {
				maxMB = 30
			}
			opts.MaxBytes = int64(maxMB) << 20
		}
	}

	result, err := imageproc.Process(file, header.Filename, s.store.Dir(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	info := library.CurrentImage{
		PublicName:   result.PublicName,
		ContentType:  result.ContentType,
		Width:        result.Width,
		Height:       result.Height,
		OriginalName: header.Filename,
	}
	if err := s.store.SetCurrent(result.Path, info); err != nil {
		http.Error(w, "failed to save image", http.StatusInternalServerError)
		return
	}
	_ = os.Remove(result.Path)
	writeJSON(w, s.state(r))
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

func (s *Server) handleCurrentImage(w http.ResponseWriter, r *http.Request) {
	path, img, ok := s.store.CurrentPath()
	if !ok {
		http.Error(w, "no image selected", http.StatusNotFound)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "current image is unavailable", http.StatusNotFound)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", img.ContentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeFileName(img.PublicName)))
	http.ServeContent(w, r, img.PublicName, img.UpdatedAt, file)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) state(r *http.Request) map[string]interface{} {
	s.mu.RLock()
	upnpResult := s.upnp
	s.mu.RUnlock()

	localImageURL := s.lanURL + "image/current"
	publicImageURL := ""
	if current := s.store.Current(); current != nil {
		localImageURL = s.lanURL + "image/current?v=" + current.ID
		if upnpResult.OK && upnpResult.ExternalIP != "" {
			publicImageURL = s.cfg.URLForHost(upnpResult.ExternalIP) + "image/current?v=" + current.ID
		}
	}
	imageURL := publicImageURL
	if imageURL == "" {
		imageURL = "外部URLは未取得です"
	}

	return map[string]interface{}{
		"appName":        "ImagePadServer",
		"phoneURL":       s.lanURL,
		"imageURL":       imageURL,
		"publicImageURL": publicImageURL,
		"localImageURL":  localImageURL,
		"qrURL":          "/qr/phone.png",
		"upnp":           upnpResult,
		"current":        s.store.Current(),
		"remoteAddr":     r.RemoteAddr,
	}
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func safeFileName(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, `\`, "")
	if name == "" {
		return "current"
	}
	return name
}

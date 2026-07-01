package server

import (
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/network"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/toolchain"
	"imagepadserver/internal/upnp"
	"imagepadserver/internal/video"
)

const (
	// maxMultipartMemory is kept low so large uploads spill to temp files instead of RAM.
	maxMultipartMemory  = 32 << 20
	maxVideoUploadBytes = 2 << 30 // matches yt-dlp --max-filesize 2G
)

var (
	pageMediaDownloader = video.DownloadMediaURL
	networkMeasurer     = video.MeasureNetwork
	ensureFFmpeg        = toolchain.EnsureFFmpeg
)

type uploadURLAction int

const (
	uploadURLPublish uploadURLAction = iota
	uploadURLQueue
)

type uploadURLRequest struct {
	URL          string `json:"url"`
	Format       string `json:"format"`
	Quality      string `json:"quality"`
	MaxDimension string `json:"maxDimension"`
	MaxMB        string `json:"maxMB"`
}

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
	exitRequested   func()
	adminToken      string
	obs             *obsrtmp.Manager
	pairings        map[string]pairingRequest
	relayNonces     map[string]time.Time
	ingest          ingestStatus

	toolInstallMu  sync.Mutex
	toolInstalling bool

	settingsMu    sync.RWMutex
	settingsCache *settings.Settings
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
	obsStreamKey, err := settings.EnsureOBSStreamKey()
	if err != nil {
		obsStreamKey = adminToken
	}
	srv := &Server{
		cfg:            cfg,
		store:          store,
		upnp:           upnp.Result{Message: "Checking router UPnP support..."},
		tmpl:           template.Must(template.New("index").Parse(indexHTML)),
		lanURL:         lanURL,
		imageURLBase:   imageURLBase,
		previewURLBase: lanURL,
		tunnelStatus:   map[string]interface{}{"ok": false, "message": "Cloudflare Tunnel starting..."},
		adminToken:     adminToken,
		pairings:       make(map[string]pairingRequest),
		relayNonces:    make(map[string]time.Time),
	}
	srv.obs = obsrtmp.New(store.Dir(), advertisedHost, 1935, obsStreamKey, srv.videoQualityPreset, srv.obsLatencyProfile, obsrtmp.Callbacks{
		OnStart: srv.handleOBSStreamStart,
		OnDone:  srv.handleOBSStreamDone,
	})
	return srv
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

// SetExitRequested wires the callback that quits the application. It is invoked
// by the /api/quit handler so the web UI can shut the app down through the same
// graceful path as the tray "Exit" item.
func (s *Server) SetExitRequested(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exitRequested = fn
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

func (s *Server) StopOBSReceiver() {
	if s.obs != nil {
		s.obs.StopAndWait(8 * time.Second)
	}
}

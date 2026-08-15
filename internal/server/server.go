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
	"imagepadserver/internal/gpu"
	"imagepadserver/internal/imageproc"
	"imagepadserver/internal/library"
	"imagepadserver/internal/network"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/playlist"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/upnp"
	"imagepadserver/internal/video"
	"imagepadserver/internal/ytdlpauth"
)

const (
	// maxMultipartMemory is kept low so large uploads spill to temp files instead of RAM.
	maxMultipartMemory           = 32 << 20
	maxLocalVideoUploadBytes     = 8 << 30
	maxLocalVideoUploadBytesText = "8 GiB"
)

type shareModeContextKey struct{}

var (
	pageMediaDownloader    = video.DownloadMediaURL
	pageHLSMediaDownloader = downloadPageHLSMedia
	networkMeasurer        = video.MeasureNetwork
	ensureFFmpeg           = video.EnsureFFmpeg
	ytdlpLoginLauncher     = func() error {
		_, err := ytdlpauth.LaunchLoginAndSave(context.Background())
		return err
	}
)

var (
	stateEventThrottleDelay  = 300 * time.Millisecond
	stateEventHeartbeatDelay = 15 * time.Second
)

type Server struct {
	cfg   config.Config
	store *library.Store

	mu                    sync.RWMutex
	upnp                  upnp.Result
	tmpl                  *template.Template
	lanURL                string
	imageURLBase          string
	previewURLBase        string
	tunnelStatus          map[string]interface{}
	tunnelURLBase         string
	tunnelReconnect       chan<- struct{}
	exitRequested         func()
	adminToken            string
	obs                   *obsrtmp.Manager
	obsCommitMu           sync.Mutex
	obsCallbackGeneration uint64
	obsSessionActive      func(string, uint64) bool
	obsSessionLatest      func(string, uint64) bool
	beforeOBSCommit       func(obsrtmp.Session)
	pairings              map[string]pairingRequest
	relayNonces           map[string]time.Time
	ingest                ingestStatus

	toolInstallMu    sync.Mutex
	toolInstalling   bool
	rtspMap          rtspMappingHandle
	rtspSource       string
	rtspSessionID    string
	rtspGeneration   uint64
	rtspReadySeq     uint64
	mapRTSPPort      rtspPortMapper
	setRTSPURL       func(obsrtmp.RTSPEndpoint, string, string) bool
	setRadioRTSPURL  func(obsrtmp.RTSPEndpoint, string, string) bool
	onRadioRTSPReady func(obsrtmp.RTSPEndpoint) // test seam; invoked before public mapping
	eventMu          sync.Mutex
	stateEvents      map[chan struct{}]struct{}
	lastStateEvent   time.Time
	stateEventDue    bool

	musicQueue                      *playlist.Queue
	playlistStore                   *playlist.Store
	radio                           musicRadioController
	startMusicRadio                 func() error
	musicJobs                       chan func()
	musicPendingMu                  sync.Mutex
	musicPendingTrack               string
	musicPendingOffset              int
	musicPaused                     bool
	musicPausedTrack                string
	musicPausedOffset               int
	musicRadioStartMu               sync.Mutex
	musicTimelineMu                 sync.Mutex
	playlistGPUEvaluationArmed      bool
	musicTimelineEpoch              uint64
	musicNextSequence               uint64
	musicActiveTrackID              string
	musicActiveBaseSeq              uint64
	musicActiveTailSeq              uint64
	musicActiveStartedAt            time.Time
	musicTransitionReservedNextSeq  uint64
	musicPlaylistGPUTransitionSink  func(video.TransitionPlan) error
	musicPlaylistGPUFrameSink       func(video.EncodedH264Frame) error
	musicPlaylistPublisherSink      io.Writer
	musicPlaylistPublisherMu        sync.RWMutex
	musicPlaylistGPUWorker          *video.PlaylistGPUWorker
	musicPlaylistGPUController      *video.PlaylistGPUTransitionController
	musicPlaylistGPUSessionCtx      context.Context
	musicPlaylistGPUSessionCancel   context.CancelFunc
	musicPlaylistGPUExecutionMu     sync.Mutex
	musicPlaylistGPUActiveOutput    *playlistGPUOutputSession
	musicPlaylistGPUContinuousReady bool
	musicPlaylistGPUContinuousSink  func(video.TrackAssets, video.TrackTimeline, video.TransitionPlan) error
	musicPlaylistGPUAssets          map[string]video.TrackAssets
	musicPlaylistGPUTimelines       map[string]video.TrackTimeline
	musicPlaylistGPUHandledTrackID  string
	musicPlaylistGPUHandledEpoch    uint64
	musicPlaylistGPUOutputTelemetry video.PlaylistGPUOutputTelemetry
	activeCanonicalHeight           int
}

type rtspMappingHandle interface {
	ExternalIP() string
	ExternalPort() int
	Close() error
}

type rtspPortMapper func(protocol string, internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result)

type rtspMappingSet struct {
	control rtspMappingHandle
	owned   []rtspMappingHandle
}

func (m *rtspMappingSet) ExternalIP() string {
	if m == nil || m.control == nil {
		return ""
	}
	return m.control.ExternalIP()
}

func (m *rtspMappingSet) ExternalPort() int {
	if m == nil || m.control == nil {
		return 0
	}
	return m.control.ExternalPort()
}

func (m *rtspMappingSet) Close() error {
	if m == nil {
		return nil
	}
	var firstErr error
	for _, mapping := range m.owned {
		if mapping == nil {
			continue
		}
		if err := mapping.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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
	activeCanonicalHeight := settings.ActiveMusicPlaylistCanonicalHeight()
	srv := &Server{
		cfg:                   cfg,
		store:                 store,
		upnp:                  upnp.Result{Message: "Checking router UPnP support..."},
		tmpl:                  template.Must(template.New("index").Parse(indexHTML)),
		lanURL:                lanURL,
		imageURLBase:          imageURLBase,
		previewURLBase:        lanURL,
		tunnelStatus:          map[string]interface{}{"ok": false, "message": "Cloudflare Tunnel starting..."},
		adminToken:            adminToken,
		pairings:              make(map[string]pairingRequest),
		relayNonces:           make(map[string]time.Time),
		stateEvents:           make(map[chan struct{}]struct{}),
		activeCanonicalHeight: activeCanonicalHeight,
	}
	srv.obs = obsrtmp.New(store.Dir(), advertisedHost, 1935, obsStreamKey, srv.videoQualityPreset, srv.obsLatencyProfile, obsrtmp.Callbacks{
		OnStart:     srv.handleOBSStreamStart,
		OnDone:      srv.handleOBSStreamDone,
		OnRTSPReady: srv.handleRTSPReady,
		OnRTSPDone:  srv.handleRTSPDone,
	})
	srv.obsSessionActive = srv.obs.IsSessionActive
	srv.obsSessionLatest = srv.obs.IsLatestSession
	srv.mapRTSPPort = func(protocol string, internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result) {
		var mapping *upnp.TCPMapping
		var result upnp.Result
		if strings.EqualFold(protocol, "UDP") {
			mapping, result = upnp.MapUDP(internalPort, externalPort, description)
		} else {
			mapping, result = upnp.MapTCP(internalPort, externalPort, description)
		}
		return mapping, result
	}
	srv.setRTSPURL = srv.obs.SetRTSPEndpointURL
	srv.initMusicPlaylist(advertisedHost)
	return srv
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", s.admin(s.handleIndex))
	mux.HandleFunc("/api/state", s.admin(s.handleState))
	mux.HandleFunc("/api/events", s.admin(s.handleEvents))
	mux.HandleFunc("/api/tunnel/reconnect", s.admin(s.handleTunnelReconnect))
	mux.HandleFunc("/api/quit", s.admin(s.handleQuit))
	mux.HandleFunc("/api/ytdlp/login", s.admin(s.handleYTDLPLogin))
	mux.HandleFunc("/api/ytdlp/cookies", s.admin(s.handleYTDLPCookies))
	mux.HandleFunc("/api/upload", s.admin(s.handleUpload))
	mux.HandleFunc("/api/upload-queue", s.admin(s.handleUploadQueue))
	mux.HandleFunc("/api/upload-url", s.admin(s.handleUploadURL))
	mux.HandleFunc("/api/upload-url-queue", s.admin(s.handleUploadURLQueue))
	mux.HandleFunc("/api/browser-media-candidates", s.admin(s.handleBrowserMediaCandidates))
	mux.HandleFunc("/api/clear", s.admin(s.handleClear))
	mux.HandleFunc("/api/pairing/request", s.handlePairingRequest)
	mux.HandleFunc("/api/pairing/confirm", s.handlePairingConfirm)
	mux.HandleFunc("/api/obs/relay-config", s.handleOBSRelayConfig)
	mux.HandleFunc("/api/obs/start", s.admin(s.handleOBSStart))
	mux.HandleFunc("/api/obs/end", s.admin(s.handleOBSEnd))
	mux.HandleFunc("/api/obs/key", s.admin(s.handleOBSKey))
	mux.HandleFunc("/api/obs/latency", s.admin(s.handleOBSLatency))
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
	mux.HandleFunc("/api/music-mode", s.admin(s.handleMusicMode))
	mux.HandleFunc("/api/music/playlist", s.admin(s.handleMusicPlaylist))
	mux.HandleFunc("/api/music/playlist/add", s.admin(s.handleMusicPlaylistAdd))
	mux.HandleFunc("/api/music/playlist/remove", s.admin(s.handleMusicPlaylistRemove))
	mux.HandleFunc("/api/music/playlist/reorder", s.admin(s.handleMusicPlaylistReorder))
	mux.HandleFunc("/api/music/playlist/start", s.admin(s.handleMusicPlaylistStart))
	mux.HandleFunc("/api/music/playlist/gpu-evaluation/start", s.admin(s.handleMusicPlaylistGPUEvaluationStart))
	mux.HandleFunc("/api/music/playlist/play", s.admin(s.handleMusicPlaylistPlay))
	mux.HandleFunc("/api/music/playlist/pause", s.admin(s.handleMusicPlaylistPause))
	mux.HandleFunc("/api/music/playlist/seek", s.admin(s.handleMusicPlaylistSeek))
	mux.HandleFunc("/api/music/playlist/next", s.admin(s.handleMusicPlaylistNext))
	mux.HandleFunc("/api/music/playlist/stop", s.admin(s.handleMusicPlaylistStop))
	mux.HandleFunc("/api/music/playlist/options", s.admin(s.handleMusicPlaylistOptions))
	mux.HandleFunc("/api/music/playlist/artwork", s.admin(s.handleMusicPlaylistArtwork))
	mux.HandleFunc("/api/music/playlists", s.admin(s.handleMusicPlaylists))
	mux.HandleFunc("/api/music/playlists/load", s.admin(s.handleMusicPlaylistsLoad))
	mux.HandleFunc("/api/music/playlists/delete", s.admin(s.handleMusicPlaylistsDelete))
	mux.HandleFunc("/radio/", s.handleRadioHLS)
	mux.HandleFunc("/api/ffmpeg", s.admin(s.handleFFmpeg))
	mux.HandleFunc("/api/video-quality", s.admin(s.handleVideoQuality))
	mux.HandleFunc("/api/encoder-mode", s.admin(s.handleEncoderMode))
	mux.HandleFunc("/api/network-check", s.admin(s.handleNetworkCheck))
	mux.HandleFunc("/qr/phone.png", s.admin(s.handlePhoneQR))
	mux.HandleFunc("/history/", s.admin(s.handleHistoryMedia))
	mux.HandleFunc("/image/current", s.handleCurrentImage)
	mux.HandleFunc("/image/current.png", s.handleCurrentImage)
	mux.HandleFunc("/image/current.jpg", s.handleCurrentImage)
	mux.HandleFunc("/video/current.mp4", s.handleCurrentVideo)
	mux.HandleFunc("/stream/current.m3u8", s.handleCurrentHLS)
	mux.HandleFunc("/stream/", s.handleStream)
	mux.HandleFunc("/pub/", s.handlePubItem)
	mux.HandleFunc("/assets/fonts/", s.handleUIFont)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/app-icon.png", s.handleAppIcon)
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
	s.mu.Unlock()
	s.broadcastStateChanged()
}

func (s *Server) subscribeStateEvents() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.eventMu.Lock()
	if s.stateEvents == nil {
		s.stateEvents = make(map[chan struct{}]struct{})
	}
	s.stateEvents[ch] = struct{}{}
	s.eventMu.Unlock()
	unsubscribe := func() {
		s.eventMu.Lock()
		if _, ok := s.stateEvents[ch]; ok {
			delete(s.stateEvents, ch)
			close(ch)
		}
		s.eventMu.Unlock()
	}
	return ch, unsubscribe
}

func (s *Server) broadcastStateChanged() {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	s.lastStateEvent = time.Now()
	s.broadcastStateChangedLocked()
}

func (s *Server) broadcastStateChangedThrottled() {
	s.eventMu.Lock()
	now := time.Now()
	elapsed := now.Sub(s.lastStateEvent)
	if s.lastStateEvent.IsZero() || elapsed >= stateEventThrottleDelay {
		s.lastStateEvent = now
		s.stateEventDue = false
		s.broadcastStateChangedLocked()
		s.eventMu.Unlock()
		return
	}
	if s.stateEventDue {
		s.eventMu.Unlock()
		return
	}
	s.stateEventDue = true
	delay := stateEventThrottleDelay - elapsed
	s.eventMu.Unlock()
	time.AfterFunc(delay, func() {
		s.eventMu.Lock()
		if s.stateEventDue {
			s.stateEventDue = false
			s.lastStateEvent = time.Now()
			s.broadcastStateChangedLocked()
		}
		s.eventMu.Unlock()
	})
}

func (s *Server) broadcastStateChangedLocked() {
	for ch := range s.stateEvents {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Server) SyncOBSReceiver() {
	if s.obs == nil {
		return
	}
	if s.videoPlayerEnabled() {
		s.obs.Start()
		return
	}
	s.closeRTSPMapping("", obsrtmp.RTSPEndpoint{})
	s.obs.Stop()
}

func (s *Server) StopOBSReceiver() {
	s.closeRTSPMapping("", obsrtmp.RTSPEndpoint{})
	if s.obs != nil {
		s.obs.StopAndWait(8 * time.Second)
	}
	if s.radio != nil {
		s.radio.Stop(8 * time.Second)
	}
}

func (s *Server) handleRTSPReady(endpoint obsrtmp.RTSPEndpoint) {
	if endpoint.Generation != 0 && !s.isOBSActiveSession(endpoint.SessionID, endpoint.Generation) {
		return
	}
	s.mu.Lock()
	s.rtspReadySeq++
	seq := s.rtspReadySeq
	mapPort := s.mapRTSPPort
	setURL := s.setRTSPURL
	s.mu.Unlock()
	s.broadcastStateChanged()

	if mapPort == nil || setURL == nil {
		return
	}
	go s.publishRTSPReady("obs", endpoint, seq, mapPort, setURL, func() bool {
		return endpoint.Generation == 0 || s.isOBSActiveSession(endpoint.SessionID, endpoint.Generation)
	})
}

func (s *Server) handleRadioRTSPReady(endpoint obsrtmp.RTSPEndpoint) {
	if endpoint.Generation != 0 {
		owner, ok := s.radio.(interface{ IsActiveSession(string, uint64) bool })
		if !ok || !owner.IsActiveSession(endpoint.SessionID, endpoint.Generation) {
			return
		}
	}
	s.mu.Lock()
	s.rtspReadySeq++
	seq := s.rtspReadySeq
	mapPort := s.mapRTSPPort
	setURL := s.setRadioRTSPURL
	onReady := s.onRadioRTSPReady
	s.mu.Unlock()
	if onReady != nil {
		onReady(endpoint)
	}
	s.broadcastStateChanged()

	if mapPort == nil || setURL == nil {
		return
	}
	go s.publishRTSPReady("radio", endpoint, seq, mapPort, setURL, func() bool {
		if endpoint.Generation == 0 {
			return true
		}
		owner, ok := s.radio.(interface{ IsActiveSession(string, uint64) bool })
		return ok && owner.IsActiveSession(endpoint.SessionID, endpoint.Generation)
	})
}

func (s *Server) publishRTSPReady(source string, endpoint obsrtmp.RTSPEndpoint, seq uint64, mapPort rtspPortMapper, setURL func(obsrtmp.RTSPEndpoint, string, string) bool, isCurrent func() bool) {
	defer s.broadcastStateChanged()
	if !isCurrent() {
		return
	}
	mapping, result := mapRTSPCompatibilityPorts(mapPort, endpoint)
	if !isCurrent() {
		if mapping != nil {
			_ = mapping.Close()
		}
		return
	}
	if mapping == nil || !result.OK {
		message := "RTSP is available on LAN/Tailscale; UPnP publication failed"
		if result.Message != "" {
			message += ": " + result.Message
		}
		if isCurrent() {
			setURL(endpoint, "", message)
		}
		return
	}
	if !upnp.IsGloballyRoutableIPv4(mapping.ExternalIP()) {
		_ = mapping.Close()
		if isCurrent() {
			setURL(endpoint, "",
				"RTSP is available on LAN/Tailscale; CGNAT or upstream NAT prevents direct publication.")
		}
		return
	}

	s.mu.Lock()
	if seq != s.rtspReadySeq || !isCurrent() {
		s.mu.Unlock()
		_ = mapping.Close()
		return
	}
	previous := s.rtspMap
	s.rtspMap = mapping
	s.rtspSource = source
	s.rtspSessionID = endpoint.SessionID
	s.rtspGeneration = endpoint.Generation
	s.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}

	publicURL := fmt.Sprintf("rtsp://%s:%d/%s", mapping.ExternalIP(), mapping.ExternalPort(), endpoint.Path)
	if !isCurrent() || !setURL(endpoint, publicURL,
		"RTSP TCP/UDP is published through UPnP at "+mapping.ExternalIP()+".") {
		s.closeRTSPMapping(source, endpoint)
	}
}

func mapRTSPCompatibilityPorts(mapPort rtspPortMapper, endpoint obsrtmp.RTSPEndpoint) (rtspMappingHandle, upnp.Result) {
	type requestedMapping struct {
		protocol    string
		internal    int
		external    int
		description string
	}
	requests := []requestedMapping{
		{protocol: "TCP", internal: endpoint.Port, external: endpoint.Port, description: "ImagePadServer RTSP TCP"},
	}
	if endpoint.RTPPort > 0 {
		requests = append(requests, requestedMapping{protocol: "UDP", internal: endpoint.RTPPort, external: endpoint.RTPPort, description: "ImagePadServer RTSP RTP"})
	}
	if endpoint.RTCPPort > 0 {
		requests = append(requests, requestedMapping{protocol: "UDP", internal: endpoint.RTCPPort, external: endpoint.RTCPPort, description: "ImagePadServer RTSP RTCP"})
	}

	var owned []rtspMappingHandle
	for i, request := range requests {
		mapping, result := mapPort(request.protocol, request.internal, request.external, request.description)
		if mapping == nil || !result.OK {
			for _, previous := range owned {
				_ = previous.Close()
			}
			return nil, result
		}
		owned = append(owned, mapping)
		if i == 0 && !upnp.IsGloballyRoutableIPv4(mapping.ExternalIP()) {
			return &rtspMappingSet{control: mapping, owned: owned}, result
		}
	}
	return &rtspMappingSet{control: owned[0], owned: owned}, upnp.Result{
		OK:         true,
		Message:    "RTSP TCP/UDP ports mapped by UPnP",
		ExternalIP: owned[0].ExternalIP(),
	}
}

func (s *Server) handleRTSPDone(endpoint obsrtmp.RTSPEndpoint) {
	s.closeRTSPMapping("obs", endpoint)
	s.broadcastStateChanged()
}

func (s *Server) handleRadioRTSPDone(endpoint obsrtmp.RTSPEndpoint) {
	s.closeRTSPMapping("radio", endpoint)
	s.broadcastStateChanged()
}

func (s *Server) closeRTSPMapping(source string, endpoint obsrtmp.RTSPEndpoint) {
	s.mu.Lock()
	if endpoint.SessionID != "" && (s.rtspSource != source || s.rtspSessionID != endpoint.SessionID || s.rtspGeneration != endpoint.Generation) {
		s.mu.Unlock()
		return
	}
	mapping := s.rtspMap
	s.rtspMap = nil
	s.rtspSource = ""
	s.rtspSessionID = ""
	s.rtspGeneration = 0
	s.rtspReadySeq++
	s.mu.Unlock()
	if mapping != nil {
		_ = mapping.Close()
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

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unsupported", http.StatusInternalServerError)
		return
	}
	events, unsubscribe := s.subscribeStateEvents()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	_, _ = io.WriteString(w, "retry: 1000\n")
	_, _ = io.WriteString(w, "event: state\ndata: {}\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(stateEventHeartbeatDelay)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = io.WriteString(w, "event: heartbeat\ndata: {}\n\n")
			flusher.Flush()
		case _, ok := <-events:
			if !ok {
				return
			}
			_, _ = io.WriteString(w, "event: state\ndata: {}\n\n")
			flusher.Flush()
		}
	}
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

func (s *Server) handleYTDLPLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := ytdlpLoginLauncher(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.broadcastStateChanged()
	writeJSON(w, ytdlpauth.Status())
}

func (s *Server) handleYTDLPCookies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, ytdlpauth.Status())
	case http.MethodDelete:
		if err := ytdlpauth.DeleteCookies(); err != nil {
			http.Error(w, "failed to delete yt-dlp cookies", http.StatusInternalServerError)
			return
		}
		s.broadcastStateChanged()
		writeJSON(w, ytdlpauth.Status())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	exit := s.exitRequested
	s.mu.RUnlock()
	if exit == nil {
		http.Error(w, "app shutdown unavailable", http.StatusServiceUnavailable)
		return
	}

	// Respond before triggering shutdown so the browser receives the reply,
	// then quit through the same graceful path as the tray "Exit" item.
	writeJSON(w, map[string]interface{}{"ok": true, "message": "アプリを終了します"})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		exit()
	}()
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer s.broadcastStateChanged()
	trackedBody, finishReceive := s.trackUploadReceiveProgress(r.Body, r.ContentLength, uploadReceiveTitle(r))
	r.Body = trackedBody
	if err := r.ParseMultipartForm(uploadMemoryLimit()); err != nil {
		finishReceive()
		http.Error(w, "failed to parse upload", http.StatusBadRequest)
		return
	}
	finishReceive()

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
	defer s.broadcastStateChanged()
	trackedBody, finishReceive := s.trackUploadReceiveProgress(r.Body, r.ContentLength, uploadReceiveTitle(r))
	r.Body = trackedBody
	if err := r.ParseMultipartForm(uploadMemoryLimit()); err != nil {
		finishReceive()
		http.Error(w, "failed to parse upload", http.StatusBadRequest)
		return
	}
	finishReceive()

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
	defer s.broadcastStateChanged()

	var req struct {
		URL          string `json:"url"`
		Format       string `json:"format"`
		Quality      string `json:"quality"`
		MaxDimension string `json:"maxDimension"`
		MaxMB        string `json:"maxMB"`
		ShareMode    string `json:"shareMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid URL upload request", http.StatusBadRequest)
		return
	}
	r = requestWithShareMode(r, req.ShareMode)

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

		if !s.tryBeginIngest(ingestDownloading, req.URL) {
			http.Error(w, "別の取り込み処理が進行中です", http.StatusConflict)
			return
		}
		defer s.clearIngest()

		// Preserve SoundCloud-page detection (uses yt-dlp).
		if isSoundCloudURL(req.URL) {
			var media video.DownloadedMedia
			err := s.withYTDLPIngestProgress(func() error {
				var downloadErr error
				media, downloadErr = video.DownloadMediaURL(req.URL, s.store.Dir())
				return downloadErr
			})
			if err != nil {
				http.Error(w, videoURLDownloadError(err), http.StatusBadRequest)
				return
			}
			acquired, err := s.acquireDownloadedSoundCloud(r.Context(), media)
			if err != nil {
				os.Remove(media.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state, err := s.processAudioFileAndPublish(r, acquired)
			if err != nil {
				os.Remove(acquired.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)
			return
		}

		if s.musicModeEnabled() {
			var acquired video.AcquiredAudio
			err := s.withYTDLPIngestProgress(func() error {
				var acquireErr error
				acquired, acquireErr = musicURLAcquirer(r.Context(), s, req.URL)
				return acquireErr
			})
			if err != nil {
				http.Error(w, videoURLDownloadError(err), http.StatusBadRequest)
				return
			}
			state, err := s.processAudioFileAndPublish(r, acquired)
			if err != nil {
				os.Remove(acquired.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)
			return
		}

		// Try yt-dlp first — it handles YouTube, X/Twitter, niconico and many
		// other video pages. If it cannot handle the URL, fall back to the
		// bounded SSRF-safe direct downloader for direct media file links.
		var ytMedia video.DownloadedMedia
		ytdlpErr := s.withYTDLPIngestProgress(func() error {
			var downloadErr error
			ytMedia, downloadErr = pageMediaDownloader(req.URL, s.store.Dir())
			return downloadErr
		})
		if ytdlpErr == nil {
			state, err := s.processVideoFileAndPublish(r, ytMedia.SourcePath, ytMedia.Name, ytMedia.ThumbnailPath)
			if err != nil {
				os.Remove(ytMedia.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)
			return
		}

		// Page URLs (YouTube, Twitter/X, SoundCloud) only return HTML to a
		// plain GET; skip the direct-download fallback and surface the yt-dlp
		// error so the real cause (e.g. Twitter "Bad guest token", YouTube
		// "Requested format is not available") is not masked by a misleading
		// ffprobe "Invalid data found" on the saved HTML.
		if video.IsPageMediaURL(req.URL) {
			http.Error(w, videoURLDownloadError(ytdlpErr), http.StatusBadRequest)
			return
		}

		var hlsErr error
		var hlsMedia video.DownloadedMedia
		hlsErr = s.withYTDLPIngestProgress(func() error {
			var downloadErr error
			hlsMedia, downloadErr = pageHLSMediaDownloader(r.Context(), req.URL, s.store.Dir())
			return downloadErr
		})
		if hlsErr == nil {
			state, err := s.processVideoFileAndPublish(r, hlsMedia.SourcePath, hlsMedia.Name, hlsMedia.ThumbnailPath)
			if err != nil {
				os.Remove(hlsMedia.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)
			return
		}

		// Fallback: bounded SSRF-safe downloader. Redirects are revalidated and
		// the completed bytes are classified by ffprobe.
		media, err := s.downloadDirectMedia(r.Context(), req.URL)
		if err != nil {
			http.Error(w, videoURLDownloadError(combineURLErrors(combineURLErrors(ytdlpErr, hlsErr), err)), http.StatusBadRequest)
			return
		}
		probe := media.Probe
		class := media.Class
		switch class {
		case video.MediaAudio:
			meta := extractEmbeddedMetadata(probe)
			ffmpeg, aErr := video.EnsureFFmpeg()
			if aErr != nil {
				os.Remove(media.Path)
				http.Error(w, aErr.Error(), http.StatusBadRequest)
				return
			}
			candidates, aErr := video.ExtractEmbeddedArtwork(r.Context(), ffmpeg, media.Path, s.store.Dir(), probe)
			if aErr != nil {
				candidates = nil
			}
			acquired := video.AcquiredAudio{
				SourcePath:       media.Path,
				SourceName:       media.Name,
				Kind:             video.SourceRemoteAudio,
				Probe:            probe,
				EmbeddedMetadata: meta,
				EmbeddedArtwork:  candidates,
			}
			state, aErr := s.processAudioFileAndPublish(r, acquired)
			if aErr != nil {
				os.Remove(media.Path)
				http.Error(w, aErr.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)

		case video.MediaVideo:
			state, vErr := s.processVideoFileAndPublish(r, media.Path, media.Name, "")
			if vErr != nil {
				http.Error(w, vErr.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)

		default:
			os.Remove(media.Path)
			http.Error(w, "unsupported media type", http.StatusBadRequest)
		}
		return
	}
	remote, name, err := downloadRemoteImage(req.URL, opts.MaxInputBytes)
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
	defer s.broadcastStateChanged()

	var req struct {
		URL          string `json:"url"`
		Format       string `json:"format"`
		Quality      string `json:"quality"`
		MaxDimension string `json:"maxDimension"`
		MaxMB        string `json:"maxMB"`
		ShareMode    string `json:"shareMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid URL upload request", http.StatusBadRequest)
		return
	}
	r = requestWithShareMode(r, req.ShareMode)
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

		if !s.tryBeginIngest(ingestDownloading, req.URL) {
			http.Error(w, "別の取り込み処理が進行中です", http.StatusConflict)
			return
		}
		defer s.clearIngest()

		// Preserve SoundCloud-page detection (uses yt-dlp).
		if isSoundCloudURL(req.URL) {
			var media video.DownloadedMedia
			err := s.withYTDLPIngestProgress(func() error {
				var downloadErr error
				media, downloadErr = video.DownloadMediaURL(req.URL, s.store.Dir())
				return downloadErr
			})
			if err != nil {
				http.Error(w, videoURLDownloadError(err), http.StatusBadRequest)
				return
			}
			acquired, err := s.acquireDownloadedSoundCloud(r.Context(), media)
			if err != nil {
				os.Remove(media.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state, err := s.processAudioFileAndQueue(r, acquired)
			if err != nil {
				os.Remove(acquired.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)
			return
		}

		if s.musicModeEnabled() {
			var acquired video.AcquiredAudio
			err := s.withYTDLPIngestProgress(func() error {
				var acquireErr error
				acquired, acquireErr = musicURLAcquirer(r.Context(), s, req.URL)
				return acquireErr
			})
			if err != nil {
				http.Error(w, videoURLDownloadError(err), http.StatusBadRequest)
				return
			}
			state, err := s.processAudioFileAndQueue(r, acquired)
			if err != nil {
				os.Remove(acquired.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)
			return
		}

		// Try yt-dlp first (YouTube, X/Twitter, niconico, …); fall back to the
		// bounded SSRF-safe direct downloader for direct media file links.
		var ytMedia video.DownloadedMedia
		ytdlpErr := s.withYTDLPIngestProgress(func() error {
			var downloadErr error
			ytMedia, downloadErr = pageMediaDownloader(req.URL, s.store.Dir())
			return downloadErr
		})
		if ytdlpErr == nil {
			state, err := s.processVideoFileAndQueue(r, ytMedia.SourcePath, ytMedia.Name, ytMedia.ThumbnailPath)
			if err != nil {
				os.Remove(ytMedia.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)
			return
		}

		// Page URLs (YouTube, Twitter/X, SoundCloud) only return HTML to a
		// plain GET; skip the direct-download fallback and surface the yt-dlp
		// error so the real cause is not masked by a misleading ffprobe
		// "Invalid data found" on the saved HTML.
		if video.IsPageMediaURL(req.URL) {
			http.Error(w, videoURLDownloadError(ytdlpErr), http.StatusBadRequest)
			return
		}

		var hlsErr error
		var hlsMedia video.DownloadedMedia
		hlsErr = s.withYTDLPIngestProgress(func() error {
			var downloadErr error
			hlsMedia, downloadErr = pageHLSMediaDownloader(r.Context(), req.URL, s.store.Dir())
			return downloadErr
		})
		if hlsErr == nil {
			state, err := s.processVideoFileAndQueue(r, hlsMedia.SourcePath, hlsMedia.Name, hlsMedia.ThumbnailPath)
			if err != nil {
				os.Remove(hlsMedia.SourcePath)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)
			return
		}

		media, err := s.downloadDirectMedia(r.Context(), req.URL)
		if err != nil {
			http.Error(w, videoURLDownloadError(combineURLErrors(combineURLErrors(ytdlpErr, hlsErr), err)), http.StatusBadRequest)
			return
		}
		probe := media.Probe
		class := media.Class
		switch class {
		case video.MediaAudio:
			meta := extractEmbeddedMetadata(probe)
			ffmpeg, aErr := video.EnsureFFmpeg()
			if aErr != nil {
				os.Remove(media.Path)
				http.Error(w, aErr.Error(), http.StatusBadRequest)
				return
			}
			candidates, aErr := video.ExtractEmbeddedArtwork(r.Context(), ffmpeg, media.Path, s.store.Dir(), probe)
			if aErr != nil {
				candidates = nil
			}
			acquired := video.AcquiredAudio{
				SourcePath:       media.Path,
				SourceName:       media.Name,
				Kind:             video.SourceRemoteAudio,
				Probe:            probe,
				EmbeddedMetadata: meta,
				EmbeddedArtwork:  candidates,
			}
			state, aErr := s.processAudioFileAndQueue(r, acquired)
			if aErr != nil {
				os.Remove(media.Path)
				http.Error(w, aErr.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)

		case video.MediaVideo:
			state, vErr := s.processVideoFileAndQueue(r, media.Path, media.Name, "")
			if vErr != nil {
				http.Error(w, vErr.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, state)

		default:
			os.Remove(media.Path)
			http.Error(w, "unsupported media type", http.StatusBadRequest)
		}
		return
	}
	remote, name, err := downloadRemoteImage(req.URL, opts.MaxInputBytes)
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

	if s.videoPlayerEnabled() && (isAudioUpload(name, contentType) || shouldProbeUploadedMedia(name, contentType)) {
		// Local uploads have no download step, so they never pass through the
		// tryBeginIngest/clearIngest pair in the URL handlers. Own the ingest
		// lifecycle here: surface progress while probing/analyzing and always
		// release the slot on return so the UI advances from the indeterminate
		// sweep to the conversion progress bar instead of stalling, and so later
		// ingests aren't rejected as "already in progress".
		s.setIngest(ingestProcessing, name)
		defer s.clearIngest()
		acquired, err := s.acquireUploadedAudio(r.Context(), reader, name)
		if err != nil {
			return nil, err
		}
		state, err := s.processAudioFileAndPublish(r, acquired)
		if err != nil {
			os.Remove(acquired.SourcePath)
			return nil, err
		}
		return state, nil
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
	// Replacing the published media with a still image must also discard the
	// previous video's in-flight conversion, otherwise the old HLS job can
	// resume after the image URL has been issued.
	if prev := s.store.Current(); prev != nil && prev.ID != "" {
		video.CancelConversion(s.store.Dir(), prev.ID)
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
	return s.withClipboardResult(r, state), nil
}

func (s *Server) processAndQueue(r *http.Request, reader io.Reader, name, contentType string, opts imageproc.Options) (map[string]interface{}, error) {
	if !s.videoPlayerEnabled() {
		return nil, fmt.Errorf("video player support is disabled")
	}
	if isVideoUpload(name, contentType) {
		return s.processVideoAndQueue(r, reader, name)
	}

	if isAudioUpload(name, contentType) || shouldProbeUploadedMedia(name, contentType) {
		// See processAndPublish: own the ingest lifecycle for the local-upload
		// path so the status is always released on return.
		s.setIngest(ingestProcessing, name)
		defer s.clearIngest()
		acquired, err := s.acquireUploadedAudio(r.Context(), reader, name)
		if err != nil {
			return nil, err
		}
		state, err := s.processAudioFileAndQueue(r, acquired)
		if err != nil {
			os.Remove(acquired.SourcePath)
			return nil, err
		}
		return state, nil
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
	written, err := io.Copy(source, io.LimitReader(reader, maxLocalVideoUploadBytes+1))
	if err != nil {
		_ = source.Close()
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("failed to save video upload")
	}
	if err := source.Close(); err != nil {
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("failed to save video upload")
	}
	if written > maxLocalVideoUploadBytes {
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("video exceeds local upload size limit of %s", maxLocalVideoUploadBytesText)
	}
	return s.processVideoFileAndPublish(r, sourcePath, name, "")
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
	written, err := io.Copy(source, io.LimitReader(reader, maxLocalVideoUploadBytes+1))
	if err != nil {
		_ = source.Close()
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("failed to save video upload")
	}
	if err := source.Close(); err != nil {
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("failed to save video upload")
	}
	if written > maxLocalVideoUploadBytes {
		_ = os.Remove(sourcePath)
		return nil, fmt.Errorf("video exceeds local upload size limit of %s", maxLocalVideoUploadBytesText)
	}
	return s.processVideoFileAndQueue(r, sourcePath, name, "")
}

func (s *Server) processVideoFileAndPublish(r *http.Request, sourcePath, name, providedThumbnail string) (map[string]interface{}, error) {
	if _, err := video.EnsureFFmpeg(); err != nil {
		return nil, err
	}
	thumbnail := s.useOrCreateVideoThumbnail(sourcePath, providedThumbnail)
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
	// Replacing the published video: discard the previous media's in-flight
	// conversion so a preempted job cannot resume and resurface the old video.
	if prev := s.store.Current(); prev != nil && prev.ID != "" {
		video.CancelConversion(s.store.Dir(), prev.ID)
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
	return s.withClipboardResult(r, state), nil
}

func (s *Server) processVideoFileAndQueue(r *http.Request, sourcePath, name, providedThumbnail string) (map[string]interface{}, error) {
	if _, err := ensureFFmpeg(); err != nil {
		return nil, err
	}
	thumbnail := s.useOrCreateVideoThumbnail(sourcePath, providedThumbnail)
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

// useOrCreateVideoThumbnail copies an externally provided thumbnail into the
// store directory when available, otherwise generates one from the video.
func (s *Server) useOrCreateVideoThumbnail(sourcePath, provided string) string {
	if provided != "" {
		if info, err := os.Stat(provided); err == nil && !info.IsDir() && info.Size() > 0 {
			name := "video-thumb-external-" + randomSuffix() + filepath.Ext(provided)
			dest := filepath.Join(s.store.Dir(), name)
			if err := copyFile(provided, dest); err == nil {
				return name
			}
		}
	}
	return s.createVideoThumbnail(sourcePath)
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

func copyFile(dst, src string) error {
	if filepath.Clean(dst) == filepath.Clean(src) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func (s *Server) createOBSVideoThumbnail(session obsrtmp.Session) string {
	if thumbnail := s.createVideoThumbnail(session.Recording); thumbnail != "" {
		return thumbnail
	}
	playlist := filepath.Join(s.store.Dir(), session.PlaylistName)
	if thumbnail := s.createVideoThumbnail(playlist); thumbnail != "" {
		return thumbnail
	}
	return ""
}

func (s *Server) handleOBSStreamStart(session obsrtmp.Session) {
	if session.Generation != 0 && !s.isOBSActiveSession(session.ID, session.Generation) {
		return
	}
	if s.beforeOBSCommit != nil {
		s.beforeOBSCommit(session)
	}
	s.obsCommitMu.Lock()
	if session.Generation != 0 {
		if session.Generation < s.obsCallbackGeneration || !s.isOBSActiveSession(session.ID, session.Generation) {
			s.obsCommitMu.Unlock()
			return
		}
		s.obsCallbackGeneration = session.Generation
	}
	info := library.CurrentImage{
		ID:           session.ID,
		Kind:         "video",
		SourceKind:   "obs",
		FileName:     filepath.Base(session.Recording),
		PublicName:   "obs-" + session.ID + ".mp4",
		ContentType:  "video/mp4",
		OriginalName: session.Title,
	}
	_ = s.store.SetCurrentInfoWithIDInMemory(info)
	s.obsCommitMu.Unlock()
	go func() { _ = s.store.Save() }()
	s.broadcastStateChanged()
}

func (s *Server) handleOBSStreamDone(session obsrtmp.Session) {
	if session.Generation != 0 && !s.isOBSLatestSession(session.ID, session.Generation) {
		return
	}
	current := s.store.Current()
	thumbnail := s.createOBSVideoThumbnail(session)
	s.obsCommitMu.Lock()
	if session.Generation != 0 {
		if session.Generation < s.obsCallbackGeneration || !s.isOBSLatestSession(session.ID, session.Generation) {
			s.obsCommitMu.Unlock()
			return
		}
		s.obsCallbackGeneration = session.Generation
	}
	info := library.CurrentImage{
		ID:           session.ID,
		Kind:         "video",
		SourceKind:   "obs",
		FileName:     filepath.Base(session.Recording),
		PublicName:   "obs-" + session.ID + ".mp4",
		ContentType:  "video/mp4",
		OriginalName: session.Title,
		Thumbnail:    thumbnail,
	}
	if current != nil && current.ID == session.ID {
		info = *current
	}
	info.ID = session.ID
	info.Kind = "video"
	info.SourceKind = "obs"
	info.FileName = filepath.Base(session.Recording)
	info.PublicName = "obs-" + session.ID + ".mp4"
	info.ContentType = "video/mp4"
	info.OriginalName = session.Title
	info.Thumbnail = thumbnail
	if stat, err := os.Stat(session.Recording); err == nil {
		info.SizeBytes = stat.Size()
	}
	_ = s.store.SetCurrentInfoWithIDInMemory(info)
	s.obsCommitMu.Unlock()
	files := video.GeneratedFiles(s.store.Dir(), session.ID)
	if len(files) > 0 && s.isOBSLatestSession(session.ID, session.Generation) {
		_ = s.store.MarkConverted(session.ID, files)
	}
	go func() { _ = s.store.Save() }()
	s.broadcastStateChanged()
}

func (s *Server) isOBSActiveSession(sessionID string, generation uint64) bool {
	if s.obsSessionActive != nil {
		return s.obsSessionActive(sessionID, generation)
	}
	return s.obs != nil && s.obs.IsSessionActive(sessionID, generation)
}

func (s *Server) isOBSLatestSession(sessionID string, generation uint64) bool {
	if s.obsSessionLatest != nil {
		return s.obsSessionLatest(sessionID, generation)
	}
	return s.obs != nil && s.obs.IsLatestSession(sessionID, generation)
}

func (s *Server) withClipboardResult(r *http.Request, state map[string]interface{}) map[string]interface{} {
	if mode := requestedShareMode(r); mode != "" {
		state["shareMode"] = mode
	}
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

func requestWithShareMode(r *http.Request, mode string) *http.Request {
	if normalizeShareMode(mode) == "" {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), shareModeContextKey{}, normalizeShareMode(mode)))
}

func requestedShareMode(r *http.Request) string {
	if r == nil {
		return ""
	}
	if mode, _ := r.Context().Value(shareModeContextKey{}).(string); mode != "" {
		return normalizeShareMode(mode)
	}
	return normalizeShareMode(r.FormValue("shareMode"))
}

func normalizeShareMode(mode string) string {
	switch mode {
	case "file", "link", "obs", "obs_rtsp", "obs_hls":
		return mode
	default:
		return ""
	}
}

func (s *Server) clearPublication() {
	if current := s.store.Current(); current != nil && current.ID != "" {
		video.CancelConversion(s.store.Dir(), current.ID)
	}
	_ = s.store.Clear()
}

var qualityPresetToJPEG = map[string]int{
	"highest": 95,
	"high":    85,
	"medium":  75,
	"low":     60,
	"lowest":  45,
}

var qualityPresetToWebP = map[string]int{
	"highest": 90,
	"high":    80,
	"medium":  70,
	"low":     55,
	"lowest":  40,
}

func optionsFromValues(value func(string) string) imageproc.Options {
	opts := imageproc.DefaultOptions()
	if v := value("format"); v != "" {
		opts.Format = v
	}
	if v := value("quality"); v != "" {
		if q, ok := qualityPresetToJPEG[v]; ok {
			opts.JPEGQuality = q
			opts.PNGQuality = v
		} else if v == "lossless" {
			opts.PNGQuality = v
		} else if q, err := strconv.Atoi(v); err == nil {
			opts.JPEGQuality = q
		}
		if q, ok := qualityPresetToWebP[v]; ok {
			opts.WebPQuality = q
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

func uploadReceiveTitle(r *http.Request) string {
	if r == nil {
		return "ファイル"
	}
	name := strings.TrimSpace(r.Header.Get("X-ImagePad-Upload-Name"))
	if name == "" {
		return "ファイル"
	}
	if decoded, err := url.QueryUnescape(name); err == nil && strings.TrimSpace(decoded) != "" {
		name = decoded
	}
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) {
		return "ファイル"
	}
	return name
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
	defer s.broadcastStateChanged()
	if err := s.store.Clear(); err != nil {
		http.Error(w, "failed to clear image", http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.state(r))
}

func (s *Server) handleOBSEnd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer s.broadcastStateChanged()
	if !s.videoPlayerEnabled() {
		http.Error(w, "video player support is disabled", http.StatusBadRequest)
		return
	}
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.obs.Restart(8 * time.Second)
	writeJSON(w, s.state(r))
}

func (s *Server) handleOBSStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer s.broadcastStateChanged()
	if !s.videoPlayerEnabled() {
		http.Error(w, "video player support is disabled", http.StatusBadRequest)
		return
	}
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	s.obs.StartPublishing()
	writeJSON(w, s.state(r))
}

func (s *Server) handleOBSRelayConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.adminAllowed(r) && !s.relayDeviceAllowed(r) {
		http.Error(w, "OBS relay config requires admin access or a paired relay device", http.StatusForbidden)
		return
	}
	s.rememberAdminToken(w, r)
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	relayConfig, err := s.obsRelayConfig(true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, relayConfig)
}

func (s *Server) obsRelayConfig(startReceiver bool) (map[string]interface{}, error) {
	if s.obs == nil {
		return nil, fmt.Errorf("OBS receiver is unavailable")
	}
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to enable video player support")
	}
	if startReceiver {
		s.obs.Start()
		s.obs.StartPublishing()
	}
	status := s.obs.Status()
	serverAddress := strings.TrimRight(status.ServerAddress, "/")
	return map[string]interface{}{
		"ok":                 true,
		"serverAddress":      status.ServerAddress,
		"streamKey":          status.StreamKey,
		"rtmpURL":            serverAddress + "/" + url.PathEscape(status.StreamKey),
		"videoPlayerEnabled": true,
		"listening":          status.Listening,
		"publishing":         status.Publishing,
		"latency":            status.Latency,
	}, nil
}

func (s *Server) handleOBSKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer s.broadcastStateChanged()
	if s.obs == nil {
		http.Error(w, "OBS receiver is unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		StreamKey string `json:"streamKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid OBS stream key request", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(req.StreamKey)
	if key == "" || strings.ContainsAny(key, "\\/ \t\r\n?#") {
		http.Error(w, "invalid OBS stream key", http.StatusBadRequest)
		return
	}
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.OBSStreamKey = key
		return nil
	}); err != nil {
		http.Error(w, "failed to update OBS stream key", http.StatusInternalServerError)
		return
	}
	s.obs.SetStreamKey(key, 8*time.Second)
	writeJSON(w, s.state(r))
}

func (s *Server) handleOBSLatency(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.obsState())
	case http.MethodPost:
		defer s.broadcastStateChanged()
		var req struct {
			Mode string `json:"mode"`
			DVR  bool   `json:"dvr"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid OBS latency request", http.StatusBadRequest)
			return
		}
		mode := obsrtmp.NormalizeLatencyMode(req.Mode)
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.OBSLatencyMode = mode
			appSettings.OBSDVREnabled = false
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		if s.obs != nil {
			s.obs.Restart(8 * time.Second)
		}
		writeJSON(w, s.obsState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
	defer s.broadcastStateChanged()
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
	defer s.broadcastStateChanged()
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
	if !s.tryBeginIngest(ingestAnalyzing, req.ID) {
		http.Error(w, "別の取り込み処理が進行中です", http.StatusConflict)
		return
	}
	defer s.clearIngest()
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
	defer s.broadcastStateChanged()
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
	current := s.store.Current()
	if current != nil && current.Converted {
		state := s.stateWithHistoryTargetMode(r, *current)
		state = s.withClipboardResult(r, state)
		writeJSON(w, state)
		return
	}
	if path, current, ok := s.store.CurrentPath(); ok && s.videoPlayerEnabled() {
		if !s.tryBeginIngest(ingestAnalyzing, current.ID) {
			http.Error(w, "別の取り込み処理が進行中です", http.StatusConflict)
			return
		}
		defer s.clearIngest()
		if current.SourceKind == "soundcloud" || current.SourceKind == "local_audio" || current.SourceKind == "remote_audio" {
			input, err := s.audioRenderInputForStored(r.Context(), path, *current)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			s.enqueueAudioConversion(input, current.ID, current.OriginalName)
		} else if current.Kind == "video" {
			s.enqueueUploadedConversion(path, current.ID, current.OriginalName)
		} else {
			s.enqueueStillConversion(path, current.ID, current.OriginalName)
		}
	}
	if current := s.store.Current(); current != nil {
		state := s.stateWithHistoryTargetMode(r, *current)
		state = s.withClipboardResult(r, state)
		writeJSON(w, state)
		return
	}
	writeJSON(w, s.withClipboardResult(r, s.state(r)))
}

func (s *Server) stateWithHistoryTargetMode(r *http.Request, current library.CurrentImage) map[string]interface{} {
	state := s.state(r)
	state["historyTargetMode"] = historyTargetMode(current)
	return withResolvedShareURLs(state)
}

func (s *Server) enqueueHistoryItem(id string) error {
	path, item, ok := s.store.HistoryPath(id)
	if !ok {
		return os.ErrNotExist
	}
	if item.Converted {
		return s.store.SetCurrentFromHistory(id)
	}
	if item.SourceKind == "soundcloud" || item.SourceKind == "local_audio" || item.SourceKind == "remote_audio" {
		input, err := s.audioRenderInputForStored(context.Background(), path, item.CurrentImage)
		if err != nil {
			return fmt.Errorf("audio re-analysis: %w", err)
		}
		s.enqueueAudioConversion(input, item.ID, item.OriginalName)
		return nil
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
	probe, ok := s.probeVideoMedia(path)
	preset := s.videoQualityPreset()
	totalSeconds := 0
	if ok {
		preset = s.videoQualityPresetForSourceProbe(probe)
		totalSeconds = videoDurationSeconds(probe)
	}
	jobID := video.EnqueueUploadedVideoForID(path, s.store.Dir(), id, title, preset, totalSeconds)
	s.watchConversion(jobID, id)
}

func (s *Server) videoQualityPresetForSourceProbe(probe video.MediaProbe) video.QualityPreset {
	return video.AdaptQualityPresetToSource(s.videoQualityPreset(), probe)
}

// probeVideoDuration returns the source video's duration in whole seconds via
// ffprobe, rounding partial seconds up so the segment-based progress percentage
// (completed / total) never reports >100% mid-conversion. Returns 0 when the
// duration cannot be determined, in which case the queue falls back to a raw
// segment count instead of a percentage.
func (s *Server) probeVideoDuration(path string) int {
	probe, ok := s.probeVideoMedia(path)
	if !ok {
		return 0
	}
	return videoDurationSeconds(probe)
}

func (s *Server) probeVideoMedia(path string) (video.MediaProbe, bool) {
	ffprobe, err := findFFprobe()
	if err != nil {
		return video.MediaProbe{}, false
	}
	probe, err := video.ProbeMedia(context.Background(), ffprobe, path)
	if err != nil {
		return video.MediaProbe{}, false
	}
	return probe, true
}

func videoDurationSeconds(probe video.MediaProbe) int {
	if probe.Duration <= 0 {
		return 0
	}
	secs := int(probe.Duration)
	if float64(secs) < probe.Duration {
		secs++
	}
	return secs
}

func soundCloudCurrentInfo(media video.DownloadedMedia, publicName, thumbnail string) library.CurrentImage {
	return library.CurrentImage{
		Kind:         "video",
		SourceKind:   "soundcloud",
		FileName:     filepath.Base(media.SourcePath),
		PublicName:   publicName,
		ContentType:  soundCloudContentType(media.SourcePath),
		OriginalName: media.Name,
		Thumbnail:    thumbnail,
	}
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
							convertedSize := totalFileSize(files)
							if current := s.store.Current(); current != nil && current.ID == mediaID {
								_ = s.store.UpdateCurrentSize(convertedSize)
							}
							_ = s.store.UpdateHistorySize(mediaID, convertedSize)
							_ = s.store.MarkConverted(mediaID, files)
						}
						s.broadcastStateChanged()
						return
					case "error", "canceled":
						s.broadcastStateChanged()
						return
					}
					s.broadcastStateChangedThrottled()
				}
			}
		}
	}()
}

func totalFileSize(paths []string) int64 {
	var total int64
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			total += info.Size()
		}
	}
	return total
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
		Mode   string `json:"mode"`
		Value  string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid copy request", http.StatusBadRequest)
		return
	}

	state := s.state(r)
	if mode := normalizeShareMode(req.Mode); mode != "" {
		state["shareMode"] = mode
	}
	copiedURL := ""
	if req.Target == "plShareUrl" {
		copiedURL = copyableDisplayedURL(req.Value)
		if copiedURL == "" {
			copiedURL = urlForCopyTarget(s.musicPlaylistState(), req.Target)
		}
	}
	if copiedURL == "" {
		copiedURL = urlForCopyTarget(state, req.Target)
	}
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
		defer s.broadcastStateChanged()
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid video player request", http.StatusBadRequest)
			return
		}
		if req.Enabled && !videoToolsReady() {
			// Tools not ready: install in the background and return now. The
			// toggle stays OFF until install succeeds (or reverts on failure).
			// The UI shows progress via state.toolInstall.
			s.startVideoToolInstall()
			writeJSON(w, s.videoPlayerState())
			return
		}
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.VideoPlayerEnabled = req.Enabled
			if !req.Enabled {
				appSettings.MusicModeEnabled = false
			}
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
		s.SyncOBSReceiver()
		writeJSON(w, s.videoPlayerState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMusicMode(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.videoPlayerState())
	case http.MethodPost:
		defer s.broadcastStateChanged()
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid music mode request", http.StatusBadRequest)
			return
		}
		if req.Enabled && !s.videoPlayerEnabled() {
			writeJSONError(w, gpu.RequiredError(), http.StatusConflict)
			return
		}
		if req.Enabled && !gpu.RenderingAvailable() {
			writeJSONError(w, gpu.UnavailableError(), http.StatusServiceUnavailable)
			return
		}
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.MusicModeEnabled = req.Enabled && appSettings.VideoPlayerEnabled
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		writeJSON(w, s.videoPlayerState())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// combineURLErrors merges the yt-dlp and direct-download failures so the user
// sees why both routes failed for a URL that is neither a supported video page
// nor a direct media file.
func combineURLErrors(ytdlpErr, directErr error) error {
	if ytdlpErr == nil {
		return directErr
	}
	if directErr == nil {
		return ytdlpErr
	}
	return fmt.Errorf("%v (direct download: %v)", ytdlpErr, directErr)
}

func (s *Server) handleFFmpeg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path, err := video.EnsureFFmpeg()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":   true,
		"path": path,
	})
}

func (s *Server) handleVideoQuality(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.videoQualityState())
	case http.MethodPost:
		defer s.broadcastStateChanged()
		var req struct {
			Mode                         *string `json:"mode"`
			MusicPlaylistLatencyMode     *string `json:"musicPlaylistLatencyMode"`
			MusicPlaylistDeliveryProfile *string `json:"musicPlaylistDeliveryProfile"`
			MusicPlaylistCanonicalHeight *int    `json:"musicPlaylistCanonicalHeight"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid video quality request", http.StatusBadRequest)
			return
		}
		if err := settings.Update(func(appSettings *settings.Settings) error {
			if req.Mode != nil {
				appSettings.VideoQualityMode = normalizeQualityMode(*req.Mode)
			}
			if req.MusicPlaylistDeliveryProfile != nil {
				appSettings.MusicPlaylistDeliveryProfile = normalizeMusicPlaylistDeliveryProfile(*req.MusicPlaylistDeliveryProfile)
			}
			if req.MusicPlaylistLatencyMode != nil {
				appSettings.MusicPlaylistLatencyMode = normalizeMusicPlaylistLatencyMode(*req.MusicPlaylistLatencyMode)
			}
			if req.MusicPlaylistCanonicalHeight != nil {
				appSettings.MusicPlaylistCanonicalHeight = settings.NormalizeMusicPlaylistCanonicalHeight(*req.MusicPlaylistCanonicalHeight)
			}
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

func (s *Server) handleEncoderMode(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.videoQualityState())
	case http.MethodPost:
		defer s.broadcastStateChanged()
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid encoder mode request", http.StatusBadRequest)
			return
		}
		mode := settings.NormalizeEncoderMode(req.Mode)
		if !strings.EqualFold(strings.TrimSpace(req.Mode), "gpu") {
			http.Error(w, "invalid encoder mode", http.StatusBadRequest)
			return
		}
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.EncoderMode = mode
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		video.ResetVideoEncoderCache()
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
	defer s.broadcastStateChanged()
	measurement := networkMeasurer()
	if err := settings.Update(func(appSettings *settings.Settings) error {
		appSettings.NetworkUploadMbps = measurement.UploadMbps
		return nil
	}); err != nil {
		http.Error(w, "failed to save settings", http.StatusInternalServerError)
		return
	}
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

func (s *Server) handleAppIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(appicon.IconPNG)
}

func (s *Server) handleUIFont(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/fonts/")
	if name != "NotoSansJP-Regular.ttf" && name != "NotoSansJP-Medium.ttf" && name != "NotoSansJP-SemiBold.ttf" {
		http.NotFound(w, r)
		return
	}
	fonts, err := video.VisualizerFonts()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	paths := map[string]string{
		"NotoSansJP-Regular.ttf":  fonts.Regular400,
		"NotoSansJP-Medium.ttf":   fonts.Medium500,
		"NotoSansJP-SemiBold.ttf": fonts.SemiBold600,
	}
	w.Header().Set("Content-Type", "font/ttf")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, paths[name])
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

// handlePubItem は公開済み項目の安定アドレス GET /pub/{id} を配信する。
// Published=false の項目は 404 ではなく ERROR INACTIVE ADDRESS プレースホルダを
// 返す（HTTP 200）。
func (s *Server) handlePubItem(w http.ResponseWriter, r *http.Request) {
	if !publicReadAllowed(r) {
		http.NotFound(w, r)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/pub/"), "/")
	if u, err := url.PathUnescape(id); err == nil {
		id = u
	}
	path, item, ok := s.store.HistoryPath(id)
	if !ok || !item.Published {
		s.serveInactivePlaceholder(w, r)
		return
	}
	// 動画の HLS 配信は後続タスク。現時点では画像のみ実ファイルを配信し、
	// 動画はプレースホルダへフォールバックする。
	if item.Kind == "video" {
		s.serveInactivePlaceholder(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		s.serveInactivePlaceholder(w, r)
		return
	}
	defer file.Close()
	contentType := item.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	http.ServeContent(w, r, safeFileName(item.PublicName), item.UpdatedAt, file)
}

func (s *Server) serveInactivePlaceholder(w http.ResponseWriter, r *http.Request) {
	contentType := deletedContentType(r.URL.Path)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Content-Disposition", `inline; filename="inactive.jpg"`)
	if contentType == "image/png" {
		_ = png.Encode(w, inactiveImage())
		return
	}
	_ = jpeg.Encode(w, inactiveImage(), &jpeg.Options{Quality: 90})
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
	if requestedID := streamRequestID(r); requestedID != "" && s.obsMediaActive(requestedID) {
		if s.serveLHLSArtifact(w, r, requestedID) {
			return
		}
		if s.serveLLHLSProxy(w, r, requestedID) {
			return
		}
		s.serveGeneratedFile(w, r, video.PlaylistName(requestedID), "application/vnd.apple.mpegurl", "current.m3u8", time.Now())
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
	if requestedID := streamRequestID(r); requestedID != "" && s.obsMediaActive(requestedID) {
		if s.serveLHLSArtifact(w, r, requestedID) {
			return
		}
		if s.serveLLHLSProxy(w, r, requestedID) {
			return
		}
		fileName := filepath.Base(r.URL.Path)
		if !isHLSSegmentName(fileName) {
			http.NotFound(w, r)
			return
		}
		s.serveGeneratedFile(w, r, fileName, "video/mp2t", fileName, time.Now())
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

func (s *Server) obsMediaActive(id string) bool {
	if s.obs == nil || id == "" {
		return false
	}
	status := s.obs.Status()
	return status.Connected && status.MediaID == id
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

// serveLHLSArtifact serves community-LHLS playlists and fMP4 segments from the
// active OBS session's private sink directory. It returns true when it has
// handled the request (i.e. LHLS is the active transport), so the HLS-family
// handlers fall back to the standard MPEG-TS path only for non-LHLS modes.
func (s *Server) serveLHLSArtifact(w http.ResponseWriter, r *http.Request, id string) bool {
	if s.obs == nil {
		return false
	}
	if obsrtmp.NormalizeLatencyMode(s.obs.Status().Latency.Mode) != obsrtmp.LatencyModeLHLS {
		return false
	}
	name := filepath.Base(r.URL.Path)
	if isOBSEntryPlaylistAlias(id, name) {
		name = "master.m3u8"
	}
	path, ok := s.obs.LHLSPublicFile(id, name)
	if !ok {
		http.NotFound(w, r)
		return true
	}
	s.serveGeneratedAbsFile(w, r, path, lhlsContentType(name), name, time.Now())
	return true
}

// serveLLHLSProxy forwards LL-HLS playlist/segment requests for the active OBS
// session to its MediaMTX sidecar. It returns true when LL-HLS is the active
// transport and the request was proxied, so the HLS-family handlers do not fall
// back to the standard MPEG-TS path.
func (s *Server) serveLLHLSProxy(w http.ResponseWriter, r *http.Request, id string) bool {
	if s.obs == nil {
		return false
	}
	status := s.obs.Status()
	mode := obsrtmp.NormalizeLatencyMode(status.Latency.Mode)
	if mode != obsrtmp.LatencyModeLLHLS && status.Latency.Transport != obsrtmp.LatencyModeRTSPT {
		return false
	}
	name := filepath.Base(r.URL.Path)
	if isOBSEntryPlaylistAlias(id, name) {
		name = "index.m3u8"
	}
	return s.obs.ProxyLLHLS(w, r, id, name)
}

func isOBSEntryPlaylistAlias(id, name string) bool {
	return name == "." || name == "/" || name == "current.m3u8" || name == video.PlaylistName(id)
}

func (s *Server) serveGeneratedAbsFile(w http.ResponseWriter, r *http.Request, absPath, contentType, publicName string, modTime time.Time) {
	file, err := os.Open(absPath)
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
	obsStatus := s.obsState()
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
	current := s.store.Current() // 単一読取（torn read 解消: Task 4a）
	if current != nil {
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
		if videoEnabled && (videoStatus.HLS || videoStatus.Active) {
			streamPath := hlsURLPath(current.ID)
			hlsURL = imageURLBase + streamPath
			if tunnelURLBase != "" {
				publicHLSURL = tunnelURLBase + streamPath
			}
		}
	} else {
		videoPlayer := s.videoPlayerEmptyState()
		state := map[string]interface{}{
			"imageURL":      imageURL,
			"videoURL":      videoURL,
			"hlsURL":        hlsURL,
			"localImageURL": localImageURL,
			"videoPlayer":   videoPlayer,
			"obs":           obsStatus,
			"obsLatency":    s.obsLatencyProfile(),
		}
		shareURL, shareURLLabel := primaryShareURL(state)
		return withResolvedShareURLs(s.stateWithMedia(r, current, upnpResult, tunnelStatus, videoPlayer, obsStatus, imageURL, videoURL, hlsURL, shareURL, shareURLLabel, publicImageURL, publicVideoURL, publicHLSURL, localImageURL, previewImageURL))
	}
	if imageURL == "" {
		imageURL = ""
	}
	videoPlayer := s.videoPlayerStateForID("")
	if current != nil {
		videoPlayer = s.videoPlayerStateForID(current.ID)
	}
	shareURL, shareURLLabel := primaryShareURL(map[string]interface{}{
		"imageURL":      imageURL,
		"videoURL":      videoURL,
		"hlsURL":        hlsURL,
		"localImageURL": localImageURL,
		"videoPlayer":   videoPlayer,
		"obs":           obsStatus,
		"obsLatency":    s.obsLatencyProfile(),
		"current":       current,
	})

	return withResolvedShareURLs(map[string]interface{}{
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
		"obs":             obsStatus,
		"pairing":         s.pairingState(),
		"videoQueue":      s.videoQueueState(),
		"ytdlpAuth":       ytdlpauth.Status(),
		"ingest":          s.ingestState(),
		"toolInstall":     video.ToolInstallStatus(),
		"current":         current,
		"history":         s.historyState(),
		"remoteAddr":      r.RemoteAddr,
	})
}

func (s *Server) stateWithMedia(r *http.Request, current *library.CurrentImage, upnpResult upnp.Result, tunnelStatus map[string]interface{}, videoPlayer map[string]interface{}, obsStatus obsrtmp.Status, imageURL, videoURL, hlsURL, shareURL, shareURLLabel, publicImageURL, publicVideoURL, publicHLSURL, localImageURL, previewImageURL string) map[string]interface{} {
	return withResolvedShareURLs(map[string]interface{}{
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
		"obs":             obsStatus,
		"pairing":         s.pairingState(),
		"videoQueue":      s.videoQueueState(),
		"ytdlpAuth":       ytdlpauth.Status(),
		"ingest":          s.ingestState(),
		"toolInstall":     video.ToolInstallStatus(),
		"current":         current,
		"history":         s.historyState(),
		"remoteAddr":      r.RemoteAddr,
	})
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
			"sourceKind":   item.SourceKind,
			"targetMode":   historyTargetMode(item.CurrentImage),
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

func historyTargetMode(item library.CurrentImage) string {
	switch {
	case item.SourceKind == "obs" || strings.HasPrefix(item.PublicName, "obs-"):
		return "file"
	case item.SourceKind == "soundcloud" || item.SourceKind == "local_audio" || item.SourceKind == "remote_audio":
		return "link"
	case item.SourceKind != "":
		return "link"
	default:
		return "file"
	}
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

func (s *Server) musicModeEnabled() bool {
	appSettings, err := settings.Load()
	if err != nil {
		return false
	}
	return appSettings.VideoPlayerEnabled && appSettings.MusicModeEnabled
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
	state := map[string]interface{}{
		"enabled":          enabled,
		"musicModeEnabled": enabled && s.musicModeEnabled(),
		"status":           status,
		"quality":          s.videoQualityPreset(),
	}
	if encoder, ok := video.CurrentVideoEncoder(); ok {
		state["encoder"] = encoder
	}
	return state
}

func (s *Server) videoPlayerEmptyState() map[string]interface{} {
	enabled := s.videoPlayerEnabled()
	status := video.Result{Message: "VRChat動画出力はまだ生成されていません。"}
	if !enabled {
		status = video.Result{Message: "VRChat video player support is disabled."}
	}
	return map[string]interface{}{
		"enabled":          enabled,
		"musicModeEnabled": enabled && s.musicModeEnabled(),
		"status":           status,
		"quality":          s.videoQualityPreset(),
	}
}

func (s *Server) musicQualityPreset() video.QualityPreset {
	appSettings, err := settings.Load()
	if err != nil {
		return video.ResolveQualityForMusic("auto", 0, 0)
	}
	preset := video.ResolveQualityForMusic(appSettings.VideoQualityMode, appSettings.NetworkMbps, appSettings.NetworkUploadMbps)
	if active, ok := video.ActiveQuality(s.store.Dir()); ok {
		return video.BitrateOnlyPreset(preset, active)
	}
	return preset
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
	encoderMode := "auto"
	musicPlaylistLatencyMode := "rtsp-ultra"
	musicPlaylistDeliveryProfile := "rtsp-ultra"
	desiredCanonicalHeight := 720
	if appSettings, err := settings.Load(); err == nil {
		encoderMode = settings.NormalizeEncoderMode(appSettings.EncoderMode)
		musicPlaylistLatencyMode = normalizeMusicPlaylistLatencyMode(appSettings.MusicPlaylistLatencyMode)
		if appSettings.MusicPlaylistDeliveryProfile != "" {
			musicPlaylistDeliveryProfile = normalizeMusicPlaylistDeliveryProfile(appSettings.MusicPlaylistDeliveryProfile)
		} else {
			musicPlaylistDeliveryProfile = musicPlaylistLatencyMode
		}
		desiredCanonicalHeight = settings.NormalizeMusicPlaylistCanonicalHeight(appSettings.MusicPlaylistCanonicalHeight)
	}
	activeDeliveryProfile := musicPlaylistDeliveryProfile
	running := false
	if s.radio != nil {
		status := s.radio.Status()
		running = status.Running
		if status.ActiveSession != nil && status.ActiveSession.DeliveryProfile != "" {
			activeDeliveryProfile = normalizeMusicPlaylistDeliveryProfile(status.ActiveSession.DeliveryProfile)
		}
	}
	activeCanonicalHeight := s.activeMusicPlaylistCanonicalHeight()
	return map[string]interface{}{
		"mode":                         preset.Mode,
		"encoderMode":                  encoderMode,
		"musicPlaylistLatencyMode":     musicPlaylistLatencyMode,
		"musicPlaylistDeliveryProfile": musicPlaylistDeliveryProfile,
		"desiredDeliveryProfile":       musicPlaylistDeliveryProfile,
		"activeDeliveryProfile":        activeDeliveryProfile,
		"desiredCanonicalHeight":       desiredCanonicalHeight,
		"activeCanonicalHeight":        activeCanonicalHeight,
		"deliveryRestartRequired":      running && activeDeliveryProfile != musicPlaylistDeliveryProfile,
		"canonicalRestartRequired":     activeCanonicalHeight != desiredCanonicalHeight,
		"effective":                    preset.Effective,
		"height":                       preset.Height,
		"uploadMbps":                   preset.UploadMbps,
		"preset":                       preset,
	}
}

func (s *Server) obsLatencyProfile() obsrtmp.LatencyProfile {
	appSettings, err := settings.Load()
	if err != nil {
		return obsrtmp.NormalizeLatencyProfile("auto")
	}
	profile := obsrtmp.ResolveLatencyProfile(appSettings.OBSLatencyMode, appSettings.NetworkUploadMbps)
	if appSettings.OBSDVREnabled {
		profile = obsrtmp.EnableDVR(profile)
	}
	return profile
}

func (s *Server) obsState() obsrtmp.Status {
	if s.obs == nil {
		return obsrtmp.Status{Message: "OBS RTMP受信機能は利用できません。"}
	}
	status := s.obs.Status()
	status.Capabilities = obsrtmp.LatencyCapabilities()
	applyOBSPreviewURL(&status, s.adminPath, s.obs.HLSPreviewReady)
	syntheticRows := obsConnectionRows(status)
	status.Connections = mergeOBSConnectionRows(s.obs.ConnectionRows(250*time.Millisecond), syntheticRows)
	return status
}

func applyOBSPreviewURL(status *obsrtmp.Status, adminPath func(string) string, ready func(id, name string) bool) {
	if status == nil || strings.TrimSpace(status.MediaID) == "" || adminPath == nil {
		return
	}
	name := video.PlaylistName(status.MediaID)
	if ready != nil && !ready(status.MediaID, name) {
		return
	}
	status.PreviewURL = adminPath("/stream/" + url.PathEscape(status.MediaID) + "/" + name)
}

func obsConnectionRows(status obsrtmp.Status) []obsrtmp.ConnectionStatus {
	if !status.Connected || strings.TrimSpace(status.MediaID) == "" {
		return nil
	}
	rows := make([]obsrtmp.ConnectionStatus, 0, 2)
	if strings.TrimSpace(status.RTSPTURL) != "" {
		lag := estimateRTSPLagSeconds(status.Latency)
		rows = append(rows, obsrtmp.ConnectionStatus{
			IP:         hostFromStreamURL(status.RTSPTURL),
			Protocol:   "RTSP/TCP",
			Device:     "Unknown",
			State:      "接続中",
			Quality:    "良好",
			LagSeconds: lag,
			LagLevel:   obsLagLevel(lag),
			Note:       "RTSP/TCP経路のプロファイル推定です",
		})
	}
	if strings.TrimSpace(status.PreviewURL) != "" {
		lag := estimateHLSPreviewLagSeconds(status.Latency)
		rows = append(rows, obsrtmp.ConnectionStatus{
			IP:         "local",
			Protocol:   "HLS Preview",
			Device:     "Browser",
			State:      "接続中",
			Quality:    "通常",
			LagSeconds: lag,
			LagLevel:   obsLagLevel(lag),
			Note:       "管理画面プレビュー用のローカルHLSです",
		})
	}
	return rows
}

func mergeOBSConnectionRows(realRows, syntheticRows []obsrtmp.ConnectionStatus) []obsrtmp.ConnectionStatus {
	if len(realRows) == 0 {
		return syntheticRows
	}
	rows := make([]obsrtmp.ConnectionStatus, 0, len(realRows)+len(syntheticRows))
	rows = append(rows, realRows...)
	for _, row := range syntheticRows {
		if row.Protocol == "HLS Preview" {
			rows = append(rows, row)
		}
	}
	return rows
}

func hostFromStreamURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "unknown"
	}
	return parsed.Hostname()
}

func estimateRTSPLagSeconds(profile obsrtmp.LatencyProfile) float64 {
	switch profile.Mode {
	case obsrtmp.LatencyModeRTSPRealtime:
		return 0.8
	case obsrtmp.LatencyModeRTSPUltra:
		return 1.5
	case obsrtmp.LatencyModeRTSPLow:
		return 3.5
	case obsrtmp.LatencyModeHLS:
		return 5
	case obsrtmp.LatencyModeHLSHigh:
		return 10
	default:
		return 4
	}
}

func estimateHLSPreviewLagSeconds(profile obsrtmp.LatencyProfile) float64 {
	lag := estimateRTSPLagSeconds(profile) + 1.7
	if lag < 2.5 {
		return 2.5
	}
	return lag
}

func obsLagLevel(seconds float64) string {
	switch {
	case seconds <= 1.2:
		return "good"
	case seconds <= 2.5:
		return "ok"
	case seconds <= 5:
		return "warn"
	default:
		return "bad"
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

func normalizeMusicPlaylistLatencyMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "rtsp-low", "rtsp-realtime":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "rtsp-ultra"
	}
}

func normalizeMusicPlaylistDeliveryProfile(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "hls-high", "hls", "rtsp-low", "rtsp-ultra", "rtsp-realtime":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "rtsp-ultra"
	}
}

func musicPlaylistRTSPLatency(profile string) string {
	switch normalizeMusicPlaylistDeliveryProfile(profile) {
	case "rtsp-low", "rtsp-ultra", "rtsp-realtime":
		return normalizeMusicPlaylistDeliveryProfile(profile)
	default:
		return "rtsp-ultra"
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

func writeJSONError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
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

// isSoundCloudURL reports whether rawURL points to a SoundCloud page. It
// duplicates video.isSoundCloudURL (unexported) to keep server.go self-
// contained for URL dispatch decisions.
func isSoundCloudURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "soundcloud.com", "www.soundcloud.com", "m.soundcloud.com", "on.soundcloud.com":
		return true
	}
	return false
}

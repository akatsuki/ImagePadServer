package obsrtmp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

// mediaMTXPorts are the loopback management/HLS ports and the advertised RTSP
// port for one app-owned MediaMTX sidecar.
type mediaMTXPorts struct {
	API         int
	HLS         int
	RTMP        int
	RTSP        int
	RTP         int
	RTCP        int
	BackendRTSP int
	BackendRTP  int
	BackendRTCP int
}

// mediaMTXSessionConfig describes a single OBS session's MediaMTX instance:
// one publish path protected by a per-session credential, with every protocol
// except RTSP and HLS disabled.
type mediaMTXSessionConfig struct {
	Path           string
	PublishUser    string
	PublishPass    string
	Ports          mediaMTXPorts
	AdvertiseHost  string
	DebugLogPath   string
	HLSVariant     string
	HLSAlwaysRemux bool
	HLSDirectory   string
	// EnableRTMP opens a loopback RTMP ingest (the playlist radio's
	// persistent publisher pushes FLV there).
	EnableRTMP bool
}

// renderMediaMTXConfig renders a minimal MediaMTX YAML configuration. Only the
// loopback API, the loopback LL-HLS server, and the advertised RTSP/TCP server
// are enabled; RTMP, WebRTC, SRT, and MoQ listeners are disabled. Publishing is
// restricted to the single app-owned path with a per-session credential from
// loopback only.
func renderMediaMTXConfig(cfg mediaMTXSessionConfig) string {
	var b strings.Builder
	b.WriteString("logLevel: debug\n")
	if cfg.DebugLogPath != "" {
		b.WriteString("logDestinations: [stdout, file]\n")
		fmt.Fprintf(&b, "logFile: %q\n", filepath.ToSlash(cfg.DebugLogPath))
	} else {
		b.WriteString("logDestinations: [stdout]\n")
	}
	b.WriteString("readTimeout: 10s\n")
	b.WriteString("writeTimeout: 10s\n")

	b.WriteString("api: yes\n")
	fmt.Fprintf(&b, "apiAddress: 127.0.0.1:%d\n", cfg.Ports.API)

	// Disable everything that is not RTSP or HLS (plus the loopback RTMP
	// ingest when the session asks for it).
	if cfg.EnableRTMP && cfg.Ports.RTMP > 0 {
		b.WriteString("rtmp: yes\n")
		fmt.Fprintf(&b, "rtmpAddress: 127.0.0.1:%d\n", cfg.Ports.RTMP)
	} else {
		b.WriteString("rtmp: no\n")
	}
	b.WriteString("webrtc: no\n")
	b.WriteString("srt: no\n")
	b.WriteString("moq: no\n")

	b.WriteString("rtsp: yes\n")
	b.WriteString("rtspTransports: [tcp, udp]\n")
	b.WriteString("rtspEncryption: \"no\"\n")
	fmt.Fprintf(&b, "rtspAddress: 127.0.0.1:%d\n", cfg.Ports.mediaMTXRTSPPort())
	fmt.Fprintf(&b, "rtpAddress: 127.0.0.1:%d\n", cfg.Ports.mediaMTXRTPPort())
	fmt.Fprintf(&b, "rtcpAddress: 127.0.0.1:%d\n", cfg.Ports.mediaMTXRTCPPort())

	b.WriteString("hls: yes\n")
	fmt.Fprintf(&b, "hlsAddress: 127.0.0.1:%d\n", cfg.Ports.HLS)
	hlsVariant := strings.TrimSpace(cfg.HLSVariant)
	if hlsVariant == "" {
		hlsVariant = "lowLatency"
	}
	fmt.Fprintf(&b, "hlsVariant: %s\n", hlsVariant)
	if cfg.HLSAlwaysRemux {
		b.WriteString("hlsAlwaysRemux: yes\n")
	} else {
		b.WriteString("hlsAlwaysRemux: no\n")
	}
	b.WriteString("hlsEncryption: no\n")
	if cfg.HLSDirectory != "" {
		fmt.Fprintf(&b, "hlsDirectory: %q\n", filepath.ToSlash(cfg.HLSDirectory))
	}

	// Per-session publish credential, restricted to loopback and to the single
	// owned path. Anonymous readers can access only the randomized active path.
	b.WriteString("authInternalUsers:\n")
	fmt.Fprintf(&b, "  - user: %s\n", cfg.PublishUser)
	fmt.Fprintf(&b, "    pass: %s\n", cfg.PublishPass)
	b.WriteString("    ips: ['127.0.0.1/32']\n")
	b.WriteString("    permissions:\n")
	fmt.Fprintf(&b, "      - action: publish\n        path: %s\n", cfg.Path)
	b.WriteString("  - user: any\n")
	b.WriteString("    permissions:\n")
	fmt.Fprintf(&b, "      - action: read\n        path: %s\n", cfg.Path)
	fmt.Fprintf(&b, "      - action: playback\n        path: %s\n", cfg.Path)
	// The API and metrics endpoints are themselves gated by authInternalUsers;
	// without this the app could not health-check or manage its own loopback
	// MediaMTX. Restricted to loopback like every other permission here.
	b.WriteString("  - user: any\n")
	b.WriteString("    ips: ['127.0.0.1/32']\n")
	b.WriteString("    permissions:\n")
	b.WriteString("      - action: api\n")
	b.WriteString("      - action: metrics\n")
	b.WriteString("      - action: pprof\n")

	b.WriteString("paths:\n")
	fmt.Fprintf(&b, "  %s:\n", cfg.Path)
	b.WriteString("    source: publisher\n")
	return b.String()
}

// managedProcess is the owned MediaMTX child process. The runtime only ever
// signals the handle it started, never a process discovered by name or PID, so
// it cannot terminate an unrelated process.
type managedProcess interface {
	pid() int
	stop() error
	kill() error
	done() <-chan error
}

type osManagedProcess struct {
	cmd  *exec.Cmd
	exit chan error
}

func (p *osManagedProcess) pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *osManagedProcess) stop() error {
	if p.cmd.Process == nil {
		return nil
	}
	// MediaMTX has no portable graceful signal on Windows; Interrupt is a
	// best effort and the runtime falls back to kill after the grace period.
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

func (p *osManagedProcess) kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *osManagedProcess) done() <-chan error { return p.exit }

func realStartMediaMTXProcess(ctx context.Context, exe, configPath string) (managedProcess, error) {
	_ = ctx
	cmd := exec.Command(exe, configPath)
	cmd.Dir = filepath.Dir(configPath)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MediaMTX: %w", err)
	}
	proc := &osManagedProcess{cmd: cmd, exit: make(chan error, 1)}
	pid := proc.pid()
	_ = registerMediaMTXProcess(pid)
	go func() {
		err := cmd.Wait()
		_ = unregisterMediaMTXProcess(pid)
		proc.exit <- err
	}()
	return proc, nil
}

// mediaMTXRuntime owns one MediaMTX sidecar process for the lifetime of an OBS
// session and proxies its LL-HLS output to the public surface.
type mediaMTXRuntime struct {
	exe        string
	cfg        mediaMTXSessionConfig
	httpClient *http.Client
	stopGrace  time.Duration

	startProcess func(ctx context.Context, exe, configPath string) (managedProcess, error)
	checkHealth  func(ctx context.Context, apiBase string) error

	mu         sync.Mutex
	proc       managedProcess
	dir        string
	configPath string
	stopped    bool
}

func newMediaMTXRuntime(exe string, cfg mediaMTXSessionConfig) *mediaMTXRuntime {
	return &mediaMTXRuntime{
		exe:          exe,
		cfg:          cfg,
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		stopGrace:    5 * time.Second,
		startProcess: realStartMediaMTXProcess,
		checkHealth:  defaultMediaMTXHealthCheck,
	}
}

func defaultMediaMTXHealthCheck(ctx context.Context, apiBase string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/v3/config/global/get", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MediaMTX API status %s", resp.Status)
	}
	return nil
}

// start writes the config, launches the owned process, and waits until the API
// reports healthy, the process exits early (e.g. a port conflict), or ctx is
// done. On any failure it tears the process down before returning.
func (r *mediaMTXRuntime) start(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "imagepad-mediamtx-")
	if err != nil {
		return fmt.Errorf("create MediaMTX work directory: %w", err)
	}
	configPath := filepath.Join(dir, "mediamtx.yml")
	if err := os.WriteFile(configPath, []byte(renderMediaMTXConfig(r.cfg)), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("write MediaMTX config: %w", err)
	}

	proc, err := r.startProcess(ctx, r.exe, configPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	r.mu.Lock()
	r.proc = proc
	r.dir = dir
	r.configPath = configPath
	r.mu.Unlock()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	apiBase := r.apiBaseURL()
	for {
		select {
		case <-ctx.Done():
			_ = r.stop(r.stopGrace)
			return ctx.Err()
		case err := <-proc.done():
			r.cleanupDir()
			if err != nil {
				return fmt.Errorf("MediaMTX exited before becoming healthy: %w", err)
			}
			return errors.New("MediaMTX exited before becoming healthy")
		case <-ticker.C:
			healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := r.checkHealth(healthCtx, apiBase)
			cancel()
			if err == nil {
				return nil
			}
		}
	}
}

// wait exposes the owned process exit so the manager can detect a sidecar
// crash while the session is running.
func (r *mediaMTXRuntime) wait() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		ch := make(chan error, 1)
		ch <- errors.New("MediaMTX not started")
		return ch
	}
	return r.proc.done()
}

// stop gracefully terminates the owned process, escalating to kill after the
// timeout, then removes the work directory. It only ever acts on the process
// this runtime started.
func (r *mediaMTXRuntime) stop(timeout time.Duration) error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	proc := r.proc
	r.mu.Unlock()

	if proc == nil {
		r.cleanupDir()
		return nil
	}

	_ = proc.stop()
	if timeout <= 0 {
		timeout = r.stopGrace
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-proc.done():
	case <-timer.C:
		_ = proc.kill()
		<-proc.done()
	}
	r.cleanupDir()
	return nil
}

func (r *mediaMTXRuntime) cleanupDir() {
	r.mu.Lock()
	dir := r.dir
	r.dir = ""
	r.mu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

func (r *mediaMTXRuntime) apiBaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", r.cfg.Ports.API)
}

func (r *mediaMTXRuntime) hlsBaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/%s", r.cfg.Ports.HLS, r.cfg.Path)
}

// publishURL is the loopback RTSP target FFmpeg publishes to, carrying the
// per-session credential.
func (r *mediaMTXRuntime) publishURL() string {
	return fmt.Sprintf("rtsp://%s:%s@127.0.0.1:%d/%s",
		r.cfg.PublishUser, r.cfg.PublishPass, r.cfg.Ports.mediaMTXRTSPPort(), r.cfg.Path)
}

// rtmpPublishURL is the loopback RTMP target for the playlist radio's
// persistent publisher, carrying the per-session credential as query params.
func (r *mediaMTXRuntime) rtmpPublishURL() string {
	return fmt.Sprintf("rtmp://127.0.0.1:%d/%s?user=%s&pass=%s",
		r.cfg.Ports.RTMP, r.cfg.Path, r.cfg.PublishUser, r.cfg.PublishPass)
}

// rtspURL is the advertised RTSP URL handed to players.
func (r *mediaMTXRuntime) rtspURL() string {
	host := strings.TrimSpace(r.cfg.AdvertiseHost)
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("rtsp://%s:%d/%s", host, r.cfg.Ports.RTSP, r.cfg.Path)
}

// proxyHLS forwards a public LL-HLS request to the loopback MediaMTX HLS server
// without rewriting protocol semantics. It preserves the request method, the
// session/blocking-reload query string, range requests, status codes, content
// types, and request cancellation.
func (r *mediaMTXRuntime) proxyHLS(w http.ResponseWriter, req *http.Request, name string) {
	target := r.hlsBaseURL() + "/" + name
	if req.URL.RawQuery != "" {
		target += "?" + req.URL.RawQuery
	}
	outReq, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if rng := req.Header.Get("Range"); rng != "" {
		outReq.Header.Set("Range", rng)
	}
	if im := req.Header.Get("If-None-Match"); im != "" {
		outReq.Header.Set("If-None-Match", im)
	}
	resp, err := r.httpClient.Do(outReq)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyProxyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (r *mediaMTXRuntime) hlsArtifactReady(ctx context.Context, name string) bool {
	if r == nil {
		return false
	}
	target := r.hlsBaseURL() + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

type mediaMTXConnectionKind string

const (
	mediaMTXConnectionKindRTSP mediaMTXConnectionKind = "rtsp"
	mediaMTXConnectionKindHLS  mediaMTXConnectionKind = "hls"
)

type mediaMTXConnectionList struct {
	Items []mediaMTXConnectionItem `json:"items"`
}

type mediaMTXConnectionItem struct {
	RemoteAddr string `json:"remoteAddr"`
	State      string `json:"state"`
	Path       string `json:"path"`
	Transport  string `json:"transport"`
	UserAgent  string `json:"userAgent"`
}

func (r *mediaMTXRuntime) connectionRows(ctx context.Context, path string, profile LatencyProfile) []ConnectionStatus {
	if r == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	endpoints := []struct {
		path string
		kind mediaMTXConnectionKind
	}{
		{path: "/v3/rtspsessions/list", kind: mediaMTXConnectionKindRTSP},
		{path: "/v3/hlssessions/list", kind: mediaMTXConnectionKindHLS},
	}
	var rows []ConnectionStatus
	seen := map[string]bool{}
	for _, endpoint := range endpoints {
		body, ok := r.fetchAPI(ctx, endpoint.path)
		if !ok {
			continue
		}
		for _, row := range mediaMTXConnectionRowsFromList(body, path, endpoint.kind, profile) {
			key := row.Protocol + "\x00" + row.IP
			if seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, row)
		}
	}
	return rows
}

func (r *mediaMTXRuntime) fetchAPI(ctx context.Context, path string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiBaseURL()+path, nil)
	if err != nil {
		return nil, false
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, false
	}
	return data, true
}

func mediaMTXConnectionRowsFromList(body []byte, path string, kind mediaMTXConnectionKind, profile LatencyProfile) []ConnectionStatus {
	var list mediaMTXConnectionList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil
	}
	path = strings.TrimSpace(path)
	rows := make([]ConnectionStatus, 0, len(list.Items))
	for _, item := range list.Items {
		if path != "" && strings.TrimSpace(item.Path) != path {
			continue
		}
		if kind == mediaMTXConnectionKindRTSP && !strings.EqualFold(strings.TrimSpace(item.State), "read") {
			continue
		}
		host := mediaMTXRemoteHost(item.RemoteAddr)
		if host == "" {
			continue
		}
		rows = append(rows, mediaMTXConnectionRow(host, item, kind, profile))
	}
	return rows
}

func mediaMTXConnectionRow(host string, item mediaMTXConnectionItem, kind mediaMTXConnectionKind, profile LatencyProfile) ConnectionStatus {
	protocol := "RTSP"
	quality := "良好"
	lag := mediaMTXEstimateRTSPLagSeconds(profile)
	note := "MediaMTXのRTSP読者セッションです"
	if strings.EqualFold(strings.TrimSpace(item.Transport), "tcp") {
		protocol = "RTSP/TCP"
	}
	if kind == mediaMTXConnectionKindHLS {
		protocol = "HLS"
		quality = "通常"
		lag = mediaMTXEstimateHLSLagSeconds(profile)
		note = "MediaMTXのHLS読者セッションです"
	}
	return ConnectionStatus{
		IP:         host,
		Protocol:   protocol,
		Device:     mediaMTXDeviceLabel(item.UserAgent),
		State:      "接続中",
		Quality:    quality,
		LagSeconds: lag,
		LagLevel:   mediaMTXLagLevel(lag),
		Note:       note,
	}
}

func mediaMTXRemoteHost(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return host
	}
	return remoteAddr
}

func mediaMTXDeviceLabel(userAgent string) string {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return "Unknown"
	}
	lower := strings.ToLower(ua)
	if strings.Contains(lower, "mozilla/") {
		return "Browser"
	}
	return ua
}

func mediaMTXEstimateRTSPLagSeconds(profile LatencyProfile) float64 {
	switch profile.Mode {
	case LatencyModeRTSPRealtime:
		return 0.8
	case LatencyModeRTSPUltra:
		return 1.5
	case LatencyModeRTSPLow:
		return 3.5
	case LatencyModeHLS:
		return 5
	case LatencyModeHLSHigh:
		return 10
	default:
		return 4
	}
}

func mediaMTXEstimateHLSLagSeconds(profile LatencyProfile) float64 {
	lag := mediaMTXEstimateRTSPLagSeconds(profile) + 1.7
	if lag < 2.5 {
		return 2.5
	}
	return lag
}

func mediaMTXLagLevel(seconds float64) string {
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

func importMediaMTXHLS(outDir, id, hlsDir, pathName string) ([]string, error) {
	playlistPath, baseDir, err := findMediaMTXPlaylist(hlsDir, pathName)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return nil, err
	}

	segmentPattern := video.SegmentPattern(id)
	segmentIndex := 0
	files := []string{}
	var rewritten strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			rewritten.WriteString(line)
			rewritten.WriteByte('\n')
			continue
		}
		sourceName := strings.Split(trimmed, "?")[0]
		sourcePath := filepath.Join(baseDir, filepath.FromSlash(sourceName))
		destName := fmt.Sprintf(segmentPattern, segmentIndex)
		segmentIndex++
		destPath := filepath.Join(outDir, destName)
		if err := copyMediaMTXFile(destPath, sourcePath); err != nil {
			return nil, err
		}
		files = append(files, destPath)
		rewritten.WriteString(destName)
		rewritten.WriteByte('\n')
	}
	text := rewritten.String()
	if !strings.Contains(text, "#EXT-X-ENDLIST") {
		text += "#EXT-X-ENDLIST\n"
	}
	playlistOut := filepath.Join(outDir, video.PlaylistName(id))
	if err := os.WriteFile(playlistOut, []byte(text), 0600); err != nil {
		return nil, err
	}
	return append([]string{playlistOut}, files...), nil
}

func findMediaMTXPlaylist(hlsDir, pathName string) (string, string, error) {
	if strings.TrimSpace(hlsDir) == "" {
		return "", "", errors.New("MediaMTX HLS directory is empty")
	}
	candidates := []string{
		filepath.Join(hlsDir, pathName, "index.m3u8"),
		filepath.Join(hlsDir, pathName, "stream.m3u8"),
		filepath.Join(hlsDir, "index.m3u8"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, filepath.Dir(candidate), nil
		}
	}
	var found string
	_ = filepath.WalkDir(hlsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || found != "" {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".m3u8") {
			found = path
		}
		return nil
	})
	if found == "" {
		return "", "", fmt.Errorf("MediaMTX HLS playlist not found in %s", hlsDir)
	}
	return found, filepath.Dir(found), nil
}

func copyMediaMTXFile(dst, src string) error {
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

func copyProxyHeaders(dst, src http.Header) {
	for _, key := range []string{
		"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"Cache-Control", "ETag", "Last-Modified",
	} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

// llhlsRequiredTags are the Apple LL-HLS media-playlist tags that must be
// present before the public URL is exposed.
var llhlsRequiredTags = []string{
	"#EXT-X-SERVER-CONTROL",
	"#EXT-X-PART-INF",
	"#EXT-X-MAP",
	"#EXT-X-PART",
	"#EXT-X-PRELOAD-HINT",
}

// fetch retrieves a file from the loopback MediaMTX HLS server.
func (r *mediaMTXRuntime) fetch(ctx context.Context, name string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.hlsBaseURL()+"/"+name, nil)
	if err != nil {
		return nil, false
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false
	}
	return data, true
}

// llhlsReady reports whether the LL-HLS media playlist advertises the full set
// of low-latency tags, following the master playlist's first variant if needed.
func (r *mediaMTXRuntime) llhlsReady(ctx context.Context) bool {
	master, ok := r.fetch(ctx, "index.m3u8")
	if !ok {
		return false
	}
	if llhlsMediaReady(master) {
		return true
	}
	if variant := firstVariantURI(master); variant != "" {
		if media, ok := r.fetch(ctx, variant); ok {
			return llhlsMediaReady(media)
		}
	}
	return false
}

// pathReady reports whether the MediaMTX API shows the owned path with an
// active publisher.
func (r *mediaMTXRuntime) pathReady(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiBaseURL()+"/v3/paths/get/"+r.cfg.Path, nil)
	if err != nil {
		return false
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return false
	}
	text := strings.ReplaceAll(string(data), " ", "")
	return strings.Contains(text, "\"ready\":true")
}

// firstVariantURI returns the first non-comment line of a master playlist.
func firstVariantURI(master []byte) string {
	for _, line := range strings.Split(string(master), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

// llhlsMediaReady reports whether a media playlist advertises the full set of
// LL-HLS low-latency tags.
func llhlsMediaReady(playlist []byte) bool {
	text := string(playlist)
	for _, tag := range llhlsRequiredTags {
		if !strings.Contains(text, tag) {
			return false
		}
	}
	return true
}

// freeLoopbackPort asks the OS for an unused TCP port on the loopback
// interface. There is an inherent bind race, but MediaMTX claims the port
// moments later and a conflict surfaces as an early process exit during start.
func freeLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// freeUDPPortPair finds an even/odd consecutive UDP port pair (RTP, RTCP).
// MediaMTX rejects an odd rtpAddress port outright ("RTP port must be even")
// and requires rtcpAddress = rtpAddress+1, and the pair must actually bind as
// UDP — probing with TCP can select ports inside Windows' excluded UDP ranges
// ("bind: An attempt was made to access a socket in a way forbidden...").
func freeUDPPortPair(seen map[int]bool) (int, int, error) {
	for attempt := 0; attempt < 64; attempt++ {
		probe, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return 0, 0, err
		}
		candidate := probe.LocalAddr().(*net.UDPAddr).Port
		_ = probe.Close()
		even := candidate &^ 1
		if even <= 1024 || seen[even] || seen[even+1] {
			continue
		}
		rtp, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", even))
		if err != nil {
			continue
		}
		rtcp, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", even+1))
		if err != nil {
			_ = rtp.Close()
			continue
		}
		_ = rtp.Close()
		_ = rtcp.Close()
		return even, even + 1, nil
	}
	return 0, 0, fmt.Errorf("no free UDP RTP/RTCP port pair found")
}

func allocMediaMTXPorts() (mediaMTXPorts, error) {
	var ports mediaMTXPorts
	seen := map[int]bool{}
	for _, target := range []*int{&ports.API, &ports.HLS, &ports.RTMP, &ports.RTSP, &ports.BackendRTSP} {
		for {
			port, err := freeLoopbackPort()
			if err != nil {
				return mediaMTXPorts{}, err
			}
			if seen[port] {
				continue
			}
			seen[port] = true
			*target = port
			break
		}
	}
	for _, pair := range [][2]*int{{&ports.RTP, &ports.RTCP}, {&ports.BackendRTP, &ports.BackendRTCP}} {
		rtp, rtcp, err := freeUDPPortPair(seen)
		if err != nil {
			return mediaMTXPorts{}, err
		}
		seen[rtp] = true
		seen[rtcp] = true
		*pair[0] = rtp
		*pair[1] = rtcp
	}
	return ports, nil
}

func (p mediaMTXPorts) mediaMTXRTSPPort() int {
	if p.BackendRTSP > 0 {
		return p.BackendRTSP
	}
	return p.RTSP
}

func (p mediaMTXPorts) mediaMTXRTPPort() int {
	if p.BackendRTP > 0 {
		return p.BackendRTP
	}
	return p.RTP
}

func (p mediaMTXPorts) mediaMTXRTCPPort() int {
	if p.BackendRTCP > 0 {
		return p.BackendRTCP
	}
	return p.RTCP
}

func mediaMTXCredential() (string, string, error) {
	user, err := lhlsToken()
	if err != nil {
		return "", "", err
	}
	pass, err := lhlsToken()
	if err != nil {
		return "", "", err
	}
	return "obs-" + user[:8], pass, nil
}

func mediaMTXDebugLogPath() string {
	if strings.HasSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".test.exe") ||
		strings.HasSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".test") {
		return ""
	}
	return filepath.Join(settings.Dir(), "mediamtx-rtsp-debug.log")
}

// mediaMTXPathName derives a safe MediaMTX path name from a session id, keeping
// only characters MediaMTX accepts in a path.
func mediaMTXPathName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" {
		name = "session"
	}
	return "obs_" + name
}

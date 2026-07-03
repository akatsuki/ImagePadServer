package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"imagepadserver/internal/video"

	"golang.org/x/net/websocket"
)

type browserMediaRequest struct {
	URL         string `json:"url"`
	Method      string `json:"method,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Source      string `json:"source,omitempty"`
}

type browserMediaCandidate struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	ContentType string `json:"contentType,omitempty"`
	Method      string `json:"method,omitempty"`
	Source      string `json:"source,omitempty"`
}

var browserMediaProbe = detectBrowserMediaCandidates

type cdpMessage struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func (s *Server) handleBrowserMediaCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	pageURL := strings.TrimSpace(req.URL)
	if _, err := validatePublicURL(pageURL); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if video.IsPageMediaURL(pageURL) {
		writeJSON(w, map[string]any{
			"candidates":  []browserMediaCandidate{},
			"unavailable": true,
			"message":     "このサイトはyt-dlpの結果を優先します",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	candidates, err := browserMediaProbe(ctx, pageURL)
	if err != nil {
		writeJSON(w, map[string]any{
			"candidates":  []browserMediaCandidate{},
			"unavailable": true,
			"message":     err.Error(),
		})
		return
	}
	if candidates == nil {
		candidates = []browserMediaCandidate{}
	}
	writeJSON(w, map[string]any{"candidates": candidates})
}

func detectBrowserMediaCandidates(ctx context.Context, pageURL string) ([]browserMediaCandidate, error) {
	browserPath, err := findCDPBrowser()
	if err != nil {
		return nil, err
	}
	port, err := reserveLocalPort()
	if err != nil {
		return nil, err
	}
	userDataDir, err := os.MkdirTemp("", "imagepad-cdp-profile-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(userDataDir)

	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--mute-audio",
		"--autoplay-policy=no-user-gesture-required",
		"--remote-debugging-address=127.0.0.1",
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
		"about:blank",
	}
	cmd := exec.CommandContext(ctx, browserPath, args...)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("非表示ブラウザ検出を起動できません: %w", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	wsURL, err := waitForCDPWebSocket(ctx, port)
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	events, err := runCDPNetworkProbe(ctx, wsURL, pageURL)
	if err != nil {
		return nil, err
	}
	return browserMediaCandidatesFromRequests(browserMediaRequestsFromCDPEvents(events)), nil
}

func browserMediaProbeBackend() string {
	return "cdp"
}

func findCDPBrowser() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_CDP_BROWSER")); configured != "" {
		return configured, nil
	}
	names := []string{"msedge.exe", "chrome.exe", "chromium.exe", "msedge", "google-chrome", "chromium", "chromium-browser"}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if runtime.GOOS == "windows" {
		for _, p := range []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		} {
			if p != "" {
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("非表示ブラウザ検出に使える Edge/Chrome が見つかりません")
}

func reserveLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitForCDPWebSocket(ctx context.Context, port int) (string, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	client := &http.Client{Timeout: 800 * time.Millisecond}
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("非表示ブラウザのDevTools接続が開きません")
		case <-ticker.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			var pages []struct {
				Type                 string `json:"type"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			err = json.NewDecoder(resp.Body).Decode(&pages)
			resp.Body.Close()
			if err != nil {
				continue
			}
			for _, page := range pages {
				if page.Type == "page" && page.WebSocketDebuggerURL != "" {
					return page.WebSocketDebuggerURL, nil
				}
			}
		}
	}
}

func runCDPNetworkProbe(ctx context.Context, wsURL, pageURL string) ([]cdpMessage, error) {
	origin := "http://127.0.0.1"
	ws, err := websocket.Dial(wsURL, "", origin)
	if err != nil {
		return nil, fmt.Errorf("DevTools Protocolへ接続できません: %w", err)
	}
	defer ws.Close()

	eventsCh := make(chan cdpMessage, 256)
	errCh := make(chan error, 1)
	go func() {
		for {
			var msg cdpMessage
			if err := websocket.JSON.Receive(ws, &msg); err != nil {
				errCh <- err
				return
			}
			if msg.Method != "" {
				select {
				case eventsCh <- msg:
				default:
				}
			}
		}
	}()

	send := func(id int, method string, params map[string]any) error {
		return websocket.JSON.Send(ws, map[string]any{"id": id, "method": method, "params": params})
	}
	if err := send(1, "Network.enable", nil); err != nil {
		return nil, err
	}
	if err := send(2, "Page.enable", nil); err != nil {
		return nil, err
	}
	if err := send(3, "Runtime.enable", nil); err != nil {
		return nil, err
	}
	if err := send(4, "Page.navigate", map[string]any{"url": pageURL}); err != nil {
		return nil, err
	}

	var events []cdpMessage
	playTimer := time.NewTimer(2 * time.Second)
	defer playTimer.Stop()
	done := time.NewTimer(10 * time.Second)
	defer done.Stop()
	playSent := false
	for {
		select {
		case <-ctx.Done():
			return events, ctx.Err()
		case err := <-errCh:
			if len(events) > 0 {
				return events, nil
			}
			return nil, fmt.Errorf("DevTools Protocolの受信に失敗しました: %w", err)
		case event := <-eventsCh:
			events = append(events, event)
		case <-playTimer.C:
			if !playSent {
				playSent = true
				_ = send(5, "Runtime.evaluate", map[string]any{
					"expression":   browserMediaPlayExpression,
					"awaitPromise": false,
				})
			}
		case <-done.C:
			return events, nil
		}
	}
}

func browserMediaRequestsFromCDPEvents(events []cdpMessage) []browserMediaRequest {
	var requests []browserMediaRequest
	for _, event := range events {
		switch event.Method {
		case "Network.requestWillBeSent":
			var payload struct {
				Request struct {
					URL    string `json:"url"`
					Method string `json:"method"`
				} `json:"request"`
			}
			if json.Unmarshal(event.Params, &payload) == nil && payload.Request.URL != "" {
				requests = append(requests, browserMediaRequest{
					URL:    payload.Request.URL,
					Method: payload.Request.Method,
					Source: "cdp-request",
				})
			}
		case "Network.responseReceived":
			var payload struct {
				Response struct {
					URL      string         `json:"url"`
					MimeType string         `json:"mimeType"`
					Headers  map[string]any `json:"headers"`
				} `json:"response"`
			}
			if json.Unmarshal(event.Params, &payload) != nil || payload.Response.URL == "" {
				continue
			}
			contentType := payload.Response.MimeType
			for key, value := range payload.Response.Headers {
				if strings.EqualFold(key, "content-type") {
					contentType = fmt.Sprint(value)
					break
				}
			}
			requests = append(requests, browserMediaRequest{
				URL:         payload.Response.URL,
				ContentType: contentType,
				Source:      "cdp-response",
			})
		}
	}
	return requests
}

func browserMediaCandidatesFromRequests(raw []browserMediaRequest) []browserMediaCandidate {
	seen := map[string]bool{}
	var candidates []browserMediaCandidate
	for _, req := range raw {
		u := strings.TrimSpace(req.URL)
		if u == "" || seen[u] {
			continue
		}
		if _, err := validatePublicURL(u); err != nil {
			continue
		}
		kind := classifyBrowserMediaURL(u, req.ContentType)
		if kind == "" {
			continue
		}
		seen[u] = true
		candidates = append(candidates, browserMediaCandidate{
			ID:          fmt.Sprintf("candidate-%d", len(candidates)+1),
			URL:         u,
			Kind:        kind,
			Label:       browserMediaCandidateLabel(kind, req.ContentType, u),
			ContentType: strings.TrimSpace(req.ContentType),
			Method:      strings.TrimSpace(req.Method),
			Source:      strings.TrimSpace(req.Source),
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return browserMediaKindRank(candidates[i].Kind) < browserMediaKindRank(candidates[j].Kind)
	})
	return candidates
}

func classifyBrowserMediaURL(rawURL, contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	parsed, err := url.Parse(rawURL)
	ext := ""
	if err == nil {
		ext = strings.ToLower(path.Ext(parsed.Path))
	}
	switch {
	case ct == "application/vnd.apple.mpegurl" || ct == "application/x-mpegurl" || ext == ".m3u8":
		return "hls"
	case ct == "application/dash+xml" || ext == ".mpd":
		return "dash"
	case strings.HasPrefix(ct, "video/") && ct != "video/mp2t", ext == ".mp4", ext == ".mov", ext == ".m4v", ext == ".webm":
		return "video"
	case strings.HasPrefix(ct, "audio/") || ext == ".mp3" || ext == ".m4a" || ext == ".aac" || ext == ".ogg" || ext == ".opus":
		return "audio"
	default:
		return ""
	}
}

func browserMediaKindRank(kind string) int {
	switch kind {
	case "hls":
		return 0
	case "dash":
		return 1
	case "video":
		return 2
	case "audio":
		return 3
	default:
		return 9
	}
}

func browserMediaCandidateLabel(kind, contentType, rawURL string) string {
	switch kind {
	case "hls":
		return "HLS"
	case "dash":
		return "DASH"
	case "video":
		return "動画ファイル"
	case "audio":
		return "音声ファイル"
	}
	if contentType != "" {
		return contentType
	}
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "メディア"
}

const browserMediaPlayExpression = `(() => {
  for (const video of document.querySelectorAll('video')) {
    video.muted = true;
    video.playsInline = true;
    video.play().catch(() => {});
  }
  const selectors = ['button[aria-label*=Play]', 'button[title*=Play]', '.play', '.vjs-big-play-button'];
  for (const selector of selectors) {
    const el = document.querySelector(selector);
    if (el) { el.click(); break; }
  }
})()`

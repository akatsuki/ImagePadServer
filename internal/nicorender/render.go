// Package nicorender renders a fixed Nico Nico comment snapshot into
// deterministic RGBA frames. The renderer is deliberately offline: a private
// headless browser owns one isolated profile for the duration of a job and the
// browser never receives the source URL or account cookies.
package nicorender

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"imagepadserver/internal/niconico"

	"golang.org/x/net/websocket"
)

//go:embed assets/bundle.js
var bundledRenderer []byte

// FrameSink receives one decoded, tightly packed RGBA frame at a time.
// Pixels are immutable and retain ownership for their lifetime, so a bounded
// asynchronous sink may retain the slice without copying. Sinks must not edit it.
type FrameSink interface {
	WriteRGBA(ctx context.Context, sequence uint64, pixels []byte) error
}

type RenderOptions struct {
	Width         int
	Height        int
	DurationMs    int64
	FPSNum        int64
	FPSDen        int64
	BundlePath    string
	BrowserPath   string
	RendererLabel string
	// Backend selects the video pipeline: auto (default), browser, or native.
	// Render itself remains the browser RGBA API.
	Backend string
	// CompositorPath overrides the embedded helper for explicit local diagnosis.
	CompositorPath string
	// NativeCopyOutput retains the staging-to-flat copy for diagnostic A/B tests.
	NativeCopyOutput bool
	// SpriteCompression defaults to lossless deflate after palette/gray packing.
	// "none" retains packing only for diagnosis.
	SpriteCompression string
	// Transport selects the frame transport. Empty and "png" retain the
	// existing CDP PNG path; "binary" uses the loopback RGBA transport.
	Transport string
	// ReuseUnchanged allows the renderer to emit repeat packets when the
	// niconicomments drawCanvas call reports no visual change.
	ReuseUnchanged bool
	// BatchFrames sends this many timestamps per browser request. Zero retains
	// single-frame requests. Pixel read-ahead remains capped at two frames.
	BatchFrames int
	// SparseFrames losslessly omits all-zero RGBA spans from full frames.
	SparseFrames bool
	// Progress is called after each frame is delivered. The first argument is
	// the number of completed frames (1-based), the second is the total.
	Progress func(completed, total int64)
}

type RenderReport struct {
	FrameCount     int64
	Width          int
	Height         int
	FPSNum         int64
	FPSDen         int64
	RendererLabel  string
	Backend        string
	FallbackReason string
	// Sprite payload counters exclude base64/JSON framing and draw commands.
	SpriteTextureBytes int64
	SpritePayloadBytes int64
	SpriteTextures     int
}

var (
	ErrUnavailable = errors.New("niconico comment renderer unavailable")
	findBrowser    = defaultFindBrowser
)

func (o RenderOptions) validate() error {
	if o.Width <= 0 || o.Height <= 0 || o.Width > 3840 || o.Height > 2160 {
		return fmt.Errorf("niconico: invalid render size %dx%d", o.Width, o.Height)
	}
	if o.DurationMs < 0 {
		return fmt.Errorf("niconico: negative duration %dms", o.DurationMs)
	}
	if o.BatchFrames < 0 || o.BatchFrames > 120 {
		return fmt.Errorf("niconico: invalid render batch size %d", o.BatchFrames)
	}
	if _, err := niconico.NewFrameClock(o.FPSNum, o.FPSDen); err != nil {
		return err
	}
	if o.Transport != "" && o.Transport != "png" && o.Transport != "binary" {
		return fmt.Errorf("niconico: unsupported render transport %q", o.Transport)
	}
	return nil
}

// Render draws every output frame at the output frame clock. Comment time is
// converted from milliseconds to the renderer's centisecond vpos only after
// the rational frame timestamp is computed, so source frame rate cannot speed
// up or slow down comments.
func Render(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, sink FrameSink) (RenderReport, error) {
	if sink == nil {
		return RenderReport{}, errors.New("niconico: frame sink is required")
	}
	if err := options.validate(); err != nil {
		return RenderReport{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Transport == "binary" {
		return renderBinary(ctx, snapshot, options, sink)
	}
	threads := snapshot.RendererThreads()
	clock, _ := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	frameCount := clock.FrameCountForDurationMs(options.DurationMs)
	if frameCount == 0 {
		return RenderReport{FrameCount: 0, Width: options.Width, Height: options.Height, FPSNum: options.FPSNum, FPSDen: options.FPSDen, RendererLabel: options.RendererLabel}, nil
	}

	bundlePath, cleanupBundle, err := materializeBundle(options.BundlePath)
	if err != nil {
		return RenderReport{}, err
	}
	defer cleanupBundle()
	pagePath, err := writeRendererPage(options.Width, options.Height, bundlePath, threads, nil)
	if err != nil {
		return RenderReport{}, err
	}
	defer os.Remove(pagePath)

	session, err := startBrowser(ctx, options.BrowserPath, pagePath)
	if err != nil {
		return RenderReport{}, err
	}
	defer session.close()
	if err := session.waitReady(ctx, false); err != nil {
		return RenderReport{}, err
	}

	for frame := int64(0); frame < frameCount; frame++ {
		select {
		case <-ctx.Done():
			return RenderReport{}, ctx.Err()
		default:
		}
		vpos := clock.CommentTimeMs(frame) / 10
		pngData, err := session.drawPNG(ctx, vpos)
		if err != nil {
			return RenderReport{}, fmt.Errorf("niconico: render frame %d: %w", frame, err)
		}
		pixels, err := pngToRGBA(pngData, options.Width, options.Height)
		if err != nil {
			return RenderReport{}, fmt.Errorf("niconico: decode frame %d: %w", frame, err)
		}
		if err := sink.WriteRGBA(ctx, uint64(frame), pixels); err != nil {
			return RenderReport{}, fmt.Errorf("niconico: write frame %d: %w", frame, err)
		}
		if options.Progress != nil {
			options.Progress(frame+1, frameCount)
		}
	}
	return RenderReport{FrameCount: frameCount, Width: options.Width, Height: options.Height, FPSNum: options.FPSNum, FPSDen: options.FPSDen, RendererLabel: options.RendererLabel}, nil
}

func pngToRGBA(data []byte, width, height int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	if bounds.Dx() != width || bounds.Dy() != height {
		return nil, fmt.Errorf("frame dimensions %dx%d, want %dx%d", bounds.Dx(), bounds.Dy(), width, height)
	}
	pix := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, a := img.At(x+bounds.Min.X, y+bounds.Min.Y).RGBA()
			i := (y*width + x) * 4
			pix[i] = byte(r >> 8)
			pix[i+1] = byte(g >> 8)
			pix[i+2] = byte(b >> 8)
			pix[i+3] = byte(a >> 8)
		}
	}
	return pix, nil
}

func materializeBundle(configured string) (string, func(), error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return "", func() {}, fmt.Errorf("niconico: bundle path: %w", err)
		}
		if info, err := os.Stat(absolute); err != nil || info.IsDir() {
			if err == nil {
				err = fmt.Errorf("path is a directory")
			}
			return "", func() {}, fmt.Errorf("niconico: bundle: %w", err)
		}
		return absolute, func() {}, nil
	}
	f, err := os.CreateTemp("", "imagepad-niconicomments-*.js")
	if err != nil {
		return "", func() {}, fmt.Errorf("niconico: materialize bundle: %w", err)
	}
	name := f.Name()
	if _, err := f.Write(bundledRenderer); err != nil {
		f.Close()
		os.Remove(name)
		return "", func() {}, fmt.Errorf("niconico: materialize bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", func() {}, fmt.Errorf("niconico: materialize bundle: %w", err)
	}
	return name, func() { _ = os.Remove(name) }, nil
}

func writeRendererPage(width, height int, bundlePath string, threads []niconico.RendererThread, transport *frameTransportConfig) (string, error) {
	data, err := json.Marshal(threads)
	if err != nil {
		return "", fmt.Errorf("niconico: encode renderer input: %w", err)
	}
	// Prevent comment text from terminating the JSON script element.
	data = bytes.ReplaceAll(data, []byte("<"), []byte(`\u003c`))
	bundleURL := fileURL(bundlePath)
	transportScript := ""
	if transport != nil {
		transportScript = transport.script(width, height)
	}
	html := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><style>html,body{margin:0;padding:0;overflow:hidden;background:transparent}canvas{display:block}</style><canvas id="canvas" width="%d" height="%d"></canvas><script id="comment-data" type="application/json">%s</script><script src="%s"></script><script>
(() => {
  try {
    const data = JSON.parse(document.getElementById('comment-data').textContent);
    const canvas = document.getElementById('canvas');
    window.__niconi = new NiconiComments(canvas, data, {format:'v1', mode:'html5', keepCA:false, lazy:false});
    window.__niconiReady = true;
    window.__draw = async (vpos) => { window.__niconi.drawCanvas(vpos, true); await new Promise(requestAnimationFrame); return canvas.toDataURL('image/png'); };
  } catch (e) {
    window.__niconiError = String(e && (e.stack || e.message) || e);
  }
})();
</script>%s`, width, height, data, bundleURL, transportScript)
	f, err := os.CreateTemp("", "imagepad-niconi-page-*.html")
	if err != nil {
		return "", fmt.Errorf("niconico: create renderer page: %w", err)
	}
	name := f.Name()
	if _, err := io.WriteString(f, html); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("niconico: write renderer page: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("niconico: close renderer page: %w", err)
	}
	return name, nil
}

func fileURL(name string) string {
	p := filepath.ToSlash(name)
	u := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(p, "/")}
	return u.String()
}

type browserSession struct {
	cmd  *exec.Cmd
	ws   *websocket.Conn
	next int
}

func startBrowser(ctx context.Context, configured, pagePath string) (*browserSession, error) {
	browserPath := strings.TrimSpace(configured)
	if browserPath == "" {
		var err error
		browserPath, err = findBrowser()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	port, err := reservePort()
	if err != nil {
		return nil, err
	}
	profile, err := os.MkdirTemp("", "imagepad-niconi-profile-*")
	if err != nil {
		return nil, err
	}
	args := []string{"--headless=new", "--allow-file-access-from-files", "--disable-background-networking", "--disable-component-update", "--disable-extensions", "--disable-sync", "--no-proxy-server", "--remote-debugging-address=127.0.0.1", fmt.Sprintf("--remote-debugging-port=%d", port), "--remote-allow-origins=*", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check"}
	args = append(args, "--disable-gpu", "about:blank")
	cmd := exec.CommandContext(ctx, browserPath, args...)
	// Nil uses the OS null device directly. io.Discard would create output
	// copy goroutines, making Wait wait for inherited pipes in descendants.
	if err := cmd.Start(); err != nil {
		os.RemoveAll(profile)
		return nil, fmt.Errorf("%w: start browser: %v", ErrUnavailable, err)
	}
	wsURL, err := waitWebSocket(ctx, port)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.RemoveAll(profile)
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	ws, err := websocket.Dial(wsURL, "", "http://127.0.0.1")
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.RemoveAll(profile)
		return nil, fmt.Errorf("%w: DevTools接続: %v", ErrUnavailable, err)
	}
	s := &browserSession{cmd: cmd, ws: ws, next: 1}
	if _, err := s.call(ctx, "Page.enable", nil); err != nil {
		s.close()
		return nil, err
	}
	if _, err := s.call(ctx, "Runtime.enable", nil); err != nil {
		s.close()
		return nil, err
	}
	if _, err := s.call(ctx, "Page.navigate", map[string]any{"url": fileURL(pagePath)}); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *browserSession) close() {
	if s == nil {
		return
	}
	if s.ws != nil {
		_ = s.ws.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
		if len(s.cmd.Args) > 0 {
			for _, arg := range s.cmd.Args {
				if strings.HasPrefix(arg, "--user-data-dir=") {
					_ = os.RemoveAll(strings.TrimPrefix(arg, "--user-data-dir="))
				}
			}
		}
	}
}

func (s *browserSession) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := s.next
	s.next++
	if err := s.ws.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return nil, err
	}
	if err := websocket.JSON.Send(s.ws, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := websocket.JSON.Receive(s.ws, &msg); err != nil {
			return nil, err
		}
		if msg.ID != id {
			continue
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, fmt.Errorf("CDP %s: %s", method, string(msg.Error))
		}
		return msg.Result, nil
	}
}

func (s *browserSession) waitReady(ctx context.Context, binary bool) error {
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		expression := "({ready:Boolean(window.__niconiReady),error:String(window.__niconiError||''),url:String(location.href),type:typeof NiconiComments,body:String(document.body&&document.body.innerText||'')})"
		if binary {
			expression = "({ready:Boolean(window.__niconiReady&&window.__binaryReady),error:String(window.__niconiError||window.__binaryError||''),url:String(location.href),type:typeof NiconiComments,body:String(document.body&&document.body.innerText||'')})"
		}
		result, err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true})
		if err == nil {
			var envelope struct {
				Result struct {
					Value struct {
						Ready bool   `json:"ready"`
						Error string `json:"error"`
					} `json:"value"`
				} `json:"result"`
			}
			if json.Unmarshal(result, &envelope) == nil {
				if envelope.Result.Value.Ready {
					return nil
				}
				if envelope.Result.Value.Error != "" {
					return fmt.Errorf("niconico: renderer page: %s", envelope.Result.Value.Error)
				}
			}
		}
		if err != nil {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("niconico: renderer page did not become ready: %w", lastErr)
	}
	return errors.New("niconico: renderer page did not become ready")
}

func (s *browserSession) drawPNG(ctx context.Context, vpos int64) ([]byte, error) {
	expression := fmt.Sprintf("window.__draw(%d)", vpos)
	result, err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return nil, err
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(envelope.Result.Value, prefix) {
		return nil, errors.New("renderer returned no PNG")
	}
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(envelope.Result.Value, prefix))
}

func reservePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitWebSocket(ctx context.Context, port int) (string, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		resp, err := client.Do(req)
		if err == nil {
			var pages []struct {
				Type                 string `json:"type"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			err = json.NewDecoder(resp.Body).Decode(&pages)
			resp.Body.Close()
			if err == nil {
				for _, page := range pages {
					if page.Type == "page" && page.WebSocketDebuggerURL != "" {
						return page.WebSocketDebuggerURL, nil
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", errors.New("DevTools WebSocket did not open")
}

func defaultFindBrowser() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_NICONICO_RENDER_BROWSER")); configured != "" {
		return configured, nil
	}
	for _, name := range []string{"msedge.exe", "chrome.exe", "chromium.exe", "msedge", "google-chrome", "chromium"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if runtime.GOOS == "windows" {
		for _, p := range []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
		} {
			if p != "" {
				if info, err := os.Stat(p); err == nil && !info.IsDir() {
					return p, nil
				}
			}
		}
	}
	return "", errors.New("Edge/Chrome executable not found")
}

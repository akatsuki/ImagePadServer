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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"imagepadserver/internal/niconico"

	"golang.org/x/net/websocket"
	"golang.org/x/text/encoding/japanese"
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
	// Backend selects the video pipeline: auto (default), browser, native, or
	// timeline (WGPU first, then CPU fallback if the attempt fails).
	// Render itself remains the browser RGBA API.
	Backend string
	// CompositorPath overrides the embedded helper for explicit local diagnosis.
	CompositorPath string
	// CompositorDevice selects "warp" (default) or "hardware" for the native
	// compositor. Hardware is explicit so GPU use can be measured independently.
	CompositorDevice string
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
	// TimelineEnabled allows the opt-in WGPU comment timeline when Backend is
	// auto. Backend="timeline" forces a WGPU attempt before CPU fallback.
	TimelineEnabled bool
	// TimelineCompositorPath overrides the WGPU helper for local diagnosis.
	TimelineCompositorPath string
	// TimelineReadbackSlots selects a bounded 1..3 frame readback ring; zero is
	// the runtime default.
	TimelineReadbackSlots int
	// TimelineGPUBackend is auto, dx12, vulkan, or metal.
	TimelineGPUBackend string
	// TimelineRandomSeed optionally makes the renderer's collision fallback
	// deterministic. Nil preserves the normal Math.random behavior.
	TimelineRandomSeed *uint32
	// TimelineAssetLayout selects separate comment textures or the opt-in WGPU atlas.
	// Empty keeps the existing separate-texture layout.
	TimelineAssetLayout string
	// OnTimelineFallback is called once when a selected WGPU renderer fails and
	// the pipeline is about to retry with the CPU renderer.
	OnTimelineFallback func()

	// browserPool is injected by the owner for isolated, per-job timeline capture.
	browserPool *BrowserPool
}

// WithBrowserPool returns options that use the provided owner-scoped browser
// pool for per-job timeline capture. The pool reference remains private and is
// intentionally absent from JSON representations of RenderOptions.
func (o RenderOptions) WithBrowserPool(pool *BrowserPool) RenderOptions {
	o.browserPool = pool
	return o
}

// NewBrowserPool starts a dedicated browser owned by owner for reuse across
// jobs. Each capture still receives a fresh BrowserContext and page lease.
func NewBrowserPool(owner context.Context, options RenderOptions) (*BrowserPool, error) {
	return newBrowserProcessPool(owner, options.BrowserPath)
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
	if _, err := nativeCompositorModeArgument(o.CompositorDevice); err != nil {
		return err
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
	return writeRendererPageWithRandomSeed(width, height, bundlePath, threads, transport, nil)
}

// writeRendererPageWithRandomSeed adds a deterministic Math.random prelude for
// browser-reference parity tests. Production callers use writeRendererPage,
// which leaves the renderer's normal randomized collision fallback untouched.
func writeRendererPageWithRandomSeed(width, height int, bundlePath string, threads []niconico.RendererThread, transport *frameTransportConfig, seed *uint32) (string, error) {
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
	randomSeedScript := ""
	if seed != nil {
		seedValue := *seed
		if seedValue == 0 {
			seedValue = 0x6d2b79f5
		}
		randomSeedScript = fmt.Sprintf(`<script>(()=>{let state=%d>>>0;Math.random=()=>{state^=state<<13;state^=state>>>17;state^=state<<5;return(state>>>0)/4294967296;};})();</script>`, seedValue)
	}
	html := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><style>html,body{margin:0;padding:0;overflow:hidden;background:transparent}canvas{display:block}</style><canvas id="canvas" width="%d" height="%d"></canvas><script id="comment-data" type="application/json">%s</script>%s<script src="%s"></script><script>
(() => {
  try {
    const data = JSON.parse(document.getElementById('comment-data').textContent);
    const canvas = document.getElementById('canvas');
    window.__niconi = new NiconiComments(canvas, data, {format:'v1', mode:'html5', keepCA:false, lazy:false, config:{canvasWidth:%d,canvasHeight:%d}});
    window.__niconiReady = true;
    window.__draw = async (vpos) => { window.__niconi.drawCanvas(vpos, true); await new Promise(requestAnimationFrame); return canvas.toDataURL('image/png'); };
  } catch (e) {
    window.__niconiError = String(e && (e.stack || e.message) || e);
  }
})();
</script>%s`, width, height, data, randomSeedScript, bundleURL, width, height, transportScript)
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
	cmd         *exec.Cmd
	ws          *websocket.Conn
	next        int
	pooledCall  func(context.Context, string, map[string]any) (json.RawMessage, error)
	releasePool func() error
	releaseOnce sync.Once
	releaseErr  error
}

type browserSessionCaller interface {
	call(context.Context, string, map[string]any) (json.RawMessage, error)
}

type freshBrowserStarter func(context.Context, string, string) (*browserSession, error)

func openCaptureBrowser(ctx context.Context, options RenderOptions, pagePath string, freshStarter freshBrowserStarter) (*browserSession, error) {
	if options.browserPool == nil {
		if freshStarter == nil {
			freshStarter = startBrowser
		}
		return freshStarter(ctx, options.BrowserPath, pagePath)
	}
	lease, err := options.browserPool.Acquire(ctx, pagePath)
	if err != nil {
		return nil, err
	}
	caller, ok := lease.pageSession.(browserSessionCaller)
	if !ok {
		return nil, errors.Join(errors.New("niconico: pooled page session does not support CDP calls"), releaseCaptureLease(options.browserPool, lease))
	}
	return &browserSession{
		pooledCall:  caller.call,
		releasePool: func() error { return releaseCaptureLease(options.browserPool, lease) },
	}, nil
}

func releaseCaptureLease(pool *BrowserPool, lease *BrowserLease) error {
	err := pool.Release(lease)
	if !errors.Is(err, errBrowserLeaseStale) {
		return err
	}
	<-lease.done
	return lease.cleanupErr
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
	headless := strings.TrimSpace(os.Getenv("IMAGEPAD_NICONICO_RENDER_HEADLESS"))
	if headless == "" {
		headless = "new"
	}
	if headless != "new" && headless != "old" {
		removeBrowserProfile(profile)
		return nil, fmt.Errorf("niconico: unsupported headless mode %q", headless)
	}
	gpuMode := strings.TrimSpace(os.Getenv("IMAGEPAD_NICONICO_RENDER_GPU"))
	if gpuMode == "" {
		gpuMode = "disabled"
	}
	if gpuMode != "disabled" && gpuMode != "enabled" && gpuMode != "swiftshader" && gpuMode != "swiftshader-inprocess" && gpuMode != "inprocess" && gpuMode != "software" && gpuMode != "software-inprocess" && gpuMode != "single-process" && gpuMode != "disabled-no-dawn-cache" && gpuMode != "disabled-no-gpu-sandbox" {
		removeBrowserProfile(profile)
		return nil, fmt.Errorf("niconico: unsupported GPU mode %q", gpuMode)
	}
	args := browserLaunchArgs(port, profile, headless, gpuMode)
	cmd := exec.CommandContext(ctx, browserPath, args...)
	// Nil uses the OS null device directly. io.Discard would create output
	// copy goroutines, making Wait wait for inherited pipes in descendants.
	if os.Getenv("IMAGEPAD_NICONICO_RENDER_DEBUG") == "1" {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		removeBrowserProfile(profile)
		return nil, fmt.Errorf("%w: start browser: %v", ErrUnavailable, err)
	}
	wsURL, err := waitWebSocket(ctx, port)
	if err != nil {
		terminateBrowserProcessTree(cmd)
		_ = cmd.Wait()
		removeBrowserProfile(profile)
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	ws, err := websocket.Dial(wsURL, "", "http://127.0.0.1")
	if err != nil {
		terminateBrowserProcessTree(cmd)
		_ = cmd.Wait()
		removeBrowserProfile(profile)
		return nil, fmt.Errorf("%w: DevTools接続: %v", ErrUnavailable, err)
	}
	s := &browserSession{cmd: cmd, ws: ws, next: 1}
	// Page.enable is not required for the CDP commands used by the renderer.
	// On the supported Windows Chromium path it can terminate the GPU helper
	// before navigation, so keep startup to Runtime.enable and Page.navigate.
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

func browserLaunchArgs(port int, profile, headless, gpuMode string) []string {
	args := []string{"--headless=" + headless, "--allow-file-access-from-files", "--disable-background-networking", "--disable-component-update", "--disable-extensions", "--disable-sync", "--no-proxy-server", "--remote-debugging-address=127.0.0.1", fmt.Sprintf("--remote-debugging-port=%d", port), "--remote-allow-origins=*", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check"}
	switch gpuMode {
	case "disabled":
		args = append(args, "--disable-gpu")
	case "swiftshader":
		args = append(args, "--use-angle=swiftshader", "--use-gl=angle")
	case "swiftshader-inprocess":
		args = append(args, "--use-angle=swiftshader", "--use-gl=angle", "--in-process-gpu")
	case "inprocess":
		args = append(args, "--in-process-gpu")
	case "software":
		args = append(args, "--disable-gpu", "--disable-gpu-compositing", "--disable-features=UseSkiaRenderer")
	case "software-inprocess":
		args = append(args, "--in-process-gpu", "--disable-gpu", "--disable-gpu-compositing", "--disable-features=UseSkiaRenderer,VizDisplayCompositor")
	case "single-process":
		args = append(args, "--single-process", "--disable-gpu", "--disable-gpu-compositing", "--disable-features=UseSkiaRenderer,VizDisplayCompositor")
	case "disabled-no-dawn-cache":
		// Chrome 153 enables Skia Graphite's persistent Dawn cache on Windows;
		// this diagnostic mode isolates cache initialization from GPU startup.
		args = append(args, "--disable-gpu", "--disable-features=SkiaGraphiteUsePersistentCache")
	case "disabled-no-gpu-sandbox":
		// Diagnostic only: do not weaken the sandbox in the default renderer.
		args = append(args, "--disable-gpu", "--disable-gpu-sandbox")
	}
	return append(args, "about:blank")
}

type browserProcessChild interface {
	Close() error
}

type browserProcessRuntime struct {
	findBrowser           func(configured string) (string, error)
	reservePort           func() (int, error)
	makeProfile           func() (string, error)
	launch                func(context.Context, string, []string) (browserProcessChild, error)
	discover              func(context.Context, int) (string, error)
	clearActivePortFile   func(profile string) error
	readActivePortFile    func(profile string) ([]byte, error)
	discoveryPollInterval time.Duration
	discoveryTimeout      time.Duration
	removeProfile         func(string) error
}

const dedicatedBrowserCloseTimeout = 5 * time.Second
const dedicatedBrowserChildCloseTimeout = 4 * time.Second

type browserProcessOwner struct {
	child                  browserProcessChild
	profile                string
	browserWebSocketURL    string
	removeProfile          func(string) error
	gracefulCloseRequested bool
	closeOnce              sync.Once
	closeErr               error
}

func (b *browserProcessOwner) Close() error {
	return b.closeWithTimeout(dedicatedBrowserCloseTimeout)
}

func (b *browserProcessOwner) closeWithTimeout(timeout time.Duration) error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		deadline := time.Now().Add(timeout)
		if b.child != nil {
			if b.gracefulCloseRequested {
				if child, ok := b.child.(*commandBrowserChild); ok {
					child.gracefulShutdownRequested = true
				}
			}
			closed := make(chan error, 1)
			go func() { closed <- b.child.Close() }()
			select {
			case err := <-closed:
				b.closeErr = errors.Join(b.closeErr, err)
			case <-time.After(time.Until(deadline)):
				b.closeErr = errors.Join(b.closeErr, fmt.Errorf("niconico: dedicated browser close timed out after %s", timeout))
			}
		}
		if b.closeErr == nil && b.removeProfile != nil {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				b.closeErr = fmt.Errorf("niconico: dedicated browser profile cleanup timed out after %s", timeout)
				return
			}
			removed := make(chan error, 1)
			go func() { removed <- b.removeProfile(b.profile) }()
			select {
			case err := <-removed:
				b.closeErr = errors.Join(b.closeErr, err)
			case <-time.After(remaining):
				b.closeErr = errors.Join(b.closeErr, fmt.Errorf("niconico: dedicated browser profile cleanup timed out after %s", timeout))
			}
		}
	})
	return b.closeErr
}

type commandBrowserChild struct {
	cmd                       *exec.Cmd
	ops                       commandBrowserChildOps
	gracefulShutdownRequested bool
	closeOnce                 sync.Once
	closeErr                  error
}

type commandBrowserChildOps struct {
	terminateTree func(context.Context) error
	killRoot      func() error
	wait          func() error
}

func (c *commandBrowserChild) Close() error {
	return c.closeWithTimeout(dedicatedBrowserChildCloseTimeout)
}

func (c *commandBrowserChild) closeWithTimeout(timeout time.Duration) error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		ops := c.ops
		if c.cmd == nil || c.cmd.Process == nil {
			if ops.terminateTree == nil && ops.killRoot == nil && ops.wait == nil {
				return
			}
		}
		if ops.terminateTree == nil {
			ops.terminateTree = func(ctx context.Context) error { return terminateDedicatedBrowserTree(ctx, c.cmd) }
		}
		if ops.killRoot == nil {
			ops.killRoot = func() error {
				if c.cmd == nil || c.cmd.Process == nil {
					return nil
				}
				err := c.cmd.Process.Kill()
				if errors.Is(err, os.ErrProcessDone) {
					return nil
				}
				return err
			}
		}
		if ops.wait == nil {
			ops.wait = func() error {
				if c.cmd == nil || c.cmd.Process == nil {
					return nil
				}
				err := c.cmd.Wait()
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return nil
				}
				return err
			}
		}
		deadline := time.Now().Add(timeout)
		termTimeout := timeout / 2
		if termTimeout <= 0 || termTimeout > time.Until(deadline) {
			termTimeout = time.Until(deadline)
		}
		termCtx, cancel := context.WithTimeout(context.Background(), termTimeout)
		termDone := make(chan error, 1)
		go func() { termDone <- ops.terminateTree(termCtx) }()
		treeTerminationSucceeded := false
		treeTerminationCompleted := false
		var treeTerminationErr error
		select {
		case err := <-termDone:
			treeTerminationCompleted = true
			treeTerminationSucceeded = err == nil
			treeTerminationErr = err
		case <-termCtx.Done():
			treeTerminationErr = fmt.Errorf("niconico: dedicated browser tree termination timed out: %w", termCtx.Err())
		}
		cancel()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.closeErr = errors.Join(c.closeErr, errors.New("niconico: dedicated browser close timed out before root kill"))
			return
		}
		killed := make(chan error, 1)
		go func() { killed <- ops.killRoot() }()
		var rootKillErr error
		select {
		case err := <-killed:
			rootKillErr = err
		case <-time.After(remaining):
			c.closeErr = errors.Join(c.closeErr, errors.New("niconico: dedicated browser root kill timed out"))
			return
		}

		remaining = time.Until(deadline)
		if remaining <= 0 {
			c.closeErr = errors.Join(c.closeErr, errors.New("niconico: dedicated browser close timed out before wait"))
			return
		}
		waited := make(chan error, 1)
		go func() { waited <- ops.wait() }()
		select {
		case err := <-waited:
			// Only tolerate taskkill's root-PID race after Browser.close was sent,
			// Wait reaped the root, and the command reports no other PID failures.
			gracefulRootAlreadyExited := c.gracefulShutdownRequested && treeTerminationCompleted && err == nil && taskkillFailureOnlyNamesRootAsMissing(treeTerminationErr, processID(c.cmd))
			if gracefulRootAlreadyExited {
				treeTerminationErr = nil
			}
			if (treeTerminationSucceeded || gracefulRootAlreadyExited) && err == nil && isWindowsAccessDenied(rootKillErr) {
				rootKillErr = nil
			}
			c.closeErr = errors.Join(c.closeErr, treeTerminationErr, rootKillErr, err)
		case <-time.After(remaining):
			c.closeErr = errors.Join(c.closeErr, treeTerminationErr, rootKillErr, errors.New("niconico: dedicated browser Wait timed out"))
		}
	})
	return c.closeErr
}

func processID(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}

func isWindowsAccessDenied(err error) bool {
	if runtime.GOOS != "windows" || err == nil {
		return false
	}
	var code syscall.Errno
	return errors.As(err, &code) && code == syscall.Errno(5)
}

func terminateDedicatedBrowserTree(ctx context.Context, cmd *exec.Cmd) error {
	if runtime.GOOS != "windows" || cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	taskkill := exec.CommandContext(ctx, "taskkill", "/PID", fmt.Sprint(pid), "/T", "/F")
	output, err := taskkill.CombinedOutput()
	if err != nil {
		return taskkillCommandError(pid, output, err)
	}
	return nil
}

func taskkillCommandError(pid int, output []byte, err error) error {
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	decoded, decodeErr := japanese.ShiftJIS.NewDecoder().Bytes(output)
	if decodeErr == nil {
		output = decoded
	}
	return &dedicatedBrowserTreeTerminationError{rootPID: pid, output: strings.TrimSpace(string(output)), exitCode: exitCode, err: err}
}

type dedicatedBrowserTreeTerminationError struct {
	rootPID  int
	output   string
	exitCode int
	err      error
}

func (e *dedicatedBrowserTreeTerminationError) Error() string {
	if e == nil {
		return "niconico: taskkill browser tree failed"
	}
	if e.output == "" {
		return fmt.Sprintf("niconico: taskkill browser tree PID %d: %v", e.rootPID, e.err)
	}
	return fmt.Sprintf("niconico: taskkill browser tree PID %d: %v: %s", e.rootPID, e.err, e.output)
}

func (e *dedicatedBrowserTreeTerminationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func taskkillFailureOnlyNamesRootAsMissing(err error, pid int) bool {
	var taskkillErr *dedicatedBrowserTreeTerminationError
	if !errors.As(err, &taskkillErr) || taskkillErr.rootPID != pid || taskkillErr.exitCode != 128 {
		return false
	}
	lines := strings.FieldsFunc(strings.TrimSpace(taskkillErr.output), func(r rune) bool { return r == '\r' || r == '\n' })
	if len(lines) != 1 || !strings.Contains(lines[0], strconv.Itoa(pid)) {
		return false
	}
	line := strings.ToLower(lines[0])
	return strings.Contains(line, "not found") || strings.Contains(line, "could not be found") || strings.Contains(lines[0], "見つかりません") || strings.Contains(lines[0], "存在しません")
}

func defaultBrowserProcessRuntime() browserProcessRuntime {
	return browserProcessRuntime{
		findBrowser: func(configured string) (string, error) {
			if configured = strings.TrimSpace(configured); configured != "" {
				return configured, nil
			}
			return findBrowser()
		},
		reservePort: reservePort,
		makeProfile: func() (string, error) { return os.MkdirTemp("", "imagepad-niconi-profile-*") },
		launch: func(_ context.Context, path string, args []string) (browserProcessChild, error) {
			// The session owner closes the process tree explicitly. CommandContext
			// can kill only the root process, before tree cleanup is coordinated.
			cmd := exec.Command(path, args...)
			if os.Getenv("IMAGEPAD_NICONICO_RENDER_DEBUG") == "1" {
				cmd.Stderr = os.Stderr
			}
			if err := cmd.Start(); err != nil {
				return nil, err
			}
			return &commandBrowserChild{cmd: cmd}, nil
		},
		discover: waitBrowserWebSocket,
		clearActivePortFile: func(profile string) error {
			err := os.Remove(filepath.Join(profile, "DevToolsActivePort"))
			if os.IsNotExist(err) {
				return nil
			}
			return err
		},
		readActivePortFile: func(profile string) ([]byte, error) {
			return os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		},
		discoveryPollInterval: 100 * time.Millisecond,
		discoveryTimeout:      8 * time.Second,
		removeProfile:         removeBrowserProfileChecked,
	}
}

func startDedicatedBrowser(owner context.Context, configured string) (*browserProcessOwner, error) {
	return startDedicatedBrowserWithRuntime(owner, configured, defaultBrowserProcessRuntime())
}

func startDedicatedBrowserWithRuntime(owner context.Context, configured string, runtime browserProcessRuntime) (*browserProcessOwner, error) {
	if owner == nil {
		return nil, errors.New("niconico: dedicated browser owner context is required")
	}
	if err := owner.Err(); err != nil {
		return nil, err
	}
	if runtime.findBrowser == nil || runtime.makeProfile == nil || runtime.launch == nil || runtime.discover == nil || runtime.removeProfile == nil {
		return nil, errors.New("niconico: incomplete dedicated browser runtime")
	}
	useActivePortFile := runtime.readActivePortFile != nil
	if useActivePortFile && runtime.clearActivePortFile == nil {
		return nil, errors.New("niconico: dedicated browser runtime cannot clear DevToolsActivePort")
	}
	if !useActivePortFile && runtime.reservePort == nil {
		return nil, errors.New("niconico: incomplete legacy dedicated browser runtime")
	}
	browserPath, err := runtime.findBrowser(configured)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	browserPath = strings.TrimSpace(browserPath)
	if browserPath == "" {
		return nil, fmt.Errorf("%w: browser path is empty", ErrUnavailable)
	}
	profile, err := runtime.makeProfile()
	if err != nil {
		return nil, err
	}
	cleanupProfile := func() error { return runtime.removeProfile(profile) }
	port := 0
	if useActivePortFile {
		if err := runtime.clearActivePortFile(profile); err != nil {
			return nil, errors.Join(fmt.Errorf("niconico: clear stale DevToolsActivePort: %w", err), cleanupProfile())
		}
	} else {
		port, err = runtime.reservePort()
		if err != nil {
			return nil, errors.Join(err, cleanupProfile())
		}
	}
	headless := strings.TrimSpace(os.Getenv("IMAGEPAD_NICONICO_RENDER_HEADLESS"))
	if headless == "" {
		headless = "new"
	}
	if headless != "new" && headless != "old" {
		return nil, errors.Join(fmt.Errorf("niconico: unsupported headless mode %q", headless), cleanupProfile())
	}
	gpuMode := strings.TrimSpace(os.Getenv("IMAGEPAD_NICONICO_RENDER_GPU"))
	if gpuMode == "" {
		gpuMode = "disabled"
	}
	if gpuMode != "disabled" && gpuMode != "enabled" && gpuMode != "swiftshader" && gpuMode != "swiftshader-inprocess" && gpuMode != "inprocess" && gpuMode != "software" && gpuMode != "software-inprocess" && gpuMode != "single-process" && gpuMode != "disabled-no-dawn-cache" && gpuMode != "disabled-no-gpu-sandbox" {
		return nil, errors.Join(fmt.Errorf("niconico: unsupported GPU mode %q", gpuMode), cleanupProfile())
	}
	child, err := runtime.launch(owner, browserPath, browserLaunchArgs(port, profile, headless, gpuMode))
	if err != nil {
		if child != nil {
			processOwner := &browserProcessOwner{child: child, profile: profile, removeProfile: runtime.removeProfile}
			err = errors.Join(err, processOwner.Close())
		} else {
			err = errors.Join(err, cleanupProfile())
		}
		return nil, fmt.Errorf("%w: start dedicated browser: %v", ErrUnavailable, err)
	}
	if child == nil {
		return nil, errors.Join(fmt.Errorf("%w: browser launcher returned no process", ErrUnavailable), cleanupProfile())
	}
	processOwner := &browserProcessOwner{child: child, profile: profile, removeProfile: runtime.removeProfile}
	activePath := ""
	if useActivePortFile {
		pollInterval := runtime.discoveryPollInterval
		if pollInterval <= 0 {
			pollInterval = 100 * time.Millisecond
		}
		timeout := runtime.discoveryTimeout
		if timeout <= 0 {
			timeout = 8 * time.Second
		}
		port, activePath, err = waitDevToolsActivePort(owner, profile, runtime.readActivePortFile, pollInterval, timeout)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("%w: read DevToolsActivePort", ErrUnavailable), err, processOwner.Close())
		}
	}
	browserURL, err := runtime.discover(owner, port)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: browser WebSocket discovery: %v", ErrUnavailable, err), processOwner.Close())
	}
	if err := owner.Err(); err != nil {
		return nil, errors.Join(err, processOwner.Close())
	}
	if useActivePortFile {
		err = validateBrowserWebSocketURLAgainstActivePort(browserURL, port, activePath)
	} else {
		err = validateBrowserWebSocketURL(browserURL, port)
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: browser WebSocket discovery: %v", ErrUnavailable, err), processOwner.Close())
	}
	processOwner.browserWebSocketURL = browserURL
	return processOwner, nil
}

func waitDevToolsActivePort(ctx context.Context, profile string, readFile func(string) ([]byte, error), interval, timeout time.Duration) (int, string, error) {
	if readFile == nil {
		return 0, "", errors.New("DevToolsActivePort reader is unavailable")
	}
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		contents, err := readFile(profile)
		if err == nil {
			port, path, parseErr := parseDevToolsActivePort(contents)
			if parseErr != nil {
				return 0, "", parseErr
			}
			return port, path, nil
		}
		if !os.IsNotExist(err) {
			return 0, "", fmt.Errorf("read DevToolsActivePort: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, "", errors.New("DevToolsActivePort did not appear before timeout")
		}
		wait := interval
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, "", ctx.Err()
		case <-timer.C:
		}
	}
}

func parseDevToolsActivePort(contents []byte) (int, string, error) {
	if len(contents) == 0 || len(contents) > 4096 {
		return 0, "", errors.New("DevToolsActivePort has invalid size")
	}
	text := strings.ReplaceAll(string(contents), "\r\n", "\n")
	if strings.ContainsRune(text, '\r') {
		return 0, "", errors.New("DevToolsActivePort contains an unexpected carriage return")
	}
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	if len(lines) != 2 {
		return 0, "", fmt.Errorf("DevToolsActivePort must contain exactly two lines, got %d", len(lines))
	}
	if strings.TrimSpace(lines[0]) != lines[0] || lines[0] == "" {
		return 0, "", errors.New("DevToolsActivePort has a malformed port")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1 || port > 65535 {
		return 0, "", fmt.Errorf("DevToolsActivePort has invalid port %q", lines[0])
	}
	path := lines[1]
	if !strings.HasPrefix(path, "/devtools/browser/") || strings.TrimPrefix(path, "/devtools/browser/") == "" || strings.ContainsAny(path, "?#\\\t ") {
		return 0, "", fmt.Errorf("DevToolsActivePort has invalid browser path %q", path)
	}
	return port, path, nil
}

func parseBrowserWebSocketVersion(body []byte, expectedPort int) (string, error) {
	var response struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("decode browser version response: %w", err)
	}
	if err := validateBrowserWebSocketURL(response.WebSocketDebuggerURL, expectedPort); err != nil {
		return "", err
	}
	return response.WebSocketDebuggerURL, nil
}

func validateBrowserWebSocketURL(raw string, expectedPort int) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid browser WebSocket URL: %w", err)
	}
	if u.Hostname() != "127.0.0.1" || u.Port() != fmt.Sprintf("%d", expectedPort) {
		return fmt.Errorf("browser WebSocket authority %q does not match expected endpoint 127.0.0.1:%d", u.Host, expectedPort)
	}
	if (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/devtools/browser/") || strings.TrimPrefix(u.Path, "/devtools/browser/") == "" {
		return fmt.Errorf("invalid browser WebSocket URL %q", raw)
	}
	return nil
}

func validateBrowserWebSocketURLAgainstActivePort(raw string, expectedPort int, expectedPath string) error {
	if err := validateBrowserWebSocketURL(raw, expectedPort); err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.EscapedPath() != expectedPath || u.RawQuery != "" {
		return fmt.Errorf("browser WebSocket path %q does not match DevToolsActivePort path %q", u.EscapedPath(), expectedPath)
	}
	return nil
}

func waitBrowserWebSocket(ctx context.Context, port int) (string, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
				_ = resp.Body.Close()
				if readErr != nil {
					return "", readErr
				}
				return parseBrowserWebSocketVersion(body, port)
			}
			_ = resp.Body.Close()
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", errors.New("browser WebSocket did not open")
}

func (s *browserSession) close() error {
	if s == nil {
		return nil
	}
	if s.releasePool != nil {
		s.releaseOnce.Do(func() { s.releaseErr = s.releasePool() })
		return s.releaseErr
	}
	if s.ws != nil {
		_ = s.ws.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		terminateBrowserProcessTree(s.cmd)
		_ = s.cmd.Wait()
		if len(s.cmd.Args) > 0 {
			for _, arg := range s.cmd.Args {
				if strings.HasPrefix(arg, "--user-data-dir=") {
					removeBrowserProfile(strings.TrimPrefix(arg, "--user-data-dir="))
				}
			}
		}
	}
	return nil
}

// terminateBrowserProcessTree closes the browser we started and its renderer/
// GPU descendants. Process.Kill only targets the Chromium parent on Windows;
// leaving descendants alive keeps the private profile locked and can make the
// next isolated render fail before CDP navigation. The PID is the process we
// just spawned, so taskkill's /T scope cannot reach the user's existing browser.
func terminateBrowserProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/PID", fmt.Sprint(cmd.Process.Pid), "/T", "/F").Run()
	}
	_ = cmd.Process.Kill()
}

// removeBrowserProfile retries briefly because Chromium's child processes can
// release profile files just after taskkill returns. A single RemoveAll call
// otherwise leaves a private profile behind after a GPU/CDP startup failure.
func removeBrowserProfile(path string) {
	if path == "" {
		return
	}
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		if err := os.RemoveAll(path); err != nil {
			lastErr = err
		} else if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		} else if err != nil {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil && os.Getenv("IMAGEPAD_NICONICO_RENDER_DEBUG") == "1" {
		_, _ = fmt.Fprintf(os.Stderr, "niconico: remove browser profile %q: %v\n", path, lastErr)
	}
}

func removeBrowserProfileChecked(path string) error {
	removeBrowserProfile(path)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("niconico: dedicated browser profile %q remains after cleanup", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("niconico: inspect dedicated browser profile %q after cleanup: %w", path, err)
	}
	return nil
}

func (s *browserSession) call(ctx context.Context, method string, params map[string]any) (result json.RawMessage, err error) {
	started := time.Now()
	defer func() {
		if os.Getenv("IMAGEPAD_NICONICO_RENDER_CDP_TRACE") != "1" {
			return
		}
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "niconico cdp method=%s elapsed_ms=%.1f error=%q\n", method, float64(time.Since(started).Microseconds())/1000, err.Error())
			return
		}
		_, _ = fmt.Fprintf(os.Stderr, "niconico cdp method=%s elapsed_ms=%.1f ok\n", method, float64(time.Since(started).Microseconds())/1000)
	}()
	if s.pooledCall != nil {
		return s.pooledCall(ctx, method, params)
	}
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

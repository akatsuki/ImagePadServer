package nicorender

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

var (
	errBrowserPoolClosed   = errors.New("niconico: browser pool is closed")
	errBrowserPoolBusy     = errors.New("niconico: browser pool already has an active lease")
	errBrowserLeaseStale   = errors.New("niconico: browser lease is unknown or already released")
	errLeaseCleanupTimeout = errors.New("niconico: browser lease cleanup timed out")
)

const browserPoolCleanupTimeout = 5 * time.Second
const browserPoolGracefulCloseTimeout = 2 * time.Second

// browserPoolCDP is the lifecycle seam between the pool and browser-level
// control. The implementation is supplied by the session owner; tests use an
// in-memory fake and T3.3 supplies the real CDP implementation.
type browserPoolCDP interface {
	CreateBrowserContext(context.Context) (string, error)
	CreateTarget(context.Context, string, string) (string, error)
	OpenPageSession(context.Context, string, string) (browserPageSession, error)
	DisposeBrowserContext(context.Context, string) error
	Close() error
}

type browserPoolGracefulCloser interface {
	CloseBrowser(context.Context) error
}

type browserPageSession interface {
	Prepare(context.Context, string) error
	Close() error
}

// BrowserPool is owned by a worker session. Its dedicated browser process is
// owner-scoped; each lease represents only one job's context and target.
type BrowserPool struct {
	mu            sync.Mutex
	owner         context.Context
	cdp           browserPoolCDP
	browser       *browserProcessOwner
	browserWSURL  string
	active        *BrowserLease
	nextGen       uint64
	acquireCancel context.CancelFunc

	acquiring     bool
	closed        bool
	broken        error
	closing       bool
	closeFinished bool
	closeDone     chan struct{}
	changed       chan struct{}
	closeErr      error
}

// BrowserLease identifies a single generation of job-local browser state.
// Its identity, not its context/target string, is used to authorize Release.
type BrowserLease struct {
	pool        *BrowserPool
	generation  uint64
	contextID   string
	targetID    string
	pagePath    string
	jobCtx      context.Context
	pageSession browserPageSession

	mu           sync.Mutex
	pageStoppers []func() error
	localClosers []func() error
	released     bool
	cleaning     bool
	cleanupErr   error
	done         chan struct{}
}

// newBrowserPool creates a pool around a browser-level control connection.
// Keeping this constructor private prevents callers from mistaking the test
// seam for the eventual process-launching API.
func newBrowserPool(owner context.Context, cdp browserPoolCDP) (*BrowserPool, error) {
	if owner == nil {
		return nil, errors.New("niconico: browser pool owner context is required")
	}
	if cdp == nil {
		return nil, errors.New("niconico: browser pool CDP control is required")
	}
	return newBrowserPoolWithResources(owner, cdp, nil)
}

func newBrowserProcessPool(owner context.Context, configuredBrowser string) (*BrowserPool, error) {
	pool, err := newBrowserProcessPoolWithRuntime(owner, configuredBrowser, defaultBrowserProcessRuntime())
	if err != nil {
		return nil, err
	}
	cdp, err := newBrowserProcessCDP(owner, pool.browserWSURL)
	if err != nil {
		return nil, errors.Join(err, pool.Close())
	}
	pool.mu.Lock()
	if pool.closed || owner.Err() != nil {
		pool.mu.Unlock()
		return nil, errors.Join(errBrowserPoolClosed, cdp.Close(), pool.Close())
	}
	pool.cdp = cdp
	pool.mu.Unlock()
	return pool, nil
}

func newBrowserProcessPoolWithRuntime(owner context.Context, configuredBrowser string, runtime browserProcessRuntime) (*BrowserPool, error) {
	if owner == nil {
		return nil, errors.New("niconico: browser pool owner context is required")
	}
	browser, err := startDedicatedBrowserWithRuntime(owner, configuredBrowser, runtime)
	if err != nil {
		return nil, err
	}
	return newBrowserPoolWithResources(owner, nil, browser)
}

func newBrowserPoolWithResources(owner context.Context, cdp browserPoolCDP, browser *browserProcessOwner) (*BrowserPool, error) {
	if owner == nil {
		return nil, errors.New("niconico: browser pool owner context is required")
	}
	if cdp == nil && browser == nil {
		return nil, errors.New("niconico: browser pool requires a CDP seam or owned browser process")
	}
	p := &BrowserPool{
		owner:   owner,
		cdp:     cdp,
		browser: browser,
		browserWSURL: func() string {
			if browser != nil {
				return browser.browserWebSocketURL
			}
			return ""
		}(),
		closeDone: make(chan struct{}),
		changed:   make(chan struct{}),
	}
	go func() {
		select {
		case <-owner.Done():
			_ = p.Close()
		case <-p.closeDone:
		}
	}()
	return p, nil
}

// Acquire reserves the sole lease slot before making any asynchronous CDP
// calls. ctx belongs to the job, so cancellation releases only that lease.
func (p *BrowserPool) Acquire(ctx context.Context, pagePath string) (*BrowserLease, error) {
	if ctx == nil {
		return nil, errors.New("niconico: browser lease context is required")
	}
	p.mu.Lock()
	if p.cdp == nil {
		p.mu.Unlock()
		return nil, errors.New("niconico: page CDP operations are not initialized")
	}
	if err := p.unavailableLocked(); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if p.active != nil || p.acquiring {
		p.mu.Unlock()
		return nil, errBrowserPoolBusy
	}
	callCtx, cancelCall := context.WithCancel(ctx)
	stopOwnerCancellation := context.AfterFunc(p.owner, cancelCall)
	p.nextGen++
	lease := &BrowserLease{
		pool:       p,
		generation: p.nextGen,
		pagePath:   pagePath,
		jobCtx:     ctx,
		done:       make(chan struct{}),
	}
	p.active = lease // Reserve the slot before any CDP operation.
	p.acquiring = true
	p.acquireCancel = cancelCall
	p.signalLocked()
	p.mu.Unlock()
	defer func() {
		stopOwnerCancellation()
		cancelCall()
		p.mu.Lock()
		if p.active == lease {
			p.acquireCancel = nil
		}
		p.mu.Unlock()
	}()

	contextID, err := p.cdp.CreateBrowserContext(callCtx)
	if err != nil {
		var rollbackErr error
		if contextID != "" {
			rollbackErr = p.disposeContext(contextID)
		}
		cause := errors.Join(err, rollbackErr)
		p.failAcquire(lease, fmt.Errorf("create browser context: %w", cause), !p.shutdownCancellationIsNonFaulting(err, rollbackErr))
		return nil, fmt.Errorf("niconico: create browser context: %w", cause)
	}
	if contextID == "" {
		err := errors.New("CDP returned an empty BrowserContext ID")
		p.failAcquire(lease, err, true)
		return nil, fmt.Errorf("niconico: create browser context: %w", err)
	}
	lease.contextID = contextID

	if err := callCtx.Err(); err != nil {
		rollbackErr := p.disposeContext(contextID)
		cause := errors.Join(err, rollbackErr)
		p.failAcquire(lease, cause, !p.shutdownCancellationIsNonFaulting(err, rollbackErr))
		return nil, fmt.Errorf("niconico: acquire browser lease canceled: %w", cause)
	}
	targetID, err := p.cdp.CreateTarget(callCtx, contextID, "about:blank")
	if err != nil {
		rollbackErr := p.disposeContext(contextID)
		cause := errors.Join(err, rollbackErr)
		p.failAcquire(lease, fmt.Errorf("create target: %w", cause), !p.shutdownCancellationIsNonFaulting(err, rollbackErr))
		return nil, fmt.Errorf("niconico: create target: %w", cause)
	}
	if targetID == "" {
		rollbackErr := p.disposeContext(contextID)
		cause := errors.Join(errors.New("CDP returned an empty Target ID"), rollbackErr)
		p.failAcquire(lease, cause, true)
		return nil, fmt.Errorf("niconico: create target: %w", cause)
	}
	lease.targetID = targetID
	if err := callCtx.Err(); err != nil {
		disposeErr := p.disposeContext(contextID)
		cause := errors.Join(err, disposeErr)
		p.failAcquire(lease, cause, !p.shutdownCancellationIsNonFaulting(err, disposeErr))
		return nil, fmt.Errorf("niconico: acquire browser lease canceled: %w", cause)
	}
	pageSession, err := p.cdp.OpenPageSession(callCtx, targetID, pagePath)
	if err != nil || pageSession == nil {
		var pageCloseErr error
		if pageSession != nil {
			pageCloseErr = pageSession.Close()
			err = errors.Join(err, pageCloseErr)
		}
		if err == nil {
			err = errors.New("CDP returned an empty page session")
		}
		disposeErr := p.disposeContext(contextID)
		cause := errors.Join(err, disposeErr)
		p.failAcquire(lease, fmt.Errorf("open page session: %w", cause), !p.shutdownCancellationIsNonFaulting(err, errors.Join(pageCloseErr, disposeErr)))
		return nil, fmt.Errorf("niconico: open page session: %w", cause)
	}
	if err := pageSession.Prepare(callCtx, pagePath); err != nil {
		closeErr := pageSession.Close()
		disposeErr := p.disposeContext(contextID)
		cause := errors.Join(err, closeErr, disposeErr)
		p.failAcquire(lease, fmt.Errorf("prepare page session: %w", cause), !p.shutdownCancellationIsNonFaulting(err, errors.Join(closeErr, disposeErr)))
		return nil, fmt.Errorf("niconico: prepare page session: %w", cause)
	}
	lease.pageSession = pageSession
	lease.localClosers = append(lease.localClosers, pageSession.Close)

	p.mu.Lock()
	closing := p.closed || p.active != lease || p.broken != nil
	p.acquiring = false
	p.acquireCancel = nil
	p.signalLocked()
	p.mu.Unlock()
	if closing {
		releaseErr := p.Release(lease)
		return nil, errors.Join(errBrowserPoolClosed, releaseErr)
	}

	go func() {
		select {
		case <-ctx.Done():
			_ = p.Release(lease)
		case <-lease.done:
		}
	}()
	return lease, nil
}

func (p *BrowserPool) isClosing() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed || p.closing
}

func (p *BrowserPool) shutdownCancellationIsNonFaulting(cause, cleanupErr error) bool {
	if cleanupErr != nil || (!errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded)) {
		return false
	}
	return p.isClosing() || p.owner.Err() != nil
}

// AddPageStopper registers a callback that stops job users before local CDP
// connections/listeners are closed. Registration is rejected after cleanup
// has begun.
func (l *BrowserLease) AddPageStopper(stop func() error) error {
	if stop == nil {
		return errors.New("niconico: nil page stopper")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.cleaning {
		return errBrowserLeaseStale
	}
	l.pageStoppers = append(l.pageStoppers, stop)
	return nil
}

// AddLocalCloser registers page-local connection/listener cleanup. All local
// closers run before BrowserContext disposal.
func (l *BrowserLease) AddLocalCloser(close func() error) error {
	if close == nil {
		return errors.New("niconico: nil page-local closer")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.cleaning {
		return errBrowserLeaseStale
	}
	l.localClosers = append(l.localClosers, close)
	return nil
}

// Release disposes one known lease. A stale or duplicate lease cannot mutate
// the current generation.
func (p *BrowserPool) Release(lease *BrowserLease) error {
	if lease == nil || lease.pool != p {
		return errBrowserLeaseStale
	}
	p.mu.Lock()
	for {
		if p.active != lease || lease.generation == 0 {
			p.mu.Unlock()
			return errBrowserLeaseStale
		}
		lease.mu.Lock()
		if lease.released {
			lease.mu.Unlock()
			p.mu.Unlock()
			return errBrowserLeaseStale
		}
		if lease.cleaning {
			lease.mu.Unlock()
			changed := p.changed
			p.mu.Unlock()
			<-changed
			p.mu.Lock()
			continue
		}
		lease.cleaning = true
		stoppers := append([]func() error(nil), lease.pageStoppers...)
		closers := append([]func() error(nil), lease.localClosers...)
		lease.mu.Unlock()
		p.mu.Unlock()

		cleanupErr, cleanupTimedOut := runLeaseCleanupBounded(stoppers, closers, browserPoolCleanupTimeout)
		var disposeErr error
		if lease.contextID != "" && !cleanupTimedOut {
			disposeErr = p.disposeContext(lease.contextID)
		}
		cleanupErr = errors.Join(cleanupErr, disposeErr)
		p.finishLease(lease, cleanupErr)
		return cleanupErr
	}
}

// Close is idempotent. It settles any in-flight acquisition or lease cleanup,
// then closes owner-scoped browser control exactly once.
func (p *BrowserPool) Close() error {
	p.mu.Lock()
	if p.closing {
		done := p.closeDone
		p.mu.Unlock()
		<-done
		p.mu.Lock()
		err := p.closeErr
		p.mu.Unlock()
		return err
	}
	if p.closeFinished {
		err := p.closeErr
		p.mu.Unlock()
		return err
	}
	p.closed = true
	p.closing = true
	if p.acquireCancel != nil {
		p.acquireCancel()
	}
	p.signalLocked()
	for p.acquiring {
		changed := p.changed
		p.mu.Unlock()
		<-changed
		p.mu.Lock()
	}
	lease := p.active
	cdp := p.cdp
	p.mu.Unlock()

	var closeErr error
	if lease != nil {
		closeErr = p.Release(lease)
		if errors.Is(closeErr, errBrowserLeaseStale) {
			closeErr = nil
		}
	}
	if cdp != nil {
		if graceful, ok := cdp.(browserPoolGracefulCloser); ok {
			gracefulCtx, cancel := context.WithTimeout(context.Background(), browserPoolGracefulCloseTimeout)
			if err := graceful.CloseBrowser(gracefulCtx); err == nil && p.browser != nil {
				p.browser.gracefulCloseRequested = true
			}
			cancel()
		}
		closeErr = errors.Join(closeErr, cdp.Close())
	}
	if p.browser != nil {
		closeErr = errors.Join(closeErr, p.browser.Close())
	}

	p.mu.Lock()
	if p.broken != nil {
		closeErr = errors.Join(closeErr, p.broken)
	}
	p.closeErr = closeErr
	p.closing = false
	p.closeFinished = true
	p.signalLocked()
	close(p.closeDone)
	p.mu.Unlock()
	return closeErr
}

// ActiveLease returns the currently reserved lease for contract tests and
// lifecycle diagnostics. Callers must not retain it as an ownership token.
func (p *BrowserPool) ActiveLease() *BrowserLease {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

func (p *BrowserPool) unavailableLocked() error {
	if p.closed {
		return errBrowserPoolClosed
	}
	if err := p.owner.Err(); err != nil {
		return errors.Join(errBrowserPoolClosed, err)
	}
	if p.broken != nil {
		return fmt.Errorf("niconico: browser pool is terminal: %w", p.broken)
	}
	return nil
}

func (p *BrowserPool) failAcquire(lease *BrowserLease, cause error, terminal bool) {
	p.mu.Lock()
	if terminal && p.broken == nil {
		p.broken = cause
		p.closed = true
	}
	if p.active == lease {
		p.active = nil
	}
	p.acquiring = false
	p.acquireCancel = nil
	lease.mu.Lock()
	lease.released = true
	lease.mu.Unlock()
	close(lease.done)
	p.signalLocked()
	p.mu.Unlock()
}

func (p *BrowserPool) finishLease(lease *BrowserLease, cleanupErr error) {
	p.mu.Lock()
	if cleanupErr != nil && p.broken == nil {
		p.broken = cleanupErr
		p.closed = true
	}
	if p.active == lease && p.active.generation == lease.generation {
		p.active = nil
	}
	lease.mu.Lock()
	lease.released = true
	lease.cleaning = false
	lease.cleanupErr = cleanupErr
	lease.mu.Unlock()
	close(lease.done)
	p.signalLocked()
	p.mu.Unlock()
}

func (p *BrowserPool) disposeContext(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), browserPoolCleanupTimeout)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.cdp.DisposeBrowserContext(ctx, id) }()
	select {
	case err := <-result:
		if err != nil {
			return fmt.Errorf("dispose browser context %q: %w", id, err)
		}
	case <-ctx.Done():
		return fmt.Errorf("dispose browser context %q: %w", id, errors.Join(errLeaseCleanupTimeout, ctx.Err()))
	}
	return nil
}

func runLeaseCleanupBounded(stoppers, closers []func() error, timeout time.Duration) (error, bool) {
	finished := make(chan error, 1)
	go func() { finished <- runLeaseCleanup(stoppers, closers) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-finished:
		return err, false
	case <-timer.C:
		return errLeaseCleanupTimeout, true
	}
}

// browserProcessCDP is the owner-scoped browser WebSocket connection plus the
// local DevTools HTTP endpoint used only to resolve a created target's page
// WebSocket. Browser-level Target commands always go through peer.
type browserProcessCDP struct {
	peer     *cdpPeer
	endpoint string
}

func newBrowserProcessCDP(ctx context.Context, browserWSURL string) (*browserProcessCDP, error) {
	u, err := url.Parse(browserWSURL)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, fmt.Errorf("niconico: invalid dedicated browser WebSocket URL %q", browserWSURL)
	}
	peer, err := dialCDPPeer(ctx, browserWSURL)
	if err != nil {
		return nil, fmt.Errorf("niconico: connect browser CDP: %w", err)
	}
	return &browserProcessCDP{peer: peer, endpoint: "http://127.0.0.1:" + u.Port()}, nil
}

func (c *browserProcessCDP) CreateBrowserContext(ctx context.Context) (string, error) {
	result, err := c.peer.call(ctx, "Target.createBrowserContext", map[string]any{"disposeOnDetach": false})
	var response struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if len(result) > 0 && string(result) != "null" {
		if decodeErr := json.Unmarshal(result, &response); decodeErr != nil {
			err = errors.Join(err, fmt.Errorf("decode Target.createBrowserContext result: %w", decodeErr))
		}
	}
	if err != nil {
		return response.BrowserContextID, err
	}
	if response.BrowserContextID == "" {
		return "", errors.New("Target.createBrowserContext returned no browserContextId")
	}
	return response.BrowserContextID, nil
}

func (c *browserProcessCDP) CreateTarget(ctx context.Context, browserContextID, targetURL string) (string, error) {
	result, err := c.peer.call(ctx, "Target.createTarget", map[string]any{"url": targetURL, "browserContextId": browserContextID})
	var response struct {
		TargetID string `json:"targetId"`
	}
	if len(result) > 0 && string(result) != "null" {
		if decodeErr := json.Unmarshal(result, &response); decodeErr != nil {
			err = errors.Join(err, fmt.Errorf("decode Target.createTarget result: %w", decodeErr))
		}
	}
	if err != nil {
		return response.TargetID, err
	}
	if response.TargetID == "" {
		return "", errors.New("Target.createTarget returned no targetId")
	}
	return response.TargetID, nil
}

func (c *browserProcessCDP) OpenPageSession(ctx context.Context, targetID, pagePath string) (browserPageSession, error) {
	pageWSURL, err := waitTargetPageWebSocket(ctx, c.endpoint, targetID)
	if err != nil {
		return nil, err
	}
	peer, err := dialCDPPeer(ctx, pageWSURL)
	if err != nil {
		return nil, err
	}
	return &cdpPageSession{peer: peer}, nil
}

func (c *browserProcessCDP) DisposeBrowserContext(ctx context.Context, browserContextID string) error {
	_, err := c.peer.call(ctx, "Target.disposeBrowserContext", map[string]any{"browserContextId": browserContextID})
	return err
}

func (c *browserProcessCDP) CloseBrowser(ctx context.Context) error {
	// Browser.close may close its WebSocket before returning the protocol reply.
	// Sending the command is enough; process exit and taskkill output confirm the
	// outcome before the owner decides whether the cleanup race was benign.
	return c.peer.notify(ctx, "Browser.close", map[string]any{})
}

func (c *browserProcessCDP) Close() error { return c.peer.Close() }

type cdpPeer struct {
	mu        sync.Mutex
	conn      *websocket.Conn
	nextID    int
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

func dialCDPPeer(ctx context.Context, rawURL string) (*cdpPeer, error) {
	config, err := websocket.NewConfig(rawURL, "http://127.0.0.1")
	if err != nil {
		return nil, err
	}
	config.Dialer = &net.Dialer{Timeout: 5 * time.Second}
	conn, err := config.DialContext(ctx)
	if err != nil {
		return nil, err
	}
	return &cdpPeer{conn: conn, nextID: 1}, nil
}

func (p *cdpPeer) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return nil, errors.New("CDP WebSocket is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(20 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := p.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = p.conn.SetDeadline(time.Now()) })
	defer func() {
		stopCancellation()
		_ = p.conn.SetDeadline(time.Time{})
	}()
	id := p.nextID
	p.nextID++
	if err := websocket.JSON.Send(p.conn, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := websocket.JSON.Receive(p.conn, &message); err != nil {
			return nil, err
		}
		if message.ID != id {
			continue
		}
		if len(message.Error) > 0 && string(message.Error) != "null" {
			return message.Result, fmt.Errorf("CDP %s: %s", method, message.Error)
		}
		if len(message.Result) == 0 {
			return nil, fmt.Errorf("CDP %s returned no result", method)
		}
		return message.Result, nil
	}
}

func (p *cdpPeer) notify(ctx context.Context, method string, params map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return errors.New("CDP WebSocket is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(browserPoolGracefulCloseTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := p.conn.SetDeadline(deadline); err != nil {
		return err
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = p.conn.SetDeadline(time.Now()) })
	defer func() {
		stopCancellation()
		_ = p.conn.SetDeadline(time.Time{})
	}()
	id := p.nextID
	p.nextID++
	return websocket.JSON.Send(p.conn, map[string]any{"id": id, "method": method, "params": params})
}

func (p *cdpPeer) Close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		// Closing the socket directly interrupts any serialized call currently
		// blocked in Receive; Close must not wait behind the call mutex.
		p.closeErr = p.conn.Close()
	})
	return p.closeErr
}

type cdpPageSession struct{ peer *cdpPeer }

func (s *cdpPageSession) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return s.peer.call(ctx, method, params)
}

func (s *cdpPageSession) Prepare(ctx context.Context, pagePath string) error {
	if _, err := s.peer.call(ctx, "Runtime.enable", map[string]any{}); err != nil {
		return err
	}
	result, err := s.peer.call(ctx, "Page.navigate", map[string]any{"url": fileURL(pagePath)})
	if err != nil {
		return err
	}
	var navigation struct {
		ErrorText string `json:"errorText"`
	}
	if err := json.Unmarshal(result, &navigation); err != nil {
		return fmt.Errorf("decode Page.navigate result: %w", err)
	}
	if navigation.ErrorText != "" {
		return fmt.Errorf("CDP Page.navigate: %s", navigation.ErrorText)
	}
	return nil
}

func (s *cdpPageSession) Close() error { return s.peer.Close() }

func waitTargetPageWebSocket(ctx context.Context, endpoint, targetID string) (string, error) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/list", nil)
		response, err := client.Do(request)
		if err == nil {
			var pages []struct {
				ID                   string `json:"id"`
				Type                 string `json:"type"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&pages)
			response.Body.Close()
			if decodeErr == nil {
				for _, page := range pages {
					if page.ID == targetID && page.Type == "page" && page.WebSocketDebuggerURL != "" {
						if err := validateTargetPageWebSocketURL(page.WebSocketDebuggerURL, endpoint, targetID); err != nil {
							return "", err
						}
						return page.WebSocketDebuggerURL, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errors.New("DevTools page WebSocket did not open for created target")
		case <-ticker.C:
		}
	}
}

func validateTargetPageWebSocketURL(rawURL, endpoint, targetID string) error {
	pageURL, err := url.Parse(strings.TrimSpace(rawURL))
	endpointURL, endpointErr := url.Parse(endpoint)
	if err != nil || endpointErr != nil || (pageURL.Scheme != "ws" && pageURL.Scheme != "wss") || pageURL.Hostname() != "127.0.0.1" || pageURL.Port() != endpointURL.Port() || pageURL.User != nil || pageURL.Fragment != "" || !strings.HasPrefix(pageURL.Path, "/devtools/page/") || strings.TrimPrefix(pageURL.Path, "/devtools/page/") == "" {
		return fmt.Errorf("niconico: invalid page WebSocket URL %q for endpoint %q", rawURL, endpoint)
	}
	pageTargetID, err := url.PathUnescape(strings.TrimPrefix(pageURL.EscapedPath(), "/devtools/page/"))
	if err != nil || pageTargetID != targetID {
		return fmt.Errorf("niconico: page WebSocket target %q does not match created target %q", pageTargetID, targetID)
	}
	return nil
}

func runLeaseCleanup(stoppers, closers []func() error) error {
	var result error
	for _, stop := range stoppers {
		result = errors.Join(result, stop())
	}
	for _, closeLocal := range closers {
		result = errors.Join(result, closeLocal())
	}
	return result
}

func (p *BrowserPool) signalLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

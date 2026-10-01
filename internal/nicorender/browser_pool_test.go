package nicorender

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// These tests exercise the pool lifecycle through an in-memory CDP seam. They
// deliberately do not start Chromium or depend on RenderOptions.
type fakePoolCDP struct {
	mu sync.Mutex

	nextContext              int
	nextTarget               int
	contexts                 map[string]bool
	targets                  map[string]string
	created                  []string
	disposed                 []string
	events                   []string
	maxActive                int
	closed                   int
	createErr                error
	createErrorAfterCreate   bool
	targetErr                error
	disposeErr               error
	createStarted            chan struct{}
	createGate               chan struct{}
	ignoreCreateCancellation bool
	createCanceled           chan struct{}
	targetStarted            chan struct{}
	targetGate               chan struct{}
	targetCanceled           chan struct{}
	pageOpenStarted          chan struct{}
	pageOpenGate             chan struct{}
	pageOpenCanceled         chan struct{}
	prepareStarted           chan struct{}
	prepareGate              chan struct{}
	prepareCanceled          chan struct{}
	pageCall                 func(context.Context, string, map[string]any) (json.RawMessage, error)
	onPageClose              func(string)
	onDispose                func(string)
	targetCalls              int
	disposeCtxErr            error
	pageCalls                int
	pageErr                  error
	pagePrepareErr           error
	pageSessions             []*fakePoolPageSession
	targetIDs                []string
	malformedContextID       bool
	malformedTargetID        bool
	targetErrorAfterCreate   bool
	disposeStarted           chan struct{}
	disposeGate              chan struct{}
	disposeCalls             int
	disposeSignalOnce        sync.Once
}

type fakePoolPageSession struct {
	mu              sync.Mutex
	prepared        string
	prepareErr      error
	closed          int
	prepareStarted  chan struct{}
	prepareGate     chan struct{}
	prepareCanceled chan struct{}
	callFunc        func(context.Context, string, map[string]any) (json.RawMessage, error)
	onClose         func(string)
}

func (s *fakePoolPageSession) Prepare(ctx context.Context, pagePath string) error {
	if s.prepareStarted != nil {
		close(s.prepareStarted)
	}
	if s.prepareGate != nil {
		select {
		case <-s.prepareGate:
		case <-ctx.Done():
			if s.prepareCanceled != nil {
				close(s.prepareCanceled)
			}
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepared = pagePath
	return s.prepareErr
}

func (s *fakePoolPageSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	if s.onClose != nil {
		s.onClose(s.prepared)
	}
	return nil
}

func (s *fakePoolPageSession) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if s.callFunc == nil {
		return nil, errors.New("fake page session has no CDP response")
	}
	return s.callFunc(ctx, method, params)
}

func newFakePoolCDP() *fakePoolCDP {
	return &fakePoolCDP{contexts: make(map[string]bool), targets: make(map[string]string)}
}

func TestDedicatedBrowserUsesChromeSelectedPortAndActivePortPath(t *testing.T) {
	ownerCtx := context.Background()
	contents := []byte("/devtools/browser/stale-session\n")
	var launchArgs []string
	var discoveredPort int
	removedBeforeLaunch := false
	runtime := defaultBrowserProcessRuntime()
	runtime.findBrowser = func(string) (string, error) { return "fake-chrome", nil }
	runtime.makeProfile = func() (string, error) { return "private-profile", nil }
	runtime.removeProfile = func(string) error { return nil }
	runtime.clearActivePortFile = func(profile string) error {
		if profile != "private-profile" {
			t.Fatalf("cleared unexpected profile %q", profile)
		}
		removedBeforeLaunch = true
		contents = nil
		return nil
	}
	runtime.readActivePortFile = func(string) ([]byte, error) {
		if len(contents) == 0 {
			return nil, os.ErrNotExist
		}
		return append([]byte(nil), contents...), nil
	}
	runtime.discoveryPollInterval = time.Millisecond
	runtime.discoveryTimeout = 50 * time.Millisecond
	runtime.launch = func(_ context.Context, _ string, args []string) (browserProcessChild, error) {
		if !removedBeforeLaunch {
			t.Fatal("stale DevToolsActivePort was not cleared before launch")
		}
		launchArgs = append([]string(nil), args...)
		contents = []byte("45678\n/devtools/browser/fresh-session\n")
		return &fakeBrowserChild{}, nil
	}
	runtime.discover = func(_ context.Context, port int) (string, error) {
		discoveredPort = port
		return "ws://127.0.0.1:45678/devtools/browser/fresh-session", nil
	}
	pool, err := startDedicatedBrowserWithRuntime(ownerCtx, "fake-chrome", runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if !containsPoolTestArg(launchArgs, "--remote-debugging-port=0") {
		t.Fatalf("dedicated launch did not request Chrome-selected port: %v", launchArgs)
	}
	if discoveredPort != 45678 {
		t.Fatalf("discovery used port %d, want DevToolsActivePort port 45678", discoveredPort)
	}
	if pool.browserWebSocketURL != "ws://127.0.0.1:45678/devtools/browser/fresh-session" {
		t.Fatalf("unexpected browser WebSocket URL %q", pool.browserWebSocketURL)
	}
}

func TestDedicatedBrowserRejectsMalformedOrMissingActivePort(t *testing.T) {
	tests := []struct {
		name string
		file []byte
	}{
		{name: "invalid port", file: []byte("70000\n/devtools/browser/session\n")},
		{name: "invalid path", file: []byte("45678\n/devtools/page/session\n")},
		{name: "extra content", file: []byte("45678\n/devtools/browser/session\nextra\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := devToolsDiscoveryTestRuntime(t, tt.file, "ws://127.0.0.1:45678/devtools/browser/session")
			if _, err := startDedicatedBrowserWithRuntime(context.Background(), "fake-chrome", runtime); err == nil {
				t.Fatal("startup accepted malformed DevToolsActivePort")
			}
		})
	}

	t.Run("absent until timeout", func(t *testing.T) {
		runtime := devToolsDiscoveryTestRuntime(t, nil, "")
		runtime.readActivePortFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
		if _, err := startDedicatedBrowserWithRuntime(context.Background(), "fake-chrome", runtime); err == nil {
			t.Fatal("startup succeeded without DevToolsActivePort")
		}
	})
}

func TestDedicatedBrowserDiscoveryHonorsOwnerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := devToolsDiscoveryTestRuntime(t, nil, "")
	runtime.readActivePortFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	runtime.launch = func(context.Context, string, []string) (browserProcessChild, error) {
		cancel()
		return &fakeBrowserChild{}, nil
	}
	if _, err := startDedicatedBrowserWithRuntime(ctx, "fake-chrome", runtime); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup error = %v, want context cancellation", err)
	}
}

func TestBrowserWebSocketMustMatchActivePortAuthorityAndPath(t *testing.T) {
	for _, raw := range []string{
		"ws://localhost:45678/devtools/browser/session",
		"ws://127.0.0.1:45679/devtools/browser/session",
		"ws://127.0.0.1:45678/devtools/browser/other-session",
		"ws://127.0.0.1:45678/devtools/browser/%73ession",
		"ws://127.0.0.1:45678/devtools/page/session",
		"ws://127.0.0.1:45678/devtools/browser/session?unexpected=1",
	} {
		t.Run(raw, func(t *testing.T) {
			if err := validateBrowserWebSocketURLAgainstActivePort(raw, 45678, "/devtools/browser/session"); err == nil {
				t.Fatalf("accepted WebSocket URL %q", raw)
			}
		})
	}
}

func devToolsDiscoveryTestRuntime(t *testing.T, file []byte, webSocketURL string) browserProcessRuntime {
	t.Helper()
	contents := append([]byte(nil), file...)
	runtime := defaultBrowserProcessRuntime()
	runtime.findBrowser = func(string) (string, error) { return "fake-chrome", nil }
	runtime.makeProfile = func() (string, error) { return "private-profile", nil }
	runtime.removeProfile = func(string) error { return nil }
	runtime.clearActivePortFile = func(string) error { contents = nil; return nil }
	runtime.readActivePortFile = func(string) ([]byte, error) {
		if len(contents) == 0 {
			return nil, os.ErrNotExist
		}
		return append([]byte(nil), contents...), nil
	}
	runtime.discoveryPollInterval = time.Millisecond
	runtime.discoveryTimeout = 5 * time.Millisecond
	runtime.launch = func(context.Context, string, []string) (browserProcessChild, error) {
		if len(file) != 0 {
			contents = append([]byte(nil), file...)
		}
		return &fakeBrowserChild{}, nil
	}
	runtime.discover = func(context.Context, int) (string, error) { return webSocketURL, nil }
	return runtime
}

func containsPoolTestArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func (f *fakePoolCDP) CreateBrowserContext(ctx context.Context) (string, error) {
	if f.createCanceled != nil {
		go func() {
			<-ctx.Done()
			close(f.createCanceled)
		}()
	}
	if f.createStarted != nil {
		close(f.createStarted)
	}
	if f.createGate != nil {
		if f.ignoreCreateCancellation {
			<-f.createGate
		} else {
			select {
			case <-f.createGate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil && !f.createErrorAfterCreate {
		return "", f.createErr
	}
	f.nextContext++
	id := contextID(f.nextContext)
	if f.malformedContextID {
		id = ""
	}
	f.contexts[id] = true
	f.created = append(f.created, id)
	if n := len(f.contexts); n > f.maxActive {
		f.maxActive = n
	}
	if f.createErrorAfterCreate {
		return id, f.createErr
	}
	return id, nil
}

func (f *fakePoolCDP) CreateTarget(ctx context.Context, contextID, _ string) (string, error) {
	if f.targetStarted != nil {
		close(f.targetStarted)
	}
	if f.targetGate != nil {
		select {
		case <-f.targetGate:
		case <-ctx.Done():
			if f.targetCanceled != nil {
				close(f.targetCanceled)
			}
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targetCalls++
	if f.targetErr != nil {
		return "", f.targetErr
	}
	if !f.contexts[contextID] {
		return "", errors.New("target created without a live context")
	}
	f.nextTarget++
	id := targetID(f.nextTarget)
	if f.malformedTargetID {
		id = ""
	}
	f.targets[id] = contextID
	f.targetIDs = append(f.targetIDs, id)
	if f.targetErrorAfterCreate {
		return id, errors.New("target create response lost after creation")
	}
	return id, nil
}

func (f *fakePoolCDP) OpenPageSession(ctx context.Context, targetID, _ string) (browserPageSession, error) {
	if f.pageOpenStarted != nil {
		close(f.pageOpenStarted)
	}
	if f.pageOpenGate != nil {
		select {
		case <-f.pageOpenGate:
		case <-ctx.Done():
			if f.pageOpenCanceled != nil {
				close(f.pageOpenCanceled)
			}
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageCalls++
	if f.pageErr != nil {
		return nil, f.pageErr
	}
	if f.targets[targetID] == "" {
		return nil, errors.New("page session opened for unknown target")
	}
	session := &fakePoolPageSession{
		prepareErr:      f.pagePrepareErr,
		prepareStarted:  f.prepareStarted,
		prepareGate:     f.prepareGate,
		prepareCanceled: f.prepareCanceled,
		callFunc:        f.pageCall,
		onClose:         f.onPageClose,
	}
	f.pageSessions = append(f.pageSessions, session)
	return session, nil
}

func (f *fakePoolCDP) DisposeBrowserContext(ctx context.Context, id string) error {
	if f.disposeStarted != nil {
		f.disposeSignalOnce.Do(func() { close(f.disposeStarted) })
	}
	if f.disposeGate != nil {
		<-f.disposeGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onDispose != nil {
		f.onDispose(id)
	}
	f.disposeCalls++
	f.disposeCtxErr = ctx.Err()
	if f.disposeErr != nil {
		return f.disposeErr
	}
	if !f.contexts[id] {
		return errors.New("unknown browser context")
	}
	delete(f.contexts, id)
	f.events = append(f.events, "dispose")
	for target, parent := range f.targets {
		if parent == id {
			delete(f.targets, target)
		}
	}
	f.disposed = append(f.disposed, id)
	return nil
}

func (f *fakePoolCDP) record(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *fakePoolCDP) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func contextID(n int) string { return "context-" + strconv.Itoa(n) }
func targetID(n int) string  { return "target-" + strconv.Itoa(n) }

func newTestPool(t *testing.T, backend *fakePoolCDP) *BrowserPool {
	t.Helper()
	p, err := newBrowserPool(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestBrowserPoolTwoJobsUseDistinctDisposedContexts(t *testing.T) {
	backend := newFakePoolCDP()
	pool := newTestPool(t, backend)

	for _, page := range []string{"job-a.html", "job-b.html"} {
		lease, err := pool.Acquire(context.Background(), page)
		if err != nil {
			t.Fatalf("Acquire(%q): %v", page, err)
		}
		if err := pool.Release(lease); err != nil {
			t.Fatalf("Release(%q): %v", page, err)
		}
	}

	if len(backend.created) != 2 || len(backend.disposed) != 2 {
		t.Fatalf("context lifecycle = created %v, disposed %v; want two distinct create/dispose cycles", backend.created, backend.disposed)
	}
	if backend.created[0] == backend.created[1] || backend.disposed[0] == backend.disposed[1] {
		t.Fatalf("jobs reused a context: created %v, disposed %v", backend.created, backend.disposed)
	}
	if backend.maxActive != 1 {
		t.Fatalf("maximum active contexts = %d, want 1", backend.maxActive)
	}
	if len(backend.targets) != 0 || backend.pageCalls != 2 || len(backend.pageSessions) != 2 || len(backend.targetIDs) != 2 {
		t.Fatalf("target/page lifecycle = live targets %v, target IDs %v, page calls %d, sessions %d; want two page sessions and disposed targets", backend.targets, backend.targetIDs, backend.pageCalls, len(backend.pageSessions))
	}
	if backend.targetIDs[0] == backend.targetIDs[1] {
		t.Fatalf("jobs reused a target: %v", backend.targetIDs)
	}
	for i, session := range backend.pageSessions {
		if session.closed != 1 {
			t.Fatalf("page session %d close count = %d, want 1", i, session.closed)
		}
	}
}

func TestBrowserPoolAcquiresPreparedPageSessionForTarget(t *testing.T) {
	backend := newFakePoolCDP()
	pool := newTestPool(t, backend)
	lease, err := pool.Acquire(context.Background(), "job-page.html")
	if err != nil {
		t.Fatal(err)
	}
	if lease.pageSession == nil {
		t.Fatal("Acquire did not retain the page session")
	}
	if got := backend.pageSessions[0].prepared; got != "job-page.html" {
		t.Fatalf("prepared page path = %q, want job-page.html", got)
	}
	if err := pool.Release(lease); err != nil {
		t.Fatal(err)
	}
	if backend.pageSessions[0].closed != 1 {
		t.Fatalf("page session close count = %d, want 1", backend.pageSessions[0].closed)
	}
}

func TestBrowserPoolMalformedCreateResponsesAreTerminalAndRolledBackWhenKnown(t *testing.T) {
	t.Run("empty context id", func(t *testing.T) {
		backend := newFakePoolCDP()
		backend.malformedContextID = true
		pool := newTestPool(t, backend)
		if _, err := pool.Acquire(context.Background(), "bad.html"); err == nil {
			t.Fatal("Acquire accepted an empty BrowserContext ID")
		}
		if backend.targetCalls != 0 || len(backend.disposed) != 0 {
			t.Fatalf("malformed context response side effects: target calls %d, disposed %v", backend.targetCalls, backend.disposed)
		}
	})
	t.Run("empty target id", func(t *testing.T) {
		backend := newFakePoolCDP()
		backend.malformedTargetID = true
		pool := newTestPool(t, backend)
		if _, err := pool.Acquire(context.Background(), "bad.html"); err == nil {
			t.Fatal("Acquire accepted an empty Target ID")
		}
		if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
			t.Fatalf("malformed target response did not rollback known context: created %v, disposed %v", backend.created, backend.disposed)
		}
		if len(backend.targets) != 0 {
			t.Fatalf("malformed target response left targets: %v", backend.targets)
		}
	})
	t.Run("known target id plus command error", func(t *testing.T) {
		backend := newFakePoolCDP()
		backend.targetErrorAfterCreate = true
		pool := newTestPool(t, backend)
		if _, err := pool.Acquire(context.Background(), "bad.html"); err == nil {
			t.Fatal("Acquire accepted target creation with a command error")
		}
		if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] || len(backend.targets) != 0 {
			t.Fatalf("known partial target was not removed by context rollback: created %v, disposed %v, targets %v", backend.created, backend.disposed, backend.targets)
		}
	})
}

func TestBrowserPoolPageSessionFailureRollsBackContext(t *testing.T) {
	backend := newFakePoolCDP()
	backend.pageErr = errors.New("page socket unavailable")
	pool := newTestPool(t, backend)
	if _, err := pool.Acquire(context.Background(), "page-failure.html"); err == nil {
		t.Fatal("Acquire succeeded without a page session")
	}
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("page-session failure did not rollback context: created %v, disposed %v", backend.created, backend.disposed)
	}
	if _, err := pool.Acquire(context.Background(), "after-failure.html"); err == nil {
		t.Fatal("pool became reusable after uncertain page-session failure")
	}
}

func TestBrowserPoolPagePrepareFailureClosesPageBeforeContextRollback(t *testing.T) {
	backend := newFakePoolCDP()
	backend.pagePrepareErr = errors.New("page navigation failed")
	pool := newTestPool(t, backend)
	if _, err := pool.Acquire(context.Background(), "page-prepare-failure.html"); err == nil {
		t.Fatal("Acquire succeeded after page preparation failed")
	}
	if len(backend.pageSessions) != 1 || backend.pageSessions[0].closed != 1 {
		t.Fatalf("partially prepared page session cleanup = %+v", backend.pageSessions)
	}
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("page preparation failure did not rollback context: created %v, disposed %v", backend.created, backend.disposed)
	}
}

func TestBrowserPoolCDPAdapterUsesBrowserAndTargetPageSockets(t *testing.T) {
	var mu sync.Mutex
	var browserCommands []map[string]any
	var pageCommands []map[string]any
	contexts := 0
	targets := 0
	serverPort := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(w, `[{"id":"target-%d","type":"page","webSocketDebuggerUrl":"ws://127.0.0.1:%s/devtools/page/target-%d"}]`, targets, serverPort, targets)
	})
	mux.Handle("/devtools/browser/fake", websocket.Handler(func(conn *websocket.Conn) {
		for {
			var command map[string]any
			if err := websocket.JSON.Receive(conn, &command); err != nil {
				return
			}
			mu.Lock()
			browserCommands = append(browserCommands, command)
			method, _ := command["method"].(string)
			id := int(command["id"].(float64))
			var result any = map[string]any{}
			switch method {
			case "Target.createBrowserContext":
				contexts++
				result = map[string]any{"browserContextId": fmt.Sprintf("context-%d", contexts)}
			case "Target.createTarget":
				targets++
				result = map[string]any{"targetId": fmt.Sprintf("target-%d", targets)}
			case "Target.disposeBrowserContext":
			default:
				mu.Unlock()
				_ = websocket.JSON.Send(conn, map[string]any{"id": id, "error": map[string]any{"code": -1, "message": "unexpected browser CDP command"}})
				continue
			}
			mu.Unlock()
			if err := websocket.JSON.Send(conn, map[string]any{"id": id, "result": result}); err != nil {
				return
			}
		}
	}))
	mux.Handle("/devtools/page/", websocket.Handler(func(conn *websocket.Conn) {
		for {
			var command map[string]any
			if err := websocket.JSON.Receive(conn, &command); err != nil {
				return
			}
			mu.Lock()
			pageCommands = append(pageCommands, command)
			mu.Unlock()
			if err := websocket.JSON.Send(conn, map[string]any{"id": command["id"], "result": map[string]any{}}); err != nil {
				return
			}
		}
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	serverPort = strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	browserWSURL := "ws://127.0.0.1:" + serverPort + "/devtools/browser/fake"
	cdp, err := newBrowserProcessCDP(context.Background(), browserWSURL)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := newBrowserPool(context.Background(), cdp)
	if err != nil {
		_ = cdp.Close()
		t.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	for _, page := range []string{"job-a.html", "job-b.html"} {
		lease, err := pool.Acquire(context.Background(), page)
		if err != nil {
			t.Fatalf("Acquire(%s): %v", page, err)
		}
		if err := pool.Release(lease); err != nil {
			t.Fatalf("Release(%s): %v", page, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if contexts != 2 || targets != 2 {
		t.Fatalf("created contexts/targets = %d/%d, want 2/2", contexts, targets)
	}
	var gotBrowserMethods []string
	for _, command := range browserCommands {
		gotBrowserMethods = append(gotBrowserMethods, command["method"].(string))
	}
	wantBrowserMethods := []string{"Target.createBrowserContext", "Target.createTarget", "Target.disposeBrowserContext", "Target.createBrowserContext", "Target.createTarget", "Target.disposeBrowserContext"}
	if strings.Join(gotBrowserMethods, ",") != strings.Join(wantBrowserMethods, ",") {
		t.Fatalf("browser CDP methods = %v, want %v", gotBrowserMethods, wantBrowserMethods)
	}
	if len(pageCommands) != 4 {
		t.Fatalf("page CDP command count = %d, want Runtime.enable + Page.navigate per target", len(pageCommands))
	}
	for i, wantMethod := range []string{"Runtime.enable", "Page.navigate", "Runtime.enable", "Page.navigate"} {
		if pageCommands[i]["method"] != wantMethod {
			t.Fatalf("page CDP command %d = %v, want %s", i, pageCommands[i]["method"], wantMethod)
		}
	}
	createParams := browserCommands[1]["params"].(map[string]any)
	if createParams["url"] != "about:blank" || createParams["browserContextId"] != "context-1" {
		t.Fatalf("first Target.createTarget params = %v", createParams)
	}
	navigate := pageCommands[1]["params"].(map[string]any)
	if !strings.HasSuffix(navigate["url"].(string), "/job-a.html") {
		t.Fatalf("first page navigation = %v", navigate)
	}
}

func TestBrowserPoolPageNavigateErrorTextFailsAndRollsBack(t *testing.T) {
	disposed := make(chan struct{}, 1)
	pageClosed := make(chan struct{}, 1)
	serverPort := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"id":"target-nav","type":"page","webSocketDebuggerUrl":"ws://127.0.0.1:%s/devtools/page/target-nav"}]`, serverPort)
	})
	mux.Handle("/devtools/browser/fake", websocket.Handler(func(conn *websocket.Conn) {
		for {
			var command struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if err := websocket.JSON.Receive(conn, &command); err != nil {
				return
			}
			result := map[string]any{}
			switch command.Method {
			case "Target.createBrowserContext":
				result["browserContextId"] = "context-nav"
			case "Target.createTarget":
				result["targetId"] = "target-nav"
			case "Target.disposeBrowserContext":
				disposed <- struct{}{}
			default:
				_ = websocket.JSON.Send(conn, map[string]any{"id": command.ID, "error": map[string]any{"code": -1, "message": "unexpected command"}})
				continue
			}
			if err := websocket.JSON.Send(conn, map[string]any{"id": command.ID, "result": result}); err != nil {
				return
			}
		}
	}))
	mux.Handle("/devtools/page/target-nav", websocket.Handler(func(conn *websocket.Conn) {
		defer func() { pageClosed <- struct{}{} }()
		for {
			var command struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if err := websocket.JSON.Receive(conn, &command); err != nil {
				return
			}
			result := map[string]any{}
			if command.Method == "Page.navigate" {
				result = map[string]any{"frameId": "frame-nav", "errorText": "ERR_FILE_NOT_FOUND"}
			}
			if err := websocket.JSON.Send(conn, map[string]any{"id": command.ID, "result": result}); err != nil {
				return
			}
		}
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	serverPort = strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	cdp, err := newBrowserProcessCDP(context.Background(), "ws://127.0.0.1:"+serverPort+"/devtools/browser/fake")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := newBrowserPool(context.Background(), cdp)
	if err != nil {
		_ = cdp.Close()
		t.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Acquire(context.Background(), "missing-page.html"); err == nil || !strings.Contains(err.Error(), "ERR_FILE_NOT_FOUND") {
		t.Fatalf("Acquire error = %v, want navigation error ERR_FILE_NOT_FOUND", err)
	}
	if pool.ActiveLease() != nil {
		t.Fatal("failed navigation retained an active lease")
	}
	select {
	case <-pageClosed:
	case <-time.After(time.Second):
		t.Fatal("failed navigation did not close the known page session")
	}
	select {
	case <-disposed:
	case <-time.After(time.Second):
		t.Fatal("failed navigation did not dispose the known BrowserContext")
	}
}

func TestCDPPeerCloseInterruptsInflightCall(t *testing.T) {
	commandSeen := make(chan struct{})
	handlerGate := make(chan struct{})
	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		var request map[string]any
		if websocket.JSON.Receive(conn, &request) == nil {
			close(commandSeen)
		}
		<-handlerGate
	}))
	defer server.Close()
	defer close(handlerGate)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test"
	peer, err := dialCDPPeer(context.Background(), wsURL)
	if err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, err := peer.call(context.Background(), "Runtime.enable", map[string]any{})
		callDone <- err
	}()
	<-commandSeen
	closeDone := make(chan error, 1)
	go func() { closeDone <- peer.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("peer Close: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("peer Close blocked behind an in-flight WebSocket receive")
	}
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("in-flight call succeeded without a CDP response")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("peer Close did not interrupt the blocked WebSocket receive")
	}
}

func TestBrowserPoolCDPAdapterRollsBackKnownContextFromErrorResponse(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	disposed := false
	mux := http.NewServeMux()
	mux.Handle("/devtools/browser/fake", websocket.Handler(func(conn *websocket.Conn) {
		for {
			var command map[string]any
			if err := websocket.JSON.Receive(conn, &command); err != nil {
				return
			}
			method := command["method"].(string)
			id := int(command["id"].(float64))
			mu.Lock()
			methods = append(methods, method)
			if method == "Target.disposeBrowserContext" {
				disposed = true
			}
			mu.Unlock()
			response := map[string]any{"id": id, "result": map[string]any{}}
			if method == "Target.createBrowserContext" {
				response["result"] = map[string]any{"browserContextId": "known-context"}
				response["error"] = map[string]any{"code": -1, "message": "command failed after create"}
			}
			if err := websocket.JSON.Send(conn, response); err != nil {
				return
			}
		}
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	cdp, err := newBrowserProcessCDP(context.Background(), "ws://127.0.0.1:"+port+"/devtools/browser/fake")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := newBrowserPool(context.Background(), cdp)
	if err != nil {
		_ = cdp.Close()
		t.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Acquire(context.Background(), "failed-create.html"); err == nil {
		t.Fatal("Acquire succeeded after Target.createBrowserContext returned an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if !disposed || strings.Join(methods, ",") != "Target.createBrowserContext,Target.disposeBrowserContext" {
		t.Fatalf("known partial context rollback = disposed %v, methods %v", disposed, methods)
	}
}

func TestValidateTargetPageWebSocketURLRequiresCreatedTarget(t *testing.T) {
	endpoint := "http://127.0.0.1:9222"
	if err := validateTargetPageWebSocketURL("ws://127.0.0.1:9222/devtools/page/target-a", endpoint, "target-a"); err != nil {
		t.Fatalf("valid created target URL rejected: %v", err)
	}
	for _, rawURL := range []string{
		"ws://127.0.0.1:9223/devtools/page/target-a",
		"ws://192.0.2.10:9222/devtools/page/target-a",
		"ws://127.0.0.1:9222/devtools/page/target-b",
	} {
		if err := validateTargetPageWebSocketURL(rawURL, endpoint, "target-a"); err == nil {
			t.Errorf("accepted foreign endpoint or target URL %q", rawURL)
		}
	}
}

func TestBrowserPoolRejectsConcurrentAcquireAndDuplicateRelease(t *testing.T) {
	backend := newFakePoolCDP()
	pool := newTestPool(t, backend)
	leaseA, err := pool.Acquire(context.Background(), "a.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Acquire(context.Background(), "b.html"); err == nil {
		t.Fatal("Acquire while a lease is active succeeded")
	}
	if err := pool.Release(leaseA); err != nil {
		t.Fatal(err)
	}
	leaseB, err := pool.Acquire(context.Background(), "b.html")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Release(leaseA); err == nil {
		t.Fatal("duplicate Release(A) succeeded")
	}
	if pool.ActiveLease() != leaseB {
		t.Fatal("duplicate Release(A) mutated the newer lease B")
	}
	if err := pool.Release(leaseB); err != nil {
		t.Fatalf("Release(B): %v", err)
	}
}

func TestBrowserPoolCloseIsIdempotentAndTerminal(t *testing.T) {
	backend := newFakePoolCDP()
	pool := newTestPool(t, backend)
	lease, err := pool.Acquire(context.Background(), "a.html")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := pool.Release(lease); err == nil {
		t.Fatal("Release of lease already settled by Close succeeded")
	}
	if _, err := pool.Acquire(context.Background(), "after-close.html"); err == nil {
		t.Fatal("Acquire after Close succeeded")
	}
	if backend.closed != 1 {
		t.Fatalf("backend Close calls = %d, want 1", backend.closed)
	}
}

func TestBrowserPoolCloseRequestsGracefulBrowserShutdownBeforeForceClose(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("taskkill root-process access-denied race is Windows-specific")
	}
	var events []string
	cdp := &gracefulCloseRecordingCDP{fakePoolCDP: newFakePoolCDP(), events: &events}
	child := &commandBrowserChild{ops: commandBrowserChildOps{
		terminateTree: func(context.Context) error {
			events = append(events, "tree-terminate")
			return &dedicatedBrowserTreeTerminationError{rootPID: 4242, output: `ERROR: The process "4242" could not be found.`, exitCode: 128, err: errors.New("exit status 128")}
		},
		killRoot: func() error { events = append(events, "root-kill"); return syscall.Errno(5) },
		wait:     func() error { events = append(events, "root-wait"); return nil },
	}, cmd: &exec.Cmd{Process: &os.Process{Pid: 4242}}}
	browser := &browserProcessOwner{child: child}
	pool, err := newBrowserPoolWithResources(context.Background(), cdp, browser)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("Close after graceful browser shutdown: %v", err)
	}
	want := []string{"browser-close", "cdp-disconnect", "tree-terminate", "root-kill", "root-wait"}
	if !equalBrowserPoolTestStrings(events, want) {
		t.Fatalf("close order = %v, want %v", events, want)
	}
}

func TestBrowserProcessCDPCloseBrowserSendsWithoutWaitingForReply(t *testing.T) {
	type request struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
	}
	requests := make(chan request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		websocket.Handler(func(conn *websocket.Conn) {
			var message request
			if err := websocket.JSON.Receive(conn, &message); err == nil {
				requests <- message
			}
		}).ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cdp, err := newBrowserProcessCDP(ctx, "ws://"+strings.TrimPrefix(server.URL, "http://")+"/devtools/browser/close-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := cdp.CloseBrowser(ctx); err != nil {
		t.Fatalf("CloseBrowser request: %v", err)
	}
	select {
	case got := <-requests:
		if got.Method != "Browser.close" || got.ID == 0 {
			t.Fatalf("CDP close message = %+v, want Browser.close request with nonzero ID", got)
		}
	case <-ctx.Done():
		t.Fatal("Browser.close message was not sent")
	}
	if err := cdp.Close(); err != nil {
		t.Fatalf("close CDP peer: %v", err)
	}
}

type gracefulCloseRecordingCDP struct {
	*fakePoolCDP
	events *[]string
}

func (c *gracefulCloseRecordingCDP) CloseBrowser(context.Context) error {
	*c.events = append(*c.events, "browser-close")
	return nil
}

func (c *gracefulCloseRecordingCDP) Close() error {
	*c.events = append(*c.events, "cdp-disconnect")
	return c.fakePoolCDP.Close()
}

func equalBrowserPoolTestStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestBrowserPoolDisposeFailureMakesPoolTerminal(t *testing.T) {
	backend := newFakePoolCDP()
	pool := newTestPool(t, backend)
	lease, err := pool.Acquire(context.Background(), "a.html")
	if err != nil {
		t.Fatal(err)
	}
	backend.disposeErr = errors.New("CDP disconnected")
	if err := pool.Release(lease); err == nil {
		t.Fatal("Release succeeded after uncertain context disposal")
	}
	if _, err := pool.Acquire(context.Background(), "b.html"); err == nil {
		t.Fatal("pool was resurrected after failed disposal")
	}
}

func TestBrowserPoolCanceledJobReleasesWithIndependentCleanupContext(t *testing.T) {
	backend := newFakePoolCDP()
	pool := newTestPool(t, backend)
	jobCtx, cancel := context.WithCancel(context.Background())
	lease, err := pool.Acquire(jobCtx, "cancel.html")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-lease.done:
	case <-time.After(time.Second):
		t.Fatal("job cancellation did not release its browser lease")
	}
	if pool.ActiveLease() != nil {
		t.Fatal("canceled job left an active lease")
	}
	if backend.disposeCtxErr != nil {
		t.Fatalf("cleanup context inherited job cancellation: %v", backend.disposeCtxErr)
	}
}

func TestBrowserPoolAcquireFailureRollsBackAndStaysTerminal(t *testing.T) {
	backend := newFakePoolCDP()
	backend.targetErr = errors.New("target creation failed")
	pool := newTestPool(t, backend)
	if _, err := pool.Acquire(context.Background(), "broken.html"); err == nil {
		t.Fatal("Acquire succeeded after target creation failure")
	}
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("partial acquisition was not rolled back: created %v, disposed %v", backend.created, backend.disposed)
	}
	if _, err := pool.Acquire(context.Background(), "resurrect.html"); err == nil {
		t.Fatal("pool was resurrected after uncertain target creation")
	}
}

func TestBrowserPoolCloseSettlesInflightAcquireWithoutResurrection(t *testing.T) {
	backend := newFakePoolCDP()
	backend.createStarted = make(chan struct{})
	backend.createGate = make(chan struct{})
	backend.ignoreCreateCancellation = true // Model a late CDP create reply after Close begins.
	pool := newTestPool(t, backend)
	acquireDone := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(context.Background(), "race.html")
		acquireDone <- err
	}()
	<-backend.createStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- pool.Close() }()
	for {
		pool.mu.Lock()
		closed := pool.closed
		changed := pool.changed
		pool.mu.Unlock()
		if closed {
			break
		}
		<-changed
	}
	close(backend.createGate)
	if err := <-acquireDone; err == nil {
		t.Fatal("in-flight Acquire succeeded after Close began")
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if pool.ActiveLease() != nil {
		t.Fatal("Close/acquire race resurrected an active lease")
	}
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("Close/acquire race leaked or double-disposed context: created %v, disposed %v", backend.created, backend.disposed)
	}
	if backend.closed != 1 {
		t.Fatalf("browser control Close calls = %d, want 1", backend.closed)
	}
}

func TestBrowserPoolCloseCancelsInflightAcquire(t *testing.T) {
	backend := newFakePoolCDP()
	backend.createStarted = make(chan struct{})
	backend.createGate = make(chan struct{})
	backend.createCanceled = make(chan struct{})
	pool := newTestPool(t, backend)
	acquireDone := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(context.Background(), "close-cancel.html")
		acquireDone <- err
	}()
	<-backend.createStarted
	closeDone := make(chan error, 1)
	go func() { closeDone <- pool.Close() }()
	select {
	case <-backend.createCanceled:
		close(backend.createGate)
	case <-time.After(250 * time.Millisecond):
		close(backend.createGate)
		<-acquireDone
		<-closeDone
		t.Fatal("BrowserPool.Close did not cancel the in-flight CDP operation")
	}
	if err := <-acquireDone; err == nil {
		t.Fatal("in-flight Acquire succeeded after Close canceled its CDP operation")
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if pool.ActiveLease() != nil {
		t.Fatal("Close left an active lease after canceling Acquire")
	}
}

func TestBrowserPoolCloseCancellationAtEachAcquireStageIsNonFaulting(t *testing.T) {
	for _, stage := range []string{"create-target", "open-page", "prepare-page"} {
		t.Run(stage, func(t *testing.T) {
			backend := newFakePoolCDP()
			started, canceled, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
			switch stage {
			case "create-target":
				backend.targetStarted, backend.targetCanceled, backend.targetGate = started, canceled, gate
			case "open-page":
				backend.pageOpenStarted, backend.pageOpenCanceled, backend.pageOpenGate = started, canceled, gate
			case "prepare-page":
				backend.prepareStarted, backend.prepareCanceled, backend.prepareGate = started, canceled, gate
			}
			pool := newTestPool(t, backend)
			acquireDone := make(chan error, 1)
			go func() { _, err := pool.Acquire(context.Background(), "shutdown.html"); acquireDone <- err }()
			<-started
			closeDone := make(chan error, 1)
			go func() { closeDone <- pool.Close() }()
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("Close did not cancel the blocked CDP stage")
			}
			if err := <-acquireDone; err == nil {
				t.Fatal("Acquire succeeded after shutdown canceled its CDP stage")
			}
			if err := <-closeDone; err != nil {
				t.Fatalf("Close after successful rollback: %v", err)
			}
			if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
				t.Fatalf("shutdown rollback created/disposed contexts = %v/%v", backend.created, backend.disposed)
			}
			if stage == "prepare-page" && (len(backend.pageSessions) != 1 || backend.pageSessions[0].closed != 1) {
				t.Fatalf("page session cleanup after Prepare cancellation = %+v", backend.pageSessions)
			}
		})
	}
}

func TestBrowserPoolOwnerCancellationDuringTargetIsNonFaulting(t *testing.T) {
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	backend := newFakePoolCDP()
	backend.targetStarted = make(chan struct{})
	backend.targetGate = make(chan struct{})
	backend.targetCanceled = make(chan struct{})
	pool, err := newBrowserPool(ownerCtx, backend)
	if err != nil {
		t.Fatal(err)
	}
	acquireDone := make(chan error, 1)
	go func() { _, err := pool.Acquire(context.Background(), "owner-cancel.html"); acquireDone <- err }()
	<-backend.targetStarted
	cancelOwner()
	select {
	case <-backend.targetCanceled:
	case <-time.After(time.Second):
		t.Fatal("owner cancellation did not interrupt Target.createTarget")
	}
	if err := <-acquireDone; err == nil {
		t.Fatal("Acquire succeeded after owner cancellation")
	}
	select {
	case <-pool.closeDone:
	case <-time.After(time.Second):
		t.Fatal("owner cancellation did not settle BrowserPool.Close")
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("Close after successful owner-cancellation rollback: %v", err)
	}
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("owner-cancellation rollback created/disposed contexts = %v/%v", backend.created, backend.disposed)
	}
}

func TestBrowserPoolJobCancellationDuringTargetRemainsTerminal(t *testing.T) {
	backend := newFakePoolCDP()
	backend.targetStarted = make(chan struct{})
	backend.targetGate = make(chan struct{})
	backend.targetCanceled = make(chan struct{})
	pool := newTestPool(t, backend)
	jobCtx, cancelJob := context.WithCancel(context.Background())
	acquireDone := make(chan error, 1)
	go func() { _, err := pool.Acquire(jobCtx, "job-cancel.html"); acquireDone <- err }()
	<-backend.targetStarted
	cancelJob()
	select {
	case <-backend.targetCanceled:
	case <-time.After(time.Second):
		t.Fatal("job cancellation did not interrupt Target.createTarget")
	}
	if err := <-acquireDone; err == nil {
		t.Fatal("Acquire succeeded after job cancellation")
	}
	if _, err := pool.Acquire(context.Background(), "resurrect.html"); err == nil {
		t.Fatal("pool remained available after job cancellation left context ownership uncertain")
	}
}

func TestBrowserPoolConcurrentCancelReleaseCloseClaimsCleanupOnce(t *testing.T) {
	backend := newFakePoolCDP()
	pool := newTestPool(t, backend)
	jobCtx, cancel := context.WithCancel(context.Background())
	lease, err := pool.Acquire(jobCtx, "racing-cleanup.html")
	if err != nil {
		t.Fatal(err)
	}
	backend.disposeStarted = make(chan struct{})
	backend.disposeGate = make(chan struct{})
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- pool.Release(lease) }()
	<-backend.disposeStarted
	cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- pool.Close() }()
	close(backend.disposeGate)
	if err := <-releaseDone; err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if backend.disposeCalls != 1 || len(backend.disposed) != 1 {
		t.Fatalf("context disposal calls/results = %d/%v, want one", backend.disposeCalls, backend.disposed)
	}
	if len(backend.pageSessions) != 1 || backend.pageSessions[0].closed != 1 {
		t.Fatalf("page connection close count = %+v, want exactly one", backend.pageSessions)
	}
	if backend.closed != 1 {
		t.Fatalf("browser CDP Close calls = %d, want one", backend.closed)
	}
}

func TestBrowserPoolReleaseOrdersPageAndLocalCleanupBeforeDispose(t *testing.T) {
	backend := newFakePoolCDP()
	pool := newTestPool(t, backend)
	lease, err := pool.Acquire(context.Background(), "cleanup.html")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.AddPageStopper(func() error { backend.record("stop"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := lease.AddLocalCloser(func() error { backend.record("local-close"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := pool.Release(lease); err != nil {
		t.Fatal(err)
	}
	if len(backend.events) != 3 || backend.events[0] != "stop" || backend.events[1] != "local-close" || backend.events[2] != "dispose" {
		t.Fatalf("page/local/context cleanup order = %v", backend.events)
	}
	if len(backend.disposed) != 1 {
		t.Fatalf("context disposal count = %d, want 1", len(backend.disposed))
	}
}

func TestBrowserPoolOwnerCancellationCancelsInflightCDPAndRollsBackLateCreate(t *testing.T) {
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	backend := newFakePoolCDP()
	backend.createStarted = make(chan struct{})
	backend.createGate = make(chan struct{})
	backend.ignoreCreateCancellation = true
	backend.createCanceled = make(chan struct{})
	pool, err := newBrowserPool(ownerCtx, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	gateClosed := false
	defer func() {
		if !gateClosed {
			close(backend.createGate)
		}
	}()

	acquireDone := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(context.Background(), "late-create.html")
		acquireDone <- err
	}()
	<-backend.createStarted
	cancelOwner()
	select {
	case <-backend.createCanceled:
	case <-time.After(time.Second):
		t.Fatal("owner cancellation did not cancel the in-flight CDP call")
	}

	// The fake deliberately ignores cancellation and returns a known context ID
	// late; the pool must dispose it and skip target creation.
	close(backend.createGate)
	gateClosed = true
	if err := <-acquireDone; err == nil {
		t.Fatal("Acquire succeeded after its owner context was canceled")
	}
	<-pool.closeDone
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("late create response was not rolled back: created %v, disposed %v", backend.created, backend.disposed)
	}
	if backend.targetCalls != 0 {
		t.Fatalf("CreateTarget calls after owner cancellation = %d, want 0", backend.targetCalls)
	}
	if backend.closed != 1 {
		t.Fatalf("browser control Close calls = %d, want 1", backend.closed)
	}
}

func TestBrowserPoolCreateErrorWithKnownContextRollsBack(t *testing.T) {
	backend := newFakePoolCDP()
	backend.createErr = errors.New("CDP create response error")
	backend.createErrorAfterCreate = true
	pool := newTestPool(t, backend)
	if _, err := pool.Acquire(context.Background(), "create-error.html"); err == nil {
		t.Fatal("Acquire succeeded after create command error")
	}
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("known context from failed create was not rolled back: created %v, disposed %v", backend.created, backend.disposed)
	}
	if _, err := pool.Acquire(context.Background(), "after-create-error.html"); err == nil {
		t.Fatal("pool was reusable after browser-level create command error")
	}
}

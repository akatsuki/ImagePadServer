package nicorender

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestDiscoverBrowserWebSocketUsesVersionEndpoint(t *testing.T) {
	var browserURL string
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"Browser":"Chrome/153","Protocol-Version":"1.3","webSocketDebuggerUrl":"` + browserURL + `"}`))
	}))
	defer server.Close()

	endpointPort := server.Listener.Addr().(*net.TCPAddr).Port
	browserURL = fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/test-id", endpointPort)
	got, err := waitBrowserWebSocket(context.Background(), endpointPort)
	if err != nil {
		t.Fatal(err)
	}
	if got != browserURL {
		t.Fatalf("browser WebSocket = %q, want %q", got, browserURL)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/json/version" {
		t.Fatalf("discovery paths = %v, want only /json/version", paths)
	}
}

func TestParseBrowserWebSocketRejectsInvalidAndPageURLs(t *testing.T) {
	for name, body := range map[string]string{
		"malformed": `{"webSocketDebuggerUrl":`,
		"missing":   `{"Browser":"Chrome/153"}`,
		"page":      `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/page/page-id"}`,
		"scheme":    `{"webSocketDebuggerUrl":"http://127.0.0.1:9222/devtools/browser/id"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseBrowserWebSocketVersion([]byte(body), 9222); err == nil {
				t.Fatalf("parseBrowserWebSocketVersion(%s) succeeded", name)
			}
		})
	}
}

func TestValidateBrowserWebSocketRequiresExactLoopbackEndpoint(t *testing.T) {
	for name, endpoint := range map[string]string{
		"foreign host":      "ws://example.invalid:9222/devtools/browser/id",
		"different port":    "ws://127.0.0.1:9223/devtools/browser/id",
		"other loopback IP": "ws://127.0.0.2:9222/devtools/browser/id",
		"localhost alias":   "ws://localhost:9222/devtools/browser/id",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateBrowserWebSocketURL(endpoint, 9222); err == nil {
				t.Fatalf("validateBrowserWebSocketURL(%q) succeeded", endpoint)
			}
		})
	}
	if err := validateBrowserWebSocketURL("ws://127.0.0.1:9222/devtools/browser/id", 9222); err != nil {
		t.Fatalf("valid reserved endpoint rejected: %v", err)
	}
}

func TestWaitBrowserWebSocketHonorsCancellation(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := waitBrowserWebSocket(ctx, port); result <- err }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitBrowserWebSocket error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("browser WebSocket discovery did not stop on cancellation")
	}
}

type fakeBrowserChild struct {
	mu           sync.Mutex
	closeErr     error
	closeCnt     int
	closeStarted chan struct{}
	closeGate    chan struct{}
	startOnce    sync.Once
}

func (f *fakeBrowserChild) Close() error {
	f.mu.Lock()
	f.closeCnt++
	err := f.closeErr
	started, gate := f.closeStarted, f.closeGate
	f.mu.Unlock()
	if started != nil {
		f.startOnce.Do(func() { close(started) })
	}
	if gate != nil {
		<-gate
	}
	return err
}

func (f *fakeBrowserChild) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCnt
}

func TestBrowserProcessOwnerCloseTimesOutWithoutRemovingProfile(t *testing.T) {
	child := &fakeBrowserChild{closeStarted: make(chan struct{}), closeGate: make(chan struct{})}
	profileRemovals := 0
	owner := &browserProcessOwner{child: child, profile: "held-profile", removeProfile: func(string) error { profileRemovals++; return nil }}
	started := time.Now()
	err := owner.closeWithTimeout(25 * time.Millisecond)
	close(child.closeGate)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Close error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Close elapsed %s, want bounded return", elapsed)
	}
	if profileRemovals != 0 {
		t.Fatalf("profile removals = %d, want 0 while process termination is unconfirmed", profileRemovals)
	}
	if err2 := owner.Close(); err2 == nil || err2.Error() != err.Error() {
		t.Fatalf("repeated Close error = %v, want cached %v", err2, err)
	}
	if child.closeCount() != 1 {
		t.Fatalf("child close calls = %d, want one", child.closeCount())
	}
}

func TestCommandBrowserChildBoundsStalledTaskkillAndStillKillsAndWaitsOnce(t *testing.T) {
	started := make(chan struct{})
	gate := make(chan struct{})
	var mu sync.Mutex
	var calls []string
	child := &commandBrowserChild{ops: commandBrowserChildOps{
		terminateTree: func(context.Context) error {
			close(started)
			<-gate
			return nil
		},
		killRoot: func() error { mu.Lock(); calls = append(calls, "kill"); mu.Unlock(); return nil },
		wait:     func() error { mu.Lock(); calls = append(calls, "wait"); mu.Unlock(); return nil },
	}}
	result := make(chan error, 1)
	go func() { result <- child.closeWithTimeout(100 * time.Millisecond) }()
	<-started
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("Close error = %v, want taskkill timeout", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(gate)
		t.Fatal("Close blocked behind stalled taskkill")
	}
	close(gate)
	if err := child.Close(); err == nil {
		t.Fatal("repeated Close lost the cached taskkill timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(calls, ",") != "kill,wait" {
		t.Fatalf("root kill/wait calls = %v, want one each in order", calls)
	}
}

func TestCommandBrowserChildBoundsStalledWaitAndDoesNotWaitTwice(t *testing.T) {
	started := make(chan struct{})
	gate := make(chan struct{})
	waits := 0
	child := &commandBrowserChild{ops: commandBrowserChildOps{
		terminateTree: func(context.Context) error { return nil },
		killRoot:      func() error { return nil },
		wait: func() error {
			waits++
			close(started)
			<-gate
			return nil
		},
	}}
	result := make(chan error, 1)
	go func() { result <- child.closeWithTimeout(100 * time.Millisecond) }()
	<-started
	err := <-result
	close(gate)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Close error = %v, want Wait timeout", err)
	}
	if repeated := child.Close(); repeated == nil || repeated.Error() != err.Error() {
		t.Fatalf("repeated Close error = %v, want cached %v", repeated, err)
	}
	if waits != 1 {
		t.Fatalf("Wait calls = %d, want one", waits)
	}
}

func TestBrowserProcessOwnerRemovesProfileAfterChildReap(t *testing.T) {
	var mu sync.Mutex
	var events []string
	child := &commandBrowserChild{ops: commandBrowserChildOps{
		terminateTree: func(context.Context) error { mu.Lock(); events = append(events, "tree"); mu.Unlock(); return nil },
		killRoot:      func() error { mu.Lock(); events = append(events, "kill"); mu.Unlock(); return nil },
		wait:          func() error { mu.Lock(); events = append(events, "wait"); mu.Unlock(); return nil },
	}}
	owner := &browserProcessOwner{child: child, profile: "profile", removeProfile: func(string) error { mu.Lock(); events = append(events, "profile"); mu.Unlock(); return nil }}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "tree,kill,wait,profile" {
		t.Fatalf("process/profile cleanup order = %v, want tree, kill, wait, profile", events)
	}
}

func TestBrowserProcessOwnerIgnoresRootAccessDeniedOnlyAfterTreeAndWaitSucceed(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Win32 ERROR_ACCESS_DENIED behavior is Windows-specific")
	}
	var mu sync.Mutex
	var events []string
	profileRemovals := 0
	child := &commandBrowserChild{ops: commandBrowserChildOps{
		terminateTree: func(context.Context) error {
			mu.Lock()
			events = append(events, "tree")
			mu.Unlock()
			return nil
		},
		killRoot: func() error {
			mu.Lock()
			events = append(events, "kill")
			mu.Unlock()
			return syscall.Errno(5) // ERROR_ACCESS_DENIED after taskkill removed the root.
		},
		wait: func() error {
			mu.Lock()
			events = append(events, "wait")
			mu.Unlock()
			return nil // cmd.Wait completed and confirmed the owned root was reaped.
		},
	}}
	owner := &browserProcessOwner{
		child: child, profile: "reaped-profile",
		removeProfile: func(string) error {
			mu.Lock()
			defer mu.Unlock()
			if len(events) == 0 || events[len(events)-1] != "wait" {
				t.Errorf("profile removal ran before successful Wait: %v", events)
			}
			profileRemovals++
			events = append(events, "profile")
			return nil
		},
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("Close error after successful tree termination and Wait: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "tree,kill,wait,profile" {
		t.Fatalf("cleanup events = %v, want profile removal after Wait", events)
	}
	if profileRemovals != 1 {
		t.Fatalf("profile removals = %d, want exactly one", profileRemovals)
	}
}

func TestBrowserProcessOwnerRetainsRootKillAndWaitFailures(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Win32 ERROR_ACCESS_DENIED behavior is Windows-specific")
	}
	tests := []struct {
		name      string
		terminate error
		wait      error
		want      string
	}{
		{name: "tree termination failure", terminate: errors.New("taskkill failed"), want: "taskkill failed"},
		{name: "wait failure", wait: errors.New("wait failed"), want: "wait failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profileRemovals := 0
			child := &commandBrowserChild{ops: commandBrowserChildOps{
				terminateTree: func(context.Context) error { return tt.terminate },
				killRoot:      func() error { return syscall.Errno(5) },
				wait:          func() error { return tt.wait },
			}}
			owner := &browserProcessOwner{child: child, profile: "must-remain", removeProfile: func(string) error { profileRemovals++; return nil }}
			err := owner.Close()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Close error = %v, want %q", err, tt.want)
			}
			if profileRemovals != 0 {
				t.Fatalf("profile removals = %d, want none after failed cleanup", profileRemovals)
			}
		})
	}
}

func TestTaskkillCommandErrorIncludesCombinedOutput(t *testing.T) {
	got := taskkillCommandError(4242, []byte("ERROR: process not found\r\n"), errors.New("exit status 128"))
	if got == nil {
		t.Fatal("taskkillCommandError returned nil")
	}
	for _, want := range []string{"4242", "exit status 128", "ERROR: process not found"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("taskkill error %q does not contain %q", got, want)
		}
	}
}

func TestTaskkillFailureOnlyNamesRootAsMissing(t *testing.T) {
	tests := []struct {
		name     string
		pid      int
		output   string
		exitCode int
		want     bool
	}{
		{name: "english root missing", pid: 4242, output: `ERROR: The process "4242" could not be found.`, exitCode: 128, want: true},
		{name: "japanese root missing", pid: 4242, output: `エラー: プロセス "4242" が見つかりませんでした。`, exitCode: 128, want: true},
		{name: "different pid missing", pid: 4242, output: `ERROR: The process "4243" could not be found.`, exitCode: 128},
		{name: "other descendant failed", pid: 4242, output: "SUCCESS: PID 4242 was terminated.\r\nERROR: PID 4243 operation is not supported.", exitCode: 128},
		{name: "unexpected exit code", pid: 4242, output: `ERROR: The process "4242" could not be found.`, exitCode: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &dedicatedBrowserTreeTerminationError{rootPID: tt.pid, output: tt.output, exitCode: tt.exitCode, err: errors.New("taskkill failed")}
			if got := taskkillFailureOnlyNamesRootAsMissing(err, tt.pid); got != tt.want {
				t.Fatalf("taskkillFailureOnlyNamesRootAsMissing() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestCommandBrowserChildDoesNotHideOtherDescendantFailuresAfterGracefulShutdown(t *testing.T) {
	child := &commandBrowserChild{
		cmd:                       &exec.Cmd{Process: &os.Process{Pid: 4242}},
		gracefulShutdownRequested: true,
		ops: commandBrowserChildOps{
			terminateTree: func(context.Context) error {
				return &dedicatedBrowserTreeTerminationError{
					rootPID: 4242, output: "SUCCESS: PID 4242 was terminated.\r\nERROR: PID 4243 operation is not supported.",
					exitCode: 128, err: errors.New("exit status 128"),
				}
			},
			killRoot: func() error { return syscall.Errno(5) },
			wait:     func() error { return nil },
		},
	}
	err := child.Close()
	if err == nil || !strings.Contains(err.Error(), "operation is not supported") {
		t.Fatalf("Close error = %v, want the unhandled descendant failure", err)
	}
}

func TestBrowserProcessOwnerRetainsUnrelatedRootKillFailure(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Win32 root process cleanup contract is Windows-specific")
	}
	profileRemovals := 0
	killErr := errors.New("unrelated root kill failure")
	child := &commandBrowserChild{ops: commandBrowserChildOps{
		terminateTree: func(context.Context) error { return nil },
		killRoot:      func() error { return killErr },
		wait:          func() error { return nil },
	}}
	owner := &browserProcessOwner{child: child, profile: "unrelated-kill-error-profile", removeProfile: func(string) error { profileRemovals++; return nil }}
	err := owner.Close()
	if !errors.Is(err, killErr) {
		t.Fatalf("Close error = %v, want unrelated root Kill error", err)
	}
	if profileRemovals != 0 {
		t.Fatalf("profile removals = %d, want none after unrelated root Kill error", profileRemovals)
	}
}

func TestBrowserProcessOwnerWaitTimeoutPreventsProfileRemoval(t *testing.T) {
	started := make(chan struct{})
	gate := make(chan struct{})
	profileRemovals := 0
	child := &commandBrowserChild{ops: commandBrowserChildOps{
		terminateTree: func(context.Context) error { return nil },
		killRoot:      func() error { return syscall.Errno(5) },
		wait: func() error {
			close(started)
			<-gate
			return nil
		},
	}}
	owner := &browserProcessOwner{child: child, profile: "timed-out-profile", removeProfile: func(string) error { profileRemovals++; return nil }}
	err := owner.closeWithTimeout(30 * time.Millisecond)
	close(gate)
	child.Close() // join the bounded child cleanup goroutine before reading its state.
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Close error = %v, want Wait timeout", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("Wait was not reached before timeout")
	}
	if profileRemovals != 0 {
		t.Fatalf("profile removals = %d, want none after Wait timeout", profileRemovals)
	}
}

func TestBrowserProcessOwnerSurfacesProfileRemovalFailure(t *testing.T) {
	var events []string
	child := &commandBrowserChild{ops: commandBrowserChildOps{
		terminateTree: func(context.Context) error { events = append(events, "tree"); return nil },
		killRoot:      func() error { events = append(events, "kill"); return nil },
		wait:          func() error { events = append(events, "wait"); return nil },
	}}
	owner := &browserProcessOwner{
		child: child, profile: "profile",
		removeProfile: func(string) error { events = append(events, "profile"); return errors.New("locked profile") },
	}
	err := owner.Close()
	if err == nil || !strings.Contains(err.Error(), "locked profile") {
		t.Fatalf("Close error = %v, want visible profile removal failure", err)
	}
	if strings.Join(events, ",") != "tree,kill,wait,profile" {
		t.Fatalf("cleanup events = %v, want process reap before profile removal", events)
	}
	if repeated := owner.Close(); repeated == nil || repeated.Error() != err.Error() {
		t.Fatalf("repeated Close error = %v, want cached %v", repeated, err)
	}
}

func TestDedicatedBrowserOwnerStartsAndClosesExactlyOnce(t *testing.T) {
	ownerCtx := context.Background()
	child := &fakeBrowserChild{}
	profileRemovals := 0
	var launchCtx context.Context
	var launchPath string
	var launchArgs []string
	runtime := browserProcessRuntime{
		findBrowser: func(configured string) (string, error) { return configured, nil },
		reservePort: func() (int, error) { return 9222, nil },
		makeProfile: func() (string, error) { return "fake-profile", nil },
		launch: func(ctx context.Context, path string, args []string) (browserProcessChild, error) {
			launchCtx, launchPath, launchArgs = ctx, path, append([]string(nil), args...)
			return child, nil
		},
		discover: func(context.Context, int) (string, error) {
			return "ws://127.0.0.1:9222/devtools/browser/fake", nil
		},
		removeProfile: func(string) error { profileRemovals++; return nil },
	}
	pool, err := newBrowserProcessPoolWithRuntime(ownerCtx, "fake-chrome", runtime)
	if err != nil {
		t.Fatal(err)
	}
	if launchCtx != ownerCtx || launchPath != "fake-chrome" {
		t.Fatalf("process launch owner/path = %v/%q", launchCtx, launchPath)
	}
	if containsArg(launchArgs, "--no-sandbox") || !containsArg(launchArgs, "--headless=new") || !containsArg(launchArgs, "--disable-gpu") {
		t.Fatalf("dedicated browser args changed safe default: %v", launchArgs)
	}
	if pool.browserWSURL != "ws://127.0.0.1:9222/devtools/browser/fake" {
		t.Fatalf("stored browser WebSocket URL = %q", pool.browserWSURL)
	}
	if pool.cdp != nil {
		t.Fatal("T3.2 connected page/browser CDP operations before T3.3")
	}
	if _, err := pool.Acquire(context.Background(), "page.html"); err == nil {
		t.Fatal("page lease was enabled before T3.3 adds page CDP operations")
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if child.closeCount() != 1 || profileRemovals != 1 {
		t.Fatalf("close/profile cleanup = %d/%d, want 1/1", child.closeCount(), profileRemovals)
	}
}

func TestDedicatedBrowserDiscoveryFailureClosesProcessAndProfile(t *testing.T) {
	child := &fakeBrowserChild{}
	profileRemovals := 0
	runtime := browserProcessRuntime{
		findBrowser: func(configured string) (string, error) { return configured, nil },
		reservePort: func() (int, error) { return 9222, nil },
		makeProfile: func() (string, error) { return "failed-profile", nil },
		launch:      func(context.Context, string, []string) (browserProcessChild, error) { return child, nil },
		discover: func(context.Context, int) (string, error) {
			return "ws://127.0.0.1:9222/devtools/page/not-browser", nil
		},
		removeProfile: func(string) error { profileRemovals++; return nil },
	}
	if _, err := newBrowserProcessPoolWithRuntime(context.Background(), "fake-chrome", runtime); err == nil {
		t.Fatal("startup succeeded after browser discovery failure")
	}
	if child.closeCount() != 1 || profileRemovals != 1 {
		t.Fatalf("failed startup cleanup = process %d/profile %d, want 1/1", child.closeCount(), profileRemovals)
	}
}

func TestDedicatedBrowserLaunchFailureClosesPartialChildAndProfile(t *testing.T) {
	child := &fakeBrowserChild{}
	profileRemovals := 0
	runtime := browserProcessRuntime{
		findBrowser: func(configured string) (string, error) { return configured, nil },
		reservePort: func() (int, error) { return 9222, nil },
		makeProfile: func() (string, error) { return "partial-profile", nil },
		launch: func(context.Context, string, []string) (browserProcessChild, error) {
			return child, errors.New("launch returned partial child")
		},
		discover: func(context.Context, int) (string, error) {
			t.Fatal("discovery called after launch error")
			return "", nil
		},
		removeProfile: func(string) error { profileRemovals++; return nil },
	}
	if _, err := startDedicatedBrowserWithRuntime(context.Background(), "fake-chrome", runtime); err == nil {
		t.Fatal("startup succeeded after launch error")
	}
	if child.closeCount() != 1 || profileRemovals != 1 {
		t.Fatalf("partial launch cleanup = process %d/profile %d, want 1/1", child.closeCount(), profileRemovals)
	}
}

func TestDedicatedBrowserOwnerCancellationDuringDiscoveryCleansUp(t *testing.T) {
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	child := &fakeBrowserChild{}
	profileRemovals := 0
	launchDone := make(chan struct{})
	runtime := browserProcessRuntime{
		findBrowser: func(configured string) (string, error) { return configured, nil },
		reservePort: func() (int, error) { return 9222, nil },
		makeProfile: func() (string, error) { return "canceled-profile", nil },
		launch: func(context.Context, string, []string) (browserProcessChild, error) {
			close(launchDone)
			return child, nil
		},
		discover: func(ctx context.Context, _ int) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		removeProfile: func(string) error { profileRemovals++; return nil },
	}
	result := make(chan error, 1)
	go func() {
		_, err := newBrowserProcessPoolWithRuntime(ownerCtx, "fake-chrome", runtime)
		result <- err
	}()
	<-launchDone
	cancelOwner()
	if err := <-result; err == nil {
		t.Fatal("startup succeeded after owner cancellation during discovery")
	}
	if child.closeCount() != 1 || profileRemovals != 1 {
		t.Fatalf("canceled startup cleanup = process %d/profile %d, want 1/1", child.closeCount(), profileRemovals)
	}
}

func TestDedicatedBrowserLateDiscoveryResponseAfterOwnerCancelCleansUp(t *testing.T) {
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	child := &fakeBrowserChild{}
	profileRemovals := 0
	discoveryStarted := make(chan struct{})
	allowLateResponse := make(chan struct{})
	runtime := browserProcessRuntime{
		findBrowser: func(configured string) (string, error) { return configured, nil },
		reservePort: func() (int, error) { return 9222, nil },
		makeProfile: func() (string, error) { return "late-profile", nil },
		launch:      func(context.Context, string, []string) (browserProcessChild, error) { return child, nil },
		discover: func(context.Context, int) (string, error) {
			close(discoveryStarted)
			<-allowLateResponse
			return "ws://127.0.0.1:9222/devtools/browser/late", nil
		},
		removeProfile: func(string) error { profileRemovals++; return nil },
	}
	result := make(chan error, 1)
	go func() {
		_, err := newBrowserProcessPoolWithRuntime(ownerCtx, "fake-chrome", runtime)
		result <- err
	}()
	<-discoveryStarted
	cancelOwner()
	close(allowLateResponse)
	if err := <-result; err == nil {
		t.Fatal("pool startup succeeded with a late discovery response after owner cancellation")
	}
	if child.closeCount() != 1 || profileRemovals != 1 {
		t.Fatalf("late discovery cleanup = process %d/profile %d, want 1/1", child.closeCount(), profileRemovals)
	}
}

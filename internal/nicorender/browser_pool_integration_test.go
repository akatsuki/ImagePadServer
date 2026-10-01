package nicorender

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

// This test is deliberately opt-in. It owns every Chrome process and profile
// it creates, and never searches for or attaches to a user's browser.
func TestBrowserPoolChromeIntegration(t *testing.T) {
	if os.Getenv("NICO_BROWSER_POOL_INTEGRATION") != "1" {
		t.Skip("set NICO_BROWSER_POOL_INTEGRATION=1 and NICO_BROWSER_POOL_BROWSER to run the owned Chrome integration")
	}
	browserPath := strings.TrimSpace(os.Getenv("NICO_BROWSER_POOL_BROWSER"))
	if browserPath == "" {
		t.Fatal("NICO_BROWSER_POOL_BROWSER must point to the Chrome executable")
	}
	info, err := os.Stat(browserPath)
	if err != nil || info.IsDir() {
		t.Fatalf("Chrome executable is unavailable at %q: %v", browserPath, err)
	}

	t.Run("same-process-capture-isolation-and-fresh-parity", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		pool, err := NewBrowserPool(ctx, RenderOptions{BrowserPath: browserPath})
		if err != nil {
			t.Fatalf("start dedicated Chrome pool: %v", err)
		}
		child, profile := registerOwnedPoolCleanup(t, pool, false)
		pid := child.cmd.Process.Pid
		if !strings.HasPrefix(filepath.Base(profile), "imagepad-niconi-profile-") {
			t.Fatalf("pool profile is not a new private profile: %q", profile)
		}
		if !isUnderDirectory(profile, os.TempDir()) {
			t.Fatalf("pool profile is outside the temp directory: %q", profile)
		}
		if product := browserPoolProduct(t, ctx, pool); !strings.Contains(product, "/153.") {
			t.Fatalf("owned browser product %q does not identify Chrome 153", product)
		}

		options := timelinePoolTestOptions()
		options.Width, options.Height, options.DurationMs = 640, 360, 1000
		options.BrowserPath = browserPath
		snapshotA := browserPoolIntegrationSnapshot("A", "alpha scene with red canvas state")
		snapshotB := browserPoolIntegrationSnapshot("B", "beta scene after isolated job")
		seedA, seedB := uint32(104729), uint32(130363)
		pooledA, _, err := captureCommentTimelineSeededForTest(ctx, snapshotA, options.WithBrowserPool(pool), seedA)
		if err != nil {
			t.Fatalf("pooled capture A: %v", err)
		}
		if child.cmd.ProcessState != nil || child.cmd.Process.Pid != pid {
			t.Fatalf("owned Chrome process changed after capture A: pid=%d state=%v", child.cmd.Process.Pid, child.cmd.ProcessState)
		}
		pooledB, _, err := captureCommentTimelineSeededForTest(ctx, snapshotB, options.WithBrowserPool(pool), seedB)
		if err != nil {
			t.Fatalf("pooled capture B: %v", err)
		}
		if child.cmd.ProcessState != nil || child.cmd.Process.Pid != pid {
			t.Fatalf("captures did not reuse the same owned Chrome process: pid=%d state=%v", child.cmd.Process.Pid, child.cmd.ProcessState)
		}

		freshB, _, err := captureCommentTimelineSeededForTest(ctx, snapshotB, options, seedB)
		if err != nil {
			t.Fatalf("fresh capture B: %v", err)
		}
		pooledBBytes := serializeTimelineForBrowserPoolTest(t, pooledB)
		freshBBytes := serializeTimelineForBrowserPoolTest(t, freshB)
		if !bytes.Equal(pooledBBytes, freshBBytes) {
			t.Fatalf("pooled B differs from fresh B after pooled A: pooled bytes=%d fresh bytes=%d", len(pooledBBytes), len(freshBBytes))
		}
		if bytes.Equal(serializeTimelineForBrowserPoolTest(t, pooledA), pooledBBytes) {
			t.Fatal("distinct A and B comment scenes produced identical serialized timelines")
		}
		t.Logf("pooled captures reused owned Chrome PID %d; B matched fresh NCT1 (%d bytes)", pid, len(pooledBBytes))

		verifyBrowserPoolPageRealmIsolation(t, ctx, pool)
		if err := pool.Close(); err != nil {
			t.Fatalf("close healthy pool: %v", err)
		}
	})

	t.Run("navigation-failure-is-terminal-and-cleans-owned-resources", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		pool, err := NewBrowserPool(ctx, RenderOptions{BrowserPath: browserPath})
		if err != nil {
			t.Fatalf("start dedicated Chrome pool: %v", err)
		}
		child, _ := registerOwnedPoolCleanup(t, pool, true)
		missingPage := filepath.Join(t.TempDir(), "page-that-does-not-exist.html")
		_, err = pool.Acquire(ctx, missingPage)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "err_file_not_found") {
			t.Fatalf("missing-page acquisition error = %v, want Chrome navigation failure", err)
		}
		if _, retryErr := pool.Acquire(ctx, missingPage); retryErr == nil {
			t.Fatal("pool accepted a new acquire after navigation failure")
		}
		if pool.ActiveLease() != nil {
			t.Fatal("navigation failure left an active lease")
		}
		if err := pool.Close(); err == nil {
			t.Fatal("terminal navigation failure was not surfaced by Close")
		}
		assertOwnedChildReaped(t, child)
	})

	t.Run("disconnect-terminates-only-the-owned-browser-and-cleans-profile", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		pool, err := NewBrowserPool(ctx, RenderOptions{BrowserPath: browserPath})
		if err != nil {
			t.Fatalf("start dedicated Chrome pool: %v", err)
		}
		child, _ := registerOwnedPoolCleanup(t, pool, true)
		pagePath := writeBrowserPoolTestPage(t, "<!doctype html><body>disconnect probe</body>")
		if err := child.Close(); err != nil {
			t.Fatalf("terminate owned Chrome child: %v", err)
		}
		assertOwnedChildReaped(t, child)
		if _, err := pool.Acquire(ctx, pagePath); err == nil {
			t.Fatal("pool accepted an acquire after its owned Chrome process disconnected")
		}
		if pool.broken == nil {
			t.Fatal("disconnect did not leave the pool terminal")
		}
		if err := pool.Close(); err == nil {
			t.Fatal("pool Close did not report the terminal disconnect")
		}
	})
}

// TestBrowserPoolCloseDiagnostic is a one-process, opt-in diagnostic used to
// separate taskkill, root Process.Kill, and Wait outcomes on Windows.
func TestBrowserPoolCloseDiagnostic(t *testing.T) {
	if os.Getenv("NICO_BROWSER_POOL_CLOSE_DIAGNOSTIC") != "1" {
		t.Skip("set NICO_BROWSER_POOL_CLOSE_DIAGNOSTIC=1 to diagnose owned Chrome cleanup")
	}
	browserPath := strings.TrimSpace(os.Getenv("NICO_BROWSER_POOL_BROWSER"))
	if browserPath == "" {
		t.Fatal("NICO_BROWSER_POOL_BROWSER must point to the Chrome executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	runtime := defaultBrowserProcessRuntime()
	baseLaunch := runtime.launch
	profile := ""
	baseMakeProfile := runtime.makeProfile
	runtime.makeProfile = func() (string, error) {
		path, err := baseMakeProfile()
		profile = path
		return path, err
	}
	var child *commandBrowserChild
	runtime.launch = func(launchCtx context.Context, path string, args []string) (browserProcessChild, error) {
		created, err := baseLaunch(launchCtx, path, args)
		if err != nil {
			return created, err
		}
		var ok bool
		child, ok = created.(*commandBrowserChild)
		if !ok || child.cmd == nil || child.cmd.Process == nil {
			return created, fmt.Errorf("unexpected owned child %T", created)
		}
		pid := child.cmd.Process.Pid
		t.Logf("owned child PID=%d parent PID=%d executable=%q args=%q profile=%q owner=dedicated BrowserPool", pid, os.Getpid(), child.cmd.Path, child.cmd.Args, profile)
		t.Logf("Win32_Process at launch: %s", browserPoolWindowsProcessDetails(pid))
		child.ops.terminateTree = func(stopCtx context.Context) error {
			cmd := exec.CommandContext(stopCtx, "taskkill", "/PID", fmt.Sprint(pid), "/T", "/F")
			output, taskkillErr := cmd.CombinedOutput()
			t.Logf("taskkill exit=%d error=%v stdout+stderr=%q", browserPoolExitCode(taskkillErr), taskkillErr, string(output))
			return taskkillErr
		}
		child.ops.killRoot = func() error {
			before := browserPoolWindowsProcessDetails(pid)
			killErr := child.cmd.Process.Kill()
			t.Logf("root Process.Kill before=%q result=%v", before, killErr)
			return killErr
		}
		child.ops.wait = func() error {
			waitErr := child.cmd.Wait()
			code := -1
			if child.cmd.ProcessState != nil {
				code = child.cmd.ProcessState.ExitCode()
			}
			t.Logf("cmd.Wait raw result=%v exitCode=%d processState=%v", waitErr, code, child.cmd.ProcessState)
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				return nil
			}
			return waitErr
		}
		return created, nil
	}
	pool, err := newBrowserProcessPoolWithRuntime(ctx, browserPath, runtime)
	if err != nil {
		if child != nil {
			assertOwnedChildReaped(t, child)
		}
		if profile != "" && browserPoolPathExists(profile) && browserPoolProcessesForProfile(profile) == "" {
			if cleanupErr := removeBrowserProfileChecked(profile); cleanupErr != nil {
				t.Errorf("remove owned profile after verifying no matching Chrome process: %v", cleanupErr)
			}
		}
		t.Fatal(err)
	}
	cdp, err := newBrowserProcessCDP(ctx, pool.browserWSURL)
	if err != nil {
		_ = pool.Close()
		t.Fatal(err)
	}
	pool.cdp = cdp
	if child == nil {
		t.Fatal("runtime did not return its owned child")
	}
	pid := child.cmd.Process.Pid
	t.Cleanup(func() {
		closeErr := pool.Close()
		t.Logf("pool.Close error=%v childState=%v profileExists=%t", closeErr, child.cmd.ProcessState, browserPoolPathExists(profile))
		if child.cmd.ProcessState == nil || !child.cmd.ProcessState.Exited() {
			t.Errorf("diagnostic child was not reaped: %v", child.cmd.ProcessState)
			return
		}
		matching := browserPoolProcessesForProfile(profile)
		if matching != "" {
			t.Errorf("owned Chrome process remains; retaining profile %q: %s", profile, matching)
			return
		}
		if browserPoolPathExists(profile) {
			if cleanupErr := removeBrowserProfileChecked(profile); cleanupErr != nil {
				t.Errorf("remove owned profile after confirming no matching Chrome process: %v", cleanupErr)
			}
		}
		if browserPoolPathExists(profile) {
			t.Errorf("owned browser profile remains after cleanup: %q", profile)
		}
	})
	t.Logf("diagnostic pool ready with PID=%d profile=%q", pid, profile)
	closeErr := pool.Close()
	t.Logf("pool.Close returned error=%v childState=%v profileExists=%t", closeErr, child.cmd.ProcessState, browserPoolPathExists(profile))
	if child.cmd.ProcessState == nil || !child.cmd.ProcessState.Exited() {
		t.Errorf("diagnostic child was not reaped: %v", child.cmd.ProcessState)
	}
	if err := closeErr; err != nil {
		t.Logf("existing bounded cleanup reported error; profile is retained by contract: %q", profile)
	}
}

func browserPoolWindowsProcessDetails(pid int) string {
	command := fmt.Sprintf("$p=Get-CimInstance Win32_Process -Filter 'ProcessId = %d'; if ($p) { '{0}|{1}|{2}|{3}' -f $p.ProcessId,$p.ParentProcessId,$p.ExecutablePath,$p.CommandLine }", pid)
	output, err := exec.Command("powershell.exe", "-NoProfile", "-Command", command).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("inventory error: %v output=%q", err, string(output))
	}
	return strings.TrimSpace(string(output))
}

func browserPoolProcessesForProfile(profile string) string {
	command := `Get-CimInstance Win32_Process | Where-Object { $_.Name -eq 'chrome.exe' -and $_.CommandLine.Contains($env:NICO_BROWSER_POOL_PROFILE) } | ForEach-Object { '{0}|{1}|{2}' -f $_.ProcessId,$_.ParentProcessId,$_.CommandLine }`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", command)
	cmd.Env = append(os.Environ(), "NICO_BROWSER_POOL_PROFILE="+profile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("inventory error: %v output=%q", err, string(output))
	}
	return strings.TrimSpace(string(output))
}

func browserPoolPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func browserPoolExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func browserPoolIntegrationSnapshot(id, body string) niconico.Snapshot {
	snapshot := timelinePoolTestSnapshot()
	snapshot.VideoID = "sm-browser-pool-" + id
	snapshot.Threads[0].Comments[0].ID = "comment-" + id
	snapshot.Threads[0].Comments[0].Body = body
	snapshot.Threads[0].Comments[0].PostedAt = "2020-01-02T03:04:05Z"
	return snapshot
}

func serializeTimelineForBrowserPoolTest(t *testing.T, scene CommentTimeline) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := WriteCommentTimeline(&out, scene); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func registerOwnedPoolCleanup(t *testing.T, pool *BrowserPool, terminalCloseExpected bool) (*commandBrowserChild, string) {
	t.Helper()
	if pool.browser == nil {
		t.Fatal("pool has no owned browser process")
	}
	child, ok := pool.browser.child.(*commandBrowserChild)
	if !ok || child.cmd == nil || child.cmd.Process == nil {
		t.Fatalf("pool child has unexpected type %T", pool.browser.child)
	}
	profile := pool.browser.profile
	t.Cleanup(func() {
		if err := pool.Close(); err != nil && !terminalCloseExpected {
			t.Errorf("close dedicated browser pool: %v", err)
		}
		assertOwnedChildReaped(t, child)
		if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("owned browser profile remains after cleanup: stat error=%v path=%q", err, profile)
		}
	})
	return child, profile
}

func assertOwnedChildReaped(t *testing.T, child *commandBrowserChild) {
	t.Helper()
	if child.cmd.ProcessState == nil || !child.cmd.ProcessState.Exited() {
		t.Errorf("owned Chrome child was not reaped: process state=%v", child.cmd.ProcessState)
	}
}

func browserPoolProduct(t *testing.T, ctx context.Context, pool *BrowserPool) string {
	t.Helper()
	cdp, ok := pool.cdp.(*browserProcessCDP)
	if !ok {
		t.Fatalf("pool CDP has unexpected type %T", pool.cdp)
	}
	result, err := cdp.peer.call(ctx, "Browser.getVersion", map[string]any{})
	if err != nil {
		t.Fatalf("Browser.getVersion: %v", err)
	}
	var version struct {
		Product string `json:"product"`
	}
	if err := json.Unmarshal(result, &version); err != nil {
		t.Fatalf("decode Browser.getVersion: %v", err)
	}
	return version.Product
}

func verifyBrowserPoolPageRealmIsolation(t *testing.T, ctx context.Context, pool *BrowserPool) {
	t.Helper()
	pageA := writeBrowserPoolTestPage(t, "<!doctype html><body></body>")
	pageB := writeBrowserPoolTestPage(t, "<!doctype html><body></body>")
	leaseA, err := pool.Acquire(ctx, pageA)
	if err != nil {
		t.Fatalf("acquire contamination page A: %v", err)
	}
	var contamination struct {
		Pixel            []int `json:"pixel"`
		WebGLAvailable   bool  `json:"webglAvailable"`
		StorageAvailable bool  `json:"storageAvailable"`
	}
	if _, err := evalBrowserPoolPage(ctx, leaseA, `(() => {
  window.__nctPoolGlobal = 'from-A';
  document.body.style.backgroundColor = 'rgb(255, 0, 0)';
  const canvas = document.createElement('canvas'); canvas.width = 1; canvas.height = 1;
  const context = canvas.getContext('2d'); context.fillStyle = '#ff0000'; context.fillRect(0, 0, 1, 1);
  window.__nctPoolCanvas = canvas;
  let pixel = Array.from(context.getImageData(0, 0, 1, 1).data);
  let webglAvailable = false;
  if (window.WebGLRenderingContext && WebGLRenderingContext.prototype) {
    webglAvailable = true;
    WebGLRenderingContext.prototype.__nctPoolHook = 'from-A';
    WebGLRenderingContext.prototype.getParameter = function(){ return 991337; };
  }
  let storageAvailable = false;
  try { localStorage.setItem('__nctPoolLeak', 'from-A'); storageAvailable = true; } catch (_) {}
  return {pixel, webglAvailable, storageAvailable};
})()`, &contamination); err != nil {
		t.Fatalf("install page A contamination probes: %v", err)
	}
	if len(contamination.Pixel) != 4 || contamination.Pixel[0] != 255 || contamination.Pixel[1] != 0 || contamination.Pixel[2] != 0 || contamination.Pixel[3] != 255 {
		t.Fatalf("page A did not retain its red canvas probe: %+v", contamination)
	}
	contextA, targetA := leaseA.contextID, leaseA.targetID
	if err := pool.Release(leaseA); err != nil {
		t.Fatalf("release page A lease: %v", err)
	}

	leaseB, err := pool.Acquire(ctx, pageB)
	if err != nil {
		t.Fatalf("acquire isolation page B: %v", err)
	}
	defer func() {
		if err := pool.Release(leaseB); err != nil {
			t.Errorf("release page B lease: %v", err)
		}
	}()
	if leaseB.contextID == contextA || leaseB.targetID == targetA {
		t.Fatalf("job B reused browser state IDs: context %q=>%q target %q=>%q", contextA, leaseB.contextID, targetA, leaseB.targetID)
	}
	var state struct {
		Global          string `json:"global"`
		Canvas          string `json:"canvas"`
		CanvasCount     int    `json:"canvasCount"`
		Background      string `json:"background"`
		WebGLHook       string `json:"webglHook"`
		Storage         string `json:"storage"`
		WebGLAvailable  bool   `json:"webglAvailable"`
		StorageReadable bool   `json:"storageReadable"`
	}
	if _, err := evalBrowserPoolPage(ctx, leaseB, `(() => {
  const p = window.WebGLRenderingContext && WebGLRenderingContext.prototype;
  let storage = '', storageReadable = false;
  try { storage = localStorage.getItem('__nctPoolLeak') || ''; storageReadable = true; } catch (_) {}
  return {
    global: window.__nctPoolGlobal || '', canvas: typeof window.__nctPoolCanvas,
    canvasCount: document.querySelectorAll('canvas').length,
    background: getComputedStyle(document.body).backgroundColor,
    webglHook: p && p.__nctPoolHook || '', webglAvailable: !!p,
    storage, storageReadable
  };
})()`, &state); err != nil {
		t.Fatalf("inspect page B isolation state: %v", err)
	}
	if state.Global != "" || state.Canvas != "undefined" || state.CanvasCount != 0 || state.Background == "rgb(255, 0, 0)" || state.WebGLHook != "" || state.Storage == "from-A" {
		t.Fatalf("page A state leaked into page B: %+v", state)
	}
	if state.WebGLAvailable {
		t.Log("WebGL prototype hook was isolated between BrowserContexts; GPU driver/device state remains outside this page-realm check")
	} else {
		t.Log("this Chrome build exposes no WebGLRenderingContext; global/canvas/color isolation passed, GPU driver/device state remains untested")
	}
	if state.StorageReadable {
		t.Log("localStorage was readable in both file pages; page B observed no job A value")
	}
}

func evalBrowserPoolPage(ctx context.Context, lease *BrowserLease, expression string, output ...any) (json.RawMessage, error) {
	session, ok := lease.pageSession.(*cdpPageSession)
	if !ok {
		return nil, fmt.Errorf("unexpected page session type %T", lease.pageSession)
	}
	result, err := session.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return nil, fmt.Errorf("decode Runtime.evaluate result: %w", err)
	}
	if len(envelope.ExceptionDetails) != 0 && string(envelope.ExceptionDetails) != "null" {
		return nil, fmt.Errorf("page evaluation exception: %s", envelope.ExceptionDetails)
	}
	if len(output) > 0 && output[0] != nil {
		if err := json.Unmarshal(envelope.Result.Value, output[0]); err != nil {
			return nil, fmt.Errorf("decode page evaluation value: %w", err)
		}
	}
	return envelope.Result.Value, nil
}

func writeBrowserPoolTestPage(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "browser-pool-page.html")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func isUnderDirectory(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

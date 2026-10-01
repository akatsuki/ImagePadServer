package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/nicoexportworker"
)

func TestNicoWorkerSessionOwnsPrivateTempDirectoryUntilRetirement(t *testing.T) {
	var spec nicoexportbudget.ProcessSpec
	manager := newNicoWorkerSessionManager(context.Background(), func(_ context.Context, got nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		spec = got
		return newFakeNicoSessionProcess(got), nil
	}, func() (string, error) { return "private-temp-session", nil })
	defer func() { _ = manager.close() }()

	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("private-temp-run", "media"), nicoWorkerCPUOptions(), nil); err != nil {
		t.Fatal(err)
	}
	var tempRoot string
	for _, key := range []string{"TEMP", "TMP", "TMPDIR"} {
		values := nicoWorkerSessionTestEnvValues(spec.Env, key)
		if len(values) != 1 || values[0] == "" {
			t.Fatalf("worker %s environment values = %v, want one private temp root", key, values)
		}
		if tempRoot == "" {
			tempRoot = values[0]
		} else if values[0] != tempRoot {
			t.Fatalf("worker %s=%q, want shared private root %q", key, values[0], tempRoot)
		}
	}
	if !filepath.IsAbs(tempRoot) {
		t.Fatalf("worker temp root is not absolute: %q", tempRoot)
	}
	if info, err := os.Stat(tempRoot); err != nil || !info.IsDir() {
		t.Fatalf("worker temp root is unavailable: %q (%v)", tempRoot, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	leftover := filepath.Join(tempRoot, "chrome_chrome_url_fetcher_test")
	if err := os.Mkdir(leftover, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "scratch.tmp"), []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.close(); err != nil {
		t.Fatalf("retire worker session: %v", err)
	}
	if _, err := os.Stat(tempRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session-owned temp root remains after process retirement: %q (err=%v)", tempRoot, err)
	}
}

func TestNicoWorkerSessionStartFailureRemovesPrivateTempDirectory(t *testing.T) {
	var tempRoot string
	manager := newNicoWorkerSessionManager(context.Background(), func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		values := nicoWorkerSessionTestEnvValues(spec.Env, "TEMP")
		if len(values) == 1 {
			tempRoot = values[0]
		}
		return nil, errors.New("expected process start failure")
	}, func() (string, error) { return "private-temp-start-failure", nil })
	if err := manager.start(); err == nil || !strings.Contains(err.Error(), "expected process start failure") {
		t.Fatalf("manager.start error = %v, want injected start failure", err)
	}
	if tempRoot == "" {
		t.Fatal("worker start did not receive a private temp root")
	}
	if _, err := os.Stat(tempRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private temp root remains after process start failure: %q (err=%v)", tempRoot, err)
	}
}

func nicoWorkerSessionTestEnvValues(env []string, key string) []string {
	var values []string
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, key) {
			values = append(values, value)
		}
	}
	return values
}

func TestNicoWorkerSessionReusesProcessAfterSuccessfulRequestContextEnds(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	var starts atomic.Int32
	var process *fakeNicoSessionProcess
	manager := newNicoWorkerSessionManager(owner, func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		starts.Add(1)
		process = newFakeNicoSessionProcess(spec)
		return process, nil
	}, func() (string, error) { return "parent-session-1", nil })

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	first, firstReport, err := manager.run(firstCtx, validNicoSessionRequest("run-1", "media-1"), nicoWorkerCPUOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != "run-1" || first.MediaID != "media-1" || !first.OK || !firstReport.Verified {
		t.Fatalf("first result=%+v report=%+v", first, firstReport)
	}
	cancelFirst()
	if process.closeCalls.Load() != 0 || process.waitCalls.Load() != 0 {
		t.Fatalf("successful request context ended but session retired: close=%d wait=%d", process.closeCalls.Load(), process.waitCalls.Load())
	}

	second, _, err := manager.run(context.Background(), validNicoSessionRequest("run-2", "media-2"), nicoWorkerCPUOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.RunID != "run-2" || second.MediaID != "media-2" || starts.Load() != 1 {
		t.Fatalf("second result=%+v process starts=%d, want reuse", second, starts.Load())
	}
	if got := process.runCount.Load(); got != 2 {
		t.Fatalf("wire run count=%d, want two serialized requests", got)
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	if process.closeCalls.Load() != 0 || process.waitCalls.Load() != 1 {
		t.Fatalf("EOF retirement close/wait calls=%d/%d, want 0/1", process.closeCalls.Load(), process.waitCalls.Load())
	}
}

func TestNicoWorkerSessionSendsEOFBeforeGracefulPipeClosure(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	var fake *fakeNicoSessionProcess
	manager := newNicoWorkerSessionManager(owner, func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		fake = newFakeNicoSessionProcess(spec)
		return fake, nil
	}, func() (string, error) { return "close-order-session", nil })
	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("close-order-run", "media"), nicoWorkerCPUOptions(), nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	if fake.closePipeErr != nil {
		t.Fatalf("stdout pipe was already closed when process Close ran: %v", fake.closePipeErr)
	}
}

func TestNicoWorkerSessionRetirementSendsEOFAndDrainsBeforeWaiting(t *testing.T) {
	var fake *gracefulNicoSessionProcess
	var processCtx context.Context
	owner, cancelOwner := context.WithCancel(context.Background())
	manager := newNicoWorkerSessionManager(owner, func(ctx context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		processCtx = ctx
		fake = newGracefulNicoSessionProcess(spec, false)
		return fake, nil
	}, func() (string, error) { return "graceful-session", nil })
	manager.retireGrace = 100 * time.Millisecond
	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("graceful-run", "media"), nicoWorkerCPUOptions(), nil); err != nil {
		t.Fatal(err)
	}
	fake.processCtx = processCtx
	manager.mu.Lock()
	session := manager.current
	manager.mu.Unlock()
	for len(session.messages) < cap(session.messages) {
		session.messages <- nicoSessionReadResult{message: nicoexportworker.SessionMessage{Version: 1, Type: "ready", SessionID: session.id}}
	}
	cancelOwner()
	select {
	case <-fake.waitCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("owner shutdown did not begin process Wait")
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	if err := session.closeAndWait(); err != nil {
		t.Fatal(err)
	}
	<-fake.done
	if got := fake.closeCalls.Load(); got != 0 {
		t.Fatalf("graceful retirement called Close %d times, want 0", got)
	}
	if got := fake.waitCalls.Load(); got != 1 {
		t.Fatalf("graceful retirement called Wait %d times, want 1", got)
	}
	if got := fake.drained.Load(); got < 1000 {
		t.Fatalf("stdout lines drained=%d, want at least 1000", got)
	}
	if !fake.waitSawLive.Load() {
		t.Fatal("process context was canceled before Wait completed")
	}
	select {
	case <-processCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("session-owned process context was not canceled after Wait")
	}
}

func TestNicoWorkerSessionRetirementUsesFifteenSecondProductionGrace(t *testing.T) {
	manager := newNicoWorkerSessionManager(context.Background(), nil, nil)
	if manager.retireGrace != 15*time.Second {
		t.Fatalf("production retirement grace=%s, want 15s", manager.retireGrace)
	}
}

func TestNicoWorkerSessionRetirementFallsBackAfterGraceAndKeepsWaitSingle(t *testing.T) {
	var fake *gracefulNicoSessionProcess
	manager := newNicoWorkerSessionManager(context.Background(), func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		fake = newGracefulNicoSessionProcess(spec, true)
		return fake, nil
	}, func() (string, error) { return "grace-deadline-session", nil })
	manager.retireGrace = 10 * time.Millisecond
	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("grace-timeout-run", "media"), nicoWorkerCPUOptions(), nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	<-fake.done
	if fake.closeCalls.Load() != 1 || fake.waitCalls.Load() != 1 {
		t.Fatalf("fallback Close/Wait=%d/%d, want 1/1", fake.closeCalls.Load(), fake.waitCalls.Load())
	}
}

func TestNicoWorkerSessionIdleRetirementResetsFromLastCompletedJob(t *testing.T) {
	clock := newFakeNicoSessionClock(time.Unix(100, 0))
	var starts atomic.Int32
	var processes []*fakeNicoSessionProcess
	manager := newTestRetirementManager(t, clock, func(spec nicoexportbudget.ProcessSpec) *fakeNicoSessionProcess {
		starts.Add(1)
		process := newFakeNicoSessionProcess(spec)
		processes = append(processes, process)
		return process
	}, func(nicoexportworker.Request, nicoexportbudget.Options) (string, error) {
		return "worker-hash-a|launch-flags-a", nil
	})

	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("idle-1", "media"), nicoWorkerCPUOptions(), nil); err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Second)
	if processes[0].closeCalls.Load() != 0 {
		t.Fatal("session retired before 60 seconds idle")
	}
	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("idle-2", "media"), nicoWorkerCPUOptions(), nil); err != nil {
		t.Fatal(err)
	}
	clock.Advance(59 * time.Second)
	if processes[0].closeCalls.Load() != 0 {
		t.Fatal("session retired 59 seconds after the last completed job")
	}
	clock.Advance(time.Second)
	if processes[0].closeCalls.Load() != 0 || processes[0].waitCalls.Load() != 1 {
		t.Fatalf("idle retirement Close/Wait=%d/%d, want 0/1", processes[0].closeCalls.Load(), processes[0].waitCalls.Load())
	}
	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("idle-3", "media"), nicoWorkerCPUOptions(), nil); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 2 {
		t.Fatalf("fresh job starts=%d, want new generation after idle retirement", starts.Load())
	}
	_ = manager.close()
}

func TestNicoWorkerSessionIdleTimeoutDoesNotRetireActiveJob(t *testing.T) {
	clock := newFakeNicoSessionClock(time.Unix(200, 0))
	var starts atomic.Int32
	var process *fakeNicoSessionProcess
	manager := newTestRetirementManager(t, clock, func(spec nicoexportbudget.ProcessSpec) *fakeNicoSessionProcess {
		starts.Add(1)
		process = newFakeNicoSessionProcess(spec)
		return process
	}, func(nicoexportworker.Request, nicoexportbudget.Options) (string, error) { return "stable", nil })
	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("active-1", "media"), nicoWorkerCPUOptions(), nil); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	process.runGate = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := manager.run(context.Background(), validNicoSessionRequest("active-2", "media"), nicoWorkerCPUOptions(), nil)
		done <- err
	}()
	select {
	case <-process.runReceivedSecond:
	case <-time.After(2 * time.Second):
		t.Fatal("second run did not become active")
	}
	clock.Advance(2 * time.Minute)
	if process.closeCalls.Load() != 0 {
		t.Fatal("idle timer retired the process while a job was active")
	}
	close(process.runGate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active job did not complete")
	}
	clock.Advance(59 * time.Second)
	if process.closeCalls.Load() != 0 {
		t.Fatal("active duration counted toward idle timeout")
	}
	clock.Advance(time.Second)
	if process.closeCalls.Load() != 0 || process.waitCalls.Load() != 1 {
		t.Fatalf("post-job idle retirement Close/Wait=%d/%d, want 0/1", process.closeCalls.Load(), process.waitCalls.Load())
	}
	if starts.Load() != 1 {
		t.Fatalf("active job unexpectedly replaced session; starts=%d", starts.Load())
	}
}

func TestNicoWorkerSessionRetiresAt64JobsAndOnIdentityChange(t *testing.T) {
	t.Run("64th completed job", func(t *testing.T) {
		clock := newFakeNicoSessionClock(time.Unix(300, 0))
		var starts atomic.Int32
		var processes []*fakeNicoSessionProcess
		manager := newTestRetirementManager(t, clock, func(spec nicoexportbudget.ProcessSpec) *fakeNicoSessionProcess {
			starts.Add(1)
			process := newFakeNicoSessionProcess(spec)
			processes = append(processes, process)
			return process
		}, func(nicoexportworker.Request, nicoexportbudget.Options) (string, error) { return "stable", nil })
		for i := 1; i <= 63; i++ {
			if _, _, err := manager.run(context.Background(), validNicoSessionRequest(fmt.Sprintf("count-%d", i), "media"), nicoWorkerCPUOptions(), nil); err != nil {
				t.Fatalf("job %d: %v", i, err)
			}
		}
		if starts.Load() != 1 || processes[0].closeCalls.Load() != 0 {
			t.Fatalf("before 64th job starts=%d close=%d", starts.Load(), processes[0].closeCalls.Load())
		}
		if _, _, err := manager.run(context.Background(), validNicoSessionRequest("count-64", "media"), nicoWorkerCPUOptions(), nil); err != nil {
			t.Fatal(err)
		}
		if starts.Load() != 1 || processes[0].closeCalls.Load() != 0 || processes[0].waitCalls.Load() != 1 {
			t.Fatalf("64th job retirement starts=%d Close/Wait=%d/%d", starts.Load(), processes[0].closeCalls.Load(), processes[0].waitCalls.Load())
		}
		if _, _, err := manager.run(context.Background(), validNicoSessionRequest("count-65", "media"), nicoWorkerCPUOptions(), nil); err != nil {
			t.Fatal(err)
		}
		if starts.Load() != 2 {
			t.Fatalf("65th job starts=%d, want fresh generation", starts.Load())
		}
		_ = manager.close()
	})

	t.Run("worker hash and launch flags change", func(t *testing.T) {
		clock := newFakeNicoSessionClock(time.Unix(400, 0))
		var starts atomic.Int32
		var processes []*fakeNicoSessionProcess
		identity := "hash-a|flags-a"
		manager := newTestRetirementManager(t, clock, func(spec nicoexportbudget.ProcessSpec) *fakeNicoSessionProcess {
			starts.Add(1)
			process := newFakeNicoSessionProcess(spec)
			processes = append(processes, process)
			return process
		}, func(nicoexportworker.Request, nicoexportbudget.Options) (string, error) { return identity, nil })
		if _, _, err := manager.run(context.Background(), validNicoSessionRequest("identity-1", "media"), nicoWorkerCPUOptions(), nil); err != nil {
			t.Fatal(err)
		}
		identity = "hash-b|flags-a"
		if _, _, err := manager.run(context.Background(), validNicoSessionRequest("identity-2", "media"), nicoWorkerCPUOptions(), nil); err != nil {
			t.Fatal(err)
		}
		identity = "hash-b|flags-b"
		if _, _, err := manager.run(context.Background(), validNicoSessionRequest("identity-3", "media"), nicoWorkerCPUOptions(), nil); err != nil {
			t.Fatal(err)
		}
		if starts.Load() != 3 {
			t.Fatalf("identity changes started %d sessions, want 3", starts.Load())
		}
		for i := 0; i < 2; i++ {
			if processes[i].closeCalls.Load() != 0 || processes[i].waitCalls.Load() != 1 {
				t.Fatalf("old generation %d Close/Wait=%d/%d, want 0/1", i+1, processes[i].closeCalls.Load(), processes[i].waitCalls.Load())
			}
		}
		_ = manager.close()
	})
}

func TestNicoWorkerSessionIdentityTracksBinaryAndLaunchConfiguration(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "worker.exe")
	if err := os.WriteFile(executable, []byte("worker-build-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldExecutable := nicoWorkerExecutable
	nicoWorkerExecutable = func() (string, error) { return executable, nil }
	t.Cleanup(func() { nicoWorkerExecutable = oldExecutable })
	t.Setenv("IMAGEPAD_NICONICO_RENDER_HEADLESS", "1")
	t.Setenv("IMAGEPAD_NICONICO_RENDER_GPU", "0")
	t.Setenv(nicoexportworker.SessionFreshBrowserPerJobEnv, "")

	request := validNicoSessionRequest("identity-run", "media")
	options := nicoWorkerCPUOptions()
	base, err := nicoWorkerSessionIdentity(request, options)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(executable, []byte("worker-build-b"), 0o600); err != nil {
		t.Fatal(err)
	}
	changedBinary, err := nicoWorkerSessionIdentity(request, options)
	if err != nil || changedBinary == base {
		t.Fatalf("binary content change identity=%q base=%q err=%v", changedBinary, base, err)
	}
	if err := os.WriteFile(executable, []byte("worker-build-a"), 0o600); err != nil {
		t.Fatal(err)
	}

	request.BrowserPath = `C:\tools\browser-b.exe`
	changedBrowser, err := nicoWorkerSessionIdentity(request, options)
	if err != nil || changedBrowser == base {
		t.Fatalf("browser path change identity=%q base=%q err=%v", changedBrowser, base, err)
	}

	request = validNicoSessionRequest("identity-run", "media")
	options.Percent++
	changedOptions, err := nicoWorkerSessionIdentity(request, options)
	if err != nil || changedOptions == base {
		t.Fatalf("CPU options change identity=%q base=%q err=%v", changedOptions, base, err)
	}
	options = nicoWorkerCPUOptions()

	t.Setenv("IMAGEPAD_NICONICO_RENDER_HEADLESS", "0")
	changedHeadless, err := nicoWorkerSessionIdentity(request, options)
	if err != nil || changedHeadless == base {
		t.Fatalf("headless launch flag change identity=%q base=%q err=%v", changedHeadless, base, err)
	}
	t.Setenv("IMAGEPAD_NICONICO_RENDER_HEADLESS", "1")

	t.Setenv("IMAGEPAD_NICONICO_RENDER_GPU", "1")
	changedGPU, err := nicoWorkerSessionIdentity(request, options)
	if err != nil || changedGPU == base {
		t.Fatalf("GPU launch flag change identity=%q base=%q err=%v", changedGPU, base, err)
	}
	t.Setenv("IMAGEPAD_NICONICO_RENDER_GPU", "0")
	t.Setenv(nicoexportworker.SessionFreshBrowserPerJobEnv, "1")
	changedFreshBrowserMode, err := nicoWorkerSessionIdentity(request, options)
	if err != nil || changedFreshBrowserMode == base {
		t.Fatalf("fresh-browser session mode change identity=%q base=%q err=%v", changedFreshBrowserMode, base, err)
	}
}

func TestNicoWorkerSessionIdentityHashCost(t *testing.T) {
	executable, err := nicoWorkerExecutable()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	request := validNicoSessionRequest("identity-cost", "media")
	options := nicoWorkerCPUOptions()
	start := time.Now()
	if _, err := nicoWorkerSessionIdentity(request, options); err != nil {
		t.Fatal(err)
	}
	firstCall := time.Since(start)
	start = time.Now()
	if _, err := nicoWorkerSessionIdentity(request, options); err != nil {
		t.Fatal(err)
	}
	warmCall := time.Since(start)
	t.Logf("identity SHA-256 over %d-byte executable: first call %s, warm call %s", info.Size(), firstCall, warmCall)
}

func TestNicoWorkerSessionCancelOrEOFUsesFreshGeneration(t *testing.T) {
	for _, mode := range []string{"cancel", "eof"} {
		t.Run(mode, func(t *testing.T) {
			clock := newFakeNicoSessionClock(time.Unix(500, 0))
			var starts atomic.Int32
			var processes []*fakeNicoSessionProcess
			created := make(chan *fakeNicoSessionProcess, 2)
			manager := newTestRetirementManager(t, clock, func(spec nicoexportbudget.ProcessSpec) *fakeNicoSessionProcess {
				generation := starts.Add(1)
				process := newFakeNicoSessionProcess(spec)
				process.holdRun = mode == "cancel" && generation == 1
				process.eofFirst = mode == "eof" && generation == 1
				processes = append(processes, process)
				created <- process
				return process
			}, func(nicoexportworker.Request, nicoexportbudget.Options) (string, error) { return "stable", nil })
			manager.retireGrace = 10 * time.Millisecond
			if mode == "cancel" {
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() {
					_, _, err := manager.run(ctx, validNicoSessionRequest("cancel-1", "media"), nicoWorkerCPUOptions(), nil)
					done <- err
				}()
				first := <-created
				select {
				case <-first.runReceived:
				case <-time.After(2 * time.Second):
					cancel()
					t.Fatal("canceled job did not start")
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel error=%v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("canceled job did not finish")
				}
			} else {
				if _, _, err := manager.run(context.Background(), validNicoSessionRequest("eof-1", "media"), nicoWorkerCPUOptions(), nil); err == nil {
					t.Fatal("worker EOF without result unexpectedly succeeded")
				}
			}
			wantClose := int32(1)
			if mode == "eof" {
				wantClose = 0
			}
			if processes[0].closeCalls.Load() != wantClose || processes[0].waitCalls.Load() != 1 {
				t.Fatalf("terminal generation Close/Wait=%d/%d, want %d/1", processes[0].closeCalls.Load(), processes[0].waitCalls.Load(), wantClose)
			}
			if result, _, err := manager.run(context.Background(), validNicoSessionRequest(mode+"-fresh", "media"), nicoWorkerCPUOptions(), nil); err != nil || !result.OK {
				t.Fatalf("fresh generation result=%+v err=%v", result, err)
			}
			if starts.Load() != 2 {
				t.Fatalf("fresh generation starts=%d, want 2", starts.Load())
			}
			_ = manager.close()
		})
	}
}

func TestNicoWorkerSessionDoesNotCommitSuccessBeforeReady(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	fake := &fakeNicoSessionProcess{waitDone: make(chan struct{}), resultSent: make(chan struct{}, 8), runReceived: make(chan struct{}), readyGate: make(chan struct{})}
	manager := newNicoWorkerSessionManager(owner, func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		fake.start(spec)
		return fake, nil
	}, func() (string, error) { return "parent-session-ready", nil })
	done := make(chan error, 1)
	go func() {
		_, _, err := manager.run(context.Background(), validNicoSessionRequest("run-ready", "media-ready"), nicoWorkerCPUOptions(), nil)
		done <- err
	}()
	select {
	case <-fake.resultSent:
	case <-time.After(2 * time.Second):
		select {
		case err := <-done:
			t.Fatalf("job ended before result: %v", err)
		default:
			t.Fatalf("fake worker did not send result: runCount=%d", fake.runCount.Load())
		}
	}
	select {
	case err := <-done:
		t.Fatalf("job completed before matching cleanup/ready: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(fake.readyGate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job did not complete after matching ready")
	}
	_ = manager.close()
}

func TestNicoWorkerSessionTerminalFailureClosesWaitsAndNextCallStartsFresh(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	var starts atomic.Int32
	var processesMu sync.Mutex
	var processes []*fakeNicoSessionProcess
	manager := newNicoWorkerSessionManager(owner, func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		generation := starts.Add(1)
		process := newFakeNicoSessionProcess(spec)
		process.failFirst = generation == 1
		processesMu.Lock()
		processes = append(processes, process)
		processesMu.Unlock()
		return process, nil
	}, func() (string, error) { return "parent-session-fresh", nil })
	if _, _, err := manager.run(context.Background(), validNicoSessionRequest("run-bad", "media-bad"), nicoWorkerCPUOptions(), nil); err == nil {
		t.Fatal("failed result unexpectedly succeeded")
	}
	if starts.Load() != 1 {
		t.Fatalf("terminal run retried automatically: starts=%d", starts.Load())
	}
	processesMu.Lock()
	first := processes[0]
	processesMu.Unlock()
	if first.runCount.Load() != 1 || first.closeCalls.Load() != 0 || first.waitCalls.Load() != 1 {
		t.Fatalf("failed generation run/close/wait=%d/%d/%d", first.runCount.Load(), first.closeCalls.Load(), first.waitCalls.Load())
	}
	if result, _, err := manager.run(context.Background(), validNicoSessionRequest("run-fresh", "media-fresh"), nicoWorkerCPUOptions(), nil); err != nil || !result.OK {
		t.Fatalf("fresh generation result=%+v error=%v", result, err)
	}
	if starts.Load() != 2 {
		t.Fatalf("fresh call starts=%d, want second process", starts.Load())
	}
	_ = manager.close()
}

func TestNicoWorkerSessionCancellationAndLifecycleCloseWaitExactlyOnce(t *testing.T) {
	t.Run("request cancellation", func(t *testing.T) {
		owner, cancelOwner := context.WithCancel(context.Background())
		defer cancelOwner()
		fake := &fakeNicoSessionProcess{waitDone: make(chan struct{}), resultSent: make(chan struct{}, 8), runReceived: make(chan struct{}), holdRun: true}
		manager := newNicoWorkerSessionManager(owner, func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
			fake.start(spec)
			return fake, nil
		}, func() (string, error) { return "parent-session-cancel", nil })
		manager.retireGrace = 10 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, _, err := manager.run(ctx, validNicoSessionRequest("run-cancel", "media-cancel"), nicoWorkerCPUOptions(), nil)
			done <- err
		}()
		select {
		case <-fake.runReceived:
		case <-time.After(2 * time.Second):
			select {
			case err := <-done:
				t.Fatalf("job ended before run: %v", err)
			default:
				t.Fatalf("run was not sent: runCount=%d", fake.runCount.Load())
			}
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v, want cancellation", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("canceled request did not retire session")
		}
		if fake.closeCalls.Load() != 1 || fake.waitCalls.Load() != 1 {
			t.Fatalf("close/wait=%d/%d, want 1/1", fake.closeCalls.Load(), fake.waitCalls.Load())
		}
	})

	t.Run("server lifecycle cancellation while idle", func(t *testing.T) {
		owner, cancelOwner := context.WithCancel(context.Background())
		var fake *fakeNicoSessionProcess
		manager := newNicoWorkerSessionManager(owner, func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
			fake = newFakeNicoSessionProcess(spec)
			return fake, nil
		}, func() (string, error) { return "parent-session-shutdown", nil })
		// Explicit launch establishes a persistent generation without a job.
		if err := manager.start(); err != nil {
			t.Fatal(err)
		}
		cancelOwner()
		select {
		case <-fake.waitCalled:
		case <-time.After(2 * time.Second):
			t.Fatal("lifecycle cancellation did not wait for process")
		}
		if fake.closeCalls.Load() != 0 || fake.waitCalls.Load() != 1 {
			t.Fatalf("close/wait=%d/%d, want 0/1", fake.closeCalls.Load(), fake.waitCalls.Load())
		}
	})
}

func TestServerUsesLifecycleContextForSessionOwner(t *testing.T) {
	server := &Server{}
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.SetLifecycleContext(owner)
	if got := server.lifecycleContext(); got != owner {
		t.Fatal("server did not retain the configured lifecycle context")
	}
}

func TestNicoWorkerRequestDispatchDefaultsToSessionsOnWindowsAndAllowsOverrides(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	server := &Server{}
	server.SetLifecycleContext(owner)
	var legacyCalls atomic.Int32
	oldRunner := niconicoWorkerRunner
	t.Cleanup(func() { niconicoWorkerRunner = oldRunner })
	niconicoWorkerRunner = func(_ context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
		legacyCalls.Add(1)
		return nicoexportworker.Event{Version: 1, Type: "result", RunID: request.RunID, MediaID: request.MediaID, OK: true}, nicoexportbudget.Report{Verified: true}, nil
	}
	var starts atomic.Int32
	var fake *fakeNicoSessionProcess
	server.nicoWorkerSessions = newNicoWorkerSessionManager(owner, func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		starts.Add(1)
		fake = newFakeNicoSessionProcess(spec)
		return fake, nil
	}, func() (string, error) { return "dispatch-session", nil })
	server.nicoWorkerSessions.identityFunc = func(nicoexportworker.Request, nicoexportbudget.Options) (string, error) {
		return "dispatch-identity", nil
	}
	t.Setenv("IMAGEPAD_NICO_WORKER_SESSION", "")
	defaultResult, _, err := server.runNicoWorkerRequest(context.Background(), validNicoSessionRequest("default-run", "media"))
	wantDefaultSession := runtime.GOOS == "windows"
	if wantDefaultSession {
		if err != nil || defaultResult.RunID != "default-run" || starts.Load() != 1 || legacyCalls.Load() != 0 {
			t.Fatalf("default session dispatch result=%+v err=%v starts=%d legacyCalls=%d", defaultResult, err, starts.Load(), legacyCalls.Load())
		}
	} else if err != nil || defaultResult.RunID != "default-run" || starts.Load() != 0 || legacyCalls.Load() != 1 {
		t.Fatalf("non-Windows default dispatch result=%+v err=%v starts=%d legacyCalls=%d", defaultResult, err, starts.Load(), legacyCalls.Load())
	}

	t.Setenv("IMAGEPAD_NICO_WORKER_SESSION", "0")
	legacyCallsBeforeOptOut := legacyCalls.Load()
	legacy, _, err := server.runNicoWorkerRequest(context.Background(), validNicoSessionRequest("legacy-run", "media"))
	if err != nil || legacy.RunID != "legacy-run" || legacyCalls.Load() != legacyCallsBeforeOptOut+1 {
		t.Fatalf("explicit legacy dispatch result=%+v err=%v calls=%d", legacy, err, legacyCalls.Load())
	}

	t.Setenv("IMAGEPAD_NICO_WORKER_SESSION", "1")
	sessionResult, _, err := server.runNicoWorkerRequest(context.Background(), validNicoSessionRequest("session-run", "media"))
	if err != nil || sessionResult.RunID != "session-run" || starts.Load() != 1 || legacyCalls.Load() != legacyCallsBeforeOptOut+1 {
		t.Fatalf("session dispatch result=%+v err=%v starts=%d legacyCalls=%d", sessionResult, err, starts.Load(), legacyCalls.Load())
	}
	fake.mu.Lock()
	args := append([]string(nil), fake.args...)
	sessionID := fake.sessionID
	fake.mu.Unlock()
	if len(args) != 3 || args[0] != "nico-export-session" || args[1] != "--session-id" || args[2] != "dispatch-session" || sessionID != "dispatch-session" {
		t.Fatalf("session launch args=%q handshake ID=%q", args, sessionID)
	}
	if err := server.nicoWorkerSessions.close(); err != nil {
		t.Fatal(err)
	}
}

func newTestRetirementManager(
	t *testing.T,
	clock *fakeNicoSessionClock,
	makeProcess func(nicoexportbudget.ProcessSpec) *fakeNicoSessionProcess,
	identity func(nicoexportworker.Request, nicoexportbudget.Options) (string, error),
) *nicoWorkerSessionManager {
	t.Helper()
	var sessionIDs atomic.Int32
	manager := newNicoWorkerSessionManager(context.Background(), func(_ context.Context, spec nicoexportbudget.ProcessSpec, _ nicoexportbudget.Options) (nicoSessionProcess, error) {
		return makeProcess(spec), nil
	}, func() (string, error) { return fmt.Sprintf("fake-session-%d", sessionIDs.Add(1)), nil })
	manager.nowFn = clock.Now
	manager.afterFunc = clock.AfterFunc
	manager.idleTimeout = time.Minute
	manager.maxJobs = 64
	manager.identityFunc = identity
	return manager
}

type fakeNicoSessionClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeNicoSessionTimer
}

func newFakeNicoSessionClock(now time.Time) *fakeNicoSessionClock {
	return &fakeNicoSessionClock{now: now}
}

func (c *fakeNicoSessionClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeNicoSessionClock) AfterFunc(delay time.Duration, callback func()) nicoSessionRetirementTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeNicoSessionTimer{clock: c, at: c.now.Add(delay), callback: callback}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *fakeNicoSessionClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	var due []*fakeNicoSessionTimer
	for _, timer := range c.timers {
		if !timer.stopped && !timer.fired && !timer.at.After(c.now) {
			timer.fired = true
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		timer.callback()
	}
}

type fakeNicoSessionTimer struct {
	clock    *fakeNicoSessionClock
	at       time.Time
	callback func()
	stopped  bool
	fired    bool
}

func (t *fakeNicoSessionTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := !t.stopped && !t.fired
	t.stopped = true
	return wasActive
}

type fakeNicoSessionProcess struct {
	closeCalls        atomic.Int32
	waitCalls         atomic.Int32
	runCount          atomic.Int32
	waitDone          chan struct{}
	resultSent        chan struct{}
	runReceived       chan struct{}
	runReceivedSecond chan struct{}
	waitCalled        chan struct{}
	readyGate         chan struct{}
	holdRun           bool
	failFirst         bool
	exitOnce          sync.Once
	runReceivedOnce   sync.Once
	waitCalledOnce    sync.Once
	mu                sync.Mutex
	args              []string
	sessionID         string
	stdout            io.Writer
	closePipeErr      error
	runGate           chan struct{}
	eofFirst          bool
}

type gracefulNicoSessionProcess struct {
	closeCalls  atomic.Int32
	waitCalls   atomic.Int32
	drained     atomic.Int32
	done        chan struct{}
	waitEOF     bool
	processCtx  context.Context
	waitSawLive atomic.Bool
	once        sync.Once
	waitCalled  chan struct{}
	waitOnce    sync.Once
}

func newGracefulNicoSessionProcess(spec nicoexportbudget.ProcessSpec, waitEOF bool) *gracefulNicoSessionProcess {
	f := &gracefulNicoSessionProcess{done: make(chan struct{}), waitEOF: waitEOF, waitCalled: make(chan struct{})}
	go func() {
		defer f.once.Do(func() { close(f.done) })
		id := extractSessionID(spec.Args)
		writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "hello", SessionID: id})
		writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "ready", SessionID: id})
		reader := bufio.NewReader(spec.Stdin)
		line, _ := reader.ReadString('\n')
		var message nicoexportworker.SessionMessage
		_ = json.Unmarshal([]byte(line), &message)
		if message.Request != nil {
			event := nicoexportworker.Event{Version: 1, Type: "result", RunID: message.RunID, MediaID: message.MediaID, OK: true, Output: message.Request.OutputPath, Playlist: message.Request.HLSStagingDir + `\playlist.m3u8`}
			writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "result", SessionID: id, RunID: message.RunID, MediaID: message.MediaID, Event: &event})
			writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "cleanup", SessionID: id, RunID: message.RunID, MediaID: message.MediaID})
			writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "ready", SessionID: id})
		}
		if f.waitEOF {
			<-f.done
			return
		}
		_, _ = io.Copy(io.Discard, reader)
		for i := 0; i < 1000; i++ {
			// Emit syntactically valid control records while retirement drains stdout.
			writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "ready", SessionID: id})
			f.drained.Add(1)
		}
	}()
	return f
}

func (f *gracefulNicoSessionProcess) Close() error {
	f.closeCalls.Add(1)
	f.once.Do(func() { close(f.done) })
	return nil
}
func (f *gracefulNicoSessionProcess) Wait() (nicoexportbudget.Report, error) {
	f.waitCalls.Add(1)
	f.waitOnce.Do(func() { close(f.waitCalled) })
	if f.processCtx != nil && f.processCtx.Err() == nil {
		f.waitSawLive.Store(true)
	}
	<-f.done
	return nicoexportbudget.Report{}, nil
}
func (f *gracefulNicoSessionProcess) Snapshot() (nicoexportbudget.Report, error) {
	return nicoexportbudget.Report{Verified: true, JobCPURate: 10000}, nil
}

func newFakeNicoSessionProcess(spec nicoexportbudget.ProcessSpec) *fakeNicoSessionProcess {
	f := &fakeNicoSessionProcess{waitDone: make(chan struct{}), resultSent: make(chan struct{}, 8), runReceived: make(chan struct{}), waitCalled: make(chan struct{})}
	f.start(spec)
	return f
}

func (f *fakeNicoSessionProcess) start(spec nicoexportbudget.ProcessSpec) {
	if f.waitDone == nil {
		f.waitDone = make(chan struct{})
	}
	if f.resultSent == nil {
		f.resultSent = make(chan struct{})
	}
	if f.runReceived == nil {
		f.runReceived = make(chan struct{})
	}
	if f.runReceivedSecond == nil {
		f.runReceivedSecond = make(chan struct{})
	}
	if f.waitCalled == nil {
		f.waitCalled = make(chan struct{})
	}
	go func() {
		defer f.exitOnce.Do(func() { close(f.waitDone) })
		if spec.Stdin == nil || spec.Stdout == nil {
			return
		}
		if closer, ok := spec.Stdout.(io.Closer); ok {
			defer closer.Close()
		}
		// The generated token is in the launch flags; tests assert it through the request handshake.
		f.mu.Lock()
		f.sessionID = extractSessionID(spec.Args)
		f.args = append([]string(nil), spec.Args...)
		f.stdout = spec.Stdout
		f.mu.Unlock()
		if f.sessionID == "" {
			f.sessionID = "parent-session-1"
		}
		writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "hello", SessionID: f.sessionID})
		writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "ready", SessionID: f.sessionID})
		reader := bufio.NewReader(spec.Stdin)
		decoder := nicoexportworker.NewSessionDecoder(f.sessionID)
		for {
			fragment, err := reader.ReadString('\n')
			if len(fragment) != 0 {
				_ = decoder.Feed([]byte(fragment))
				message, ok, decodeErr := decoder.Next()
				if decodeErr != nil || !ok {
					return
				}
				request := message.Request
				if request == nil {
					return
				}
				generation := f.runCount.Add(1)
				if generation == 1 {
					f.runReceivedOnce.Do(func() { close(f.runReceived) })
				} else if generation == 2 {
					close(f.runReceivedSecond)
				}
				if f.holdRun {
					<-f.waitDone
					return
				}
				if f.runGate != nil {
					select {
					case <-f.runGate:
					case <-f.waitDone:
						return
					}
				}
				if f.eofFirst && generation == 1 {
					if closer, ok := spec.Stdout.(io.Closer); ok {
						_ = closer.Close()
					}
					return
				}
				event := nicoexportworker.Event{Version: 1, Type: "result", RunID: message.RunID, MediaID: message.MediaID, OK: !(f.failFirst && generation == 1)}
				if !event.OK {
					event.Error = "fake session job failed"
				}
				if event.OK {
					event.Output = request.OutputPath
					event.Playlist = request.HLSStagingDir + `\playlist.m3u8`
				}
				writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "result", SessionID: f.sessionID, RunID: message.RunID, MediaID: message.MediaID, Event: &event})
				select {
				case f.resultSent <- struct{}{}:
				default:
				}
				if !event.OK {
					return
				}
				writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "cleanup", SessionID: f.sessionID, RunID: message.RunID, MediaID: message.MediaID})
				if f.readyGate != nil {
					<-f.readyGate
				}
				writeFakeSessionMessage(spec.Stdout, nicoexportworker.SessionMessage{Version: 1, Type: "ready", SessionID: f.sessionID})
			}
			if err != nil {
				return
			}
		}
	}()
}

func (f *fakeNicoSessionProcess) Close() error {
	f.closeCalls.Add(1)
	f.mu.Lock()
	if f.stdout != nil {
		_, f.closePipeErr = f.stdout.Write([]byte("\n"))
	}
	f.mu.Unlock()
	f.exitOnce.Do(func() { close(f.waitDone) })
	return nil
}
func (f *fakeNicoSessionProcess) Wait() (nicoexportbudget.Report, error) {
	f.waitCalls.Add(1)
	f.waitCalledOnce.Do(func() { close(f.waitCalled) })
	<-f.waitDone
	return nicoexportbudget.Report{Verified: true, JobCPURate: 10000, PIDs: []uint32{101, 202}}, nil
}
func (f *fakeNicoSessionProcess) Snapshot() (nicoexportbudget.Report, error) {
	return nicoexportbudget.Report{Verified: true, JobCPURate: 10000, PIDs: []uint32{101, 202}}, nil
}

func validNicoSessionRequest(runID, mediaID string) nicoexportworker.Request {
	mediaDir := filepath.Join(os.TempDir(), "imagepad-nico-session-test")
	return nicoexportworker.Request{
		Version: 1, RunID: runID, MediaID: mediaID,
		SourcePath: filepath.Join(mediaDir, "source.mp4"), SnapshotPath: filepath.Join(mediaDir, "snapshot.json"),
		OutputPath: filepath.Join(mediaDir, "out.mp4"), HLSStagingDir: filepath.Join(mediaDir, "hls"),
		FFmpeg: filepath.Join(os.TempDir(), "tools", "ffmpeg.exe"),
		Width:  1280, Height: 720, DurationMs: 1000, FPSNum: 30, FPSDen: 1, CRF: 28, AudioBitrate: "160k",
	}
}

func TestServerSessionManagerInitializationDoesNotReenterServerLock(t *testing.T) {
	server := &Server{}
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	server.SetLifecycleContext(owner)

	finished := make(chan error, 1)
	go func() {
		_, _, err := server.runNicoWorkerSession(context.Background(), nicoexportworker.Request{}, nicoexportbudget.Options{})
		finished <- err
	}()

	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("invalid request unexpectedly succeeded")
		}
		if server.nicoWorkerSessions == nil {
			t.Fatal("session manager was not initialized")
		}
	case <-time.After(time.Second):
		t.Fatal("lazy session-manager initialization deadlocked while reading lifecycle context")
	}
}

func extractSessionID(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--session-id" {
			return args[i+1]
		}
	}
	return ""
}

func writeFakeSessionMessage(w io.Writer, m nicoexportworker.SessionMessage) {
	b, _ := json.Marshal(m)
	_, _ = w.Write(append(b, '\n'))
}

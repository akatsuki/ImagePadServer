package obsrtmp

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMediaMTXStartCanceledBeforeLaunchDoesNotCreateProcess(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	calls := 0
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { calls++; return proc, nil }
	t.Cleanup(func() { proc.finish(nil); _ = rt.stop(time.Second) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := rt.start(ctx)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled startup launched: calls=%d err=%v", calls, err)
	}
}

func TestMediaMTXStartHealthCannotOverrideTerminalState(t *testing.T) {
	for _, mode := range []string{"cancel", "exit"} {
		t.Run(mode, func(t *testing.T) {
			rt := testRuntime(defaultTestConfig())
			proc := newFakeProcess()
			proc.exitOnStop = true
			rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
			t.Cleanup(func() { proc.finish(nil); _ = rt.stop(time.Second) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rt.checkHealth = func(context.Context, string) error {
				if mode == "cancel" {
					cancel()
				} else {
					proc.finish(errors.New("backend crashed"))
				}
				return nil
			}
			if err := rt.start(ctx); err == nil {
				t.Fatal("terminal backend became ready")
			}
		})
	}
}

func TestMediaMTXStartRetainsUnconfirmedCleanupError(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	proc.exitOnKill = false
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	rt.checkHealth = func(context.Context, string) error { return errors.New("not healthy") }
	t.Cleanup(func() { proc.finish(nil); _ = rt.stop(time.Second) })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := rt.start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errMediaMTXProcessExitUnconfirmed) {
		t.Fatalf("startup discarded termination status: %v", err)
	}
	rt.mu.Lock()
	retained := rt.retiring && rt.proc == proc && rt.dir != "" && rt.configPath != ""
	rt.mu.Unlock()
	if !retained {
		t.Fatal("unconfirmed backend ownership was lost")
	}
}

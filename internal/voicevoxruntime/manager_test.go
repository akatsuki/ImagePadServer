package voicevoxruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeProcess struct {
	stop sync.Once
	done chan struct{}
}

func (p *fakeProcess) Close() error { p.stop.Do(func() { close(p.done) }); return nil }
func (p *fakeProcess) Wait() error  { <-p.done; return nil }

func TestManagerSingleStartupAndLifecycleShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(t.TempDir(), nil)
	var mu sync.Mutex
	installs := 0
	launches := 0
	p := &fakeProcess{done: make(chan struct{})}
	m.installFn = func(ctx context.Context, root string, a Asset, progress Progress) (string, error) {
		mu.Lock()
		installs++
		mu.Unlock()
		return root, nil
	}
	m.launchFn = func(context.Context, string, string) (ownedProcess, error) {
		mu.Lock()
		launches++
		mu.Unlock()
		return p, nil
	}
	m.probeFn = func(context.Context, string) error { return nil }
	for i := 0; i < 10; i++ {
		m.Start(ctx)
	}
	readyCtx, cancelReady := context.WithTimeout(ctx, time.Second)
	defer cancelReady()
	if err := m.Ensure(readyCtx, ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if installs != 1 || launches != 1 {
		t.Fatalf("installs=%d launches=%d", installs, launches)
	}
	mu.Unlock()
	cancel()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	default:
		t.Fatal("owned process not closed")
	}
	if m.Status().Phase != "stopped" {
		t.Fatalf("status: %+v", m.Status())
	}
}

func TestManagerFailureCanRetryAndDoesNotClaimReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(t.TempDir(), nil)
	var attempts atomic.Int32
	m.installFn = func(context.Context, string, Asset, Progress) (string, error) {
		attempts.Add(1)
		return "", errors.New("fixture install failure")
	}
	waitCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := m.Ensure(waitCtx, ctx); err == nil {
		t.Fatal("failed installation succeeded")
	}
	if m.Status().Phase != "failed" {
		t.Fatalf("status: %+v", m.Status())
	}
	m.Retry(ctx)
	for i := 0; i < 100 && m.Status().Phase != "failed"; i++ {
		time.Sleep(time.Millisecond)
	}
	if attempts.Load() != 2 {
		t.Fatalf("retry attempts: %d", attempts.Load())
	}
}

func TestCancelledManagerRequiresExplicitRetry(t *testing.T) {
	m := New(t.TempDir(), nil)
	var calls atomic.Int32
	m.installFn = func(ctx context.Context, _ string, _ Asset, _ Progress) (string, error) {
		calls.Add(1)
		<-ctx.Done()
		return "", ctx.Err()
	}
	m.Start(context.Background())
	for i := 0; i < 100 && calls.Load() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m.Start(context.Background())
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 1 || m.Status().Phase != "stopped" {
		t.Fatal("cancelled preparation restarted")
	}
	m.Retry(context.Background())
	for i := 0; i < 100 && calls.Load() < 2; i++ {
		time.Sleep(time.Millisecond)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("explicit retry ignored")
	}
}

func TestProbeRejectsWrongVersionAndBusyPortIsNeverAdopted(t *testing.T) {
	var version atomic.Value
	version.Store("wrong")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			fmt.Fprintf(w, "%q", version.Load().(string))
			return
		}
		fmt.Fprint(w, `[{"speaker_uuid":"a","name":"speaker","styles":[{"id":0,"name":"normal"}]}]`)
	}))
	defer server.Close()
	if err := probe(context.Background(), server.URL); err == nil {
		t.Fatal("wrong engine version accepted")
	}
	version.Store(Version)
	if err := probe(context.Background(), server.URL); err != nil {
		t.Fatal(err)
	}
	if err := availableEndpoint(server.URL); err == nil {
		t.Fatal("busy port adopted")
	}
	resp, err := http.Get(server.URL + "/version")
	if err != nil {
		t.Fatal("unrelated engine stopped")
	}
	resp.Body.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	listener.Close()
	if err := availableEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
}

func TestUnexpectedEngineExitCanRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := New(t.TempDir(), nil)
	defer m.Close()
	var launched atomic.Int32
	var processMu sync.Mutex
	var process *fakeProcess
	m.installFn = func(_ context.Context, root string, _ Asset, _ Progress) (string, error) { return root, nil }
	m.launchFn = func(context.Context, string, string) (ownedProcess, error) {
		processMu.Lock()
		defer processMu.Unlock()
		process = &fakeProcess{done: make(chan struct{})}
		launched.Add(1)
		return process, nil
	}
	m.probeFn = func(context.Context, string) error { return nil }
	if err := m.Ensure(ctx, ctx); err != nil {
		t.Fatal(err)
	}
	processMu.Lock()
	p := process
	processMu.Unlock()
	p.Close()
	for m.Status().Phase != "failed" && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	if m.Status().Phase != "failed" {
		t.Fatal("unexpected exit did not fail")
	}
	m.Retry(ctx)
	if err := m.Ensure(ctx, ctx); err != nil {
		t.Fatal(err)
	}
	if launched.Load() != 2 {
		t.Fatal("exit retry failed")
	}
}

func TestConcurrentCancelAndExitDoNotLetRetryEscape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := New(t.TempDir(), nil)
	defer m.Close()
	var calls atomic.Int32
	process := &fakeProcess{done: make(chan struct{})}
	m.installFn = func(_ context.Context, root string, _ Asset, _ Progress) (string, error) { return root, nil }
	m.launchFn = func(context.Context, string, string) (ownedProcess, error) {
		if calls.Add(1) == 1 {
			return process, nil
		}
		return &fakeProcess{done: make(chan struct{})}, nil
	}
	m.probeFn = func(context.Context, string) error { return nil }
	if err := m.Ensure(ctx, ctx); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	m.mu.Lock()
	original := m.cancel
	m.cancel = func() { enteredOnce.Do(func() { close(entered) }); <-release; original() }
	m.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	<-entered
	process.Close()
	time.Sleep(20 * time.Millisecond)
	retried := make(chan struct{})
	go func() { m.Retry(ctx); close(retried) }()
	select {
	case <-retried:
	case <-ctx.Done():
		t.Fatal("retry did not reject pending cancellation promptly")
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || m.Status().Phase != "stopped" {
		t.Fatal("retry escaped the pending cancellation")
	}
	m.Retry(ctx)
	if err := m.Ensure(ctx, ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("later explicit retry should be allowed")
	}
}

package nicoexportworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/nicorender"
)

func TestRunSessionSerialJobsAndCommitsAfterCleanup(t *testing.T) {
	first := sessionRequest("run-1", "media-1", "out-1.mp4", "hls-1")
	second := sessionRequest("run-2", "media-2", "out-2.mp4", "hls-2")
	first.BrowserPath, second.BrowserPath = "browser.exe", "browser.exe"
	reader, input := io.Pipe()
	defer input.Close()
	output := &signalingSessionWriter{messages: make(chan string, 32)}
	pool := &fakeSessionPool{}
	var poolCalls atomic.Int32
	var poolBrowserPath string
	var runs atomic.Int32
	expectByRun := map[string]Request{first.RunID: first, second.RunID: second}
	firstStarted := make(chan struct{}, 1)
	secondStarted := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	deps := sessionDependencies{
		newPool: func(_ context.Context, request Request) (sessionBrowserPool, error) {
			poolCalls.Add(1)
			poolBrowserPath = request.BrowserPath
			return pool, nil
		},
		runJob: func(ctx context.Context, request Request, _ sessionBrowserPool, emit func(Event) error) error {
			if !reflect.DeepEqual(request, expectByRun[request.RunID]) {
				t.Errorf("runner received request %#v, want %#v", request, expectByRun[request.RunID])
			}
			switch runs.Add(1) {
			case 1:
				firstStarted <- struct{}{}
				select {
				case <-releaseFirst:
				case <-ctx.Done():
					return ctx.Err()
				}
			case 2:
				secondStarted <- struct{}{}
			default:
				t.Errorf("runner invoked more than twice")
			}
			if err := emit(Event{Version: ProtocolVersion, Type: "progress", RunID: request.RunID, MediaID: request.MediaID, Stage: "render"}); err != nil {
				return err
			}
			return emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, OK: true})
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), "sess-parent-1", reader, output, io.Discard, deps)
	}()
	if _, err := io.WriteString(input, sessionRunLines(t, "sess-parent-1", first)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not start")
	}
	select {
	case <-secondStarted:
		t.Fatal("second run started while first was active")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	waitForReadyCount(t, output, 2)
	if _, err := io.WriteString(input, sessionRunLines(t, "sess-parent-1", second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("second run did not start after first commit")
	}
	waitForReadyCount(t, output, 3)
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	err := <-done
	if err != nil {
		t.Fatalf("idle EOF after successful serial jobs should retire successfully: %v", err)
	}
	if runs.Load() != 2 || poolCalls.Load() != 1 || pool.closeCalls.Load() != 1 {
		t.Fatalf("runs=%d pool calls=%d close calls=%d", runs.Load(), poolCalls.Load(), pool.closeCalls.Load())
	}
	if poolBrowserPath != first.BrowserPath {
		t.Fatalf("pool BrowserPath = %q, want first request %q", poolBrowserPath, first.BrowserPath)
	}
	messages := decodeSessionOutput(t, output.Bytes())
	want := []string{"hello", "ready", "progress", "result", "cleanup", "ready", "progress", "result", "cleanup", "ready"}
	if got := sessionTypes(messages); !equalStrings(got, want) {
		t.Fatalf("message order = %v, want %v", got, want)
	}
	if messages[0].SessionID != "sess-parent-1" {
		t.Fatalf("handshake session id = %q", messages[0].SessionID)
	}
	if messages[3].RunID != first.RunID || messages[3].MediaID != first.MediaID || messages[3].Event == nil || !messages[3].Event.OK {
		t.Fatalf("first result identity/payload = %#v", messages[3])
	}
	if messages[7].RunID != second.RunID || messages[7].MediaID != second.MediaID || messages[7].Event == nil || !messages[7].Event.OK {
		t.Fatalf("second result identity/payload = %#v", messages[7])
	}
	if messages[4].RunID != first.RunID || messages[5].Type != "ready" || messages[8].RunID != second.RunID || messages[9].Type != "ready" {
		t.Fatalf("cleanup/ready boundaries = %#v", messages)
	}
	if first.OutputPath == second.OutputPath || first.HLSStagingDir == second.HLSStagingDir {
		t.Fatal("fixture must use independent output staging")
	}
}

func TestRunSessionFreshBrowserPerJobUsesNoSessionBrowserPool(t *testing.T) {
	first := sessionRequest("fresh-browser-1", "media-1", "out-1.mp4", "hls-1")
	second := sessionRequest("fresh-browser-2", "media-2", "out-2.mp4", "hls-2")
	first.BrowserPath, second.BrowserPath = "browser.exe", "browser.exe"
	reader, input := io.Pipe()
	defer input.Close()
	output := &signalingSessionWriter{messages: make(chan string, 32)}
	var poolCalls atomic.Int32
	var runs atomic.Int32
	deps := sessionDependencies{
		freshBrowserPerJob: true,
		newPool: func(context.Context, Request) (sessionBrowserPool, error) {
			poolCalls.Add(1)
			return nil, errors.New("fresh-browser session must not create a BrowserPool")
		},
		runJob: func(_ context.Context, request Request, pool sessionBrowserPool, emit func(Event) error) error {
			if pool != nil {
				t.Errorf("run %s received session browser pool %T, want nil for a fresh browser", request.RunID, pool)
			}
			runs.Add(1)
			if err := emit(Event{Version: ProtocolVersion, Type: "progress", RunID: request.RunID, MediaID: request.MediaID, Stage: "render"}); err != nil {
				return err
			}
			return emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, OK: true})
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), "sess-fresh-browser", reader, output, io.Discard, deps)
	}()
	if _, err := io.WriteString(input, sessionRunLines(t, "sess-fresh-browser", first)); err != nil {
		t.Fatal(err)
	}
	waitForReadyCount(t, output, 2)
	if _, err := io.WriteString(input, sessionRunLines(t, "sess-fresh-browser", second)); err != nil {
		t.Fatal(err)
	}
	waitForReadyCount(t, output, 3)
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("fresh-browser session did not retire cleanly: %v", err)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("fresh-browser session ran %d jobs, want 2", got)
	}
	if got := poolCalls.Load(); got != 0 {
		t.Fatalf("fresh-browser session requested a BrowserPool %d times, want 0", got)
	}
	if got := sessionTypes(decodeSessionOutput(t, output.Bytes())); !equalStrings(got, []string{"hello", "ready", "progress", "result", "cleanup", "ready", "progress", "result", "cleanup", "ready"}) {
		t.Fatalf("fresh-browser session protocol = %v", got)
	}
}

func TestRunSessionFailureIsTerminalNoDoubleSendAndFreshSessionWorks(t *testing.T) {
	failed := sessionRequest("run-fail", "media-fail", "failed.mp4", "failed-hls")
	queued := sessionRequest("run-queued", "media-queued", "queued.mp4", "queued-hls")
	reader, input := io.Pipe()
	defer input.Close()
	var output bytes.Buffer
	pool := &fakeSessionPool{}
	var runs atomic.Int32
	deps := sessionDependencies{
		newPool: func(context.Context, Request) (sessionBrowserPool, error) { return pool, nil },
		runJob: func(_ context.Context, request Request, _ sessionBrowserPool, emit func(Event) error) error {
			runs.Add(1)
			if err := emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, Error: "worker failed"}); err != nil {
				return err
			}
			return errors.New("worker failed")
		},
	}
	done := make(chan error, 1)
	go func() { done <- runSession(context.Background(), "sess-failed", reader, &output, io.Discard, deps) }()
	if _, err := io.WriteString(input, sessionRunLines(t, "sess-failed", failed)); err != nil {
		t.Fatal(err)
	}
	err := <-done
	if err == nil || runs.Load() != 1 || pool.closeCalls.Load() != 1 {
		t.Fatalf("terminal failure: err=%v runs=%d close calls=%d", err, runs.Load(), pool.closeCalls.Load())
	}
	messages := decodeSessionOutput(t, output.Bytes())
	want := []string{"hello", "ready", "result"}
	if got := sessionTypes(messages); !equalStrings(got, want) {
		t.Fatalf("failed session messages = %v, want %v", got, want)
	}
	if messages[2].Event == nil || messages[2].Event.OK || messages[2].RunID != failed.RunID {
		t.Fatalf("failed result = %#v", messages[2])
	}

	freshReader, freshInput := io.Pipe()
	defer freshInput.Close()
	freshOutput := &signalingSessionWriter{messages: make(chan string, 16)}
	freshPool := &fakeSessionPool{}
	freshRuns := 0
	freshDeps := sessionDependencies{
		newPool: func(context.Context, Request) (sessionBrowserPool, error) { return freshPool, nil },
		runJob: func(_ context.Context, request Request, _ sessionBrowserPool, emit func(Event) error) error {
			freshRuns++
			return emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, OK: true})
		},
	}
	freshDone := make(chan error, 1)
	go func() {
		freshDone <- runSession(context.Background(), "sess-fresh", freshReader, freshOutput, io.Discard, freshDeps)
	}()
	if _, err := io.WriteString(freshInput, sessionRunLines(t, "sess-fresh", queued)); err != nil {
		t.Fatal(err)
	}
	waitForReadyCount(t, freshOutput, 2)
	if err := freshInput.Close(); err != nil {
		t.Fatal(err)
	}
	freshErr := <-freshDone
	if freshErr != nil || freshRuns != 1 {
		t.Fatalf("fresh session: err=%v runs=%d", freshErr, freshRuns)
	}
	freshMessages := decodeSessionOutput(t, freshOutput.Bytes())
	if got := sessionTypes(freshMessages); !equalStrings(got, []string{"hello", "ready", "result", "cleanup", "ready"}) {
		t.Fatalf("fresh session messages = %v", got)
	}
}

func TestRunSessionMalformedIdentityEOFAndPoolFailuresAreTerminal(t *testing.T) {
	valid := sessionRequest("run-1", "media-1", "out.mp4", "hls")
	validLine := sessionRunLines(t, "sess-valid", valid)
	wrongSession := sessionRunLines(t, "sess-other", valid)
	wrongPayload := valid
	wrongPayload.RunID = "payload-other"
	wrongRunIdentity := sessionMessageLine(t, SessionMessage{Version: SessionProtocolVersion, Type: "run", SessionID: "sess-valid", RunID: valid.RunID, MediaID: valid.MediaID, Request: &wrongPayload})
	wrongMediaPayload := valid
	wrongMediaPayload.MediaID = "payload-media-other"
	wrongMediaIdentity := sessionMessageLine(t, SessionMessage{Version: SessionProtocolVersion, Type: "run", SessionID: "sess-valid", RunID: valid.RunID, MediaID: valid.MediaID, Request: &wrongMediaPayload})
	malformed := "{\"version\":1,\"type\":\"unknown\",\"session_id\":\"sess-valid\"}\n"
	for _, tc := range []struct {
		name, input string
		wantErr     bool
	}{
		{name: "malformed", input: malformed, wantErr: true},
		{name: "wrong session", input: wrongSession, wantErr: true},
		{name: "payload identity", input: wrongRunIdentity, wantErr: true},
		{name: "payload media identity", input: wrongMediaIdentity, wantErr: true},
		{name: "idle eof", input: ""},
		{name: "incomplete eof", input: validLine[:len(validLine)-1], wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			var factoryCalls, runCalls atomic.Int32
			deps := sessionDependencies{
				newPool: func(context.Context, Request) (sessionBrowserPool, error) {
					factoryCalls.Add(1)
					return &fakeSessionPool{}, nil
				},
				runJob: func(context.Context, Request, sessionBrowserPool, func(Event) error) error {
					runCalls.Add(1)
					return nil
				},
			}
			err := runSession(context.Background(), "sess-valid", strings.NewReader(tc.input), &output, io.Discard, deps)
			if (err != nil) != tc.wantErr || factoryCalls.Load() != 0 || runCalls.Load() != 0 {
				t.Fatalf("err=%v pool calls=%d run calls=%d", err, factoryCalls.Load(), runCalls.Load())
			}
			if got := sessionTypes(decodeSessionOutput(t, output.Bytes())); !equalStrings(got, []string{"hello", "ready"}) {
				t.Fatalf("messages = %v", got)
			}
		})
	}
	t.Run("pool creation", func(t *testing.T) {
		var output bytes.Buffer
		factoryErr := errors.New("pool startup failed")
		deps := sessionDependencies{
			newPool: func(context.Context, Request) (sessionBrowserPool, error) { return nil, factoryErr },
			runJob: func(context.Context, Request, sessionBrowserPool, func(Event) error) error {
				t.Fatal("runner called after pool creation failure")
				return nil
			},
		}
		err := runSession(context.Background(), "sess-valid", strings.NewReader(validLine), &output, io.Discard, deps)
		if !errors.Is(err, factoryErr) {
			t.Fatalf("error = %v, want pool startup error", err)
		}
		if got := sessionTypes(decodeSessionOutput(t, output.Bytes())); !equalStrings(got, []string{"hello", "ready"}) {
			t.Fatalf("messages = %v", got)
		}
	})
	t.Run("invalid request fails before browser startup", func(t *testing.T) {
		invalid := valid
		invalid.Width = 5000
		var output bytes.Buffer
		var factoryCalls atomic.Int32
		deps := sessionDependencies{
			newPool: func(context.Context, Request) (sessionBrowserPool, error) {
				factoryCalls.Add(1)
				return &fakeSessionPool{}, nil
			},
			runJob: func(_ context.Context, request Request, pool sessionBrowserPool, emit func(Event) error) error {
				if pool != nil {
					t.Errorf("invalid request received browser pool %T", pool)
				}
				err := request.Validate()
				if err == nil {
					t.Fatal("fixture request unexpectedly validates")
				}
				writeErr := emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, Error: err.Error()})
				return errors.Join(err, writeErr)
			},
		}
		err := runSession(context.Background(), "sess-valid", strings.NewReader(sessionRunLines(t, "sess-valid", invalid)), &output, io.Discard, deps)
		if err == nil || factoryCalls.Load() != 0 {
			t.Fatalf("err=%v browser factory calls=%d", err, factoryCalls.Load())
		}
		messages := decodeSessionOutput(t, output.Bytes())
		if got := sessionTypes(messages); !equalStrings(got, []string{"hello", "ready", "result"}) || messages[2].Event == nil || messages[2].Event.OK {
			t.Fatalf("invalid request messages = %#v", messages)
		}
	})
	t.Run("changed BrowserPath requires fresh session", func(t *testing.T) {
		firstRequest := sessionRequest("run-1", "media-1", "one.mp4", "one-hls")
		firstRequest.BrowserPath = "browser-a.exe"
		changed := sessionRequest("run-2", "media-2", "two.mp4", "two-hls")
		changed.BrowserPath = "browser-b.exe"
		pool := &fakeSessionPool{}
		var factoryCalls, runCalls atomic.Int32
		deps := sessionDependencies{
			newPool: func(_ context.Context, request Request) (sessionBrowserPool, error) {
				factoryCalls.Add(1)
				if request.BrowserPath != firstRequest.BrowserPath {
					t.Errorf("pool constructed with BrowserPath %q", request.BrowserPath)
				}
				return pool, nil
			},
			runJob: func(_ context.Context, request Request, _ sessionBrowserPool, emit func(Event) error) error {
				runCalls.Add(1)
				return emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, OK: true})
			},
		}
		reader, input := io.Pipe()
		defer input.Close()
		writer := &signalingSessionWriter{messages: make(chan string, 16)}
		done := make(chan error, 1)
		go func() { done <- runSession(context.Background(), "sess-valid", reader, writer, io.Discard, deps) }()
		if _, err := io.WriteString(input, sessionRunLines(t, "sess-valid", firstRequest)); err != nil {
			t.Fatal(err)
		}
		waitForReadyCount(t, writer, 2)
		if _, err := io.WriteString(input, sessionRunLines(t, "sess-valid", changed)); err != nil {
			t.Fatal(err)
		}
		err := <-done
		if err == nil || factoryCalls.Load() != 1 || runCalls.Load() != 1 || pool.closeCalls.Load() != 1 {
			t.Fatalf("err=%v factory=%d runs=%d closes=%d", err, factoryCalls.Load(), runCalls.Load(), pool.closeCalls.Load())
		}
		if got := sessionTypes(decodeSessionOutput(t, writer.Bytes())); !equalStrings(got, []string{"hello", "ready", "result", "cleanup", "ready"}) {
			t.Fatalf("messages = %v", got)
		}
	})
}

func TestRunSessionCancellationAndCleanupFailureAreTerminal(t *testing.T) {
	request := sessionRequest("run-1", "media-1", "out.mp4", "hls")
	t.Run("cancel active run", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reader, input := io.Pipe()
		defer input.Close()
		pool := &fakeSessionPool{}
		started := make(chan struct{})
		var output bytes.Buffer
		deps := sessionDependencies{
			newPool: func(context.Context, Request) (sessionBrowserPool, error) { return pool, nil },
			runJob: func(ctx context.Context, _ Request, _ sessionBrowserPool, _ func(Event) error) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			},
		}
		done := make(chan error, 1)
		go func() {
			done <- runSession(ctx, "sess-cancel", reader, &output, io.Discard, deps)
		}()
		if _, err := io.WriteString(input, sessionRunLines(t, "sess-cancel", request)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("run did not start")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want cancellation", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("session did not stop after cancellation")
		}
		if pool.closeCalls.Load() != 1 {
			t.Fatalf("pool close calls = %d", pool.closeCalls.Load())
		}
		if got := sessionTypes(decodeSessionOutput(t, output.Bytes())); !equalStrings(got, []string{"hello", "ready"}) {
			t.Fatalf("messages = %v", got)
		}
	})
	t.Run("cleanup failure after provisional success", func(t *testing.T) {
		cleanupErr := errors.New("lease cleanup failed")
		reader, input := io.Pipe()
		defer input.Close()
		pool := &fakeSessionPool{}
		var output bytes.Buffer
		deps := sessionDependencies{
			newPool: func(context.Context, Request) (sessionBrowserPool, error) { return pool, nil },
			runJob: func(_ context.Context, req Request, _ sessionBrowserPool, emit func(Event) error) error {
				if err := emit(Event{Version: ProtocolVersion, Type: "result", RunID: req.RunID, MediaID: req.MediaID, OK: true}); err != nil {
					return err
				}
				return cleanupErr
			},
		}
		done := make(chan error, 1)
		go func() { done <- runSession(context.Background(), "sess-cleanup", reader, &output, io.Discard, deps) }()
		if _, err := io.WriteString(input, sessionRunLines(t, "sess-cleanup", request)); err != nil {
			t.Fatal(err)
		}
		err := <-done
		if !errors.Is(err, cleanupErr) {
			t.Fatalf("error = %v, want cleanup failure", err)
		}
		messages := decodeSessionOutput(t, output.Bytes())
		if got := sessionTypes(messages); !equalStrings(got, []string{"hello", "ready", "result"}) {
			t.Fatalf("messages = %v", got)
		}
	})
	t.Run("pool close failure", func(t *testing.T) {
		closeErr := errors.New("pool close failed")
		reader, input := io.Pipe()
		defer input.Close()
		pool := &fakeSessionPool{closeErr: closeErr}
		output := &signalingSessionWriter{messages: make(chan string, 16)}
		deps := sessionDependencies{
			newPool: func(context.Context, Request) (sessionBrowserPool, error) { return pool, nil },
			runJob: func(_ context.Context, req Request, _ sessionBrowserPool, emit func(Event) error) error {
				return emit(Event{Version: ProtocolVersion, Type: "result", RunID: req.RunID, MediaID: req.MediaID, OK: true})
			},
		}
		done := make(chan error, 1)
		go func() { done <- runSession(context.Background(), "sess-close", reader, output, io.Discard, deps) }()
		if _, err := io.WriteString(input, sessionRunLines(t, "sess-close", request)); err != nil {
			t.Fatal(err)
		}
		waitForReadyCount(t, output, 2)
		if err := input.Close(); err != nil {
			t.Fatal(err)
		}
		err := <-done
		if !errors.Is(err, closeErr) {
			t.Fatalf("error = %v, want close failure", err)
		}
	})
}

func TestRunSessionStdinEOFCancelsActiveJobAfterCleanupBeforePoolClose(t *testing.T) {
	request := sessionRequest("run-eof", "media-eof", "partial.mp4", "partial-hls")
	request.BrowserPath = "browser.exe"
	reader, input := io.Pipe()
	defer input.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	cleaned := make(chan struct{})
	resultSent := make(chan struct{})
	pool := &fakeSessionPool{closeHook: func() {
		select {
		case <-cleaned:
		default:
			t.Error("browser pool closed before active job cleanup completed")
		}
		select {
		case <-resultSent:
		default:
			t.Error("browser pool closed before terminal error result was written")
		}
	}}
	output := &signalingSessionWriter{messages: make(chan string, 16)}
	deps := sessionDependencies{
		newPool: func(context.Context, Request) (sessionBrowserPool, error) { return pool, nil },
		runJob: func(ctx context.Context, request Request, _ sessionBrowserPool, emit func(Event) error) error {
			close(started)
			<-ctx.Done()
			close(cleaned)
			err := emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, Error: "job canceled"})
			close(resultSent)
			return errors.Join(ctx.Err(), err)
		},
	}
	done := make(chan error, 1)
	go func() { done <- runSession(ctx, "sess-eof", reader, output, io.Discard, deps) }()
	if _, err := io.WriteString(input, sessionRunLines(t, "sess-eof", request)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not start")
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("session error = %v, want clean EOF retirement", err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("stdin EOF did not cancel and unwind the active job")
	}
	if pool.closeCalls.Load() != 1 {
		t.Fatalf("pool close calls = %d, want 1", pool.closeCalls.Load())
	}
	messages := decodeSessionOutput(t, output.Bytes())
	if got := sessionTypes(messages); !equalStrings(got, []string{"hello", "ready", "result"}) {
		t.Fatalf("EOF-cancelled output = %v, want one terminal error result and no cleanup or next ready", got)
	}
	if messages[2].Event == nil || messages[2].Event.OK || messages[2].RunID != request.RunID {
		t.Fatalf("EOF-cancelled result = %#v, want one error for active run", messages[2])
	}
}

func TestRunSessionInputWhilePoolFactoryBlocksCancelsAndClosesPartialPool(t *testing.T) {
	request := sessionRequest("run-factory", "media-factory", "out.mp4", "hls")
	for _, tc := range []struct {
		name   string
		active string
		close  bool
	}{
		{name: "EOF", close: true},
		{name: "malformed", active: "{\"version\":1,\"type\":\"bad\",\"session_id\":\"sess-factory\"}\n"},
		{name: "extra run", active: sessionRunLines(t, "sess-factory", sessionRequest("run-extra", "media-extra", "extra.mp4", "extra-hls"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, input := io.Pipe()
			defer input.Close()
			defer reader.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			factoryStarted := make(chan struct{})
			partialPool := &fakeSessionPool{}
			var runCalls atomic.Int32
			var output bytes.Buffer
			deps := sessionDependencies{
				newPool: func(ctx context.Context, _ Request) (sessionBrowserPool, error) {
					close(factoryStarted)
					<-ctx.Done()
					return partialPool, ctx.Err()
				},
				runJob: func(context.Context, Request, sessionBrowserPool, func(Event) error) error {
					runCalls.Add(1)
					return nil
				},
			}
			done := make(chan error, 1)
			go func() { done <- runSession(ctx, "sess-factory", reader, &output, io.Discard, deps) }()
			if _, err := io.WriteString(input, sessionRunLines(t, "sess-factory", request)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-factoryStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("pool factory did not start")
			}
			if tc.close {
				if err := input.Close(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := io.WriteString(input, tc.active); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if (err != nil) != !tc.close {
					t.Fatalf("session error = %v, want clean EOF only for the EOF case", err)
				}
			case <-time.After(time.Second):
				cancel()
				t.Fatal("active input did not cancel the blocked pool factory")
			}
			if runCalls.Load() != 0 {
				t.Fatalf("runJob called %d times while pool startup was canceled", runCalls.Load())
			}
			if partialPool.closeCalls.Load() != 1 {
				t.Fatalf("partial pool close calls = %d, want 1", partialPool.closeCalls.Load())
			}
			if got := sessionTypes(decodeSessionOutput(t, output.Bytes())); !equalStrings(got, []string{"hello", "ready"}) {
				t.Fatalf("pool-start cancellation output = %v, want no result, cleanup, or ready", got)
			}
		})
	}
}

func TestRunSessionActiveExtraOrMalformedInputCancelsWithoutCommit(t *testing.T) {
	first := sessionRequest("run-active", "media-active", "partial.mp4", "partial-hls")
	first.BrowserPath = "browser.exe"
	extra := sessionRequest("run-extra", "media-extra", "extra.mp4", "extra-hls")
	for _, tc := range []struct {
		name string
		line string
	}{
		{name: "extra run", line: func() string { return sessionRunLines(t, "sess-active", extra) }()},
		{name: "malformed message", line: "{\"version\":1,\"type\":\"unknown\",\"session_id\":\"sess-active\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, input := io.Pipe()
			defer input.Close()
			defer reader.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			pool := &fakeSessionPool{}
			var output bytes.Buffer
			deps := sessionDependencies{
				newPool: func(context.Context, Request) (sessionBrowserPool, error) { return pool, nil },
				runJob: func(ctx context.Context, _ Request, _ sessionBrowserPool, _ func(Event) error) error {
					close(started)
					<-ctx.Done()
					return ctx.Err()
				},
			}
			done := make(chan error, 1)
			go func() { done <- runSession(ctx, "sess-active", reader, &output, io.Discard, deps) }()
			if _, err := io.WriteString(input, sessionRunLines(t, "sess-active", first)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("run did not start")
			}
			writeExtra := make(chan error, 1)
			go func() {
				_, err := io.WriteString(input, tc.line)
				writeExtra <- err
			}()
			select {
			case err := <-writeExtra:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("worker did not read active input")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("active extra input did not terminate the session")
				}
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("active extra input did not cancel and unwind the job")
			}
			if pool.closeCalls.Load() != 1 {
				t.Fatalf("pool close calls = %d, want 1", pool.closeCalls.Load())
			}
			if got := sessionTypes(decodeSessionOutput(t, output.Bytes())); !equalStrings(got, []string{"hello", "ready"}) {
				t.Fatalf("terminal active-input output = %v, want no result, cleanup, or next ready", got)
			}
		})
	}
}

func TestRunSessionPersistentOpenPipeKeepsSerialSuccessJobs(t *testing.T) {
	first := sessionRequest("run-open-1", "media-open-1", "one.mp4", "one-hls")
	second := sessionRequest("run-open-2", "media-open-2", "two.mp4", "two-hls")
	first.BrowserPath, second.BrowserPath = "browser.exe", "browser.exe"
	reader, input := io.Pipe()
	defer input.Close()
	pool := &fakeSessionPool{}
	var runs atomic.Int32
	secondStarted := make(chan struct{})
	output := &signalingSessionWriter{messages: make(chan string, 16)}
	deps := sessionDependencies{
		newPool: func(context.Context, Request) (sessionBrowserPool, error) { return pool, nil },
		runJob: func(_ context.Context, request Request, _ sessionBrowserPool, emit func(Event) error) error {
			if runs.Add(1) == 2 {
				close(secondStarted)
			}
			return emit(Event{Version: ProtocolVersion, Type: "result", RunID: request.RunID, MediaID: request.MediaID, OK: true})
		},
	}
	done := make(chan error, 1)
	go func() { done <- runSession(context.Background(), "sess-open", reader, output, io.Discard, deps) }()
	if _, err := io.WriteString(input, sessionRunLines(t, "sess-open", first)); err != nil {
		t.Fatal(err)
	}
	waitForReadyCount(t, output, 2)
	if _, err := io.WriteString(input, sessionRunLines(t, "sess-open", second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start the second request")
	}
	waitForReadyCount(t, output, 3)
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("idle EOF should retire successfully: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session did not finish after second job and idle EOF")
	}
	if runs.Load() != 2 || pool.closeCalls.Load() != 1 {
		t.Fatalf("runs=%d pool close calls=%d, want two jobs and one pool close", runs.Load(), pool.closeCalls.Load())
	}
	if got := sessionTypes(decodeSessionOutput(t, output.Bytes())); !equalStrings(got, []string{"hello", "ready", "result", "cleanup", "ready", "result", "cleanup", "ready"}) {
		t.Fatalf("persistent open-pipe output = %v", got)
	}
}

type signalingSessionWriter struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	messages   chan string
	readyCount atomic.Int32
}

func (w *signalingSessionWriter) Write(p []byte) (int, error) {
	var message SessionMessage
	if err := json.Unmarshal(bytes.TrimSpace(p), &message); err != nil {
		return 0, err
	}
	w.mu.Lock()
	_, err := w.buffer.Write(p)
	w.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if message.Type == "ready" {
		w.readyCount.Add(1)
	}
	w.messages <- message.Type
	return len(p), nil
}

func (w *signalingSessionWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

func waitForReadyCount(t *testing.T, writer *signalingSessionWriter, count int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for writer.readyCount.Load() < int32(count) {
		select {
		case <-writer.messages:
		case <-deadline:
			t.Fatalf("observed %d ready messages, want %d", writer.readyCount.Load(), count)
		}
	}
}

func TestRunSessionRejectsInvalidSessionIDAndTransportWriteFailure(t *testing.T) {
	t.Run("default dependencies reject invalid request without creating browser", func(t *testing.T) {
		invalid := sessionRequest("run-invalid", "media-invalid", "out.mp4", "hls")
		invalid.Width = 5000
		var output bytes.Buffer
		err := RunSession(context.Background(), "sess-invalid", strings.NewReader(sessionRunLines(t, "sess-invalid", invalid)), &output, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "output size") {
			t.Fatalf("error = %v, want request output-size validation error", err)
		}
		messages := decodeSessionOutput(t, output.Bytes())
		if got := sessionTypes(messages); !equalStrings(got, []string{"hello", "ready", "result"}) || messages[2].Event == nil || messages[2].Event.OK {
			t.Fatalf("invalid request messages = %#v", messages)
		}
	})
	t.Run("invalid startup id", func(t *testing.T) {
		var output bytes.Buffer
		err := RunSession(context.Background(), "contains space", strings.NewReader(""), &output, io.Discard)
		if err == nil {
			t.Fatal("accepted invalid startup session id")
		}
		if output.Len() != 0 {
			t.Fatalf("wrote handshake before validating session id: %q", output.String())
		}
	})
	t.Run("write failure", func(t *testing.T) {
		writeErr := errors.New("stdout broken")
		err := RunSession(context.Background(), "sess-write", strings.NewReader(""), rejectingSessionWriter{err: writeErr}, io.Discard)
		if !errors.Is(err, writeErr) {
			t.Fatalf("error = %v, want write error", err)
		}
	})
}

type fakeSessionPool struct {
	closeCalls atomic.Int32
	closeErr   error
	closeHook  func()
}

func (p *fakeSessionPool) Close() error {
	p.closeCalls.Add(1)
	if p.closeHook != nil {
		p.closeHook()
	}
	return p.closeErr
}

func (p *fakeSessionPool) renderPool() *nicorender.BrowserPool { return nil }

type rejectingSessionWriter struct{ err error }

func (w rejectingSessionWriter) Write([]byte) (int, error) { return 0, w.err }

func sessionRequest(runID, mediaID, outputPath, hlsDir string) Request {
	r := validTestRequest()
	r.RunID, r.MediaID, r.OutputPath, r.HLSStagingDir = runID, mediaID, outputPath, hlsDir
	return r
}

func sessionRunLines(t *testing.T, sessionID string, requests ...Request) string {
	t.Helper()
	var input strings.Builder
	for i := range requests {
		request := requests[i]
		m := SessionMessage{Version: SessionProtocolVersion, Type: "run", SessionID: sessionID, RunID: request.RunID, MediaID: request.MediaID, Request: &request}
		input.WriteString(sessionMessageLine(t, m))
	}
	return input.String()
}

func sessionMessageLine(t *testing.T, message SessionMessage) string {
	t.Helper()
	line, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return string(append(line, '\n'))
}

func decodeSessionOutput(t *testing.T, data []byte) []SessionMessage {
	t.Helper()
	var messages []SessionMessage
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var message SessionMessage
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatalf("decode output line %q: %v", line, err)
		}
		if err := message.Validate(); err != nil {
			t.Fatalf("invalid output message %#v: %v", message, err)
		}
		messages = append(messages, message)
	}
	return messages
}

func sessionTypes(messages []SessionMessage) []string {
	result := make([]string, len(messages))
	for i := range messages {
		result[i] = messages[i].Type
	}
	return result
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

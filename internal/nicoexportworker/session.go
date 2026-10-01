package nicoexportworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"imagepadserver/internal/nicorender"
)

// SessionFreshBrowserPerJobEnv disables the session-owned BrowserPool while
// keeping the worker process alive across serial jobs. The shared job core
// then creates and closes a browser for each request.
const SessionFreshBrowserPerJobEnv = "NICO_TIMELINE_WORKER_SESSION_FRESH_BROWSER_PER_JOB"

// sessionBrowserPool is owned for the lifetime of one worker session. Job-local
// context/page state is released by the shared one-job core before it returns.
type sessionBrowserPool interface {
	Close() error
	renderPool() *nicorender.BrowserPool
}

type sessionDependencies struct {
	newPool            func(context.Context, Request) (sessionBrowserPool, error)
	runJob             func(context.Context, Request, sessionBrowserPool, func(Event) error) error
	freshBrowserPerJob bool
}

type ownedSessionBrowserPool struct{ pool *nicorender.BrowserPool }

func (p *ownedSessionBrowserPool) Close() error {
	if p == nil || p.pool == nil {
		return nil
	}
	return p.pool.Close()
}
func (p *ownedSessionBrowserPool) renderPool() *nicorender.BrowserPool {
	if p == nil {
		return nil
	}
	return p.pool
}

func RunSession(ctx context.Context, sessionID string, stdin io.Reader, stdout, stderr io.Writer) error {
	if stderr == nil {
		stderr = io.Discard
	}
	deps := sessionDependencies{
		freshBrowserPerJob: os.Getenv(SessionFreshBrowserPerJobEnv) == "1",
		newPool: func(owner context.Context, request Request) (sessionBrowserPool, error) {
			pool, err := nicorender.NewBrowserPool(owner, nicorender.RenderOptions{BrowserPath: request.BrowserPath})
			if pool == nil {
				return nil, err
			}
			return &ownedSessionBrowserPool{pool: pool}, err
		},
		runJob: func(jobCtx context.Context, request Request, pool sessionBrowserPool, emit func(Event) error) error {
			var renderPool *nicorender.BrowserPool
			if pool != nil {
				renderPool = pool.renderPool()
			}
			return runJob(jobCtx, request, renderPool, stderr, emit)
		},
	}
	return runSession(ctx, sessionID, stdin, stdout, stderr, deps)
}

func runSession(ctx context.Context, sessionID string, stdin io.Reader, stdout, stderr io.Writer, deps sessionDependencies) (retErr error) {
	if !validToken(sessionID) {
		return errors.New("nico export worker: invalid startup session id")
	}
	if stdin == nil || stdout == nil {
		return errors.New("nico export worker: session stdin and stdout are required")
	}
	if deps.newPool == nil || deps.runJob == nil {
		return errors.New("nico export worker: session dependencies are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if stderr == nil {
		stderr = io.Discard
	}
	ownerCtx, cancelOwner := context.WithCancel(ctx)
	defer cancelOwner()
	var pool sessionBrowserPool
	var poolBrowserPath string
	defer func() {
		if pool != nil {
			retErr = errors.Join(retErr, pool.Close())
		}
	}()

	state := NewSessionState(sessionID)
	var outputMu sync.Mutex
	write := func(message SessionMessage) error {
		if err := message.Validate(); err != nil {
			return err
		}
		line, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("nico export worker: encode session message: %w", err)
		}
		line = append(line, '\n')
		if n, err := stdout.Write(line); err != nil {
			return fmt.Errorf("nico export worker: write session message: %w", err)
		} else if n != len(line) {
			return io.ErrShortWrite
		}
		return nil
	}
	writeTransition := func(message SessionMessage) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		if err := state.Accept(message); err != nil {
			return err
		}
		return write(message)
	}
	for _, kind := range []string{"hello", "ready"} {
		message := SessionMessage{Version: SessionProtocolVersion, Type: kind, SessionID: sessionID}
		if err := writeTransition(message); err != nil {
			return err
		}
	}

	decoder := NewSessionDecoder(sessionID)
	reader := bufio.NewReaderSize(stdin, 4096)
	input := make(chan sessionInputResult, 1)
	go func() {
		for {
			message, err := readSessionMessage(reader, decoder)
			select {
			case input <- sessionInputResult{message: message, err: err}:
			case <-ownerCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		if err := ownerCtx.Err(); err != nil {
			return err
		}
		var message SessionMessage
		select {
		case result := <-input:
			message = result.message
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				return result.err
			}
		case <-ownerCtx.Done():
			return ownerCtx.Err()
		}
		if err := state.Accept(message); err != nil {
			return err
		}
		if message.Type != "run" || message.Request == nil {
			return errors.New("nico export worker: expected a run request")
		}
		request := *message.Request
		jobCtx, cancelJob := context.WithCancel(ownerCtx)
		resultCount := 0
		resultOK := false
		emit := func(event Event) error {
			outputMu.Lock()
			defer outputMu.Unlock()
			message := SessionMessage{
				Version: SessionProtocolVersion, Type: event.Type, SessionID: sessionID,
				RunID: event.RunID, MediaID: event.MediaID, Event: &event,
			}
			if err := state.Accept(message); err != nil {
				return err
			}
			if event.Type == "result" {
				resultCount++
				resultOK = event.OK
			}
			return write(message)
		}
		// Validate before starting the browser process. The shared core owns the
		// validation result and emits the same terminal failure Event as Run.
		if err := request.Validate(); err != nil {
			runErr := deps.runJob(jobCtx, request, pool, emit)
			cancelJob()
			if runErr != nil {
				return runErr
			}
			return errors.New("nico export worker: invalid request unexpectedly succeeded")
		}
		if !deps.freshBrowserPerJob && pool != nil && request.BrowserPath != poolBrowserPath {
			cancelJob()
			return errors.New("nico export worker: BrowserPath changed; start a fresh session")
		}
		if !deps.freshBrowserPerJob && pool == nil {
			poolDone := make(chan sessionPoolCreationResult, 1)
			go func() {
				created, err := deps.newPool(ownerCtx, request)
				poolDone <- sessionPoolCreationResult{pool: created, err: err}
			}()
			adoptPool := func(result sessionPoolCreationResult) {
				if result.pool != nil {
					pool = result.pool
					poolBrowserPath = request.BrowserPath
				}
			}
			var creation sessionPoolCreationResult
			select {
			case activeInput := <-input:
				terminalErr := activeSessionInputError(activeInput)
				cancelJob()
				cancelOwner()
				creation = <-poolDone
				adoptPool(creation)
				if creation.err != nil && !errors.Is(creation.err, context.Canceled) {
					terminalErr = errors.Join(terminalErr, creation.err)
				}
				if errors.Is(terminalErr, errSessionInputEOF) && (creation.err == nil || errors.Is(creation.err, context.Canceled)) {
					return nil
				}
				return terminalErr
			case creation = <-poolDone:
			case <-ownerCtx.Done():
				cancelJob()
				creation = <-poolDone
				adoptPool(creation)
				if creation.err != nil && !errors.Is(creation.err, context.Canceled) {
					return errors.Join(ownerCtx.Err(), creation.err)
				}
				return ownerCtx.Err()
			}
			adoptPool(creation)
			if creation.err != nil {
				cancelJob()
				return fmt.Errorf("nico export worker: create session browser pool: %w", creation.err)
			}
			if pool == nil {
				cancelJob()
				return errors.New("nico export worker: browser pool factory returned nil")
			}
		}
		runDone := make(chan error, 1)
		go func() { runDone <- deps.runJob(jobCtx, request, pool, emit) }()
		var runErr error
		var terminalInputErr error
		select {
		case runErr = <-runDone:
		default:
			select {
			case result := <-input:
				terminalInputErr = activeSessionInputError(result)
				cancelJob()
				runErr = <-runDone
			case runErr = <-runDone:
			}
		}
		cancelJob()
		if terminalInputErr != nil {
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				return errors.Join(terminalInputErr, runErr)
			}
			if errors.Is(terminalInputErr, errSessionInputEOF) {
				return nil
			}
			return terminalInputErr
		}
		if runErr != nil {
			return runErr
		}
		if err := ownerCtx.Err(); err != nil {
			return err
		}
		outputMu.Lock()
		gotOneSuccessfulResult := resultCount == 1 && resultOK
		outputMu.Unlock()
		if !gotOneSuccessfulResult {
			return errors.New("nico export worker: job core returned without exactly one successful result")
		}
		cleanup := SessionMessage{Version: SessionProtocolVersion, Type: "cleanup", SessionID: sessionID, RunID: request.RunID, MediaID: request.MediaID}
		if err := writeTransition(cleanup); err != nil {
			return err
		}
		ready := SessionMessage{Version: SessionProtocolVersion, Type: "ready", SessionID: sessionID}
		if err := writeTransition(ready); err != nil {
			return err
		}
	}
}

type sessionInputResult struct {
	message SessionMessage
	err     error
}

type sessionPoolCreationResult struct {
	pool sessionBrowserPool
	err  error
}

var errSessionInputEOF = errors.New("nico export worker: stdin EOF")

func activeSessionInputError(result sessionInputResult) error {
	if errors.Is(result.err, io.EOF) {
		return errSessionInputEOF
	}
	if result.err != nil {
		return fmt.Errorf("nico export worker: session input failed while a job was active: %w", result.err)
	}
	return errors.New("nico export worker: received input while a job was active")
}

func readSessionMessage(reader *bufio.Reader, decoder *SessionDecoder) (SessionMessage, error) {
	for {
		message, ok, err := decoder.Next()
		if err != nil {
			return SessionMessage{}, err
		}
		if ok {
			return message, nil
		}
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if err := decoder.Feed(fragment); err != nil {
				return SessionMessage{}, err
			}
		}
		if readErr == nil || errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			if err := decoder.Finish(); err != nil {
				return SessionMessage{}, err
			}
			return SessionMessage{}, io.EOF
		}
		return SessionMessage{}, fmt.Errorf("nico export worker: read session message: %w", readErr)
	}
}

package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	"time"

	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/nicoexportworker"
)

type nicoSessionProcess interface {
	Close() error
	Wait() (nicoexportbudget.Report, error)
	Snapshot() (nicoexportbudget.Report, error)
}

type nicoSessionStarter func(context.Context, nicoexportbudget.ProcessSpec, nicoexportbudget.Options) (nicoSessionProcess, error)
type nicoSessionIDGenerator func() (string, error)
type nicoWorkerSessionIdentityFunc func(nicoexportworker.Request, nicoexportbudget.Options) (string, error)
type nicoSessionRetirementTimer interface{ Stop() bool }

type nicoWorkerSessionManager struct {
	owner         context.Context
	startProcess  nicoSessionStarter
	newID         nicoSessionIDGenerator
	identityFunc  nicoWorkerSessionIdentityFunc
	nowFn         func() time.Time
	afterFunc     func(time.Duration, func()) nicoSessionRetirementTimer
	idleTimeout   time.Duration
	retireGrace   time.Duration
	maxJobs       int
	mu            sync.Mutex
	runMu         sync.Mutex
	current       *nicoWorkerSession
	retirementErr error
}

type nicoWorkerSession struct {
	id            string
	process       nicoSessionProcess
	tempRoot      string
	stdinReader   *io.PipeReader
	stdinWriter   *io.PipeWriter
	stdoutReader  *io.PipeReader
	stdoutWriter  *io.PipeWriter
	stderr        *nicoWorkerStderrTail
	messages      chan nicoSessionReadResult
	stop          chan struct{}
	state         *nicoexportworker.SessionState
	closeOnce     sync.Once
	closeErr      error
	report        nicoexportbudget.Report
	waitErr       error
	identity      string
	active        bool
	completedJobs int
	idleDeadline  time.Time
	idleTimer     nicoSessionRetirementTimer
	retireGrace   time.Duration
	processCancel context.CancelFunc
	readDone      chan struct{}
	readErrMu     sync.Mutex
	readErr       error
	retiring      atomic.Bool
}

type nicoSessionReadResult struct {
	message nicoexportworker.SessionMessage
	err     error
}

func newNicoWorkerSessionManager(owner context.Context, start nicoSessionStarter, newID nicoSessionIDGenerator) *nicoWorkerSessionManager {
	if owner == nil {
		owner = context.Background()
	}
	if start == nil {
		start = startNicoWorkerSessionProcess
	}
	if newID == nil {
		newID = newNicoWorkerSessionID
	}
	m := &nicoWorkerSessionManager{
		owner: owner, startProcess: start, newID: newID,
		identityFunc: nicoWorkerSessionIdentity,
		nowFn:        time.Now,
		afterFunc: func(delay time.Duration, callback func()) nicoSessionRetirementTimer {
			return time.AfterFunc(delay, callback)
		},
		idleTimeout: time.Minute,
		retireGrace: 15 * time.Second,
		maxJobs:     64,
	}
	if done := owner.Done(); done != nil {
		go func() {
			<-done
			_ = m.retireCurrent()
		}()
	}
	return m
}

func newNicoWorkerSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("niconico worker session id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func startNicoWorkerSessionProcess(ctx context.Context, spec nicoexportbudget.ProcessSpec, options nicoexportbudget.Options) (nicoSessionProcess, error) {
	executable, err := nicoWorkerExecutable()
	if err != nil {
		return nil, fmt.Errorf("niconico worker executable: %w", err)
	}
	spec.Exe = executable
	spec.Dir = filepath.Dir(executable)
	process, err := nicoexportbudget.StartSession(ctx, spec, options)
	if err != nil {
		return nil, err
	}
	return process, nil
}

func nicoWorkerSessionArgs(sessionID string) []string {
	return []string{"nico-export-session", "--session-id", sessionID}
}

func nicoWorkerSessionIdentity(request nicoexportworker.Request, options nicoexportbudget.Options) (string, error) {
	executable, err := nicoWorkerExecutable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("absolute executable path: %w", err)
	}
	file, err := os.Open(executable)
	if err != nil {
		return "", fmt.Errorf("open executable: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("hash executable: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close executable after hash: %w", err)
	}
	launchConfig := struct {
		Executable         string   `json:"executable"`
		BinarySHA256       string   `json:"binary_sha256"`
		Args               []string `json:"args"`
		CPUPercent         uint32   `json:"cpu_percent"`
		SampleInterval     string   `json:"sample_interval"`
		BrowserPath        string   `json:"browser_path"`
		Headless           string   `json:"headless"`
		BrowserGPU         string   `json:"browser_gpu"`
		FreshBrowserPerJob bool     `json:"fresh_browser_per_job"`
	}{
		Executable:         filepath.Clean(executable),
		BinarySHA256:       hex.EncodeToString(hasher.Sum(nil)),
		Args:               nicoWorkerSessionArgs("{session_id}"),
		CPUPercent:         options.Percent,
		SampleInterval:     options.SampleInterval.String(),
		BrowserPath:        filepath.Clean(request.BrowserPath),
		Headless:           os.Getenv("IMAGEPAD_NICONICO_RENDER_HEADLESS"),
		BrowserGPU:         os.Getenv("IMAGEPAD_NICONICO_RENDER_GPU"),
		FreshBrowserPerJob: os.Getenv(nicoexportworker.SessionFreshBrowserPerJobEnv) == "1",
	}
	encoded, err := json.Marshal(launchConfig)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (m *nicoWorkerSessionManager) start() error {
	identity, err := m.identityFunc(nicoexportworker.Request{}, nicoWorkerCPUOptions())
	if err != nil {
		return err
	}
	_, err = m.ensureSession(nicoWorkerCPUOptions(), identity, false)
	return err
}

func (m *nicoWorkerSessionManager) run(ctx context.Context, request nicoexportworker.Request, options nicoexportbudget.Options, onProgress func(nicoexportworker.Event)) (nicoexportworker.Event, nicoexportbudget.Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := request.Validate(); err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
	}
	if err := m.owner.Err(); err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
	}
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if err := m.takeRetirementError(); err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
	}
	identity, err := m.identityFunc(request, options)
	if err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, fmt.Errorf("niconico worker session identity: %w", err)
	}
	session, err := m.ensureSession(options, identity, true)
	if err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
	}
	before, err := session.process.Snapshot()
	if err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, fmt.Errorf("niconico worker session snapshot before job: %w", err))
	}
	run := nicoexportworker.SessionMessage{Version: nicoexportworker.SessionProtocolVersion, Type: "run", SessionID: session.id, RunID: request.RunID, MediaID: request.MediaID, Request: &request}
	if err := session.state.Accept(run); err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, err)
	}
	line, err := marshalNicoSessionMessage(run)
	if err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, err)
	}
	started := time.Now()
	if err := writeNicoSessionLine(session.stdinWriter, line); err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, fmt.Errorf("niconico worker session stdin: %w", err))
	}
	var result *nicoexportworker.Event
	for {
		read, err := waitNicoSessionMessage(ctx, m.owner, session)
		if err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, err)
		}
		message := read.message
		if err := session.state.Accept(message); err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, err)
		}
		switch message.Type {
		case "progress":
			if onProgress != nil && message.Event != nil {
				onProgress(*message.Event)
			}
		case "result":
			if message.Event == nil {
				return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, errors.New("niconico worker session result payload missing"))
			}
			copy := *message.Event
			if !copy.OK {
				if copy.Error == "" {
					copy.Error = "niconico worker session failed without an error"
				}
				return copy, nicoexportbudget.Report{}, m.failSession(session, errors.New(copy.Error))
			}
			if err := validateNicoWorkerResult(request, copy); err != nil {
				return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, err)
			}
			result = &copy
		case "ready":
			if result == nil {
				return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, errors.New("niconico worker session became ready before successful result"))
			}
			after, err := session.process.Snapshot()
			if err != nil {
				return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, fmt.Errorf("niconico worker session snapshot after job: %w", err))
			}
			if !after.Verified {
				return nicoexportworker.Event{}, nicoexportbudget.Report{}, m.failSession(session, fmt.Errorf("niconico worker CPU budget unverified: %s", after.Reason))
			}
			report := nicoSessionReportDelta(before, after)
			// JobCPURate is the configured Job Object cap, not a cumulative
			// counter, so preserve the backend's current cap value.
			report.JobCPURate = after.JobCPURate
			result.WorkerWallSeconds = time.Since(started).Seconds()
			if err := m.completeJob(session); err != nil {
				return *result, report, fmt.Errorf("niconico worker session retirement: %w", err)
			}
			return *result, report, nil
		}
	}
}

func (m *nicoWorkerSessionManager) ensureSession(options nicoexportbudget.Options, identity string, active bool) (*nicoWorkerSession, error) {
	if err := m.owner.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.current != nil {
		session := m.current
		if session.identity == identity {
			if active {
				m.beginJobLocked(session)
			}
			m.mu.Unlock()
			return session, nil
		}
		m.current = nil
		m.stopIdleTimerLocked(session)
		m.mu.Unlock()
		if err := session.closeAndWait(); err != nil {
			return nil, fmt.Errorf("close incompatible niconico worker session: %w", err)
		}
	} else {
		m.mu.Unlock()
	}

	sessionID, err := m.newID()
	if err != nil {
		return nil, err
	}
	if err := (nicoexportworker.SessionMessage{Version: nicoexportworker.SessionProtocolVersion, Type: "hello", SessionID: sessionID}).Validate(); err != nil {
		return nil, fmt.Errorf("niconico worker session id generator returned invalid token: %w", err)
	}
	tempRoot, err := os.MkdirTemp("", "imagepad-niconico-session-")
	if err != nil {
		return nil, fmt.Errorf("create private niconico worker temp root: %w", err)
	}
	tempRoot, err = filepath.Abs(tempRoot)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("resolve private niconico worker temp root: %w", err), os.RemoveAll(tempRoot))
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	session := &nicoWorkerSession{
		id: sessionID, identity: identity, stdinReader: stdinReader, stdinWriter: stdinWriter,
		stdoutReader: stdoutReader, stdoutWriter: stdoutWriter,
		tempRoot: tempRoot,
		stderr:   &nicoWorkerStderrTail{}, messages: make(chan nicoSessionReadResult, 8), stop: make(chan struct{}),
		state:    nicoexportworker.NewSessionState(sessionID),
		readDone: make(chan struct{}),
	}
	spec := nicoexportbudget.ProcessSpec{
		Args: nicoWorkerSessionArgs(sessionID), Env: nicoWorkerSessionEnvironment(tempRoot),
		Stdin: stdinReader, Stdout: stdoutWriter, Stderr: session.stderr,
	}
	processCtx, processCancel := context.WithCancel(context.WithoutCancel(m.owner))
	session.processCancel = processCancel
	session.retireGrace = m.retireGrace
	process, err := m.startProcess(processCtx, spec, options)
	if err != nil {
		processCancel()
		session.closePipes()
		return nil, errors.Join(fmt.Errorf("start niconico worker session: %w", err), os.RemoveAll(tempRoot))
	}
	session.process = process
	if process == nil {
		processCancel()
		session.closePipes()
		return nil, errors.Join(errors.New("start niconico worker session: starter returned nil process"), os.RemoveAll(tempRoot))
	}
	go session.readLoop()
	for _, expected := range []string{"hello", "ready"} {
		read, err := waitNicoSessionMessage(m.owner, m.owner, session)
		if err != nil {
			return nil, m.discardUnpublished(session, err)
		}
		if read.message.Type != expected {
			return nil, m.discardUnpublished(session, fmt.Errorf("niconico worker session expected %s, got %s", expected, read.message.Type))
		}
		if err := session.state.Accept(read.message); err != nil {
			return nil, m.discardUnpublished(session, err)
		}
	}
	if err := m.owner.Err(); err != nil {
		return nil, m.discardUnpublished(session, err)
	}
	m.mu.Lock()
	if m.current != nil {
		existing := m.current
		if existing.identity == identity && active {
			m.beginJobLocked(existing)
		}
		m.mu.Unlock()
		_ = session.closeAndWait()
		if existing.identity != identity {
			return nil, errors.New("niconico worker session changed while starting a generation")
		}
		return existing, nil
	}
	m.current = session
	if active {
		session.active = true
	} else {
		m.scheduleIdleLocked(session)
	}
	m.mu.Unlock()
	return session, nil
}

func (m *nicoWorkerSessionManager) discardUnpublished(session *nicoWorkerSession, cause error) error {
	return sessionFailureWithDiagnostics(cause, session.closeAndWait(), session.stderr)
}

func (m *nicoWorkerSessionManager) failSession(session *nicoWorkerSession, cause error) error {
	m.mu.Lock()
	if m.current == session {
		m.current = nil
	}
	m.stopIdleTimerLocked(session)
	session.active = false
	m.mu.Unlock()
	return sessionFailureWithDiagnostics(cause, session.closeAndWait(), session.stderr)
}

func (m *nicoWorkerSessionManager) takeRetirementError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.retirementErr
	m.retirementErr = nil
	return err
}

func (m *nicoWorkerSessionManager) beginJobLocked(session *nicoWorkerSession) {
	m.stopIdleTimerLocked(session)
	session.active = true
}

func (m *nicoWorkerSessionManager) stopIdleTimerLocked(session *nicoWorkerSession) {
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	session.idleDeadline = time.Time{}
}

func (m *nicoWorkerSessionManager) scheduleIdleLocked(session *nicoWorkerSession) {
	m.stopIdleTimerLocked(session)
	if m.current != session || session.active {
		return
	}
	deadline := m.nowFn().Add(m.idleTimeout)
	session.idleDeadline = deadline
	session.idleTimer = m.afterFunc(m.idleTimeout, func() { m.retireIdle(session, deadline) })
}

func (m *nicoWorkerSessionManager) retireIdle(session *nicoWorkerSession, deadline time.Time) {
	m.mu.Lock()
	if m.current != session || session.active || !session.idleDeadline.Equal(deadline) {
		m.mu.Unlock()
		return
	}
	remaining := deadline.Sub(m.nowFn())
	if remaining > 0 {
		session.idleTimer = m.afterFunc(remaining, func() { m.retireIdle(session, deadline) })
		m.mu.Unlock()
		return
	}
	m.current = nil
	m.stopIdleTimerLocked(session)
	m.mu.Unlock()
	if err := session.closeAndWait(); err != nil {
		m.mu.Lock()
		m.retirementErr = errors.Join(m.retirementErr, fmt.Errorf("retire idle niconico worker session: %w", err))
		m.mu.Unlock()
	}
}

func (m *nicoWorkerSessionManager) completeJob(session *nicoWorkerSession) error {
	m.mu.Lock()
	if m.current != session {
		m.mu.Unlock()
		return errors.New("niconico worker session retired during job completion")
	}
	session.active = false
	session.completedJobs++
	if session.completedJobs < m.maxJobs {
		m.scheduleIdleLocked(session)
		m.mu.Unlock()
		return nil
	}
	m.current = nil
	m.stopIdleTimerLocked(session)
	m.mu.Unlock()
	return session.closeAndWait()
}

func sessionFailureWithDiagnostics(cause, cleanupErr error, stderr *nicoWorkerStderrTail) error {
	if stderr != nil {
		if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
			return errors.Join(cause, cleanupErr, fmt.Errorf("niconico worker session stderr: %s", diagnostic))
		}
	}
	return errors.Join(cause, cleanupErr)
}

func (m *nicoWorkerSessionManager) retireCurrent() error {
	m.mu.Lock()
	session := m.current
	m.current = nil
	if session != nil {
		m.stopIdleTimerLocked(session)
		session.active = false
	}
	m.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.closeAndWait()
}

func (m *nicoWorkerSessionManager) close() error {
	return m.retireCurrent()
}

func (s *nicoWorkerSession) readLoop() {
	defer close(s.readDone)
	decoder := nicoexportworker.NewSessionDecoder(s.id)
	reader := bufio.NewReaderSize(s.stdoutReader, 4096)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) != 0 {
			if feedErr := decoder.Feed(fragment); feedErr != nil {
				s.setReadErr(feedErr)
				s.deliver(nicoSessionReadResult{err: feedErr})
				return
			}
			message, ok, decodeErr := decoder.Next()
			if decodeErr != nil {
				s.setReadErr(decodeErr)
				s.deliver(nicoSessionReadResult{err: decodeErr})
				return
			}
			if ok {
				s.deliver(nicoSessionReadResult{message: message})
			}
		}
		if err == nil || errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			finishErr := decoder.Finish()
			if finishErr == nil && !s.retiring.Load() {
				finishErr = io.ErrUnexpectedEOF
			}
			s.setReadErr(finishErr)
			if finishErr != nil {
				s.deliver(nicoSessionReadResult{err: finishErr})
			}
			return
		}
		readErr := fmt.Errorf("niconico worker session stdout: %w", err)
		s.setReadErr(readErr)
		s.deliver(nicoSessionReadResult{err: readErr})
		return
	}
}

func (s *nicoWorkerSession) deliver(result nicoSessionReadResult) {
	select {
	case s.messages <- result:
	case <-s.stop:
	}
}

func (s *nicoWorkerSession) closePipes() {
	_ = s.stdinWriter.CloseWithError(io.ErrClosedPipe)
	_ = s.stdinReader.CloseWithError(io.ErrClosedPipe)
	_ = s.stdoutReader.CloseWithError(io.ErrClosedPipe)
	_ = s.stdoutWriter.CloseWithError(io.ErrClosedPipe)
}

func (s *nicoWorkerSession) closeAndWait() error {
	s.closeOnce.Do(func() {
		s.retiring.Store(true)
		close(s.stop)
		closeErr := error(nil)
		if err := s.stdinWriter.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			closeErr = errors.Join(closeErr, err)
		}
		if s.process != nil {
			type waitResult struct {
				report nicoexportbudget.Report
				err    error
			}
			waited := make(chan waitResult, 1)
			go func() {
				report, err := s.process.Wait()
				waited <- waitResult{report: report, err: err}
			}()
			timer := time.NewTimer(s.retireGrace)
			forced := false
			select {
			case result := <-waited:
				s.report, s.waitErr = result.report, result.err
			case <-timer.C:
				forced = true
				closeErr = errors.Join(closeErr, s.process.Close())
				result := <-waited
				s.report, s.waitErr = result.report, result.err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if err := s.stdoutWriter.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
				closeErr = errors.Join(closeErr, err)
			}
			if forced {
				<-s.readDone
				closeErr = errors.Join(closeErr, s.getReadErr(), s.waitErr)
				s.closePipes()
			} else {
				select {
				case <-s.readDone:
				case <-time.After(s.retireGrace):
					closeErr = errors.Join(closeErr, errors.New("niconico worker stdout did not reach EOF during retirement"))
				}
				closeErr = errors.Join(closeErr, s.getReadErr(), s.waitErr)
				s.closePipes()
			}
		} else {
			s.closePipes()
		}
		if s.processCancel != nil {
			s.processCancel()
		}
		if s.tempRoot != "" {
			if err := os.RemoveAll(s.tempRoot); err != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("remove private niconico worker temp root %q: %w", s.tempRoot, err))
			}
		}
		s.closeErr = closeErr
	})
	return s.closeErr
}

func nicoWorkerSessionEnvironment(tempRoot string) []string {
	keys := map[string]struct{}{"temp": {}, "tmp": {}, "tmpdir": {}}
	env := make([]string, 0, len(os.Environ())+len(keys))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replace := keys[strings.ToLower(name)]; replace {
				continue
			}
		}
		env = append(env, entry)
	}
	return append(env, "TEMP="+tempRoot, "TMP="+tempRoot, "TMPDIR="+tempRoot)
}

func (s *nicoWorkerSession) setReadErr(err error) {
	s.readErrMu.Lock()
	s.readErr = errors.Join(s.readErr, err)
	s.readErrMu.Unlock()
}

func (s *nicoWorkerSession) getReadErr() error {
	s.readErrMu.Lock()
	defer s.readErrMu.Unlock()
	return s.readErr
}

func waitNicoSessionMessage(jobCtx, ownerCtx context.Context, session *nicoWorkerSession) (nicoSessionReadResult, error) {
	select {
	case result := <-session.messages:
		if result.err != nil {
			return result, result.err
		}
		return result, nil
	case <-jobCtx.Done():
		return nicoSessionReadResult{}, jobCtx.Err()
	case <-ownerCtx.Done():
		return nicoSessionReadResult{}, ownerCtx.Err()
	}
}

func marshalNicoSessionMessage(message nicoexportworker.SessionMessage) ([]byte, error) {
	if err := message.Validate(); err != nil {
		return nil, err
	}
	line, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

func writeNicoSessionLine(writer io.Writer, line []byte) error {
	for len(line) > 0 {
		n, err := writer.Write(line)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		line = line[n:]
	}
	return nil
}

func nicoSessionReportDelta(before, after nicoexportbudget.Report) nicoexportbudget.Report {
	report := after
	report.PIDs = append([]uint32(nil), after.PIDs...)
	if report.CPUSeconds >= before.CPUSeconds {
		report.CPUSeconds -= before.CPUSeconds
	} else {
		report.CPUSeconds = 0
	}
	startSample := len(before.Samples)
	if startSample > len(after.Samples) {
		startSample = len(after.Samples)
	}
	report.Samples = append([]nicoexportbudget.Sample(nil), after.Samples[startSample:]...)
	for i := range report.Samples {
		report.Samples[i].PIDs = append([]uint32(nil), report.Samples[i].PIDs...)
	}
	return report
}

func (s *Server) runNicoWorkerSession(ctx context.Context, request nicoexportworker.Request, options nicoexportbudget.Options) (nicoexportworker.Event, nicoexportbudget.Report, error) {
	s.mu.Lock()
	manager := s.nicoWorkerSessions
	if manager == nil {
		owner := s.lifecycleCtx
		if owner == nil {
			owner = context.Background()
		}
		manager = newNicoWorkerSessionManager(owner, nil, nil)
		s.nicoWorkerSessions = manager
	}
	s.mu.Unlock()
	return manager.run(ctx, request, options, nicoWorkerProgressFromContext(ctx))
}

func nicoWorkerSessionEnabled() bool {
	switch strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_WORKER_SESSION")) {
	case "1":
		return true
	case "0":
		return false
	case "":
		return runtime.GOOS == "windows"
	default:
		return false
	}
}

func (s *Server) runNicoWorkerRequest(ctx context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
	if nicoWorkerSessionEnabled() {
		return s.runNicoWorkerSession(ctx, request, nicoWorkerCPUOptions())
	}
	return niconicoWorkerRunner(ctx, request)
}

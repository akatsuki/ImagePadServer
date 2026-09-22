package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/nicoexportworker"
)

const maxNicoWorkerEventLine = 64 * 1024

// Nico exports share one process-wide 20% CPU budget. A second job would
// otherwise create another independent Job Object and oversubscribe VRChat.
var nicoExportMu sync.Mutex

// Kept behind a seam for the opt-in HTTP integration test. Production always
// resolves the currently running imagepadserver executable.
var nicoWorkerExecutable = os.Executable

type nicoWorkerEventCollector struct {
	mu         sync.Mutex
	line       []byte
	result     *nicoexportworker.Event
	resultSeen bool
	err        error
	onProgress func(nicoexportworker.Event)
}

type nicoWorkerProgressContextKey struct{}

func withNicoWorkerProgress(ctx context.Context, onProgress func(nicoexportworker.Event)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if onProgress == nil {
		return ctx
	}
	return context.WithValue(ctx, nicoWorkerProgressContextKey{}, onProgress)
}

func nicoWorkerProgressFromContext(ctx context.Context) func(nicoexportworker.Event) {
	if ctx == nil {
		return nil
	}
	onProgress, _ := ctx.Value(nicoWorkerProgressContextKey{}).(func(nicoexportworker.Event))
	return onProgress
}

func newNicoWorkerEventCollector(ctx context.Context, request nicoexportworker.Request) *nicoWorkerEventCollector {
	onProgress := nicoWorkerProgressFromContext(ctx)
	collector := &nicoWorkerEventCollector{}
	if onProgress == nil {
		return collector
	}
	collector.onProgress = func(event nicoexportworker.Event) {
		if event.RunID != request.RunID || event.MediaID != request.MediaID {
			return
		}
		onProgress(event)
	}
	return collector
}

func (c *nicoWorkerEventCollector) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	for index, value := range p {
		if value == '\n' {
			if err := c.consumeLineLocked(); err != nil {
				c.err = err
				return index, err
			}
			continue
		}
		c.line = append(c.line, value)
		if len(c.line) > maxNicoWorkerEventLine {
			c.err = errors.New("niconico worker stdout line exceeds 64 KiB")
			return index + 1, c.err
		}
	}
	return len(p), nil
}

func (c *nicoWorkerEventCollector) consumeLineLocked() error {
	line := bytes.TrimSpace(c.line)
	c.line = c.line[:0]
	if len(line) == 0 {
		return nil
	}
	var event nicoexportworker.Event
	if err := json.Unmarshal(line, &event); err != nil {
		return fmt.Errorf("niconico worker stdout: decode event: %w", err)
	}
	if event.Version != nicoexportworker.ProtocolVersion {
		return fmt.Errorf("niconico worker stdout: unsupported event version %d", event.Version)
	}
	if event.Type != "progress" && event.Type != "result" {
		return fmt.Errorf("niconico worker stdout: unsupported event type %q", event.Type)
	}
	if event.Type == "progress" {
		if c.onProgress != nil {
			c.onProgress(event)
		}
		return nil
	}
	if event.Type == "result" {
		if c.resultSeen {
			return errors.New("niconico worker stdout: multiple result events")
		}
		copy := event
		c.result = &copy
		c.resultSeen = true
	}
	return nil
}

func (c *nicoWorkerEventCollector) event() (nicoexportworker.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nicoexportworker.Event{}, c.err
	}
	if len(bytes.TrimSpace(c.line)) != 0 {
		return nicoexportworker.Event{}, errors.New("niconico worker stdout: unterminated event line")
	}
	if c.result == nil {
		return nicoexportworker.Event{}, errors.New("niconico worker stdout: result event is missing")
	}
	return *c.result, nil
}

func parseNicoWorkerEvents(output string) (nicoexportworker.Event, error) {
	collector := &nicoWorkerEventCollector{}
	if _, err := io.Copy(collector, strings.NewReader(output)); err != nil {
		return nicoexportworker.Event{}, err
	}
	return collector.event()
}

type nicoWorkerStderrTail struct {
	mu   sync.Mutex
	data []byte
}

func (t *nicoWorkerStderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if len(t.data) > maxNicoWorkerEventLine {
		t.data = append([]byte(nil), t.data[len(t.data)-maxNicoWorkerEventLine:]...)
	}
	return len(p), nil
}

func (t *nicoWorkerStderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(append([]byte(nil), t.data...))
}

func runNicoWorkerWithBudget(ctx context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := request.Validate(); err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
	}
	executable, err := nicoWorkerExecutable()
	if err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, fmt.Errorf("niconico worker executable: %w", err)
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
	}
	collector := newNicoWorkerEventCollector(ctx, request)
	stderr := &nicoWorkerStderrTail{}
	report, runErr := nicoexportbudget.Run(ctx, nicoexportbudget.ProcessSpec{
		Exe:    executable,
		Args:   []string{"nico-export-worker"},
		Dir:    filepath.Dir(executable),
		Stdin:  bytes.NewReader(append(requestData, '\n')),
		Stdout: collector,
		Stderr: stderr,
	}, nicoexportbudget.Options{Percent: 20})
	if runErr != nil {
		if result, parseErr := collector.event(); parseErr == nil && result.Error != "" {
			return result, report, fmt.Errorf("%w: %s", runErr, result.Error)
		}
		if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
			return nicoexportworker.Event{}, report, fmt.Errorf("%w: %s", runErr, diagnostic)
		}
		return nicoexportworker.Event{}, report, runErr
	}
	if !report.Verified {
		return nicoexportworker.Event{}, report, fmt.Errorf("niconico worker CPU budget unverified: %s", report.Reason)
	}
	result, err := collector.event()
	if err != nil {
		return nicoexportworker.Event{}, report, err
	}
	if err := validateNicoWorkerResult(request, result); err != nil {
		return result, report, err
	}
	return result, report, nil
}

func validateNicoWorkerResult(request nicoexportworker.Request, result nicoexportworker.Event) error {
	if result.Version != nicoexportworker.ProtocolVersion || result.Type != "result" {
		return errors.New("niconico worker result has an invalid protocol envelope")
	}
	if !result.OK {
		if result.Error == "" {
			return errors.New("niconico worker result failed without an error")
		}
		return errors.New(result.Error)
	}
	if result.RunID != request.RunID {
		return fmt.Errorf("niconico worker result run identity mismatch: %q", result.RunID)
	}
	if result.MediaID != request.MediaID {
		return fmt.Errorf("niconico worker result media identity mismatch: %q", result.MediaID)
	}
	if filepath.Clean(result.Output) != filepath.Clean(request.OutputPath) {
		return fmt.Errorf("niconico worker result output mismatch: %q", result.Output)
	}
	if strings.TrimSpace(result.Playlist) == "" {
		return errors.New("niconico worker result playlist is missing")
	}
	if request.HLSStagingDir != "" {
		relative, err := filepath.Rel(request.HLSStagingDir, result.Playlist)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return fmt.Errorf("niconico worker result playlist escapes staging: %q", result.Playlist)
		}
	}
	return nil
}

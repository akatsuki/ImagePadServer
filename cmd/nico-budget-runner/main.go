package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"imagepadserver/internal/nicoexportbudget"
)

const (
	recordSchemaVersion = 1
	stderrLimit         = 64 * 1024
)

type runRecord struct {
	SchemaVersion int                      `json:"schema_version"`
	StartedAt     time.Time                `json:"started_at"`
	FinishedAt    time.Time                `json:"finished_at"`
	Argv          []string                 `json:"argv"`
	Budget        nicoexportbudget.Options `json:"budget"`
	Status        string                   `json:"status"`
	Report        nicoexportbudget.Report  `json:"report"`
	Error         string                   `json:"error,omitempty"`
	StderrTail    string                   `json:"stderr_tail,omitempty"`
}

type tailWriter struct {
	mu   sync.Mutex
	dst  io.Writer
	max  int
	data []byte
}

var errLineTooLong = errors.New("child stdout JSON line exceeds 64 KiB")

type lineLimitWriter struct {
	mu        sync.Mutex
	dst       io.Writer
	max       int
	lineBytes int
}

func (w *lineLimitWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, b := range p {
		if b == '\n' {
			w.lineBytes = 0
			continue
		}
		w.lineBytes++
		if w.max > 0 && w.lineBytes > w.max {
			return i, errLineTooLong
		}
	}
	if w.dst == nil {
		return len(p), nil
	}
	return w.dst.Write(p)
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dst != nil {
		if _, err := w.dst.Write(p); err != nil {
			return 0, err
		}
	}
	if w.max > 0 {
		w.data = append(w.data, p...)
		if len(w.data) > w.max {
			w.data = append([]byte(nil), w.data[len(w.data)-w.max:]...)
		}
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(append([]byte(nil), w.data...))
}

func main() {
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cpuPercent := fs.Uint("cpu-percent", 20, "Windows Job Object CPU hard cap in percent")
	sampleMillis := fs.Uint("sample-ms", 250, "CPU accounting sample interval in milliseconds")
	recordPath := fs.String("record", "", "JSON report path")
	workingDir := fs.String("dir", "", "working directory for the child process")
	closeStdin := fs.Bool("close-stdin", false, "close child stdin instead of inheriting runner input")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	argv := fs.Args()
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if *recordPath == "" || len(argv) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: nico-budget-runner --cpu-percent 20 --record <path> -- <exe> <args...>")
		os.Exit(2)
	}

	started := time.Now().UTC()
	budget := nicoexportbudget.Options{Percent: uint32(*cpuPercent), SampleInterval: time.Duration(*sampleMillis) * time.Millisecond}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stderr := &tailWriter{dst: os.Stderr, max: stderrLimit}
	stdout := &lineLimitWriter{dst: os.Stdout, max: stderrLimit}
	report, runErr := nicoexportbudget.Run(ctx, budgetProcessSpec(argv, *workingDir, stdout, stderr, *closeStdin), budget)

	record := runRecord{
		SchemaVersion: recordSchemaVersion,
		StartedAt:     started,
		FinishedAt:    time.Now().UTC(),
		Argv:          append([]string(nil), argv...),
		Budget:        budget,
		Status:        "complete",
		Report:        report,
		StderrTail:    stderr.String(),
	}
	if runErr != nil {
		record.Status = "failed"
		record.Error = runErr.Error()
		if errors.Is(runErr, context.Canceled) {
			record.Status = "canceled"
		}
	}
	if err := writeRecord(*recordPath, record); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		os.Exit(2)
	}
	if runErr != nil {
		os.Exit(1)
	}
}

func budgetProcessSpec(argv []string, workingDir string, stdout, stderr io.Writer, closeStdin bool) nicoexportbudget.ProcessSpec {
	spec := nicoexportbudget.ProcessSpec{Dir: workingDir, Stdin: os.Stdin, Stdout: stdout, Stderr: stderr}
	if closeStdin {
		spec.Stdin = nil
	}
	if len(argv) > 0 {
		spec.Exe = argv[0]
	}
	if len(argv) > 1 {
		spec.Args = append([]string(nil), argv[1:]...)
	}
	return spec
}

func writeRecord(path string, record runRecord) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("record path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nico-budget-report-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmpPath, path)
}

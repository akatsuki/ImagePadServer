package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"imagepadserver/internal/airplay/iphonemodel"
)

type controlResult struct {
	Schema         int    `json:"schema"`
	Layer          string `json:"layer"`
	Status         string `json:"status"`
	Seed           uint64 `json:"seed"`
	Sequence       string `json:"sequence"`
	Repeat         int    `json:"repeat"`
	FragmentSize   int    `json:"fragmentSize"`
	RequestsSent   int    `json:"requestsSent"`
	Responses      int    `json:"responses"`
	LastStatus     int    `json:"lastStatus"`
	DurationMillis int64  `json:"durationMillis"`
	Error          string `json:"error,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("airplay-iphone-model", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var address, outPath, sequence string
	var repeat, fragmentSize int
	var seed uint64
	var timeout, reconnectDelay time.Duration
	flags.StringVar(&address, "address", "", "loopback UxPlay RTSP address")
	flags.StringVar(&sequence, "sequence", "setup", "control sequence: setup or full")
	flags.IntVar(&repeat, "repeat", 1, "fresh connection iterations")
	flags.Uint64Var(&seed, "seed", 1, "deterministic scenario seed")
	flags.IntVar(&fragmentSize, "fragment-size", 0, "maximum bytes per TCP write; zero writes whole requests")
	flags.DurationVar(&timeout, "timeout", 2*time.Second, "per-request timeout")
	flags.DurationVar(&reconnectDelay, "reconnect-delay", 10*time.Millisecond, "delay between fresh connections")
	flags.StringVar(&outPath, "out", "", "optional result JSON path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if address == "" {
		return errors.New("address is required")
	}

	result := controlResult{
		Schema:       1,
		Layer:        "uxplay-control",
		Status:       "FAIL",
		Seed:         seed,
		Sequence:     sequence,
		Repeat:       repeat,
		FragmentSize: fragmentSize,
	}
	report, runErr := iphonemodel.RunControlSequence(context.Background(), address, iphonemodel.RunOptions{
		Repeat:              repeat,
		Seed:                seed,
		Sequence:            sequence,
		FragmentSize:        fragmentSize,
		Timeout:             timeout,
		InterIterationDelay: reconnectDelay,
	})
	result.RequestsSent = report.RequestsSent
	result.Responses = report.Responses
	result.LastStatus = report.LastStatus
	result.DurationMillis = report.Duration.Milliseconds()
	if runErr == nil {
		result.Status = "PASS"
	} else {
		result.Error = runErr.Error()
	}
	if err := emitResult(stdout, outPath, result); err != nil {
		return err
	}
	return runErr
}

func emitResult(stdout io.Writer, outPath string, result controlResult) error {
	compact, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode compact result: %w", err)
	}
	if _, err := fmt.Fprintln(stdout, string(compact)); err != nil {
		return fmt.Errorf("write result stdout: %w", err)
	}
	if outPath == "" {
		return nil
	}
	pretty, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result file: %w", err)
	}
	pretty = append(pretty, '\n')
	if err := writeFileAtomic(outPath, pretty, 0o600); err != nil {
		return fmt.Errorf("write result file: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".airplay-iphone-model-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestLineLimitWriterRejectsOversizedJSONLine(t *testing.T) {
	var dst bytes.Buffer
	writer := &lineLimitWriter{dst: &dst, max: 8}
	_, err := writer.Write([]byte(strings.Repeat("x", 9)))
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("Write error = %v, want errLineTooLong", err)
	}
}

func TestBudgetProcessSpecForwardsStdinByDefault(t *testing.T) {
	spec := budgetProcessSpec([]string{"worker.exe", "nico-export-worker"}, "", os.Stdout, os.Stderr, false)
	if spec.Stdin != os.Stdin {
		t.Fatalf("stdin = %#v, want os.Stdin", spec.Stdin)
	}
}

func TestBudgetProcessSpecCanCloseInheritedStdin(t *testing.T) {
	spec := budgetProcessSpec([]string{"test-child.exe"}, "", nil, nil, true)
	if spec.Stdin != nil {
		t.Fatal("child stdin should be closed when explicitly requested")
	}
}

func TestTailWriterKeepsOnlyTheLastBytes(t *testing.T) {
	var dst bytes.Buffer
	writer := &tailWriter{dst: &dst, max: 4}
	if _, err := writer.Write([]byte("abcdef")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, want := writer.String(), "cdef"; got != want {
		t.Fatalf("tail = %q, want %q", got, want)
	}
}

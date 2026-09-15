package airplay

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
)

func TestSourceClockExitStatusHelperProcess(t *testing.T) {
	raw := os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_EXIT_CODE")
	if raw == "" {
		return
	}
	code, err := strconv.ParseUint(raw, 0, 32)
	if err != nil {
		os.Exit(99)
	}
	os.Exit(int(code))
}

func TestSourceClockProcessExitFromError(t *testing.T) {
	if got := sourceClockProcessExitFromError(nil); got != (sourceClockProcessExit{Code: 0, Known: true}) {
		t.Fatalf("nil exit = %+v", got)
	}
	if got := sourceClockProcessExitFromError(errors.New("start failed")); got.Known || got.Crashed {
		t.Fatalf("non-exit error became a known exit: %+v", got)
	}
	if got := sourceClockProcessExitFromError(&exec.ExitError{}); got.Known || got.Crashed {
		t.Fatalf("exit error without process state became a known exit: %+v", got)
	}

	tests := []struct {
		name        string
		code        uint32
		wantCrash   bool
		windowsOnly bool
	}{
		{name: "pipeline error", code: 22},
		{name: "unknown ordinary exit", code: 37},
		{name: "access violation", code: 0xc0000005, wantCrash: true, windowsOnly: true},
		{name: "heap corruption", code: 0xc0000374, wantCrash: true, windowsOnly: true},
		{name: "unknown high code", code: 0xc0001234, windowsOnly: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.windowsOnly && runtime.GOOS != "windows" {
				t.Skip("Windows preserves the full 32-bit process exit code")
			}
			cmd := exec.Command(os.Args[0], "-test.run=TestSourceClockExitStatusHelperProcess$")
			cmd.Env = append(os.Environ(), "IMAGEPAD_TEST_SOURCE_CLOCK_EXIT_CODE="+strconv.FormatUint(uint64(test.code), 10))
			err := cmd.Run()
			if err == nil {
				t.Fatal("helper unexpectedly succeeded")
			}
			got := sourceClockProcessExitFromError(err)
			if !got.Known || got.Code != int(test.code) || got.Crashed != test.wantCrash {
				t.Fatalf("exit status = %+v, want code=%d crashed=%t known=true", got, test.code, test.wantCrash)
			}
			wrapped := sourceClockProcessExitFromError(fmt.Errorf("wrapped child exit: %w", err))
			if wrapped != got {
				t.Fatalf("wrapped exit status = %+v, want %+v", wrapped, got)
			}
		})
	}
}

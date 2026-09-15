//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package airplay

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSourceClockSignalExitHelperProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_TEST_SOURCE_CLOCK_SIGNAL_EXIT") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestSourceClockProcessExitRecognizesUnixSignal(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSourceClockSignalExitHelperProcess$")
	cmd.Env = append(os.Environ(), "IMAGEPAD_TEST_SOURCE_CLOCK_SIGNAL_EXIT=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	if err == nil {
		t.Fatal("signal-killed helper unexpectedly succeeded")
	}
	got := sourceClockProcessExitFromError(err)
	if !got.Known || !got.Crashed {
		t.Fatalf("signal exit was not recognized as a crash: %+v", got)
	}
}

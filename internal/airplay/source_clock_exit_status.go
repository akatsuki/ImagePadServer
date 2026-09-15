package airplay

import (
	"errors"
	"os"
	"os/exec"
)

type sourceClockProcessExit struct {
	Code    int
	Crashed bool
	Known   bool
}

func sourceClockProcessExitFromError(err error) sourceClockProcessExit {
	if err == nil {
		return sourceClockProcessExit{Code: 0, Known: true}
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil {
		return sourceClockProcessExit{}
	}
	return sourceClockProcessExit{
		Code:    exitErr.ExitCode(),
		Crashed: sourceClockProcessStateCrashed(exitErr.ProcessState),
		Known:   true,
	}
}

func sourceClockPublisherExitDecision(err error) (sourceClockProcessExit, PublisherExitDecision) {
	exit := sourceClockProcessExitFromError(err)
	if !exit.Known {
		return exit, PublisherExitDecision{Reason: "unclassified-exit"}
	}
	return exit, classifySourceClockExit(exit.Code, exit.Crashed)
}

func sourceClockProcessStateCrashed(state *os.ProcessState) bool {
	if state == nil {
		return false
	}
	if status, ok := state.Sys().(interface{ Signaled() bool }); ok && status.Signaled() {
		return true
	}
	// Windows does not expose exception termination as a signal. Keep this
	// list deliberately narrow: these are the two native failures observed in
	// the source-clock receiver history. Unknown high exit codes must remain
	// unclassified instead of becoming automatic restart authority.
	switch uint32(state.ExitCode()) {
	case 0xc0000005, // access violation
		0xc0000374: // heap corruption
		return true
	default:
		return false
	}
}

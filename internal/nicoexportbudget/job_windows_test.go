//go:build windows

package nicoexportbudget

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestRunRejectsInvalidCPUPercentBeforeStartingProcess(t *testing.T) {
	for _, percent := range []uint32{0, 101} {
		t.Run(string(rune('0'+percent%10)), func(t *testing.T) {
			_, err := Run(context.Background(), ProcessSpec{Exe: filepath.Join(t.TempDir(), "does-not-run.exe")}, Options{Percent: percent})
			if err == nil {
				t.Fatalf("Run(%d) returned nil error", percent)
			}
		})
	}
}

func TestRunCapturesOutputAndCPUAccounting(t *testing.T) {
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	report, err := Run(ctx, ProcessSpec{
		Exe:    os.Getenv("ComSpec"),
		Args:   []string{"/d", "/c", "echo ready & %ComSpec% /d /c echo child"},
		Stdout: &stdout,
		Stderr: &stderr,
	}, Options{Percent: 20, SampleInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v; report=%+v; stderr=%s", err, report, stderr.String())
	}
	if !report.Verified {
		t.Fatalf("report not verified: %+v", report)
	}
	if report.LogicalCPUs == 0 || report.JobCPURate != 2000 {
		t.Fatalf("budget evidence = logical_cpus:%d job_cpu_rate:%d, want logical CPU count and 2000", report.LogicalCPUs, report.JobCPURate)
	}
	if report.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", report.ExitCode)
	}
	if !strings.Contains(stdout.String(), "ready") || !strings.Contains(stdout.String(), "child") {
		t.Fatalf("stdout = %q, want command output", stdout.String())
	}
	if len(report.Samples) == 0 {
		t.Fatalf("report has no CPU samples: %+v", report)
	}
	if report.CPUSeconds < 0 {
		t.Fatalf("CPUSeconds = %v, want non-negative", report.CPUSeconds)
	}
}

func TestRunWaitsForStdoutCopyBeforeClosingPipe(t *testing.T) {
	stdout := &delayedCapture{delay: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	report, err := Run(ctx, ProcessSpec{
		Exe:    os.Args[0],
		Args:   []string{"-test.run=TestNicoBudgetCPUHelper"},
		Env:    append(os.Environ(), "NICO_BUDGET_HELPER=1", "NICO_BUDGET_OUTPUT=1"),
		Stdout: stdout,
		Stderr: io.Discard,
	}, Options{Percent: 20, SampleInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v report=%+v", err, report)
	}
	wantBytes := 32*(32*1024+1) + len("PASS\n")
	if stdout.buffer.Len() != wantBytes {
		t.Fatalf("captured stdout bytes=%d, want %d", stdout.buffer.Len(), wantBytes)
	}
}

type delayedCapture struct {
	delay  time.Duration
	buffer bytes.Buffer
}

func (w *delayedCapture) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	return w.buffer.Write(p)
}

func TestRunCancellationTerminatesOwnedJobAndReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, ProcessSpec{
			Exe:    os.Getenv("ComSpec"),
			Args:   []string{"/d", "/c", "ping 127.0.0.1 -n 30 > nul"},
			Stdout: &stdout,
			Stderr: &stderr,
		}, Options{Percent: 20, SampleInterval: 50 * time.Millisecond})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRunTracksParentAndChildCPU(t *testing.T) {
	if os.Getenv("NICO_BUDGET_HELPER") == "1" {
		if os.Getenv("NICO_BUDGET_CHILD") == "1" {
			busyFor(700 * time.Millisecond)
			return
		}
		child := exec.Command(os.Args[0], "-test.run=TestNicoBudgetCPUHelper")
		child.Env = append(os.Environ(), "NICO_BUDGET_HELPER=1", "NICO_BUDGET_CHILD=1")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		busyFor(700 * time.Millisecond)
		if err := child.Wait(); err != nil {
			t.Fatal(err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := Run(ctx, ProcessSpec{
		Exe:    os.Args[0],
		Args:   []string{"-test.run=TestRunTracksParentAndChildCPU"},
		Env:    append(os.Environ(), "NICO_BUDGET_HELPER=1"),
		Stdout: io.Discard,
		Stderr: io.Discard,
	}, Options{Percent: 20, SampleInterval: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v report=%+v", err, report)
	}
	if len(report.Samples) < 2 || report.CPUSeconds <= 0 {
		t.Fatalf("report=%+v, want multiple CPU samples and positive CPU time", report)
	}
	for _, sample := range report.Samples {
		if sample.CPUPercent > 35 {
			t.Fatalf("sample=%+v, want OS-total CPU near the 20%% hard cap", sample)
		}
	}
	t.Logf("CPU budget report: %+v", report)
}

func TestNicoBudgetCPUHelper(t *testing.T) {
	if os.Getenv("NICO_BUDGET_HELPER") != "1" {
		return
	}
	if os.Getenv("NICO_BUDGET_OUTPUT") == "1" {
		for range 32 {
			_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", 32*1024))
		}
		return
	}
	if os.Getenv("NICO_BUDGET_CHILD") == "1" {
		busyFor(700 * time.Millisecond)
	}
}

func TestCreateSuspendedProcessIsInKillOnCloseJobBeforeResume(t *testing.T) {
	job, err := newBudgetJob(100)
	if err != nil {
		t.Fatal(err)
	}
	proc, err := createSuspendedProcessInJob(job, ownedChildSpec("root"))
	if err != nil {
		_ = windows.CloseHandle(job)
		t.Fatalf("create suspended child in Job: %v", err)
	}
	jobOwned := true
	closeOwnerJob := func() error {
		if proc.jobClosed {
			jobOwned = false
			return nil
		}
		if !jobOwned {
			return nil
		}
		err := windows.CloseHandle(job)
		if err == nil {
			jobOwned = false
		}
		return err
	}
	defer func() {
		_ = closeOwnerJob()
		status, waitErr := windows.WaitForSingleObject(proc.info.Process, 5_000)
		if waitErr != nil || status != windows.WAIT_OBJECT_0 {
			t.Errorf("failure cleanup left suspended helper alive: status=%#x err=%v", status, waitErr)
		}
		proc.close()
	}()
	if !containsPID(mustJobPIDs(t, job), proc.info.ProcessId) {
		t.Fatalf("suspended child PID %d is not assigned before resume", proc.info.ProcessId)
	}
	if err := closeOwnerJob(); err != nil {
		t.Fatal(err)
	}
	status, err := windows.WaitForSingleObject(proc.info.Process, 5_000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("suspended child after owner Job close: status=%#x err=%v", status, err)
	}
	output, err := io.ReadAll(proc.stdout.parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("suspended child wrote before resume: %q", output)
	}
}

func TestJobCloseAfterResumeTerminatesOwnedChildAndDescendant(t *testing.T) {
	job, err := newBudgetJob(100)
	if err != nil {
		t.Fatal(err)
	}
	proc, err := createSuspendedProcessInJob(job, ownedChildSpec("root"))
	if err != nil {
		_ = windows.CloseHandle(job)
		t.Fatalf("create suspended child in Job: %v", err)
	}
	jobOwned := true
	var descendant windows.Handle
	closeOwnerJob := func() error {
		if proc.jobClosed {
			jobOwned = false
			return nil
		}
		if !jobOwned {
			return nil
		}
		err := windows.CloseHandle(job)
		if err == nil {
			jobOwned = false
		}
		return err
	}
	defer func() {
		_ = closeOwnerJob()
		status, waitErr := windows.WaitForSingleObject(proc.info.Process, 5_000)
		if waitErr != nil || status != windows.WAIT_OBJECT_0 {
			t.Errorf("failure cleanup left worker alive: status=%#x err=%v", status, waitErr)
		}
		if descendant != 0 {
			status, waitErr := windows.WaitForSingleObject(descendant, 5_000)
			if waitErr != nil || status != windows.WAIT_OBJECT_0 {
				t.Errorf("failure cleanup left descendant alive: status=%#x err=%v", status, waitErr)
			}
			_ = windows.CloseHandle(descendant)
		}
		proc.close()
	}()
	if !containsPID(mustJobPIDs(t, job), proc.info.ProcessId) {
		t.Fatalf("suspended child PID %d is not assigned before resume", proc.info.ProcessId)
	}
	if err := proc.resume(job); err != nil {
		_ = closeOwnerJob()
		t.Fatalf("resume assigned child: %v", err)
	}
	line, err := bufio.NewReader(proc.stdout.parent).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "READY ") {
		_ = closeOwnerJob()
		t.Fatalf("child readiness = %q, %v", line, err)
	}
	var descendantPID uint32
	if _, err := fmt.Sscanf(line, "READY %d", &descendantPID); err != nil {
		_ = closeOwnerJob()
		t.Fatalf("parse descendant PID from %q: %v", line, err)
	}
	descendant, err = windows.OpenProcess(windows.SYNCHRONIZE, false, descendantPID)
	if err != nil {
		_ = closeOwnerJob()
		t.Fatalf("open descendant process %d: %v", descendantPID, err)
	}
	waitForPIDInJob(t, job, descendantPID)
	if err := windows.CloseHandle(job); err != nil {
		t.Fatal(err)
	}
	jobOwned = false
	for label, handle := range map[string]windows.Handle{"worker": proc.info.Process, "descendant": descendant} {
		status, err := windows.WaitForSingleObject(handle, 5_000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("%s after owner Job close: status=%#x err=%v", label, status, err)
		}
	}
}

func TestStartSessionInsideOuterJobUsesNestedCreationTimeMembership(t *testing.T) {
	outer, err := newBudgetJob(100)
	if err != nil {
		t.Fatal(err)
	}
	helperSpec := ownedChildSpec("nested-session")
	proc, err := createSuspendedProcessInJob(outer, helperSpec)
	if err != nil {
		_ = windows.CloseHandle(outer)
		t.Fatalf("create nested-session helper in outer Job: %v", err)
	}
	outerOwned := true
	var child windows.Handle
	closeOuter := func() error {
		if !outerOwned {
			return nil
		}
		err := windows.CloseHandle(outer)
		if err == nil {
			outerOwned = false
		}
		return err
	}
	defer func() {
		_ = closeOuter()
		if proc.info.Process != 0 {
			status, waitErr := windows.WaitForSingleObject(proc.info.Process, 5_000)
			if waitErr != nil || status != windows.WAIT_OBJECT_0 {
				t.Errorf("outer helper remained alive after cleanup: status=%#x err=%v", status, waitErr)
			}
		}
		if child != 0 {
			status, waitErr := windows.WaitForSingleObject(child, 5_000)
			if waitErr != nil || status != windows.WAIT_OBJECT_0 {
				t.Errorf("nested session child remained alive after cleanup: status=%#x err=%v", status, waitErr)
			}
			_ = windows.CloseHandle(child)
		}
		proc.close()
	}()
	if !containsPID(mustJobPIDs(t, outer), proc.info.ProcessId) {
		t.Fatalf("outer helper PID %d missing before resume", proc.info.ProcessId)
	}
	if err := proc.resume(outer); err != nil {
		t.Fatalf("resume outer helper: %v", err)
	}
	lineCh := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := bufio.NewReader(proc.stdout.parent).ReadString('\n')
		lineCh <- struct {
			line string
			err  error
		}{line, err}
	}()
	var line string
	select {
	case result := <-lineCh:
		line = result.line
		if result.err != nil {
			t.Fatalf("read nested session identity: %v (line %q)", result.err, line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("outer helper did not report nested StartSession result within 10 seconds")
	}
	var childPID uint32
	if strings.HasPrefix(line, "NESTED_ERROR ") {
		t.Fatalf("nested Job StartSession rejected by this Windows/host Job configuration: %s", strings.TrimSpace(strings.TrimPrefix(line, "NESTED_ERROR ")))
	}
	if _, err := fmt.Sscanf(line, "NESTED %d", &childPID); err != nil || childPID == 0 {
		t.Fatalf("invalid nested child report %q: %v", line, err)
	}
	child, err = windows.OpenProcess(windows.SYNCHRONIZE, false, childPID)
	if err != nil {
		t.Fatalf("open nested session child %d: %v", childPID, err)
	}
	if !containsPID(mustJobPIDs(t, outer), proc.info.ProcessId) || !containsPID(mustJobPIDs(t, outer), childPID) {
		t.Fatalf("outer Job does not contain helper and nested session child: helper=%d child=%d pids=%v", proc.info.ProcessId, childPID, mustJobPIDs(t, outer))
	}
	if err := closeOuter(); err != nil {
		t.Fatalf("close outer Job: %v", err)
	}
	for label, handle := range map[string]windows.Handle{"outer helper": proc.info.Process, "nested session child": child} {
		status, err := windows.WaitForSingleObject(handle, 5_000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("%s after outer Job close: status=%#x err=%v", label, status, err)
		}
	}
}

func TestSessionTerminationFailureClosesOwnedJobFallback(t *testing.T) {
	termErr := errors.New("injected TerminateJobObject failure")
	ops := defaultWindowsSessionOps()
	ops.terminateJob = func(windows.Handle, uint32) error { return termErr }
	var closeCount atomic.Int32
	ops.closeJob = func(h windows.Handle) error { closeCount.Add(1); return windows.CloseHandle(h) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, backend, err := startSessionWithOps(ctx, ownedChildSpec("root"), Options{Percent: 100, SampleInterval: 10 * time.Millisecond}, ops)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSessionTest(t, p, backend)
	root := openSessionRoot(t, backend)
	defer windows.CloseHandle(root)
	if err := p.Close(); !errors.Is(err, termErr) {
		t.Fatalf("Close error = %v, want original termination error", err)
	}
	report, err := p.Wait()
	if !errors.Is(err, termErr) || strings.Contains(fmt.Sprint(err), "tree termination unverified") {
		t.Fatalf("Wait = report:%+v err:%v; fallback close should enforce termination while retaining original error", report, err)
	}
	if closeCount.Load() != 1 {
		t.Fatalf("owner Job close count=%d, want exactly one", closeCount.Load())
	}
	if err := p.Close(); !errors.Is(err, termErr) {
		t.Fatalf("repeated Close error=%v, want cached error", err)
	}
	if status, err := windows.WaitForSingleObject(root, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("root after Job fallback: status=%#x err=%v", status, err)
	}
}

func TestSessionDoubleJobFailureAndDeadlineReportsUnverifiedTree(t *testing.T) {
	termErr, closeErr, killErr := errors.New("injected job termination error"), errors.New("injected owner close error"), errors.New("injected root kill error")
	ops := defaultWindowsSessionOps()
	ops.teardownTimeout = 150 * time.Millisecond
	ops.terminateJob = func(windows.Handle, uint32) error { return termErr }
	ops.closeJob = func(windows.Handle) error { return closeErr }
	ops.terminateProcess = func(windows.Handle, uint32) error { return killErr }
	ops.waitProcess = func(windows.Handle, time.Duration) (uint32, error) { return uint32(windows.WAIT_TIMEOUT), nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	writer := &notifyingCaptureWriter{notify: make(chan string, 8)}
	spec := ownedChildSpec("root")
	spec.Stdout = writer
	p, backend, err := startSessionWithOps(ctx, spec, Options{Percent: 100, SampleInterval: 10 * time.Millisecond}, ops)
	if err != nil {
		t.Fatal(err)
	}
	var ownedProcessHandles []windows.Handle
	defer func() {
		_ = p.Close()
		_, _ = p.Wait()
		// The injected close hook deliberately failed. Close the still-owned real Job
		// as test recovery, without querying the Job after production retired it.
		backend.handleMu.Lock()
		if backend.job != 0 {
			if closeErr := windows.CloseHandle(backend.job); closeErr != nil {
				t.Errorf("close real owner Job during fixture cleanup: %v", closeErr)
			}
			backend.job = 0
		}
		backend.jobRetired = true
		backend.handleMu.Unlock()
		for _, handle := range ownedProcessHandles {
			status, waitErr := windows.WaitForSingleObject(handle, 5_000)
			if waitErr != nil || status != windows.WAIT_OBJECT_0 {
				t.Errorf("owned helper process was not signaled after Job cleanup: status=%#x err=%v", status, waitErr)
			}
			if closeErr := windows.CloseHandle(handle); closeErr != nil {
				t.Errorf("close retained helper process handle: %v", closeErr)
			}
		}
	}()
	root := openSessionRoot(t, backend)
	ownedProcessHandles = append(ownedProcessHandles, root)
	var descendantPID uint32
	select {
	case line := <-writer.notify:
		if _, err := fmt.Sscanf(line, "READY %d", &descendantPID); err != nil {
			t.Fatalf("parse owned descendant PID from %q: %v", line, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owned helper did not report its descendant PID")
	}
	descendant, err := windows.OpenProcess(windows.SYNCHRONIZE, false, descendantPID)
	if err != nil {
		t.Fatalf("open owned descendant %d: %v", descendantPID, err)
	}
	ownedProcessHandles = append(ownedProcessHandles, descendant)
	start := time.Now()
	_ = p.Close()
	report, err := p.Wait()
	if err == nil || !strings.Contains(err.Error(), "tree termination unverified") {
		t.Fatalf("Wait = report:%+v err:%v, want explicit tree-unverified error", report, err)
	}
	for _, want := range []error{termErr, closeErr, killErr} {
		if !errors.Is(err, want) {
			t.Fatalf("Wait error %v does not retain %v", err, want)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Wait exceeded one absolute teardown deadline: %s", elapsed)
	}
}

func TestSessionBlockedWriterReturnsIncompleteDrainAndCachesWait(t *testing.T) {
	writer := &gatedBlockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	ops := defaultWindowsSessionOps()
	ops.teardownTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spec := ownedChildSpec("root")
	spec.Stdout = writer
	p, backend, err := startSessionWithOps(ctx, spec, Options{Percent: 100, SampleInterval: 10 * time.Millisecond}, ops)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSessionTest(t, p, backend)
	select {
	case <-writer.started:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture did not reach blocking writer")
	}
	_ = p.Close()
	start := time.Now()
	first, firstErr := p.Wait()
	elapsed := time.Since(start)
	if firstErr == nil || !strings.Contains(firstErr.Error(), "pipe drain incomplete") {
		t.Fatalf("Wait = report:%+v err:%v, want incomplete drain", first, firstErr)
	}
	if elapsed > time.Second {
		t.Fatalf("blocked writer exceeded deadline: %s", elapsed)
	}
	second, secondErr := p.Wait()
	if !errors.Is(secondErr, firstErr) && fmt.Sprint(secondErr) != fmt.Sprint(firstErr) {
		t.Fatalf("repeated Wait error changed: first=%v second=%v", firstErr, secondErr)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated Wait report changed: first=%+v second=%+v", first, second)
	}
	close(writer.release)
}

func TestSessionCloseReapsDescendantHoldingOutputPipe(t *testing.T) {
	writer := &notifyingCaptureWriter{notify: make(chan string, 8)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spec := ownedChildSpec("root")
	spec.Stdout = writer
	p, backend, err := startSessionWithOps(ctx, spec, Options{Percent: 100, SampleInterval: 10 * time.Millisecond}, defaultWindowsSessionOps())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSessionTest(t, p, backend)
	var descendantPID uint32
	deadline := time.After(5 * time.Second)
	for descendantPID == 0 {
		select {
		case line := <-writer.notify:
			if _, err := fmt.Sscanf(line, "READY %d", &descendantPID); err != nil {
				t.Fatalf("parse READY line %q: %v", line, err)
			}
		case <-deadline:
			t.Fatal("descendant did not hold the inherited output pipe")
		}
	}
	descendant, err := windows.OpenProcess(windows.SYNCHRONIZE, false, descendantPID)
	if err != nil {
		t.Fatalf("open descendant %d: %v", descendantPID, err)
	}
	defer windows.CloseHandle(descendant)
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	report, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait after tree enforcement: report:%+v err:%v", report, err)
	}
	if status, err := windows.WaitForSingleObject(descendant, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant after Close: status=%#x err=%v", status, err)
	}
}

func TestSessionNaturalRootExitDrainsInheritedPipeAfterJobTeardown(t *testing.T) {
	output := &notifyingCaptureWriter{notify: make(chan string, 8)}
	blocked := &gatedBlockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	ops := defaultWindowsSessionOps()
	ops.teardownTimeout = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spec := ownedChildSpec("natural-root-exit")
	spec.Stdout = io.MultiWriter(output, blocked)
	p, backend, err := startSessionWithOps(ctx, spec, Options{Percent: 100, SampleInterval: 10 * time.Millisecond}, ops)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		releaseWriter()
		cleanupSessionTest(t, p, backend)
	}()

	var descendantPID uint32
	select {
	case line := <-output.notify:
		if _, err := fmt.Sscanf(line, "READY %d", &descendantPID); err != nil {
			t.Fatalf("parse READY line %q: %v", line, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("natural-exit fixture did not report its descendant")
	}
	descendant, err := windows.OpenProcess(windows.SYNCHRONIZE, false, descendantPID)
	if err != nil {
		t.Fatalf("open descendant %d: %v", descendantPID, err)
	}
	defer windows.CloseHandle(descendant)
	select {
	case <-blocked.started:
	case <-time.After(2 * time.Second):
		t.Fatal("caller-owned stdout writer did not block")
	}
	rootStatus, err := windows.WaitForSingleObject(backend.proc.info.Process, 2_000)
	if err != nil || rootStatus != windows.WAIT_OBJECT_0 {
		t.Fatalf("session root did not exit naturally: status=%#x err=%v", rootStatus, err)
	}

	// Release only after the teardown deadline so the test exercises the
	// bounded drain after Job termination instead of the original deadline.
	releaseTimer := time.AfterFunc(400*time.Millisecond, releaseWriter)
	defer releaseTimer.Stop()
	type waitResult struct {
		report Report
		err    error
	}
	waited := make(chan waitResult, 1)
	go func() {
		report, err := p.Wait()
		waited <- waitResult{report: report, err: err}
	}()
	select {
	case result := <-waited:
		if result.err != nil {
			t.Fatalf("Wait after natural root exit and Job teardown: report=%+v err=%v", result.report, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait exceeded the bounded post-termination pipe-drain period")
	}
	if status, err := windows.WaitForSingleObject(descendant, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant after Job teardown: status=%#x err=%v", status, err)
	}
}

func TestSessionCloseWaitSnapshotAndSamplerOverlap(t *testing.T) {
	ops := defaultWindowsSessionOps()
	ops.teardownTimeout = 2 * time.Second
	queryStarted, releaseQuery := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defaultQuery := ops.queryCPU
	ops.queryCPU = func(job windows.Handle) (float64, error) {
		once.Do(func() { close(queryStarted); <-releaseQuery })
		return defaultQuery(job)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, backend, err := startSessionWithOps(ctx, ownedChildSpec("root"), Options{Percent: 100, SampleInterval: 5 * time.Millisecond}, ops)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSessionTest(t, p, backend)
	select {
	case <-queryStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("sampler did not enter injected query gate")
	}
	closeDone := make(chan error, 1)
	waitDone := make(chan error, 1)
	snapshotDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()
	go func() { _, err := p.Wait(); waitDone <- err }()
	go func() { _, err := p.Snapshot(); snapshotDone <- err }()
	close(releaseQuery)
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish")
	}
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not finish")
	}
	select {
	case <-snapshotDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Snapshot did not finish")
	}
	first, firstErr := p.Wait()
	second, secondErr := p.Wait()
	if fmt.Sprint(firstErr) != fmt.Sprint(secondErr) || !reflect.DeepEqual(first, second) {
		t.Fatalf("Wait result was not cached: (%+v,%v) vs (%+v,%v)", first, firstErr, second, secondErr)
	}
}

type gatedBlockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *gatedBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

type notifyingCaptureWriter struct {
	notify  chan string
	mu      sync.Mutex
	partial string
}

func (w *notifyingCaptureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial += string(p)
	for {
		i := strings.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		line := w.partial[:i+1]
		w.partial = w.partial[i+1:]
		select {
		case w.notify <- line:
		default:
		}
	}
	return len(p), nil
}

func openSessionRoot(t *testing.T, b *windowsSessionBackend) windows.Handle {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, b.proc.info.ProcessId)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func cleanupSessionTest(t *testing.T, p *SessionProcess, b *windowsSessionBackend) {
	t.Helper()
	_ = p.Close()
	_, _ = p.Wait()
	b.handleMu.Lock()
	if b.job != 0 {
		_ = windows.CloseHandle(b.job)
		b.job = 0
	}
	b.jobRetired = true
	b.handleMu.Unlock()
	if b.proc.info.Process != 0 {
		_, _ = windows.WaitForSingleObject(b.proc.info.Process, 5_000)
	}
}

func TestCreateSuspendedProcessCleansUpAttributeAndCreateFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func(*windowsProcessLaunchHooks)
	}{
		{name: "attribute update", hook: func(h *windowsProcessLaunchHooks) {
			h.updateAttribute = func(*windows.ProcThreadAttributeListContainer, uintptr, unsafe.Pointer, uintptr) error {
				return errors.New("injected attribute failure")
			}
		}},
		{name: "CreateProcess", hook: func(h *windowsProcessLaunchHooks) {
			h.createProcess = func(*uint16, *uint16, *windows.SecurityAttributes, *windows.SecurityAttributes, bool, uint32, *uint16, *uint16, *windows.StartupInfo, *windows.ProcessInformation) error {
				return errors.New("injected CreateProcess failure")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, err := newBudgetJob(100)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(job)
			hooks := defaultWindowsProcessLaunchHooks()
			tc.hook(&hooks)
			proc, err := createSuspendedProcessInJobWithHooks(job, ownedChildSpec("root"), hooks)
			if err == nil || proc != nil {
				t.Fatalf("creation = (%v, %v), want injected error and no child", proc, err)
			}
			if pids := mustJobPIDs(t, job); len(pids) != 0 {
				t.Fatalf("Job retained child PIDs after %s failure: %v", tc.name, pids)
			}
		})
	}
}

func TestResumeFailureTerminatesAndReapsAssignedChild(t *testing.T) {
	job, err := newBudgetJob(100)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	proc, err := createSuspendedProcessInJob(job, ownedChildSpec("root"))
	if err != nil {
		t.Fatal(err)
	}
	defer proc.close()
	hooks := defaultWindowsProcessLaunchHooks()
	hooks.resumeThread = func(windows.Handle) (uint32, error) { return 0, errors.New("injected ResumeThread failure") }
	if err := proc.resumeWithHooks(job, hooks); err == nil {
		t.Fatal("resume unexpectedly succeeded")
	}
	status, err := windows.WaitForSingleObject(proc.info.Process, 5_000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("process after resume failure: status=%#x err=%v", status, err)
	}
	if pids := mustJobPIDs(t, job); len(pids) != 0 {
		t.Fatalf("Job retained child PIDs after resume failure: %v", pids)
	}
}

func TestResumeFailureClosesOwnerJobWhenTerminationFails(t *testing.T) {
	job, err := newBudgetJob(100)
	if err != nil {
		t.Fatal(err)
	}
	jobOwned := true
	proc, err := createSuspendedProcessInJob(job, ownedChildSpec("root"))
	if err != nil {
		_ = windows.CloseHandle(job)
		t.Fatal(err)
	}
	defer func() {
		if jobOwned {
			_ = windows.CloseHandle(job)
		}
		proc.close()
	}()
	hooks := defaultWindowsProcessLaunchHooks()
	termErr := errors.New("injected TerminateJobObject failure")
	hooks.resumeThread = func(windows.Handle) (uint32, error) { return 0, errors.New("injected ResumeThread failure") }
	hooks.terminateJob = func(windows.Handle, uint32) error { return termErr }
	hooks.closeJob = func(handle windows.Handle) error {
		err := windows.CloseHandle(handle)
		if err == nil {
			jobOwned = false
		}
		return err
	}
	if err := proc.resumeWithHooks(job, hooks); !errors.Is(err, termErr) {
		t.Fatalf("resume error=%v, want joined termination error", err)
	}
	if !proc.jobClosed || jobOwned {
		t.Fatal("resume cleanup did not close the owner Job")
	}
	status, err := windows.WaitForSingleObject(proc.info.Process, 5_000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("process after owner Job close: status=%#x err=%v", status, err)
	}
}

func TestStartSessionHonorsFullAndTestCPUPercent(t *testing.T) {
	for _, percent := range []uint32{100, 20} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p, err := StartSession(ctx, ProcessSpec{
			Exe: os.Args[0], Args: []string{"-test.run=^TestNicoBudgetSessionHelper$"},
			Env: append(os.Environ(), "NICO_BUDGET_SESSION_HELPER=1"), Stdout: io.Discard, Stderr: io.Discard,
		}, Options{Percent: percent, SampleInterval: 10 * time.Millisecond})
		if err != nil {
			cancel()
			t.Fatalf("StartSession(%d): %v", percent, err)
		}
		defer func() { _ = p.Close(); _, _ = p.Wait(); cancel() }()
		report, err := p.Wait()
		if err != nil {
			t.Fatalf("Wait(%d): report=%+v err=%v", percent, report, err)
		}
		if !report.Verified || report.JobCPURate != percent*100 {
			t.Fatalf("StartSession(%d) report = %+v", percent, report)
		}
	}
}

type rejectingWriter struct{}

func (rejectingWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected output writer failure")
}

type rejectingReader struct{}

func (rejectingReader) Read([]byte) (int, error) {
	return 0, errors.New("injected input reader failure")
}

func TestStartSessionReportsTransportFailuresAndTerminates(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec func() ProcessSpec
	}{
		{name: "stdout", spec: func() ProcessSpec { s := ownedChildSpec("root"); s.Stdout = rejectingWriter{}; return s }},
		{name: "stderr", spec: func() ProcessSpec {
			s := ownedChildSpec("root")
			s.Env = append(s.Env, "NICO_BUDGET_OWNED_STDERR=1")
			s.Stderr = rejectingWriter{}
			return s
		}},
		{name: "stdin", spec: func() ProcessSpec { s := ownedChildSpec("root"); s.Stdin = rejectingReader{}; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, err := StartSession(ctx, tc.spec(), Options{Percent: 100, SampleInterval: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			report, err := p.Wait()
			if err == nil || !strings.Contains(err.Error(), "pipe") {
				t.Fatalf("Wait = report:%+v err:%v, want transport error", report, err)
			}
			if err := p.Close(); err != nil {
				t.Fatalf("Close after Wait: %v", err)
			}
		})
	}
}

func TestSessionBackendSuppressesPipeErrorsOnlyAfterIntentionalClose(t *testing.T) {
	newBackend := func() *windowsSessionBackend {
		done := make(chan struct{})
		close(done)
		return &windowsSessionBackend{
			proc: &budgetProcess{}, done: done,
			transportSignal: make(chan struct{}, 1),
		}
	}

	t.Run("intentional teardown pipe close is not a transport failure", func(t *testing.T) {
		backend := newBackend()
		if err := backend.Close(); err != nil {
			t.Fatal(err)
		}
		backend.recordTransportError("stdin", io.ErrClosedPipe)
		_, err := backend.Wait()
		if err != nil {
			t.Fatalf("Wait after intentional Close = %v, want nil", err)
		}
	})

	t.Run("transport failure observed before Close remains terminal", func(t *testing.T) {
		backend := newBackend()
		backend.recordTransportError("stdout", io.ErrClosedPipe)
		_, err := backend.Wait()
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Wait after preexisting transport failure = %v, want closed-pipe error", err)
		}
	})
}

func TestNicoBudgetSessionHelper(t *testing.T) {
	if os.Getenv("NICO_BUDGET_SESSION_HELPER") == "1" {
		fmt.Fprintln(os.Stdout, "session-ready")
	}
}

func TestNicoBudgetOwnedChildHelper(t *testing.T) {
	if os.Getenv("NICO_BUDGET_OWNED_CHILD") != "1" {
		return
	}
	if os.Getenv("NICO_BUDGET_OWNED_MODE") == "nested-session" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		spec := ProcessSpec{Exe: os.Args[0], Args: []string{"-test.run=^TestNicoBudgetNestedSessionChildHelper$"}, Env: append(os.Environ(), "NICO_BUDGET_NESTED_CHILD=1")}
		session, err := StartSession(ctx, spec, Options{Percent: 100, SampleInterval: 20 * time.Millisecond})
		if err != nil {
			fmt.Fprintf(os.Stdout, "NESTED_ERROR %v\n", err)
			return
		}
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			report, err := session.Snapshot()
			if err != nil {
				fmt.Fprintf(os.Stdout, "NESTED_ERROR snapshot: %v\n", err)
				return
			}
			for _, sample := range report.Samples {
				if len(report.PIDs) > 0 && containsPID(sample.PIDs, report.PIDs[0]) {
					fmt.Fprintf(os.Stdout, "NESTED %d\n", report.PIDs[0])
					for {
						time.Sleep(time.Hour)
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprintln(os.Stdout, "NESTED_ERROR inner Job never reported child membership")
		return
	}
	if os.Getenv("NICO_BUDGET_OWNED_MODE") == "descendant" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("NICO_BUDGET_OWNED_STDERR") == "1" {
		fmt.Fprintln(os.Stderr, "stderr transport failure fixture")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestNicoBudgetOwnedChildHelper$")
	child.Env = append(os.Environ(), "NICO_BUDGET_OWNED_CHILD=1", "NICO_BUDGET_OWNED_MODE=descendant")
	if os.Getenv("NICO_BUDGET_OWNED_MODE") == "natural-root-exit" {
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "READY %d\n", child.Process.Pid)
	if os.Getenv("NICO_BUDGET_OWNED_MODE") == "natural-root-exit" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestNicoBudgetNestedSessionChildHelper(t *testing.T) {
	if os.Getenv("NICO_BUDGET_NESTED_CHILD") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
}

func ownedChildSpec(mode string) ProcessSpec {
	return ProcessSpec{Exe: os.Args[0], Args: []string{"-test.run=^TestNicoBudgetOwnedChildHelper$"}, Env: append(os.Environ(), "NICO_BUDGET_OWNED_CHILD=1", "NICO_BUDGET_OWNED_MODE="+mode)}
}

func mustJobPIDs(t *testing.T, job windows.Handle) []uint32 {
	t.Helper()
	pids, err := queryJobPIDsStrict(job)
	if err != nil {
		t.Fatalf("query Job process list: %v", err)
	}
	return pids
}

func waitForPIDInJob(t *testing.T, job windows.Handle, pid uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if containsPID(mustJobPIDs(t, job), pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant PID %d did not join Job", pid)
}

func containsPID(pids []uint32, pid uint32) bool {
	for _, candidate := range pids {
		if candidate == pid {
			return true
		}
	}
	return false
}

func busyFor(duration time.Duration) {
	deadline := time.Now().Add(duration)
	var value uint64 = 1
	for time.Now().Before(deadline) {
		value = value*1664525 + 1013904223
	}
	if value == 0 {
		os.Exit(3)
	}
}

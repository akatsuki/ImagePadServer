//go:build windows

package nicoexportbudget

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if os.Getenv("NICO_BUDGET_CHILD") == "1" {
		busyFor(700 * time.Millisecond)
	}
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

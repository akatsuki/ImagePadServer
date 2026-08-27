//go:build windows

package video

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestFFmpegJobKillsChildWhenParentIsTerminated(t *testing.T) {
	if os.Getenv("IMAGEPAD_JOB_CHILD_HELPER") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("IMAGEPAD_JOB_PARENT_HELPER") == "1" {
		runFFmpegJobParentHelper()
		return
	}

	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "child.pid")
	parent := exec.Command(os.Args[0], "-test.run=^TestFFmpegJobKillsChildWhenParentIsTerminated$", "-test.count=1")
	parent.Env = append(os.Environ(),
		"IMAGEPAD_JOB_PARENT_HELPER=1",
		"IMAGEPAD_JOB_CHILD_HELPER=",
		"IMAGEPAD_JOB_CHILD_PID_PATH="+pidPath,
		"IMAGEPAD_DATA_DIR="+tempDir,
	)
	if err := parent.Start(); err != nil {
		t.Fatalf("start job parent helper: %v", err)
	}
	parentWaited := false
	defer func() {
		if !parentWaited {
			_ = parent.Process.Kill()
			_ = parent.Wait()
		}
	}()

	pid, err := waitForJobChildPID(pidPath, 5*time.Second)
	if err != nil {
		_ = parent.Process.Kill()
		_ = parent.Wait()
		parentWaited = true
		t.Fatal(err)
	}
	if !windowsProcessExists(pid) {
		t.Fatalf("job child pid %d exited before parent termination", pid)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("force-terminate job parent helper: %v", err)
	}
	_ = parent.Wait()
	parentWaited = true
	defer func() {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			_ = process.Kill()
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for windowsProcessExists(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if windowsProcessExists(pid) {
		t.Fatalf("job child pid %d survived forced parent termination after Job Object handle closed", pid)
	}
}

func TestTrackStartedFFmpegKillsRealFFmpegWhenParentIsTerminated(t *testing.T) {
	if os.Getenv("IMAGEPAD_REAL_FFMPEG_PARENT_HELPER") == "1" {
		runRealFFmpegJobParentHelper()
		return
	}

	ffmpegPath := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpegPath == "" {
		t.Skip("IMAGEPAD_FFMPEG is required for the real FFmpeg forced-parent-termination E2E")
	}
	if _, err := os.Stat(ffmpegPath); err != nil {
		t.Fatalf("stat IMAGEPAD_FFMPEG %q: %v", ffmpegPath, err)
	}

	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "real-ffmpeg.pid")
	parent := exec.Command(os.Args[0], "-test.run=^TestTrackStartedFFmpegKillsRealFFmpegWhenParentIsTerminated$", "-test.count=1")
	parent.Env = append(os.Environ(),
		"IMAGEPAD_REAL_FFMPEG_PARENT_HELPER=1",
		"IMAGEPAD_JOB_PARENT_HELPER=",
		"IMAGEPAD_JOB_CHILD_HELPER=",
		"IMAGEPAD_JOB_CHILD_PID_PATH="+pidPath,
		"IMAGEPAD_DATA_DIR="+tempDir,
		"IMAGEPAD_FFMPEG="+ffmpegPath,
	)
	var output bytes.Buffer
	parent.Stdout = &output
	parent.Stderr = &output
	if err := parent.Start(); err != nil {
		t.Fatalf("start real-FFmpeg parent helper: %v", err)
	}
	parentWaited := false
	defer func() {
		if !parentWaited {
			_ = parent.Process.Kill()
			_ = parent.Wait()
		}
	}()

	pid, err := waitForJobChildPID(pidPath, 10*time.Second)
	if err != nil {
		_ = parent.Process.Kill()
		_ = parent.Wait()
		parentWaited = true
		t.Fatalf("%v\nparent output:\n%s", err, output.String())
	}
	defer func() {
		if process, findErr := os.FindProcess(pid); findErr == nil {
			_ = process.Kill()
		}
	}()
	if !windowsProcessExists(pid) {
		t.Fatalf("real FFmpeg pid %d exited before parent termination\nparent output:\n%s", pid, output.String())
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("force-terminate real-FFmpeg parent helper: %v", err)
	}
	_ = parent.Wait()
	parentWaited = true

	deadline := time.Now().Add(10 * time.Second)
	for windowsProcessExists(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if windowsProcessExists(pid) {
		t.Fatalf("real FFmpeg pid %d survived forced parent termination\nparent output:\n%s", pid, output.String())
	}
}

func runRealFFmpegJobParentHelper() {
	ffmpegPath := os.Getenv("IMAGEPAD_FFMPEG")
	pidPath := os.Getenv("IMAGEPAD_JOB_CHILD_PID_PATH")
	cmd := exec.Command(ffmpegPath,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-re", "-f", "lavfi", "-i", "color=c=black:s=16x16:r=1",
		"-f", "null", "-",
	)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start real FFmpeg: %v\n", err)
		os.Exit(2)
	}
	untrack, err := TrackStartedFFmpeg(cmd)
	if err != nil {
		waitErr := cmd.Wait()
		fmt.Fprintf(os.Stderr, "track real FFmpeg: %v; wait: %v\n", err, waitErr)
		os.Exit(3)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		untrack()
		fmt.Fprintf(os.Stderr, "write real FFmpeg pid: %v\n", err)
		os.Exit(4)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func runFFmpegJobParentHelper() {
	pidPath := os.Getenv("IMAGEPAD_JOB_CHILD_PID_PATH")
	child := exec.Command(os.Args[0], "-test.run=^TestFFmpegJobKillsChildWhenParentIsTerminated$", "-test.count=1")
	child.Env = append(os.Environ(),
		"IMAGEPAD_JOB_PARENT_HELPER=",
		"IMAGEPAD_JOB_CHILD_HELPER=1",
	)
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start child: %v\n", err)
		os.Exit(2)
	}
	releaseJob, err := assignStartedFFmpegToKillOnCloseJob(child.Process)
	if err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		fmt.Fprintf(os.Stderr, "assign child to job: %v\n", err)
		os.Exit(3)
	}
	// Deliberately keep the Job handle open. The outer test force-terminates
	// this process, so Windows closes the handle without running Go defers.
	_ = releaseJob
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		fmt.Fprintf(os.Stderr, "write child pid: %v\n", err)
		os.Exit(4)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForJobChildPID(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		payload, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(payload))
			if parseErr == nil {
				return pid, nil
			}
			lastErr = parseErr
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return 0, fmt.Errorf("read child pid %s: %w", path, lastErr)
}

func windowsProcessExists(pid int) bool {
	const processQueryLimitedInformation = 0x1000
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(handle)
	return true
}

package video

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/settings"
)

type trackedProcess struct {
	PID       int       `json:"pid"`
	Path      string    `json:"path"`
	Args      []string  `json:"args,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	Marker    string    `json:"marker,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

var processRegistryMu sync.Mutex

func TrackStartedFFmpeg(cmd *exec.Cmd) func() {
	if cmd == nil || cmd.Process == nil {
		return func() {}
	}
	if !isFFmpegPath(cmd.Path) {
		return func() {}
	}
	entry := trackedProcess{
		PID:       cmd.Process.Pid,
		Path:      cmd.Path,
		Args:      append([]string(nil), cmd.Args...),
		Dir:       cmd.Dir,
		Marker:    settings.Dir(),
		StartedAt: time.Now(),
	}
	_ = addTrackedProcess(entry)
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = removeTrackedProcess(entry.PID)
		})
	}
}

func CleanupTrackedFFmpeg() (int, error) {
	processRegistryMu.Lock()
	defer processRegistryMu.Unlock()

	entries, err := readTrackedProcessesLocked()
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}

	killed := 0
	var kept []trackedProcess
	var errs []string
	for _, entry := range entries {
		if entry.PID <= 0 {
			continue
		}
		matches, matchErr := trackedProcessMatches(entry)
		if matchErr != nil {
			errs = append(errs, matchErr.Error())
		}
		if !matches {
			continue
		}
		if err := killProcessTree(entry.PID); err != nil {
			errs = append(errs, err.Error())
			kept = append(kept, entry)
			continue
		}
		killed++
	}
	if err := writeTrackedProcessesLocked(kept); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return killed, errors.New(strings.Join(errs, "; "))
	}
	return killed, nil
}

func CombinedOutputTrackedFFmpeg(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, errors.New("exec: Stdout or Stderr already set")
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	untrack := TrackStartedFFmpeg(cmd)
	err := cmd.Wait()
	untrack()
	return output.Bytes(), err
}

func addTrackedProcess(entry trackedProcess) error {
	processRegistryMu.Lock()
	defer processRegistryMu.Unlock()
	entries, err := readTrackedProcessesLocked()
	if err != nil {
		return err
	}
	filtered := entries[:0]
	for _, existing := range entries {
		if existing.PID != entry.PID {
			filtered = append(filtered, existing)
		}
	}
	filtered = append(filtered, entry)
	return writeTrackedProcessesLocked(filtered)
}

func removeTrackedProcess(pid int) error {
	processRegistryMu.Lock()
	defer processRegistryMu.Unlock()
	entries, err := readTrackedProcessesLocked()
	if err != nil {
		return err
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.PID != pid {
			filtered = append(filtered, entry)
		}
	}
	return writeTrackedProcessesLocked(filtered)
}

func readTrackedProcessesLocked() ([]trackedProcess, error) {
	data, err := os.ReadFile(processRegistryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var entries []trackedProcess
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func writeTrackedProcessesLocked(entries []trackedProcess) error {
	path := processRegistryPath()
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func processRegistryPath() string {
	return filepath.Join(settings.Dir(), "ffmpeg-processes.json")
}

func isFFmpegPath(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return name == "ffmpeg" || name == "ffmpeg.exe"
}

func trackedProcessMatches(entry trackedProcess) (bool, error) {
	cmdline, err := processCommandLine(entry.PID)
	if err != nil {
		return false, nil
	}
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return false, nil
	}
	lower := strings.ToLower(cmdline)
	if !strings.Contains(lower, "ffmpeg") {
		return false, nil
	}
	markers := []string{entry.Marker, settings.Dir(), entry.Dir}
	for _, marker := range markers {
		marker = strings.TrimSpace(marker)
		if marker == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true, nil
		}
	}
	return false, nil
}

func processCommandLine(pid int) (string, error) {
	switch runtime.GOOS {
	case "windows":
		filter := fmt.Sprintf("ProcessId=%d", pid)
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_Process -Filter '"+filter+"').CommandLine")
		hideWindow(cmd)
		out, err := cmd.Output()
		return string(out), err
	case "linux":
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil {
			return "", err
		}
		return strings.ReplaceAll(string(data), "\x00", " "), nil
	default:
		return "", fmt.Errorf("process command line inspection is unsupported on %s", runtime.GOOS)
	}
}

func killProcessTree(pid int) error {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
		hideWindow(cmd)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to stop stale ffmpeg pid %d: %w: %s", pid, err, trimOutput(output))
		}
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

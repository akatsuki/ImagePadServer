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

// SeparateOutputTrackedFFmpeg runs cmd and captures stdout and stderr into
// separate buffers. Use this instead of CombinedOutputTrackedFFmpeg when
// stdout must be parsed independently (e.g. as JSON) and stderr is only
// needed for diagnostic context.
func SeparateOutputTrackedFFmpeg(cmd *exec.Cmd) (stdout, stderr []byte, err error) {
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return nil, nil, errors.New("exec: Stdout or Stderr already set")
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	untrack := TrackStartedFFmpeg(cmd)
	runErr := cmd.Wait()
	untrack()
	return outBuf.Bytes(), errBuf.Bytes(), runErr
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

// findProcessOnPort returns the PIDs of processes listening on the given TCP
// port. Cross-platform: uses netstat on Windows, /proc/net/tcp on Linux.
func findProcessOnPort(port int) ([]int, error) {
	switch runtime.GOOS {
	case "windows":
		netstat := exec.Command("netstat", "-ano")
		hideWindow(netstat)
		out, err := netstat.Output()
		if err != nil {
			return nil, fmt.Errorf("netstat: %w", err)
		}
		target := fmt.Sprintf(":%d", port)
		var pids []int
		seen := map[int]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			// netstat output: "  TCP    0.0.0.0:1935   0.0.0.0:0    LISTENING    1234"
			if !strings.Contains(line, "LISTENING") || !strings.Contains(line, target) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			pid, err := strconv.Atoi(fields[len(fields)-1])
			if err != nil {
				continue
			}
			if seen[pid] {
				continue
			}
			seen[pid] = true
			pids = append(pids, pid)
		}
		return pids, nil
	case "linux":
		// /proc/net/tcp lines: "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode"
		// local_address is hex-encoded "050000AA:08F9" for 0.0.0.0:PORT
		data, err := os.ReadFile("/proc/net/tcp")
		if err != nil {
			data, err = os.ReadFile("/proc/net/tcp6")
			if err != nil {
				return nil, err
			}
		}
		hexPort := fmt.Sprintf("%04X", port)
		var pids []int
		seen := map[int]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			localAddr := fields[1] // "050000AA:08F9"
			addrParts := strings.SplitN(localAddr, ":", 2)
			if len(addrParts) != 2 {
				continue
			}
			if addrParts[1] != hexPort {
				continue
			}
			// Parse the inode number and look up the PID via /proc/*/fd
			// Simpler: fall back to lsof or ss
			// For now, use pidof/ss fallback
		}
		// Fallback: use ss -tlnp (requires root for other-uid processes)
		ssCmd := exec.Command("ss", "-tlnp")
		hideWindow(ssCmd)
		out, ssErr := ssCmd.Output()
		if ssErr != nil {
			return pids, nil // return whatever we got from /proc
		}
		target := fmt.Sprintf(":%d", port)
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, "LISTEN") || !strings.Contains(line, target) {
				continue
			}
			// Extract PID from users:(("ffmpeg",pid=1234,fd=5))
			idx := strings.Index(line, "pid=")
			if idx < 0 {
				continue
			}
			end := strings.IndexByte(line[idx:], ')')
			if end < 0 {
				continue
			}
			pidStr := line[idx+4 : idx+end]
			pid, err := strconv.Atoi(pidStr)
			if err != nil {
				continue
			}
			if seen[pid] {
				continue
			}
			seen[pid] = true
			pids = append(pids, pid)
		}
		return pids, nil
	default:
		return nil, fmt.Errorf("port scanning not supported on %s", runtime.GOOS)
	}
}

// isFFmpegProcess checks whether the given PID is an ffmpeg process.
func isFFmpegProcess(pid int) bool {
	cmdline, err := processCommandLine(pid)
	if err != nil {
		return false
	}
	return isFFmpegPath(splitExeFromCommandLine(cmdline))
}

// splitExeFromCommandLine extracts the executable path from a command line string.
func splitExeFromCommandLine(cmdline string) string {
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" {
		return ""
	}
	// Handle quoted paths first: "C:\path\to\ffmpeg.exe" -arg
	if cmdline[0] == '"' {
		end := strings.IndexByte(cmdline[1:], '"')
		if end >= 0 {
			return cmdline[:end+2]
		}
	}
	// Handle unquoted: C:\path\to\ffmpeg.exe -arg or ffmpeg -arg
	parts := strings.Fields(cmdline)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

var (
	listProcessIDsByName    = processIDsByName
	ownedProcessCommandLine = processCommandLine
	ownedProcessKill        = killProcessTree
)

// KillOwnedProcesses terminates only processes whose executable and command
// line marker both match. preferredPIDs are checked first, but never bypass
// live ownership validation, which protects against stale ledgers and PID reuse.
func KillOwnedProcesses(executableBase, requiredMarker string, preferredPIDs []int) (int, error) {
	candidates := append([]int(nil), preferredPIDs...)
	discovered, scanErr := listProcessIDsByName(executableBase)
	candidates = append(candidates, discovered...)

	seen := make(map[int]struct{}, len(candidates))
	killed := 0
	var errs []string
	if scanErr != nil {
		errs = append(errs, fmt.Sprintf("scan %s processes: %v", executableBase, scanErr))
	}
	for _, pid := range candidates {
		if pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		commandLine, err := ownedProcessCommandLine(pid)
		if err != nil {
			errs = append(errs, fmt.Sprintf("inspect %s pid %d: %v", executableBase, pid, err))
			continue
		}
		if !ownedProcessCommandLineMatches(commandLine, executableBase, requiredMarker) {
			continue
		}
		if err := ownedProcessKill(pid); err != nil {
			errs = append(errs, fmt.Sprintf("stop %s pid %d: %v", executableBase, pid, err))
			continue
		}
		killed++
	}
	if len(errs) > 0 {
		return killed, errors.New(strings.Join(errs, "; "))
	}
	return killed, nil
}

func ownedProcessCommandLineMatches(commandLine, executableBase, requiredMarker string) bool {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" || strings.TrimSpace(executableBase) == "" || strings.TrimSpace(requiredMarker) == "" {
		return false
	}
	executable, arguments := splitOwnedCommandLine(commandLine)
	if !strings.EqualFold(filepath.Base(executable), executableBase) {
		return false
	}
	return strings.Contains(strings.ToLower(arguments), strings.ToLower(requiredMarker))
}

func splitOwnedCommandLine(commandLine string) (string, string) {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" {
		return "", ""
	}
	if commandLine[0] == '"' {
		if end := strings.IndexByte(commandLine[1:], '"'); end >= 0 {
			end++
			return commandLine[1:end], strings.TrimSpace(commandLine[end+1:])
		}
	}
	if end := strings.IndexAny(commandLine, " \t\r\n"); end >= 0 {
		return commandLine[:end], strings.TrimSpace(commandLine[end:])
	}
	return commandLine, ""
}

func processIDsByName(executableBase string) ([]int, error) {
	switch runtime.GOOS {
	case "windows":
		filter := fmt.Sprintf("Name='%s'", strings.ReplaceAll(filepath.Base(executableBase), "'", "''"))
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "Get-CimInstance Win32_Process -Filter \""+filter+"\" | Select-Object -ExpandProperty ProcessId")
		hideWindow(cmd)
		output, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		var pids []int
		for _, field := range strings.Fields(string(output)) {
			pid, err := strconv.Atoi(field)
			if err == nil && pid > 0 {
				pids = append(pids, pid)
			}
		}
		return pids, nil
	case "linux":
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return nil, err
		}
		var pids []int
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 0 {
				continue
			}
			comm, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
			if err == nil && strings.EqualFold(strings.TrimSpace(string(comm)), executableBase) {
				pids = append(pids, pid)
			}
		}
		return pids, nil
	default:
		return nil, fmt.Errorf("process enumeration is unsupported on %s", runtime.GOOS)
	}
}

// KillFFmpegOnPort kills any ffmpeg process listening on the given TCP port.
// Returns the number of processes killed. Useful during startup cleanup to
// prevent stale ffmpeg processes from intercepting OBS RTMP connections.
func KillFFmpegOnPort(port int) (int, error) {
	pids, err := findProcessOnPort(port)
	if err != nil {
		return 0, fmt.Errorf("find process on port %d: %w", port, err)
	}
	var killed int
	var errs []string
	for _, pid := range pids {
		if !isFFmpegProcess(pid) {
			continue
		}
		if err := killProcessTree(pid); err != nil {
			errs = append(errs, err.Error())
		} else {
			killed++
		}
	}
	if len(errs) > 0 {
		return killed, fmt.Errorf("killed %d, errors: %s", killed, strings.Join(errs, "; "))
	}
	return killed, nil
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

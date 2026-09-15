package video

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOwnedProcessCommandLineMatches(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{name: "quoted path", command: `"/tools/mediamtx.exe" "/tmp/imagepad-mediamtx-123/mediamtx.yml"`, want: true},
		{name: "unquoted", command: `mediamtx.exe /tmp/imagepad-mediamtx-123/mediamtx.yml`, want: true},
		{name: "wrong executable", command: `"/tools/other.exe" "/tmp/imagepad-mediamtx-123/mediamtx.yml"`},
		{name: "missing marker", command: `"/tools/mediamtx.exe" /tmp/manual/mediamtx.yml`},
		{name: "marker only in executable", command: `/tmp/imagepad-mediamtx-tool/mediamtx.exe /tmp/manual/mediamtx.yml`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ownedProcessCommandLineMatches(tt.command, "mediamtx.exe", "imagepad-mediamtx-"); got != tt.want {
				t.Fatalf("ownedProcessCommandLineMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKillOwnedProcessesValidatesDeduplicatesAndAggregatesErrors(t *testing.T) {
	oldList := listProcessIDsByName
	oldCommandLine := ownedProcessCommandLine
	oldKill := ownedProcessKill
	t.Cleanup(func() {
		listProcessIDsByName = oldList
		ownedProcessCommandLine = oldCommandLine
		ownedProcessKill = oldKill
	})

	listProcessIDsByName = func(string) ([]int, error) { return []int{20, 30, 40, 20}, nil }
	commands := map[int]string{
		10: `mediamtx.exe /tmp/imagepad-mediamtx-ledger/mediamtx.yml`,
		20: `mediamtx.exe /tmp/imagepad-mediamtx-scan/mediamtx.yml`,
		30: `mediamtx.exe /tmp/manual/mediamtx.yml`,
		40: `other.exe /tmp/imagepad-mediamtx-other/mediamtx.yml`,
	}
	ownedProcessCommandLine = func(pid int) (string, error) { return commands[pid], nil }
	var killed []int
	ownedProcessKill = func(pid int) error {
		killed = append(killed, pid)
		if pid == 20 {
			return errors.New("access denied")
		}
		return nil
	}

	count, err := KillOwnedProcesses("mediamtx.exe", "imagepad-mediamtx-", []int{10, 20})
	if count != 1 {
		t.Fatalf("killed count = %d, want 1", count)
	}
	if !reflect.DeepEqual(killed, []int{10, 20}) {
		t.Fatalf("kill attempts = %v, want [10 20]", killed)
	}
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("error = %v, want aggregated kill failure", err)
	}
}

func TestKillOwnedProcessesContinuesWhenScanFails(t *testing.T) {
	oldList := listProcessIDsByName
	oldCommandLine := ownedProcessCommandLine
	oldKill := ownedProcessKill
	t.Cleanup(func() {
		listProcessIDsByName = oldList
		ownedProcessCommandLine = oldCommandLine
		ownedProcessKill = oldKill
	})

	listProcessIDsByName = func(string) ([]int, error) { return nil, errors.New("scan failed") }
	ownedProcessCommandLine = func(int) (string, error) {
		return `mediamtx.exe /tmp/imagepad-mediamtx-ledger/mediamtx.yml`, nil
	}
	var killed []int
	ownedProcessKill = func(pid int) error { killed = append(killed, pid); return nil }

	count, err := KillOwnedProcesses("mediamtx.exe", "imagepad-mediamtx-", []int{77})
	if count != 1 || !reflect.DeepEqual(killed, []int{77}) {
		t.Fatalf("count=%d killed=%v, want ledger PID killed", count, killed)
	}
	if err == nil || !strings.Contains(err.Error(), "scan failed") {
		t.Fatalf("error = %v, want scan error", err)
	}
}

func TestOwnedProcessCommandLineMatchesFullCommandLineMarker(t *testing.T) {
	commandLine := `"C:\Users\masah\AppData\Roaming\ImagePadServer\bin\cloudflared.exe" tunnel --no-autoupdate --url http://127.0.0.1:8080`
	if !ownedProcessCommandLineMatches(commandLine, "cloudflared.exe", `C:\Users\masah\AppData\Roaming\ImagePadServer\bin`) {
		t.Fatal("expected app-local cloudflared path marker to match full command line")
	}
	if ownedProcessCommandLineMatches(commandLine, "cloudflared.exe", `C:\OtherApp\bin`) {
		t.Fatal("unexpected match for unrelated path marker")
	}
}

func TestTrackStartedFFmpegProtectsProcessWithKillOnCloseJob(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	oldProtect := protectStartedFFmpegWithJob
	t.Cleanup(func() { protectStartedFFmpegWithJob = oldProtect })

	cmd := exec.Command(os.Args[0], "-test.run=TestTrackedFFmpegHelperProcess", "--")
	cmd.Env = append(os.Environ(), "IMAGEPAD_TRACKED_FFMPEG_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	cmd.Path = filepath.Join(t.TempDir(), "ffmpeg.exe")
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	protectedPID := 0
	releaseCalls := 0
	protectStartedFFmpegWithJob = func(process *os.Process) (func(), error) {
		entries, err := readTrackedProcessesLocked()
		if err != nil {
			t.Fatalf("read registry before Job assignment: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("registry populated before Job assignment: %+v", entries)
		}
		protectedPID = process.Pid
		return func() { releaseCalls++ }, nil
	}
	untrack, trackErr := TrackStartedFFmpeg(cmd)
	if trackErr != nil {
		t.Fatalf("track started FFmpeg: %v", trackErr)
	}
	if protectedPID != cmd.Process.Pid {
		t.Fatalf("protected pid = %d, want %d", protectedPID, cmd.Process.Pid)
	}
	entries, err := readTrackedProcessesLockedForTest()
	if err != nil {
		t.Fatalf("read tracked processes: %v", err)
	}
	if len(entries) != 1 || entries[0].PID != cmd.Process.Pid {
		t.Fatalf("tracked entries = %+v, want pid %d", entries, cmd.Process.Pid)
	}

	untrack()
	untrack()
	if releaseCalls != 1 {
		t.Fatalf("Job release calls = %d, want 1", releaseCalls)
	}
	entries, err = readTrackedProcessesLockedForTest()
	if err != nil {
		t.Fatalf("read tracked processes after untrack: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("tracked entries after untrack = %+v, want none", entries)
	}
}

func TestTrackStartedFFmpegIgnoresNonFFmpegProcess(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())

	cmd := exec.Command(os.Args[0], "-test.run=TestTrackedFFmpegHelperProcess", "--")
	cmd.Env = append(os.Environ(), "IMAGEPAD_TRACKED_FFMPEG_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	cmd.Path = filepath.Join(t.TempDir(), "other-app.exe")
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	oldProtect := protectStartedFFmpegWithJob
	called := false
	protectStartedFFmpegWithJob = func(*os.Process) (func(), error) {
		called = true
		return noopFFmpegUntrack, nil
	}
	t.Cleanup(func() { protectStartedFFmpegWithJob = oldProtect })

	untrack, trackErr := TrackStartedFFmpeg(cmd)
	if trackErr != nil {
		t.Fatalf("track non-FFmpeg process: %v", trackErr)
	}
	untrack()
	if called {
		t.Fatal("non-FFmpeg process was assigned to the FFmpeg Job Object")
	}
	entries, err := readTrackedProcessesLockedForTest()
	if err != nil {
		t.Fatalf("read tracked processes: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("non-FFmpeg process was added to registry: %+v", entries)
	}
}

func TestTrackStartedFFmpegFailsClosedWhenJobProtectionFails(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	oldProtect := protectStartedFFmpegWithJob
	t.Cleanup(func() { protectStartedFFmpegWithJob = oldProtect })

	cmd := exec.Command(os.Args[0], "-test.run=TestTrackedFFmpegHelperProcess", "--")
	cmd.Env = append(os.Environ(), "IMAGEPAD_TRACKED_FFMPEG_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	cmd.Path = filepath.Join(t.TempDir(), "ffmpeg.exe")
	protectStartedFFmpegWithJob = func(*os.Process) (func(), error) {
		return noopFFmpegUntrack, errors.New("job assignment denied")
	}

	untrack, trackErr := TrackStartedFFmpeg(cmd)
	if trackErr == nil {
		t.Fatal("TrackStartedFFmpeg returned nil error after Job assignment failure")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("unprotected ffmpeg was left running")
	}
	untrack()
	entries, err := readTrackedProcessesLockedForTest()
	if err != nil {
		t.Fatalf("read tracked processes: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("unprotected FFmpeg was written to registry: %+v", entries)
	}
}

func TestTrackStartedFFmpegFailsClosedWhenRegistryWriteFails(t *testing.T) {
	blockedDir := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blockedDir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("create blocked data dir: %v", err)
	}
	t.Setenv("IMAGEPAD_DATA_DIR", blockedDir)

	oldProtect := protectStartedFFmpegWithJob
	t.Cleanup(func() { protectStartedFFmpegWithJob = oldProtect })
	releaseCalls := 0
	protectStartedFFmpegWithJob = func(*os.Process) (func(), error) {
		return func() { releaseCalls++ }, nil
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestTrackedFFmpegHelperProcess", "--")
	cmd.Env = append(os.Environ(), "IMAGEPAD_TRACKED_FFMPEG_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	cmd.Path = filepath.Join(t.TempDir(), "ffmpeg.exe")

	untrack, trackErr := TrackStartedFFmpeg(cmd)
	if trackErr == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		untrack()
		t.Fatal("expected registry write failure")
	}
	_ = cmd.Wait()
	if releaseCalls != 1 {
		t.Fatalf("Job release calls = %d, want 1", releaseCalls)
	}
}

func TestTrackedFFmpegHelperProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_TRACKED_FFMPEG_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func readTrackedProcessesLockedForTest() ([]trackedProcess, error) {
	processRegistryMu.Lock()
	defer processRegistryMu.Unlock()
	return readTrackedProcessesLocked()
}

func TestIsFFmpegPathRecognizesWindowsWrappers(t *testing.T) {
	for _, path := range []string{"ffmpeg", "ffmpeg.exe", "ffmpeg.cmd", "ffmpeg.bat", `C:\	ools\\FFMPEG.CMD`} {
		if !isFFmpegPath(path) {
			t.Errorf("isFFmpegPath(%q) = false, want true", path)
		}
	}
	for _, path := range []string{"other.exe", "ffmpeg.ps1", "my-ffmpeg-wrapper.cmd"} {
		if isFFmpegPath(path) {
			t.Errorf("isFFmpegPath(%q) = true, want false", path)
		}
	}
}

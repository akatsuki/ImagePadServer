package video

import (
	"errors"
	"reflect"
	"strings"
	"testing"
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

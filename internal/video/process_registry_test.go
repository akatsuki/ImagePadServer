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
		{name: "quoted windows", command: `"C:\tools\mediamtx.exe" "C:\Temp\imagepad-mediamtx-123\mediamtx.yml"`, want: true},
		{name: "unquoted", command: `mediamtx.exe C:\Temp\imagepad-mediamtx-123\mediamtx.yml`, want: true},
		{name: "wrong executable", command: `"C:\tools\other.exe" "C:\Temp\imagepad-mediamtx-123\mediamtx.yml"`},
		{name: "missing marker", command: `"C:\tools\mediamtx.exe" C:\Temp\manual\mediamtx.yml`},
		{name: "marker only in executable", command: `C:\imagepad-mediamtx-tool\mediamtx.exe C:\Temp\manual\mediamtx.yml`},
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
		10: `mediamtx.exe C:\Temp\imagepad-mediamtx-ledger\mediamtx.yml`,
		20: `mediamtx.exe C:\Temp\imagepad-mediamtx-scan\mediamtx.yml`,
		30: `mediamtx.exe C:\Temp\manual\mediamtx.yml`,
		40: `other.exe C:\Temp\imagepad-mediamtx-other\mediamtx.yml`,
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
		return `mediamtx.exe C:\Temp\imagepad-mediamtx-ledger\mediamtx.yml`, nil
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

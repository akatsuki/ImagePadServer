package obsrtmp

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"imagepadserver/internal/settings"
)

func TestMediaMTXProcessRegistryDeduplicatesAndRemoves(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := registerMediaMTXProcess(42); err != nil {
		t.Fatal(err)
	}
	if err := registerMediaMTXProcess(42); err != nil {
		t.Fatal(err)
	}
	if err := registerMediaMTXProcess(7); err != nil {
		t.Fatal(err)
	}
	if got, err := readMediaMTXProcessIDs(); err != nil || !reflect.DeepEqual(got, []int{7, 42}) {
		t.Fatalf("registry = %v, %v; want [7 42]", got, err)
	}
	if err := unregisterMediaMTXProcess(42); err != nil {
		t.Fatal(err)
	}
	if got, err := readMediaMTXProcessIDs(); err != nil || !reflect.DeepEqual(got, []int{7}) {
		t.Fatalf("registry after removal = %v, %v; want [7]", got, err)
	}
}

func TestCleanupStaleMediaMTXValidatesLedgerPIDsAndRemovesLedger(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	for _, pid := range []int{91, 17, 91} {
		if err := registerMediaMTXProcess(pid); err != nil {
			t.Fatal(err)
		}
	}
	oldKill := killOwnedMediaMTX
	t.Cleanup(func() { killOwnedMediaMTX = oldKill })
	var executable, marker string
	var preferred []int
	killOwnedMediaMTX = func(exe, required string, pids []int) (int, error) {
		executable, marker = exe, required
		preferred = append([]int(nil), pids...)
		return 2, nil
	}

	killed, err := CleanupStaleMediaMTX()
	if err != nil || killed != 2 {
		t.Fatalf("cleanup = %d, %v; want 2, nil", killed, err)
	}
	if executable != mediaMTXExecutableName() || marker != "imagepad-mediamtx-" {
		t.Fatalf("ownership contract = %q, %q", executable, marker)
	}
	if !reflect.DeepEqual(preferred, []int{17, 91}) {
		t.Fatalf("preferred PIDs = %v, want [17 91]", preferred)
	}
	if _, err := os.Stat(filepath.Join(settings.Dir(), "mediamtx-processes.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ledger remains after cleanup: %v", err)
	}
}

func TestCleanupStaleMediaMTXScansDespiteMalformedLedger(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	path := filepath.Join(settings.Dir(), "mediamtx-processes.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldKill := killOwnedMediaMTX
	t.Cleanup(func() { killOwnedMediaMTX = oldKill })
	called := false
	killOwnedMediaMTX = func(string, string, []int) (int, error) {
		called = true
		return 1, nil
	}

	killed, err := CleanupStaleMediaMTX()
	if !called || killed != 1 {
		t.Fatalf("scan called=%v killed=%d, want true, 1", called, killed)
	}
	if err == nil || !strings.Contains(err.Error(), "decode MediaMTX process registry") {
		t.Fatalf("error = %v, want malformed-ledger diagnostic", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("malformed ledger remains after cleanup: %v", statErr)
	}
}

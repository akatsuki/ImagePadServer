//go:build nico_timeline_embedded && windows

package nicorender

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTimelineEmbeddedCleanupRetriesTransientWindowsSharingViolation(t *testing.T) {
	payload, manifest := timelinePayload()
	path, cleanup, err := materializeTimelinePayload(payload, manifest, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	dir := filepath.Dir(path)
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_READ,
		0,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("lock materialized helper without delete sharing: %v", err)
	}
	closed := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		closed <- syscall.CloseHandle(handle)
	}()

	cleanup()
	if err := <-closed; err != nil {
		t.Fatalf("release temporary helper lock: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temporary helper directory remains after transient lock cleared: %q err=%v", dir, err)
	}
}

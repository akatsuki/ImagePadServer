package airplay

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func runtimeInstallArchive(t *testing.T, path string, descriptor RuntimeDescriptor, bridge ...string) {
	t.Helper()
	bridgeData := "bridge"
	if len(bridge) > 0 {
		bridgeData = bridge[0]
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	license := []byte(`{"schema":1,"runtimeSetID":"` + descriptor.RuntimeSetID + `"}`)
	offer := []byte("source offer for " + descriptor.RuntimeSetID + "\n")
	descriptorData, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		runtimeDescriptorName:      descriptorData,
		descriptor.Receiver:        []byte("receiver"),
		descriptor.Bridge:          []byte(bridgeData),
		descriptor.CapabilityProbe: []byte("probe"),
		descriptor.LicenseManifest: license,
		descriptor.SourceOffer:     offer,
		filepath.ToSlash(filepath.Join(descriptor.GStreamerRoot, ".keep")): []byte("runtime"),
	}
	for name, data := range files {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write(data); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeInstallDoesNotReuseStaleSameSetArchive(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("pinned runtime descriptor is Windows amd64 only")
	}
	descriptor := validRuntimeDescriptor()
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtimes")
	oldArchive := filepath.Join(root, "old.zip")
	newArchive := filepath.Join(root, "new.zip")
	runtimeInstallArchive(t, oldArchive, descriptor, "old bridge")
	if _, err := InstallAirPlayRuntimeArchive(context.Background(), oldArchive, runtimeRoot, descriptor); err != nil {
		t.Fatal(err)
	}
	runtimeInstallArchive(t, newArchive, descriptor, "new bridge")
	installed, err := InstallAirPlayRuntimeArchive(context.Background(), newArchive, runtimeRoot, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(installed.BridgePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != "new bridge" {
		t.Fatalf("same runtimeSetID reused stale bridge: got %q, want %q", actual, "new bridge")
	}
}

func TestRuntimeInstallRetainsLastGood(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("pinned runtime descriptor is Windows amd64 only")
	}
	descriptor := validRuntimeDescriptor()
	root := t.TempDir()
	archivePath := filepath.Join(root, "runtime.zip")
	runtimeInstallArchive(t, archivePath, descriptor)
	first, err := InstallAirPlayRuntimeArchive(context.Background(), archivePath, filepath.Join(root, "runtimes"), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if first.Descriptor.RuntimeSetID != descriptor.RuntimeSetID {
		t.Fatalf("installed set = %q", first.Descriptor.RuntimeSetID)
	}
	if _, err := os.Stat(filepath.Join(root, "runtimes", runtimeActiveFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "runtimes", descriptor.RuntimeSetID, descriptor.Bridge)); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallAirPlayRuntimeArchive(context.Background(), archivePath, filepath.Join(root, "runtimes"), descriptor); err == nil {
		t.Fatal("invalid existing runtime unexpectedly replaced")
	}
	if _, err := os.Stat(filepath.Join(root, "runtimes", descriptor.RuntimeSetID)); err != nil {
		t.Fatal("last-good runtime directory was removed")
	}
}

func TestRuntimeInstallRejectsWindowsAliases(t *testing.T) {
	for _, name := range []string{"CON", "con.txt", "aux.dll", "nested\\NUL", "bad. ", "bad."} {
		if _, err := safeZipPath(name); err == nil {
			t.Errorf("safeZipPath accepted Windows alias %q", name)
		}
	}
}

func TestRuntimeInstallTwoProcessesWaitsForLock(t *testing.T) {
	root := t.TempDir()
	lockPath := filepath.Join(root, runtimeLockFile)
	first, err := acquireRuntimeFileLock(context.Background(), lockPath, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireRuntimeFileLock(ctx, lockPath, time.Hour); err == nil {
		t.Fatal("second runtime installer unexpectedly acquired active lock")
	}
}

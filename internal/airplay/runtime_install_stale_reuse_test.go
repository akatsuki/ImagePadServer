package airplay

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func runtimeInstallArchiveWithBridge(t *testing.T, path string, descriptor RuntimeDescriptor, bridge string) {
	t.Helper()
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
		descriptor.Bridge:          []byte(bridge),
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

func TestRuntimeInstallDoesNotReuseStaleSameSetArtifact(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("pinned runtime descriptor is Windows amd64 only")
	}
	descriptor := validRuntimeDescriptor()
	descriptor.RuntimeSetID = "same-set-artifact-upgrade"
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtimes")
	oldArchive := filepath.Join(root, "old.zip")
	newArchive := filepath.Join(root, "new.zip")
	runtimeInstallArchiveWithBridge(t, oldArchive, descriptor, "old bridge")
	if _, err := InstallAirPlayRuntimeArchive(context.Background(), oldArchive, runtimeRoot, descriptor); err != nil {
		t.Fatal(err)
	}
	runtimeInstallArchiveWithBridge(t, newArchive, descriptor, "new bridge")
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

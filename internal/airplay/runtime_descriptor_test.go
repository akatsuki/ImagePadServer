package airplay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeRuntimeFixture(t *testing.T, root string, descriptor RuntimeDescriptor) {
	t.Helper()
	for _, relative := range []string{descriptor.Receiver, descriptor.Bridge, descriptor.CapabilityProbe, descriptor.LicenseManifest, descriptor.SourceOffer} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, descriptor.GStreamerRoot), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, runtimeDescriptorName), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func validRuntimeDescriptor() RuntimeDescriptor {
	return RuntimeDescriptor{
		Schema: 1, RuntimeSetID: "test-set-1", Architecture: runtimeArchitecture,
		ProtocolVersion: runtimeProtocol, Receiver: "uxplay/receiver.exe",
		Bridge: "gstreamer/bridge.exe", GStreamerRoot: "gstreamer",
		CapabilityProbe: "gstreamer/probe.exe", LicenseManifest: "license-manifest.json",
		SourceOffer: "SOURCE-OFFER.md",
	}
}

func TestRuntimeResolverPinnedSet(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("pinned runtime descriptor is Windows amd64 only")
	}
	root := t.TempDir()
	descriptor := validRuntimeDescriptor()
	writeRuntimeFixture(t, root, descriptor)
	got, err := resolveAirPlayRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Descriptor.RuntimeSetID != descriptor.RuntimeSetID || got.Root != root {
		t.Fatalf("resolved runtime = %+v, want root=%q set=%q", got, root, descriptor.RuntimeSetID)
	}
}

func TestRuntimeResolverDoesNotMixPATH(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("pinned runtime descriptor is Windows amd64 only")
	}
	root := t.TempDir()
	descriptor := validRuntimeDescriptor()
	writeRuntimeFixture(t, root, descriptor)
	pathDir := t.TempDir()
	pathReceiver := filepath.Join(pathDir, "uxplay.exe")
	if err := os.WriteFile(pathReceiver, []byte("path receiver"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAirPlayRuntimeRoot, root)
	t.Setenv("PATH", pathDir)
	got, err := ResolvePinnedAirPlayRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if got.ReceiverPath == pathReceiver || got.ReceiverPath != filepath.Join(root, filepath.FromSlash(descriptor.Receiver)) {
		t.Fatalf("resolver mixed PATH receiver: %+v", got)
	}
}

func TestRuntimeResolverRejectsPartialDescriptor(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("pinned runtime descriptor is Windows amd64 only")
	}
	root := t.TempDir()
	descriptor := validRuntimeDescriptor()
	writeRuntimeFixture(t, root, descriptor)
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(descriptor.Bridge))); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAirPlayRuntime(root); err == nil {
		t.Fatal("partial runtime descriptor unexpectedly resolved")
	}
}

func TestRuntimeResolverUsesInstalledActiveSet(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("installed pinned runtime is Windows amd64 only")
	}
	dataDir := t.TempDir()
	setID := "installed-set-1"
	root := filepath.Join(dataDir, "runtimes", "airplay", setID)
	descriptor := validRuntimeDescriptor()
	descriptor.RuntimeSetID = setID
	writeRuntimeFixture(t, root, descriptor)
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), runtimeActiveFile), []byte(setID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAirPlayRuntimeRoot, "")
	t.Setenv("IMAGEPAD_DATA_DIR", dataDir)
	got, err := ResolvePinnedAirPlayRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if got.Descriptor.RuntimeSetID != setID || got.Root != root {
		t.Fatalf("resolved installed runtime = %+v, want root=%q set=%q", got, root, setID)
	}
}

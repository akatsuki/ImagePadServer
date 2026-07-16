package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectSidecarRecordsIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sidecar.exe")
	data := []byte("deterministic-sidecar")
	if err := os.WriteFile(path, data, 0755); err != nil {
		t.Fatal(err)
	}
	fp := runtimeFingerprint{Adapter: "adapter", Backend: "vulkan", Toolchain: "wgpu/test"}
	p := inspectSidecar(path, fp)
	if p.Error != "" || p.Path != path || p.SizeBytes != int64(len(data)) || p.SHA256 == "" || p.ModTime == "" {
		t.Fatalf("incomplete provenance: %+v", p)
	}
	if p.Fingerprint != fp {
		t.Fatalf("fingerprint mismatch: got %+v want %+v", p.Fingerprint, fp)
	}
}

func TestInspectSidecarMissingPathFailsClosed(t *testing.T) {
	p := inspectSidecar(filepath.Join(t.TempDir(), "missing.exe"), runtimeFingerprint{})
	if p.Error == "" {
		t.Fatal("expected missing sidecar error")
	}
}

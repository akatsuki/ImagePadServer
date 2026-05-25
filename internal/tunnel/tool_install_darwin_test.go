//go:build darwin
// +build darwin

package tunnel

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinLocalCloudflaredInstall(t *testing.T) {
	if os.Getenv("IMAGEPAD_RUN_LOCAL_TOOL_INSTALL_TESTS") != "1" {
		t.Skip("set IMAGEPAD_RUN_LOCAL_TOOL_INSTALL_TESTS=1 to download and verify local app tools")
	}

	dataDir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dataDir)
	t.Setenv("PATH", "/usr/bin:/bin")

	cloudflared, err := ensureCloudflared()
	if err != nil {
		t.Fatalf("ensureCloudflared: %v", err)
	}
	want := filepath.Join(dataDir, "bin", "cloudflared")
	if cloudflared != want {
		t.Fatalf("cloudflared path = %q, want app-local %q", cloudflared, want)
	}
	if err := exec.Command(cloudflared, "--version").Run(); err != nil {
		t.Fatalf("app-local cloudflared did not run: %v", err)
	}
}

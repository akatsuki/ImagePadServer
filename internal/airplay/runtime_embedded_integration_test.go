//go:build airplay_runtime_embedded

package airplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPreparePinnedAirPlayRuntimeFromEmbeddedArchive(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("embedded Windows runtime candidate")
	}
	dataDir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dataDir)
	t.Setenv(envAirPlayRuntimeRoot, "")
	t.Setenv(envAirPlayRuntimeBootstrap, "")
	artifact, err := loadEmbeddedRuntimeArtifact()
	if err != nil {
		t.Fatalf("read embedded runtime descriptor: %v", err)
	}
	installed, err := preparePinnedAirPlayRuntime(context.Background())
	if err != nil {
		t.Fatalf("prepare embedded runtime: %v", err)
	}
	if installed.Descriptor.RuntimeSetID != artifact.RuntimeSetID {
		t.Fatalf("runtimeSetID=%q", installed.Descriptor.RuntimeSetID)
	}
	if _, err := os.Stat(filepath.Join(installed.Root, "gstreamer", "airplay-gstreamer-bridge.exe")); err != nil {
		t.Fatalf("embedded bridge missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installed.Root, runtimeArtifactMarkerName)); err != nil {
		t.Fatalf("runtime artifact marker missing: %v", err)
	}
	proofData, err := os.ReadFile(filepath.Join(installed.Root, "gstreamer", "imagepad-airplay-gstreamer-bridge-build.json"))
	if err != nil {
		t.Fatalf("embedded H.264 build provenance missing: %v", err)
	}
	var proof struct {
		ExecutableSHA256 string `json:"executableSha256"`
		VideoContract    struct {
			ID             string `json:"id"`
			SlicesPerFrame int    `json:"slicesPerFrame"`
			TestName       string `json:"testName"`
			TestPassed     bool   `json:"testPassed"`
		} `json:"videoContract"`
	}
	// PowerShell-generated JSON may carry a UTF-8 BOM.
	if len(proofData) >= 3 && string(proofData[:3]) == "\xef\xbb\xbf" {
		proofData = proofData[3:]
	}
	if err := json.Unmarshal(proofData, &proof); err != nil {
		t.Fatal(err)
	}
	if proof.VideoContract.ID != "rtsp-h264-single-slice-v1" || proof.VideoContract.SlicesPerFrame != 1 ||
		proof.VideoContract.TestName != "airplay_source_clock_single_slice" || !proof.VideoContract.TestPassed {
		t.Fatalf("embedded runtime has no passing single-slice contract: %+v", proof.VideoContract)
	}
	bridge, err := os.ReadFile(filepath.Join(installed.Root, "gstreamer", "airplay-gstreamer-bridge.exe"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bridge)
	if hex.EncodeToString(digest[:]) != proof.ExecutableSHA256 {
		t.Fatal("embedded bridge differs from its tested build")
	}
}

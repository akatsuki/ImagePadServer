//go:build windows

package airplay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureGStreamerBridgePrefersVersionedPluginPath(t *testing.T) {
	root := t.TempDir()
	staleGeneric := filepath.Join(t.TempDir(), "stale-generic-plugins")
	verifiedVersioned := filepath.Join(t.TempDir(), "verified-plugins-1.0")
	t.Setenv("GST_PLUGIN_PATH", staleGeneric)
	t.Setenv("GST_PLUGIN_PATH_1_0", verifiedVersioned)
	t.Setenv(envAllowInheritedGStreamer, "1")

	environment := configuredGStreamerBridgeEnvironment(t, filepath.Join(root, "airplay-gstreamer-bridge.exe"))
	for _, key := range []string{"GST_PLUGIN_PATH", "GST_PLUGIN_PATH_1_0"} {
		entries := filepath.SplitList(environment[key])
		if !pathListContains(entries, verifiedVersioned) {
			t.Errorf("%s = %q, missing versioned plugin directory %q", key, environment[key], verifiedVersioned)
		}
		if pathListContains(entries, staleGeneric) {
			t.Errorf("%s = %q, unexpectedly retained lower-priority generic plugin directory %q", key, environment[key], staleGeneric)
		}
	}
	if got := environment["GST_PLUGIN_SYSTEM_PATH"]; got != "" {
		t.Fatalf("GST_PLUGIN_SYSTEM_PATH = %q, want isolated empty value", got)
	}
	if got := environment["GST_PLUGIN_SYSTEM_PATH_1_0"]; got != "" {
		t.Fatalf("GST_PLUGIN_SYSTEM_PATH_1_0 = %q, want isolated empty value", got)
	}
}

func TestConfigureGStreamerBridgeUsesGenericPluginPathOnlyWhenVersionedIsUnset(t *testing.T) {
	root := t.TempDir()
	generic := filepath.Join(t.TempDir(), "generic-plugins")
	t.Setenv("GST_PLUGIN_PATH", generic)
	t.Setenv(envAllowInheritedGStreamer, "1")
	previous, existed := os.LookupEnv("GST_PLUGIN_PATH_1_0")
	if err := os.Unsetenv("GST_PLUGIN_PATH_1_0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("GST_PLUGIN_PATH_1_0", previous)
		} else {
			_ = os.Unsetenv("GST_PLUGIN_PATH_1_0")
		}
	})

	environment := configuredGStreamerBridgeEnvironment(t, filepath.Join(root, "airplay-gstreamer-bridge.exe"))
	if !pathListContains(filepath.SplitList(environment["GST_PLUGIN_PATH_1_0"]), generic) {
		t.Fatalf("GST_PLUGIN_PATH_1_0 = %q, missing generic fallback %q", environment["GST_PLUGIN_PATH_1_0"], generic)
	}
}

func TestConfigureGStreamerBridgeHonorsExplicitEmptyVersionedPluginPath(t *testing.T) {
	root := t.TempDir()
	staleGeneric := filepath.Join(t.TempDir(), "stale-generic-plugins")
	t.Setenv("GST_PLUGIN_PATH", staleGeneric)
	t.Setenv("GST_PLUGIN_PATH_1_0", "")

	environment := configuredGStreamerBridgeEnvironment(t, filepath.Join(root, "airplay-gstreamer-bridge.exe"))
	if pathListContains(filepath.SplitList(environment["GST_PLUGIN_PATH_1_0"]), staleGeneric) {
		t.Fatalf("GST_PLUGIN_PATH_1_0 = %q, explicit empty value fell back to stale generic path", environment["GST_PLUGIN_PATH_1_0"])
	}
}

func TestConfigureGStreamerBridgePinsScannerAndWritableRegistry(t *testing.T) {
	root := t.TempDir()
	scanner := filepath.Join(root, "libexec", "gstreamer-1.0", "gst-plugin-scanner.exe")
	if err := os.MkdirAll(filepath.Dir(scanner), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scanner, []byte("scanner"), 0600); err != nil {
		t.Fatal(err)
	}
	environment := configuredGStreamerBridgeEnvironment(t, filepath.Join(root, "airplay-gstreamer-bridge.exe"))
	if got := environment["GST_PLUGIN_SCANNER"]; got != scanner {
		t.Fatalf("GST_PLUGIN_SCANNER = %q, want %q", got, scanner)
	}
	if got := environment["GST_REGISTRY"]; got == "" || !strings.Contains(strings.ToLower(got), "imagepadserver") {
		t.Fatalf("GST_REGISTRY = %q, want writable ImagePadServer cache", got)
	}
}

func TestConfigureGStreamerBridgeDoesNotInheritGenericPath(t *testing.T) {
	root := t.TempDir()
	stalePath := filepath.Join(t.TempDir(), "stale-runtime")
	stalePlugins := filepath.Join(t.TempDir(), "stale-plugins")
	t.Setenv("PATH", stalePath)
	t.Setenv("GST_PLUGIN_PATH", stalePlugins)
	t.Setenv("GST_PLUGIN_PATH_1_0", "")
	t.Setenv(envAllowInheritedGStreamer, "0")

	environment := configuredGStreamerBridgeEnvironment(t, filepath.Join(root, "airplay-gstreamer-bridge.exe"))
	if pathListContains(filepath.SplitList(environment["PATH"]), stalePath) {
		t.Fatalf("PATH = %q, unexpectedly inherited stale runtime", environment["PATH"])
	}
	if pathListContains(filepath.SplitList(environment["GST_PLUGIN_PATH"]), stalePlugins) {
		t.Fatalf("GST_PLUGIN_PATH = %q, unexpectedly inherited stale plugins", environment["GST_PLUGIN_PATH"])
	}
}

func configuredGStreamerBridgeEnvironment(t *testing.T, bridgePath string) map[string]string {
	t.Helper()
	cmd := exec.Command(bridgePath)
	configureGStreamerBridgeProcess(cmd, &limitedBuffer{max: 128}, bridgePath)
	environment := make(map[string]string)
	for _, entry := range cmd.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[strings.ToUpper(key)] = value
		}
	}
	return environment
}

func pathListContains(entries []string, want string) bool {
	for _, got := range entries {
		if strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
			return true
		}
	}
	return false
}

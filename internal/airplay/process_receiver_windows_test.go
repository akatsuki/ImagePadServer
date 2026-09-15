//go:build windows

package airplay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureUxPlayWindowsWrapperUsesIsolatedArguments(t *testing.T) {
	sessionDir := t.TempDir()
	userAppData := filepath.Join(sessionDir, "user-appdata")
	t.Setenv("APPDATA", userAppData)
	userArgumentsPath := filepath.Join(userAppData, "leapbtw", "uxplay-windows", "arguments.txt")
	if err := os.MkdirAll(filepath.Dir(userArgumentsPath), 0700); err != nil {
		t.Fatal(err)
	}
	originalArguments := []byte("-n original -nh")
	if err := os.WriteFile(userArgumentsPath, originalArguments, 0600); err != nil {
		t.Fatal(err)
	}

	receiverPath := filepath.Join(sessionDir, "uxplay-windows.exe")
	cmd := exec.Command(receiverPath, BuildReceiverArgs(41001, 41002, "Test Receiver")...)
	restore, err := configureReceiverProcess(cmd, &limitedBuffer{}, receiverPath, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if restore == nil {
		t.Fatal("wrapper configuration did not return a restore function")
	}

	argumentsPaths := []string{
		filepath.Join(sessionDir, "uxplay-programdata", "uxplay-windows", "arguments.txt"),
		filepath.Join(sessionDir, "uxplay-appdata", "leapbtw", "uxplay-windows", "arguments.txt"),
		userArgumentsPath,
	}
	for _, argumentsPath := range argumentsPaths {
		content, err := os.ReadFile(argumentsPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(content)
		for _, want := range []string{
			`-n Test-Receiver`,
			`-vs 0`,
			`port=41001`,
			`port=41002`,
			"config-interval=-1	!	udpsink	host=127.0.0.1	port=41001",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("wrapper arguments %q do not contain %q", got, want)
			}
		}
	}
	for _, want := range []string{
		"APPDATA=" + filepath.Join(sessionDir, "uxplay-appdata"),
		"LOCALAPPDATA=" + filepath.Join(sessionDir, "uxplay-localappdata"),
		"ProgramData=" + filepath.Join(sessionDir, "uxplay-programdata"),
	} {
		found := false
		for _, entry := range cmd.Env {
			if entry == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("child environment does not contain %q", want)
		}
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	restoredArguments, err := os.ReadFile(userArgumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(restoredArguments); got != string(originalArguments) {
		t.Fatalf("restored user arguments = %q, want %q", got, originalArguments)
	}
}

func TestConfigureDirectUxPlayDoesNotCreateWrapperArguments(t *testing.T) {
	sessionDir := t.TempDir()
	receiverPath := filepath.Join(sessionDir, "uxplay.exe")
	cmd := exec.Command(receiverPath, BuildReceiverArgs(41001, 41002, "Test Receiver")...)
	restore, err := configureReceiverProcess(cmd, &limitedBuffer{}, receiverPath, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if restore != nil {
		t.Fatal("direct UxPlay unexpectedly returned a restore function")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "uxplay-programdata")); !os.IsNotExist(err) {
		t.Fatalf("direct UxPlay unexpectedly created wrapper configuration: %v", err)
	}
}

func TestConfigureReceiverProcessUsesAdjacentRTPSupplement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uxplay")
	rtpRoot := root + "-rtp"
	for _, relative := range []string{
		filepath.Join("lib", "gstreamer-1.0", "libgstrtp.dll"),
		filepath.Join("lib", "gstreamer-1.0", "libgstudp.dll"),
		"libgstnet-1.0-0.dll",
		"libgstrtp-1.0-0.dll",
	} {
		path := filepath.Join(rtpRoot, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "lib", "gstreamer-1.0"), 0700); err != nil {
		t.Fatal(err)
	}
	receiverPath := filepath.Join(root, "uxplay-windows.exe")
	cmd := exec.Command(receiverPath)
	sessionDir := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(sessionDir, "user-appdata"))
	restore, err := configureReceiverProcess(cmd, &limitedBuffer{}, receiverPath, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if restore == nil {
		t.Fatal("wrapper configuration did not return a restore function")
	}
	defer func() {
		if err := restore(); err != nil {
			t.Errorf("restore wrapper configuration: %v", err)
		}
	}()

	env := make(map[string]string)
	for _, entry := range cmd.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[strings.ToUpper(key)] = value
		}
	}
	if got, want := env["IMAGEPAD_UXPLAY_RTP_DIR"], rtpRoot; got != want {
		t.Fatalf("RTP supplement directory = %q, want %q", got, want)
	}
	pluginPath := strings.Join([]string{
		filepath.Join(rtpRoot, "lib", "gstreamer-1.0"),
		filepath.Join(root, "lib", "gstreamer-1.0"),
	}, string(os.PathListSeparator))
	if got := env["GST_PLUGIN_PATH"]; got != pluginPath {
		t.Fatalf("GST_PLUGIN_PATH = %q, want %q", got, pluginPath)
	}
	pathPrefix := strings.Join([]string{rtpRoot, root, filepath.Join(root, "lib")}, string(os.PathListSeparator))
	if got := env["PATH"]; !strings.HasPrefix(got, pathPrefix) {
		t.Fatalf("PATH = %q, want prefix %q", got, pathPrefix)
	}
}

func TestConfigureReceiverProcessUsesAdjacentGStreamerRuntime(t *testing.T) {
	packageRoot := t.TempDir()
	root := filepath.Join(packageRoot, "uxplay-source-clock")
	gstreamerRoot := filepath.Join(packageRoot, "gstreamer")
	if err := os.MkdirAll(filepath.Join(gstreamerRoot, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(gstreamerRoot, "lib", "gstreamer-1.0"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gstreamer-1.0-0.dll", "gstapp-1.0-0.dll"} {
		if err := os.WriteFile(filepath.Join(gstreamerRoot, "bin", name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	receiverPath := filepath.Join(root, "uxplay-source-clock.exe")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiverPath, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(receiverPath)
	runtimeRoots, pluginDirs, rtpDir := uxPlayRuntimeDirectories(root)
	if rtpDir != "" {
		t.Fatalf("unexpected RTP supplement: %q", rtpDir)
	}
	wantBin := filepath.Join(gstreamerRoot, "bin")
	wantPlugins := filepath.Join(gstreamerRoot, "lib", "gstreamer-1.0")
	if len(runtimeRoots) == 0 || runtimeRoots[0] != wantBin {
		t.Fatalf("runtime roots = %#v, want first %q", runtimeRoots, wantBin)
	}
	if len(pluginDirs) == 0 || pluginDirs[0] != wantPlugins {
		t.Fatalf("plugin dirs = %#v, want first %q", pluginDirs, wantPlugins)
	}
	_ = cmd
}

func TestConfigureReceiverProcessPinsScannerAndSessionRegistry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "uxplay-source-clock")
	scanner := filepath.Join(root, "libexec", "gstreamer-1.0", "gst-plugin-scanner.exe")
	if err := os.MkdirAll(filepath.Dir(scanner), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scanner, []byte("scanner"), 0600); err != nil {
		t.Fatal(err)
	}
	receiverPath := filepath.Join(root, "uxplay-source-clock.exe")
	if err := os.WriteFile(receiverPath, []byte("receiver"), 0600); err != nil {
		t.Fatal(err)
	}
	sessionDir := t.TempDir()
	cmd := exec.Command(receiverPath)
	if _, err := configureReceiverProcess(cmd, &limitedBuffer{}, receiverPath, sessionDir); err != nil {
		t.Fatal(err)
	}
	environment := make(map[string]string)
	for _, entry := range cmd.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[strings.ToUpper(key)] = value
		}
	}
	if got := environment["GST_PLUGIN_SCANNER"]; got != scanner {
		t.Fatalf("GST_PLUGIN_SCANNER = %q, want %q", got, scanner)
	}
	if got, want := environment["GST_REGISTRY"], filepath.Join(sessionDir, "gstreamer-registry.bin"); got != want {
		t.Fatalf("GST_REGISTRY = %q, want %q", got, want)
	}
}

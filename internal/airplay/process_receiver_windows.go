//go:build windows

package airplay

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func configureReceiverProcess(cmd *exec.Cmd, output *limitedBuffer, receiverPath, sessionDir string) (func() error, error) {
	configureProcess(cmd, output)
	root := filepath.Dir(receiverPath)
	cmd.Dir = root
	runtimeRoots, pluginDirs, rtpDir := uxPlayRuntimeDirectories(root)
	pathEntries := append([]string{}, runtimeRoots...)
	pathEntries = append(pathEntries, filepath.Join(root, "lib"))
	if inheritedPath := os.Getenv("PATH"); inheritedPath != "" {
		pathEntries = append(pathEntries, inheritedPath)
	}
	environment := map[string]string{
		"PATH":                       strings.Join(pathEntries, string(os.PathListSeparator)),
		"GST_PLUGIN_PATH":            strings.Join(pluginDirs, string(os.PathListSeparator)),
		"GST_PLUGIN_PATH_1_0":        strings.Join(pluginDirs, string(os.PathListSeparator)),
		"GST_PLUGIN_SYSTEM_PATH":     "",
		"GST_PLUGIN_SYSTEM_PATH_1_0": "",
		"QT_PLUGIN_PATH":             root,
	}
	if rtpDir != "" {
		// The fixed UxPlay executable lives in the base bundle. The RTP
		// supplement is a DLL/plugin-only sibling bundle and must be first in
		// the child search paths so the API-started process actually uses it.
		environment["IMAGEPAD_UXPLAY_RTP_DIR"] = rtpDir
	}
	cmd.Env = replaceEnvironment(os.Environ(), environment)
	if !isUxPlayWindowsWrapper(receiverPath) {
		return nil, nil
	}
	return configureUxPlayWindowsArguments(cmd, sessionDir)
}

func uxPlayRuntimeDirectories(root string) (runtimeRoots, pluginDirs []string, rtpDir string) {
	runtimeRoots = []string{root}
	pluginDirs = []string{filepath.Join(root, "lib", "gstreamer-1.0")}
	supplement := root + "-rtp"
	if !hasUxPlayRTPSupplement(supplement) {
		return runtimeRoots, pluginDirs, ""
	}
	rtpDir = supplement
	runtimeRoots = append([]string{supplement}, runtimeRoots...)
	pluginDirs = append([]string{filepath.Join(supplement, "lib", "gstreamer-1.0")}, pluginDirs...)
	return runtimeRoots, pluginDirs, rtpDir
}

func hasUxPlayRTPSupplement(root string) bool {
	for _, relative := range []string{
		filepath.Join("lib", "gstreamer-1.0", "libgstrtp.dll"),
		filepath.Join("lib", "gstreamer-1.0", "libgstudp.dll"),
		"libgstnet-1.0-0.dll",
		"libgstrtp-1.0-0.dll",
	} {
		info, err := os.Stat(filepath.Join(root, relative))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func isUxPlayWindowsWrapper(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return name == "uxplay-windows.exe" || name == "uxplay-windows"
}

func configureUxPlayWindowsArguments(cmd *exec.Cmd, sessionDir string) (func() error, error) {
	if strings.TrimSpace(sessionDir) == "" {
		return nil, fmt.Errorf("UxPlay wrapper session directory is empty")
	}
	programData := filepath.Join(sessionDir, "uxplay-programdata")
	appData := filepath.Join(sessionDir, "uxplay-appdata")
	arguments := serializeUxPlayArguments(cmd.Args[1:])
	// Newer uxplay-windows builds prefer their machine configuration and
	// obtain ProgramData directly from the child environment. Write both
	// isolated locations so those builds never touch the user configuration.
	argumentsPaths := []string{
		filepath.Join(programData, "uxplay-windows", "arguments.txt"),
		filepath.Join(appData, "leapbtw", "uxplay-windows", "arguments.txt"),
	}
	for _, argumentsPath := range argumentsPaths {
		if err := os.MkdirAll(filepath.Dir(argumentsPath), 0700); err != nil {
			return nil, fmt.Errorf("create UxPlay wrapper configuration directory: %w", err)
		}
		if err := os.WriteFile(argumentsPath, []byte(arguments), 0600); err != nil {
			return nil, fmt.Errorf("write UxPlay wrapper arguments: %w", err)
		}
	}
	cmd.Env = replaceEnvironment(cmd.Env, map[string]string{
		"APPDATA":      appData,
		"LOCALAPPDATA": filepath.Join(sessionDir, "uxplay-localappdata"),
		"ProgramData":  programData,
	})

	// Release 2.0.0.1736 resolves QStandardPaths::AppDataLocation through the
	// Windows known-folder API, ignoring the child APPDATA override. Replace
	// that per-user file only for the short process-start window and return a
	// mandatory restore function. Never leave the user setting overwritten.
	userAppData := strings.TrimSpace(os.Getenv("APPDATA"))
	if userAppData == "" {
		return nil, fmt.Errorf("APPDATA is empty; cannot configure UxPlay wrapper")
	}
	userArgumentsPath := filepath.Join(userAppData, "leapbtw", "uxplay-windows", "arguments.txt")
	if err := os.MkdirAll(filepath.Dir(userArgumentsPath), 0700); err != nil {
		return nil, fmt.Errorf("create UxPlay user configuration directory: %w", err)
	}
	previous, readErr := os.ReadFile(userArgumentsPath)
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("read existing UxPlay user configuration: %w", readErr)
	}
	previousMode := os.FileMode(0600)
	if existed {
		if info, err := os.Stat(userArgumentsPath); err == nil {
			previousMode = info.Mode().Perm()
		}
	}
	if err := os.WriteFile(userArgumentsPath, []byte(arguments), 0600); err != nil {
		return nil, fmt.Errorf("write temporary UxPlay user configuration: %w", err)
	}
	restore := func() error {
		if existed {
			if err := os.WriteFile(userArgumentsPath, previous, previousMode); err != nil {
				return fmt.Errorf("restore UxPlay user configuration: %w", err)
			}
			return nil
		}
		if err := os.Remove(userArgumentsPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove temporary UxPlay user configuration: %w", err)
		}
		return nil
	}
	return restore, nil
}

func serializeUxPlayArguments(args []string) string {
	return strings.Join(args, " ")
}

func replaceEnvironment(environment []string, updates map[string]string) []string {
	type environmentUpdate struct {
		key   string
		value string
	}
	normalized := make(map[string]environmentUpdate, len(updates))
	for key, value := range updates {
		normalized[strings.ToUpper(key)] = environmentUpdate{key: key, value: value}
	}
	result := make([]string, 0, len(environment)+len(updates))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			result = append(result, entry)
			continue
		}
		if _, replace := normalized[strings.ToUpper(key)]; replace {
			continue
		}
		result = append(result, entry)
	}
	for _, update := range normalized {
		result = append(result, update.key+"="+update.value)
	}
	return result
}

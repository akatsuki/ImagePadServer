//go:build windows

package airplay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func configureGStreamerBridgeProcess(cmd *exec.Cmd, output *limitedBuffer, bridgePath string) {
	// Stdout is the binary Matroska transport and is attached by
	// startGStreamerBridgeProcess with StdoutPipe. Only stderr may be routed to
	// the diagnostic buffer here.
	cmd.Stderr = output
	hideProcessWindow(cmd)
	root := filepath.Dir(bridgePath)
	runtimeRoots, pluginDirs, rtpDir := uxPlayRuntimeDirectories(root)
	if os.Getenv(envAllowInheritedGStreamer) == "1" {
		if versionedPluginPath, configured := os.LookupEnv("GST_PLUGIN_PATH_1_0"); configured {
			pluginDirs = appendUniquePathList(pluginDirs, versionedPluginPath)
		} else {
			pluginDirs = appendUniquePathList(pluginDirs, os.Getenv("GST_PLUGIN_PATH"))
		}
	}
	pathEntries := append([]string{}, runtimeRoots...)
	// The official GStreamer Windows runtime keeps its import DLLs in bin,
	// while the bridge executable is deployed at the bundle root.
	pathEntries = append(pathEntries, filepath.Join(root, "bin"))
	pathEntries = append(pathEntries, filepath.Join(root, "lib"))
	updates := map[string]string{
		"PATH":                       strings.Join(pathEntries, string(os.PathListSeparator)),
		"GST_PLUGIN_PATH":            strings.Join(pluginDirs, string(os.PathListSeparator)),
		"GST_PLUGIN_PATH_1_0":        strings.Join(pluginDirs, string(os.PathListSeparator)),
		"GST_PLUGIN_SYSTEM_PATH":     "",
		"GST_PLUGIN_SYSTEM_PATH_1_0": "",
	}
	if os.Getenv("IMAGEPAD_AIRPLAY_RTP_DEBUG") == "1" {
		updates["GST_DEBUG"] = "2"
		updates["GST_DEBUG_NO_COLOR"] = "1"
	}
	if scanner := gstreamerScannerPath(root, runtimeRoots); scanner != "" {
		updates["GST_PLUGIN_SCANNER"] = scanner
	}
	if registry := gstreamerRegistryPath(); registry != "" {
		updates["GST_REGISTRY"] = registry
	}
	if rtpDir != "" {
		updates["IMAGEPAD_UXPLAY_RTP_DIR"] = rtpDir
	}
	cmd.Env = replaceEnvironment(os.Environ(), updates)
	cmd.Dir = root
}

func appendUniquePathList(entries []string, raw string) []string {
	for _, candidate := range filepath.SplitList(raw) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		duplicate := false
		for _, existing := range entries {
			if strings.EqualFold(filepath.Clean(existing), filepath.Clean(candidate)) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			entries = append(entries, candidate)
		}
	}
	return entries
}

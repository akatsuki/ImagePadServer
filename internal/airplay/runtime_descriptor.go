package airplay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"imagepadserver/internal/settings"
)

const (
	envAirPlayRuntimeRoot = "IMAGEPAD_AIRPLAY_RUNTIME_ROOT"
	runtimeDescriptorName = "imagepad-airplay-runtime.json"
	runtimeArchitecture   = "windows-amd64"
	runtimeProtocol       = 1
)

// RuntimeDescriptor describes one compatible AirPlay runtime set.  The paths
// are relative to Root so receiver, bridge, and GStreamer cannot be selected
// independently from different installations.
type RuntimeDescriptor struct {
	Schema           int    `json:"schema"`
	RuntimeSetID     string `json:"runtimeSetID"`
	Architecture     string `json:"architecture"`
	ProtocolVersion  int    `json:"protocolVersion"`
	Receiver         string `json:"receiver"`
	Bridge           string `json:"bridge"`
	GStreamerRoot    string `json:"gstreamerRoot"`
	GStreamerVersion string `json:"gstreamerVersion,omitempty"`
	CapabilityProbe  string `json:"capabilityProbe"`
	LicenseManifest  string `json:"licenseManifest"`
	SourceOffer      string `json:"sourceOffer"`
	ArchiveSHA256    string `json:"archiveSha256,omitempty"`
	ArchiveSize      int64  `json:"archiveSize,omitempty"`
}

// AirPlayRuntime is an immutable, already validated view of one runtime set.
type AirPlayRuntime struct {
	Descriptor      RuntimeDescriptor
	Root            string
	ReceiverPath    string
	BridgePath      string
	GStreamerRoot   string
	CapabilityProbe string
}

func (d RuntimeDescriptor) validate() error {
	if d.Schema != 1 {
		return fmt.Errorf("AirPlay runtime descriptor schema %d is unsupported", d.Schema)
	}
	if strings.TrimSpace(d.RuntimeSetID) == "" {
		return errors.New("AirPlay runtime descriptor runtimeSetID is empty")
	}
	if d.Architecture != runtimeArchitecture || runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("AirPlay runtime architecture is not supported: %s", d.Architecture)
	}
	if d.ProtocolVersion != runtimeProtocol {
		return fmt.Errorf("AirPlay runtime protocol version %d is unsupported", d.ProtocolVersion)
	}
	for field, value := range map[string]string{
		"receiver":        d.Receiver,
		"bridge":          d.Bridge,
		"gstreamerRoot":   d.GStreamerRoot,
		"capabilityProbe": d.CapabilityProbe,
		"licenseManifest": d.LicenseManifest,
		"sourceOffer":     d.SourceOffer,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("AirPlay runtime descriptor %s is empty", field)
		}
		clean := filepath.Clean(value)
		if strings.Contains(value, ":") || filepath.IsAbs(value) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("AirPlay runtime descriptor %s is not a relative path", field)
		}
	}
	return nil
}

func loadRuntimeDescriptor(root string) (RuntimeDescriptor, error) {
	if strings.TrimSpace(root) == "" {
		return RuntimeDescriptor{}, errors.New("AirPlay runtime root is empty")
	}
	data, err := os.ReadFile(filepath.Join(root, runtimeDescriptorName))
	if err != nil {
		return RuntimeDescriptor{}, fmt.Errorf("read AirPlay runtime descriptor: %w", err)
	}
	var descriptor RuntimeDescriptor
	if err := json.Unmarshal(data, &descriptor); err != nil {
		return RuntimeDescriptor{}, fmt.Errorf("parse AirPlay runtime descriptor: %w", err)
	}
	if err := descriptor.validate(); err != nil {
		return RuntimeDescriptor{}, err
	}
	return descriptor, nil
}

func resolveRuntimeFile(root, relative, label string) (string, error) {
	clean := filepath.Clean(relative)
	if filepath.IsAbs(relative) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("AirPlay runtime %s path escapes runtime root", label)
	}
	path := filepath.Join(root, clean)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("AirPlay runtime %s is missing: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("AirPlay runtime %s must not be a symlink", label)
	}
	if !info.Mode().IsRegular() && label != "gstreamer root" {
		return "", fmt.Errorf("AirPlay runtime %s is not a regular file", label)
	}
	if label == "gstreamer root" && !info.IsDir() {
		return "", fmt.Errorf("AirPlay runtime %s is not a directory", label)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve AirPlay runtime %s: %w", label, err)
	}
	return abs, nil
}

func resolveAirPlayRuntime(root string) (AirPlayRuntime, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return AirPlayRuntime{}, fmt.Errorf("resolve AirPlay runtime root: %w", err)
	}
	descriptor, err := loadRuntimeDescriptor(absRoot)
	if err != nil {
		return AirPlayRuntime{}, err
	}
	receiver, err := resolveRuntimeFile(absRoot, descriptor.Receiver, "receiver")
	if err != nil {
		return AirPlayRuntime{}, err
	}
	bridge, err := resolveRuntimeFile(absRoot, descriptor.Bridge, "bridge")
	if err != nil {
		return AirPlayRuntime{}, err
	}
	gstreamer, err := resolveRuntimeFile(absRoot, descriptor.GStreamerRoot, "gstreamer root")
	if err != nil {
		return AirPlayRuntime{}, err
	}
	probe, err := resolveRuntimeFile(absRoot, descriptor.CapabilityProbe, "capability probe")
	if err != nil {
		return AirPlayRuntime{}, err
	}
	for label, relative := range map[string]string{
		"license manifest": descriptor.LicenseManifest,
		"source offer":     descriptor.SourceOffer,
	} {
		if _, err := resolveRuntimeFile(absRoot, relative, label); err != nil {
			return AirPlayRuntime{}, err
		}
	}
	return AirPlayRuntime{
		Descriptor:      descriptor,
		Root:            absRoot,
		ReceiverPath:    receiver,
		BridgePath:      bridge,
		GStreamerRoot:   gstreamer,
		CapabilityProbe: probe,
	}, nil
}

// ResolvePinnedAirPlayRuntime resolves only a complete descriptor-backed set.
// It never consults PATH and never downloads. A caller may provide a test or
// administrator-selected root with IMAGEPAD_AIRPLAY_RUNTIME_ROOT.
func ResolvePinnedAirPlayRuntime() (AirPlayRuntime, error) {
	if raw := strings.TrimSpace(os.Getenv(envAirPlayRuntimeRoot)); raw != "" {
		return resolveAirPlayRuntime(raw)
	}
	if executable, err := os.Executable(); err == nil {
		if base, absErr := filepath.Abs(filepath.Dir(executable)); absErr == nil {
			if runtimeSet, resolveErr := resolveAirPlayRuntime(filepath.Join(base, "airplay")); resolveErr == nil {
				return runtimeSet, nil
			}
		}
	}
	if runtimeSet, err := resolveInstalledAirPlayRuntime(); err == nil {
		return runtimeSet, nil
	}
	return AirPlayRuntime{}, errors.New("verified AirPlay runtime set is not installed")
}

// resolveInstalledAirPlayRuntime resolves the immutable runtime set selected
// by the active marker in the per-user data directory. Startup provisioning
// installs there after downloading the descriptor-pinned archive; subsequent
// receiver/bridge lookups must use the same set instead of checking only the
// executable directory.
func resolveInstalledAirPlayRuntime() (AirPlayRuntime, error) {
	dataDir := strings.TrimSpace(settings.Dir())
	if dataDir == "" {
		return AirPlayRuntime{}, errors.New("ImagePadServer data directory is empty")
	}
	root := filepath.Join(dataDir, "runtimes", "airplay")
	marker, err := os.ReadFile(filepath.Join(root, runtimeActiveFile))
	if err != nil {
		return AirPlayRuntime{}, err
	}
	setID := strings.TrimSpace(string(marker))
	if setID == "" || strings.ContainsAny(setID, `/\\:`) || setID == "." || setID == ".." {
		return AirPlayRuntime{}, errors.New("AirPlay active runtime set is invalid")
	}
	return resolveAirPlayRuntime(filepath.Join(root, setID))
}

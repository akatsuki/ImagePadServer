package nicorender

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const timelinePayloadWGPUVersion = "0.20.1"

const (
	timelinePayloadCleanupAttempts = 40
	timelinePayloadCleanupDelay    = 25 * time.Millisecond
)

type timelinePayloadManifest struct {
	Schema          int             `json:"schema"`
	Protocol        string          `json:"protocol"`
	ProtocolVersion json.RawMessage `json:"protocolVersion,omitempty"`
	InputMode       json.RawMessage `json:"inputMode,omitempty"`
	OutputFormat    json.RawMessage `json:"outputFormat,omitempty"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	OS              string          `json:"os"`
	Architecture    string          `json:"architecture"`
	SHA256          string          `json:"sha256"`
	WGPUVersion     string          `json:"wgpuVersion"`
	CargoTarget     string          `json:"cargoTarget"`
}

var (
	timelinePayloadSupportOnce sync.Once
	timelinePayloadNCT2Support bool
)

// EmbeddedTimelineCompositorSupportsNCT2 reports whether this build contains
// a hash-valid NCT2 compositor for the current OS and architecture. It does
// not start the helper or require a working GPU adapter.
func EmbeddedTimelineCompositorSupportsNCT2() bool {
	timelinePayloadSupportOnce.Do(func() {
		payload, manifestJSON := timelinePayload()
		manifest, err := validateTimelinePayloadManifest(manifestJSON, payload, runtime.GOOS, runtime.GOARCH)
		if err != nil || manifest.Protocol != "NCT2" {
			return
		}
		timelinePayloadNCT2Support = validateTimelineExecutable(payload, runtime.GOOS, runtime.GOARCH) == nil
	})
	return timelinePayloadNCT2Support
}

func validateTimelinePayloadManifest(manifestJSON, payload []byte, goos, goarch string) (timelinePayloadManifest, error) {
	var manifest timelinePayloadManifest
	if len(manifestJSON) == 0 || len(manifestJSON) > 16*1024 {
		return manifest, fmt.Errorf("manifest size %d is outside 1..16384 bytes", len(manifestJSON))
	}
	if err := rejectDuplicateTimelineManifestKeys(manifestJSON); err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return manifest, fmt.Errorf("manifest has trailing JSON data")
	}
	if manifest.Schema != 1 {
		return manifest, fmt.Errorf("unsupported manifest schema=%d", manifest.Schema)
	}
	switch manifest.Protocol {
	case "NCT1":
		if len(manifest.ProtocolVersion) != 0 || len(manifest.InputMode) != 0 || len(manifest.OutputFormat) != 0 || len(manifest.Capabilities) != 0 {
			return manifest, fmt.Errorf("NCT1 manifest must not declare NCT2 fields")
		}
	case "NCT2":
		if err := validateTimelinePayloadNCT2Fields(manifest); err != nil {
			return manifest, err
		}
	default:
		return manifest, fmt.Errorf("unsupported manifest protocol %q", manifest.Protocol)
	}
	if manifest.OS != goos || manifest.Architecture != goarch {
		return manifest, fmt.Errorf("manifest target %s/%s does not match runtime %s/%s", manifest.OS, manifest.Architecture, goos, goarch)
	}
	if !timelineCargoTargetMatches(manifest.CargoTarget, goos, goarch) {
		return manifest, fmt.Errorf("manifest Rust target %q does not match runtime %s/%s", manifest.CargoTarget, goos, goarch)
	}
	if manifest.WGPUVersion != timelinePayloadWGPUVersion {
		return manifest, fmt.Errorf("manifest wgpu version %q does not match pinned %q", manifest.WGPUVersion, timelinePayloadWGPUVersion)
	}
	if strings.TrimSpace(manifest.CargoTarget) == "" {
		return manifest, fmt.Errorf("manifest cargoTarget is empty")
	}
	digest, err := hex.DecodeString(manifest.SHA256)
	if err != nil || len(digest) != sha256.Size || !strings.EqualFold(manifest.SHA256, fmt.Sprintf("%x", sha256.Sum256(payload))) {
		return manifest, fmt.Errorf("timeline payload SHA-256 mismatch")
	}
	return manifest, nil
}

func rejectDuplicateTimelineManifestKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return fmt.Errorf("manifest must be a JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode manifest field: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("manifest object contains a non-string key")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("manifest contains duplicate field %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode manifest field %q: %w", key, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode manifest object end: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("manifest has trailing JSON data")
	}
	return nil
}

func validateTimelinePayloadNCT2Fields(manifest timelinePayloadManifest) error {
	if len(manifest.ProtocolVersion) == 0 || string(manifest.ProtocolVersion) == "null" {
		return fmt.Errorf("NCT2 manifest protocolVersion is required")
	}
	var protocolVersion int
	if err := json.Unmarshal(manifest.ProtocolVersion, &protocolVersion); err != nil || protocolVersion != 2 {
		return fmt.Errorf("unsupported NCT2 manifest protocolVersion %s", manifest.ProtocolVersion)
	}
	var inputMode string
	if len(manifest.InputMode) == 0 || string(manifest.InputMode) == "null" || json.Unmarshal(manifest.InputMode, &inputMode) != nil || inputMode != "--stdin-stream" {
		return fmt.Errorf("unsupported NCT2 manifest inputMode %s", manifest.InputMode)
	}
	var outputFormat string
	if len(manifest.OutputFormat) == 0 || string(manifest.OutputFormat) == "null" || json.Unmarshal(manifest.OutputFormat, &outputFormat) != nil || outputFormat != "rgba8" {
		return fmt.Errorf("unsupported NCT2 manifest outputFormat %s", manifest.OutputFormat)
	}
	if len(manifest.Capabilities) == 0 || string(manifest.Capabilities) == "null" {
		return fmt.Errorf("NCT2 manifest capabilities are required")
	}
	var capabilities []string
	if err := json.Unmarshal(manifest.Capabilities, &capabilities); err != nil {
		return fmt.Errorf("decode NCT2 manifest capabilities: %w", err)
	}
	required := map[string]bool{
		"incremental-assets": false,
		"ordered-elements":   false,
		"watermarks":         false,
		"end-and-eof":        false,
	}
	for _, capability := range capabilities {
		if _, knownRequired := required[capability]; knownRequired {
			required[capability] = true
		}
	}
	for capability, present := range required {
		if !present {
			return fmt.Errorf("NCT2 manifest is missing required capability %q", capability)
		}
	}
	return nil
}

func timelineCargoTargetMatches(target, goos, goarch string) bool {
	allowed := map[string]map[string]bool{
		"windows/amd64": {"x86_64-pc-windows-msvc": true, "x86_64-pc-windows-gnu": true},
		"windows/arm64": {"aarch64-pc-windows-msvc": true},
		"darwin/amd64":  {"x86_64-apple-darwin": true},
		"darwin/arm64":  {"aarch64-apple-darwin": true},
		"linux/amd64":   {"x86_64-unknown-linux-gnu": true, "x86_64-unknown-linux-musl": true},
		"linux/arm64":   {"aarch64-unknown-linux-gnu": true, "aarch64-unknown-linux-musl": true},
	}
	return allowed[goos+"/"+goarch][target]
}

func materializeTimelinePayload(payload, manifestJSON []byte, goos, goarch string) (string, func(), error) {
	if _, err := validateTimelinePayloadManifest(manifestJSON, payload, goos, goarch); err != nil {
		return "", nil, err
	}
	if err := validateTimelineExecutable(payload, goos, goarch); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "imagepad-nico-timeline-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { cleanupTimelinePayloadDirectory(dir) }
	name := "nico-compositord"
	if goos == "windows" {
		name += ".exe"
	}
	tmpPath := filepath.Join(dir, name+".tmp")
	path := filepath.Join(dir, name)
	if err := os.WriteFile(tmpPath, payload, 0700); err != nil {
		cleanup()
		return "", nil, err
	}
	staged, err := os.ReadFile(tmpPath)
	if err != nil || sha256.Sum256(staged) != sha256.Sum256(payload) {
		cleanup()
		return "", nil, fmt.Errorf("timeline payload write verification failed: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

func cleanupTimelinePayloadDirectory(dir string) {
	var lastErr error
	for attempt := 0; attempt < timelinePayloadCleanupAttempts; attempt++ {
		lastErr = os.RemoveAll(dir)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		} else if err != nil {
			lastErr = err
		}
		if attempt+1 < timelinePayloadCleanupAttempts {
			time.Sleep(timelinePayloadCleanupDelay)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("directory still exists after %d attempts", timelinePayloadCleanupAttempts)
	}
	log.Printf("niconico: temporary timeline payload cleanup for %q: %v", dir, lastErr)
}

func validateTimelineExecutable(payload []byte, goos, goarch string) error {
	switch goos {
	case "windows":
		file, err := pe.NewFile(bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("timeline payload PE: %w", err)
		}
		machine := file.Machine
		_ = file.Close()
		want := map[string]uint16{"386": pe.IMAGE_FILE_MACHINE_I386, "amd64": pe.IMAGE_FILE_MACHINE_AMD64, "arm64": pe.IMAGE_FILE_MACHINE_ARM64}[goarch]
		if want == 0 || machine != want {
			return fmt.Errorf("timeline payload PE architecture mismatch: got %#x for %s", machine, goarch)
		}
	case "linux":
		file, err := elf.NewFile(bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("timeline payload ELF: %w", err)
		}
		machine := file.Machine
		_ = file.Close()
		want := map[string]elf.Machine{"386": elf.EM_386, "amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[goarch]
		if want == elf.EM_NONE || machine != want {
			return fmt.Errorf("timeline payload ELF architecture mismatch: got %s for %s", machine, goarch)
		}
	case "darwin":
		file, err := macho.NewFile(bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("timeline payload Mach-O: %w", err)
		}
		cpu := file.Cpu
		_ = file.Close()
		want := map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}[goarch]
		if want == 0 || cpu != want {
			return fmt.Errorf("timeline payload Mach-O architecture mismatch: got %s for %s", cpu, goarch)
		}
	default:
		return fmt.Errorf("timeline payload is unsupported on %s", goos)
	}
	return nil
}

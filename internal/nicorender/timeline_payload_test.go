package nicorender

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func timelineTestManifest(payload []byte, mutate func(*timelinePayloadManifest)) []byte {
	manifest := timelinePayloadManifest{
		Schema:       1,
		Protocol:     "NCT1",
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		SHA256:       "",
		WGPUVersion:  timelinePayloadWGPUVersion,
		CargoTarget:  timelineTestCargoTarget(),
	}
	digest := sha256.Sum256(payload)
	manifest.SHA256 = hex.EncodeToString(digest[:])
	if mutate != nil {
		mutate(&manifest)
	}
	data, _ := json.Marshal(manifest)
	return data
}

func timelineTestNCT2Manifest(payload []byte, mutate func(map[string]any)) []byte {
	digest := sha256.Sum256(payload)
	manifest := map[string]any{
		"schema":          1,
		"protocol":        "NCT2",
		"protocolVersion": 2,
		"inputMode":       "--stdin-stream",
		"outputFormat":    "rgba8",
		"capabilities": []string{
			"incremental-assets",
			"ordered-elements",
			"watermarks",
			"end-and-eof",
		},
		"os":           runtime.GOOS,
		"architecture": runtime.GOARCH,
		"sha256":       hex.EncodeToString(digest[:]),
		"wgpuVersion":  timelinePayloadWGPUVersion,
		"cargoTarget":  timelineTestCargoTarget(),
	}
	if mutate != nil {
		mutate(manifest)
	}
	data, _ := json.Marshal(manifest)
	return data
}

func timelineTestCargoTarget() string {
	return map[string]string{
		"windows/amd64": "x86_64-pc-windows-msvc",
		"windows/arm64": "aarch64-pc-windows-msvc",
		"darwin/amd64":  "x86_64-apple-darwin",
		"darwin/arm64":  "aarch64-apple-darwin",
		"linux/amd64":   "x86_64-unknown-linux-gnu",
		"linux/arm64":   "aarch64-unknown-linux-gnu",
	}[runtime.GOOS+"/"+runtime.GOARCH]
}

func TestValidateTimelinePayloadManifestRejectsTamperingAndTargetMismatch(t *testing.T) {
	if timelineTestCargoTarget() == "" {
		t.Skipf("no timeline helper target is defined for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	payload, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	valid := timelineTestManifest(payload, nil)
	if _, err := validateTimelinePayloadManifest(valid, payload, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		data   []byte
		binary []byte
		os     string
		arch   string
	}{
		{name: "hash", data: timelineTestManifest(payload, func(m *timelinePayloadManifest) { m.SHA256 = strings.Repeat("0", 64) }), binary: payload, os: runtime.GOOS, arch: runtime.GOARCH},
		{name: "wrong OS", data: timelineTestManifest(payload, func(m *timelinePayloadManifest) { m.OS = "unsupported-os" }), binary: payload, os: runtime.GOOS, arch: runtime.GOARCH},
		{name: "wrong architecture", data: timelineTestManifest(payload, func(m *timelinePayloadManifest) { m.Architecture = "unsupported-arch" }), binary: payload, os: runtime.GOOS, arch: runtime.GOARCH},
		{name: "schema", data: timelineTestManifest(payload, func(m *timelinePayloadManifest) { m.Schema = 2 }), binary: payload, os: runtime.GOOS, arch: runtime.GOARCH},
		{name: "wrong protocol", data: timelineTestManifest(payload, func(m *timelinePayloadManifest) { m.Protocol = "NPS3" }), binary: payload, os: runtime.GOOS, arch: runtime.GOARCH},
		{name: "wrong WGPU version", data: timelineTestManifest(payload, func(m *timelinePayloadManifest) { m.WGPUVersion = "0.21.0" }), binary: payload, os: runtime.GOOS, arch: runtime.GOARCH},
		{name: "wrong Rust target", data: timelineTestManifest(payload, func(m *timelinePayloadManifest) { m.CargoTarget = "unknown-target" }), binary: payload, os: runtime.GOOS, arch: runtime.GOARCH},
		{name: "binary modified", data: valid, binary: append(append([]byte(nil), payload...), 0), os: runtime.GOOS, arch: runtime.GOARCH},
		{name: "runtime target mismatch", data: valid, binary: payload, os: "linux", arch: "arm64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateTimelinePayloadManifest(tc.data, tc.binary, tc.os, tc.arch); err == nil {
				t.Fatal("accepted an invalid timeline payload manifest")
			}
		})
	}
	unknown := append([]byte(nil), valid[:len(valid)-1]...)
	unknown = append(unknown, []byte(`,"tampered":true}`)...)
	if _, err := validateTimelinePayloadManifest(unknown, payload, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("accepted unknown manifest fields")
	}
	for _, trailing := range [][]byte{append(append([]byte(nil), valid...), []byte(` {}`)...), append(append([]byte(nil), valid...), 'x')} {
		if _, err := validateTimelinePayloadManifest(trailing, payload, runtime.GOOS, runtime.GOARCH); err == nil {
			t.Fatal("accepted trailing manifest data")
		}
	}
}

func TestValidateTimelinePayloadManifestAcceptsStrictNCT2AndRejectsInvalidVariants(t *testing.T) {
	if timelineTestCargoTarget() == "" {
		t.Skipf("no timeline helper target is defined for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	payload := []byte("bounded timeline helper payload")
	valid := timelineTestNCT2Manifest(payload, nil)
	if _, err := validateTimelinePayloadManifest(valid, payload, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("valid NCT2 manifest rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing protocol version", mutate: func(m map[string]any) { delete(m, "protocolVersion") }},
		{name: "wrong protocol version", mutate: func(m map[string]any) { m["protocolVersion"] = 3 }},
		{name: "missing input mode", mutate: func(m map[string]any) { delete(m, "inputMode") }},
		{name: "wrong input mode", mutate: func(m map[string]any) { m["inputMode"] = "--stdin" }},
		{name: "missing output format", mutate: func(m map[string]any) { delete(m, "outputFormat") }},
		{name: "wrong output format", mutate: func(m map[string]any) { m["outputFormat"] = "bgra8" }},
		{name: "missing capability", mutate: func(m map[string]any) {
			m["capabilities"] = []string{"incremental-assets", "ordered-elements", "watermarks"}
		}},
		{name: "wrong target OS", mutate: func(m map[string]any) { m["os"] = "unsupported-os" }},
		{name: "wrong target architecture", mutate: func(m map[string]any) { m["architecture"] = "unsupported-arch" }},
		{name: "wrong cargo target", mutate: func(m map[string]any) { m["cargoTarget"] = "unknown-target" }},
		{name: "wrong wgpu pin", mutate: func(m map[string]any) { m["wgpuVersion"] = "0.21.0" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := timelineTestNCT2Manifest(payload, tc.mutate)
			if _, err := validateTimelinePayloadManifest(data, payload, runtime.GOOS, runtime.GOARCH); err == nil {
				t.Fatal("accepted invalid NCT2 manifest")
			}
		})
	}

	unknown := append([]byte(nil), valid[:len(valid)-1]...)
	unknown = append(unknown, []byte(`,"unexpected":true}`)...)
	if _, err := validateTimelinePayloadManifest(unknown, payload, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("accepted unknown NCT2 manifest field")
	}
	duplicate := append([]byte(nil), valid[:len(valid)-1]...)
	duplicate = append(duplicate, []byte(`,"protocolVersion":2}`)...)
	if _, err := validateTimelinePayloadManifest(duplicate, payload, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("accepted duplicate NCT2 manifest field")
	}
	trailing := append(append([]byte(nil), valid...), []byte(` {}`)...)
	if _, err := validateTimelinePayloadManifest(trailing, payload, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("accepted trailing NCT2 JSON")
	}
	hashMismatch := timelineTestNCT2Manifest(payload, func(m map[string]any) { m["sha256"] = strings.Repeat("0", 64) })
	if _, err := validateTimelinePayloadManifest(hashMismatch, payload, runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("accepted NCT2 binary hash mismatch")
	}
}

func TestTimelineExecutableMatchesManifestPlatform(t *testing.T) {
	payload, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTimelineExecutable(payload, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatalf("test executable did not match current platform: %v", err)
	}
	if err := validateTimelineExecutable([]byte("not executable"), runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("accepted non-executable payload")
	}
}

func TestMaterializeTimelinePayloadVerifiesHashAndCleansUp(t *testing.T) {
	if timelineTestCargoTarget() == "" {
		t.Skipf("no timeline helper target is defined for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	payload, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	manifest := timelineTestManifest(payload, nil)
	path, cleanup, err := materializeTimelinePayload(payload, manifest, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup == nil {
		t.Fatal("materializer returned no cleanup")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gotHash, wantHash := sha256.Sum256(got), sha256.Sum256(payload)
	if gotHash != wantHash {
		t.Fatal("materialized payload hash changed")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("materialized payload is not a regular file: %v", err)
	}
	dir := filepath.Dir(path)
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("materialized payload directory remains: %s err=%v", dir, err)
	}
}

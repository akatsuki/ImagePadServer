package video

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

func TestNormalizeNicoOutputModeDefaultsEmptyToSeparate(t *testing.T) {
	tests := []struct {
		name string
		in   NicoOutputMode
		want NicoOutputMode
	}{
		{name: "empty", in: "", want: NicoOutputSeparate},
		{name: "separate", in: NicoOutputSeparate, want: NicoOutputSeparate},
		{name: "tee", in: NicoOutputTee, want: NicoOutputTee},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeNicoOutputMode(tt.in)
			if err != nil {
				t.Fatalf("NormalizeNicoOutputMode(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeNicoOutputMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeNicoOutputModeRejectsUnknown(t *testing.T) {
	if _, err := NormalizeNicoOutputMode("sidecar"); err == nil || !strings.Contains(err.Error(), "output mode") {
		t.Fatalf("unknown output mode error = %v", err)
	}
}

func TestValidateNicoOutputStagingRejectsExistingArtifactsAndOverlap(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	staging := filepath.Join(dir, "run")
	output := filepath.Join(staging, "rendered.mp4")
	hlsDir := filepath.Join(staging, "hls")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}

	if err := ValidateNicoOutputStaging(source, output, hlsDir); err != nil {
		t.Fatalf("new staging = %v", err)
	}
	missingParent := filepath.Join(dir, "new-run")
	if err := ValidateNicoOutputStaging(source, filepath.Join(missingParent, "rendered.mp4"), filepath.Join(missingParent, "hls")); err != nil {
		t.Fatalf("new staging parent may be created by the worker = %v", err)
	}

	if err := os.WriteFile(output, []byte("existing-mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNicoOutputStaging(source, output, hlsDir); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("existing output error = %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing-mp4" {
		t.Fatalf("existing output changed to %q", data)
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(hlsDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(hlsDir, "existing.ts")
	if err := os.WriteFile(marker, []byte("existing-hls"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNicoOutputStaging(source, output, hlsDir); err == nil || !strings.Contains(err.Error(), "HLS") {
		t.Fatalf("existing HLS staging error = %v", err)
	}
	data, err = os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing-hls" {
		t.Fatalf("existing HLS staging changed to %q", data)
	}
	if err := os.RemoveAll(hlsDir); err != nil {
		t.Fatal(err)
	}

	if err := ValidateNicoOutputStaging(source, source, hlsDir); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("source/output overlap error = %v", err)
	}
	otherHLS := filepath.Join(dir, "other", "hls")
	if err := ValidateNicoOutputStaging(source, output, otherHLS); err == nil || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("split staging error = %v", err)
	}
}

func TestExportNicoCommentedRejectsExistingStagingBeforeEncoder(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "rendered.mp4")
	hlsDir := filepath.Join(dir, "hls")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("existing-mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := ExportNicoCommented(
		context.Background(), "missing-ffmpeg", source, output, hlsDir,
		niconico.Snapshot{}, nicorender.RenderOptions{}, NicoEncodeOptions{}, NicoOutputTee,
	)
	if err == nil || !strings.Contains(err.Error(), "output MP4 already exists") {
		t.Fatalf("existing output error = %v", err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "existing-mp4" {
		t.Fatalf("existing output changed to %q", data)
	}
}

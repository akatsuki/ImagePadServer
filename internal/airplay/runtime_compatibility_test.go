package airplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSourceClockCapabilityFixture(t *testing.T, receiver string, features []string) {
	t.Helper()
	data := []byte("source-clock receiver fixture")
	if err := os.WriteFile(receiver, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	manifest, err := json.Marshal(sourceClockCapabilities{
		Schema:          1,
		ProtocolVersion: 1,
		Binary:          filepath.Base(receiver),
		BinarySHA256:    hex.EncodeToString(digest[:]),
		Features:        features,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(receiver), "imagepad-source-clock-capabilities.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareOnStartupPreflightsExplicitSourceClockReceiver(t *testing.T) {
	root := t.TempDir()
	receiver := filepath.Join(root, "uxplay.exe")
	writeSourceClockCapabilityFixture(t, receiver, []string{
		"video-au", "audio-frame", "remote-ntp", "bounded-writer", "idle-wait",
		"audio-format-lock", "egress-metrics-v2", "video-bootstrap-reconnect",
	})
	t.Setenv(envFeatureFlag, "1")
	t.Setenv(envAirPlayPipeline, "source-clock")
	t.Setenv(envUxPlayPath, receiver)
	t.Setenv(envReceiverPath, "")
	setRuntimePreparation(RuntimePreparationStatus{State: runtimePreparationUnknown})
	t.Cleanup(func() { setRuntimePreparation(RuntimePreparationStatus{State: runtimePreparationUnknown}) })

	got, err := PrepareOnStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != receiver {
		t.Fatalf("prepared receiver = %q, want %q", got, receiver)
	}
	status := RuntimePreparation()
	if status.Compatibility.State != runtimeCompatibilityCompatible {
		t.Fatalf("compatibility state = %q, want compatible", status.Compatibility.State)
	}
}

func TestPrepareOnStartupRejectsExplicitSourceClockReceiverWithoutReconnectFeature(t *testing.T) {
	root := t.TempDir()
	receiver := filepath.Join(root, "uxplay.exe")
	writeSourceClockCapabilityFixture(t, receiver, []string{"video-au"})
	t.Setenv(envFeatureFlag, "1")
	t.Setenv(envAirPlayPipeline, "source-clock")
	t.Setenv(envUxPlayPath, receiver)
	t.Setenv(envReceiverPath, "")
	setRuntimePreparation(RuntimePreparationStatus{State: runtimePreparationUnknown})
	t.Cleanup(func() { setRuntimePreparation(RuntimePreparationStatus{State: runtimePreparationUnknown}) })

	if _, err := PrepareOnStartup(context.Background()); err == nil {
		t.Fatal("incompatible explicit receiver unexpectedly passed startup preflight")
	}
	status := RuntimePreparation()
	if status.Compatibility.State != runtimeCompatibilityIncompatible || len(status.Compatibility.MissingFeatures) == 0 {
		t.Fatalf("incompatible status = %+v", status.Compatibility)
	}
}

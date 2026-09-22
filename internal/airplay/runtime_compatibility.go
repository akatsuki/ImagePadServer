package airplay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	runtimeCompatibilityUnknown      = "unknown"
	runtimeCompatibilityCompatible   = "compatible"
	runtimeCompatibilityIncompatible = "incompatible"
	runtimeCompatibilityNotRequired  = "not-required"
)

// RuntimeCompatibilityStatus is the cheap, already-computed compatibility
// result exposed by the status endpoint. It is deliberately separate from
// runtime preparation: a downloaded/extracted set can still be incompatible
// with the source-clock receiver contract.
type RuntimeCompatibilityStatus struct {
	State           string   `json:"state,omitempty"`
	Message         string   `json:"message,omitempty"`
	MissingFeatures []string `json:"missingFeatures,omitempty"`
}

func runtimeCompatibilityNotRequiredStatus() RuntimeCompatibilityStatus {
	return RuntimeCompatibilityStatus{
		State:   runtimeCompatibilityNotRequired,
		Message: "source-clock receiver compatibility is not required for the selected pipeline",
	}
}

func inspectSourceClockReceiverCapabilities(receiverPath string) (RuntimeCompatibilityStatus, error) {
	result := RuntimeCompatibilityStatus{State: runtimeCompatibilityUnknown}
	fail := func(message string, err error) (RuntimeCompatibilityStatus, error) {
		result.State = runtimeCompatibilityIncompatible
		result.Message = message
		return result, err
	}
	if strings.TrimSpace(receiverPath) == "" {
		err := errors.New("source-clock receiver path is empty")
		return fail(err.Error(), err)
	}
	manifestPath := filepath.Join(filepath.Dir(receiverPath), "imagepad-source-clock-capabilities.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		wrapped := fmt.Errorf("source-clock receiver capability manifest is missing: %w", err)
		return fail(wrapped.Error(), wrapped)
	}
	var capabilities sourceClockCapabilities
	if err := json.Unmarshal(data, &capabilities); err != nil {
		wrapped := fmt.Errorf("parse source-clock receiver capability manifest: %w", err)
		return fail(wrapped.Error(), wrapped)
	}
	if capabilities.Schema != 1 || capabilities.ProtocolVersion != 1 ||
		capabilities.Binary != filepath.Base(receiverPath) || len(capabilities.BinarySHA256) != sha256.Size*2 {
		err := errors.New("source-clock receiver capability manifest is incompatible")
		return fail(err.Error(), err)
	}
	wantFeatures := []string{
		"video-au", "audio-frame", "remote-ntp", "bounded-writer", "idle-wait",
		"audio-format-lock", "egress-metrics-v2", "video-bootstrap-reconnect",
	}
	featureSet := make(map[string]bool, len(capabilities.Features))
	for _, feature := range capabilities.Features {
		featureSet[feature] = true
	}
	for _, feature := range wantFeatures {
		if !featureSet[feature] {
			result.MissingFeatures = append(result.MissingFeatures, feature)
		}
	}
	if len(result.MissingFeatures) != 0 {
		feature := result.MissingFeatures[0]
		err := fmt.Errorf("source-clock receiver capability %q is missing", feature)
		return fail(err.Error(), err)
	}
	file, err := os.Open(receiverPath)
	if err != nil {
		wrapped := fmt.Errorf("open source-clock receiver for capability validation: %w", err)
		return fail(wrapped.Error(), wrapped)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		wrapped := fmt.Errorf("hash source-clock receiver: %w", copyErr)
		return fail(wrapped.Error(), wrapped)
	}
	if closeErr != nil {
		wrapped := fmt.Errorf("close source-clock receiver: %w", closeErr)
		return fail(wrapped.Error(), wrapped)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), capabilities.BinarySHA256) {
		err := errors.New("source-clock receiver binary hash does not match capability manifest")
		return fail(err.Error(), err)
	}
	result.State = runtimeCompatibilityCompatible
	result.Message = "source-clock receiver capabilities and binary hash are compatible"
	return result, nil
}

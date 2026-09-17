package nicorender

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestNativePayloadRejectsCorruption(t *testing.T) {
	payload := []byte("not a PE file")
	for _, digest := range []string{"bad", fmt.Sprintf("%x", sha256.Sum256(payload))} {
		meta, _ := json.Marshal(map[string]any{"schema": 1, "protocol": "NPS3", "architecture": "amd64", "sha256": digest})
		path, cleanup, err := materializeNativePayload(payload, meta)
		if cleanup != nil {
			cleanup()
		}
		if err == nil || path != "" {
			t.Fatal("accepted corrupt/non-PE native payload")
		}
	}
}

func TestNativeRuntimeCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cleanup, err := PrepareNativeCompositor(ctx, "")
	if cleanup != nil {
		cleanup()
	}
	if err != context.Canceled {
		t.Fatalf("want canceled, got %v", err)
	}
}

func TestNativeRuntimeEmbedded(t *testing.T) {
	payload, _ := nativePayload()
	if len(payload) == 0 {
		t.Skip("non-embedded build")
	}
	path, cleanup, err := PrepareNativeCompositor(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("helper not cleaned: %v", err)
	}
}

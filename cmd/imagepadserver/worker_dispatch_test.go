package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"imagepadserver/internal/nicoexportworker"
)

func TestNicoExportWorkerDispatchRejectsMalformedInputWithResultEvent(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := runNicoExportWorker(strings.NewReader("not-json\n"), &stdout, &stderr)
	if err == nil {
		t.Fatal("malformed worker request unexpectedly succeeded")
	}
	var event nicoexportworker.Event
	if decodeErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); decodeErr != nil {
		t.Fatalf("event decode: %v; stdout=%q", decodeErr, stdout.String())
	}
	if event.Version != nicoexportworker.ProtocolVersion || event.Type != "result" || event.OK || event.Error == "" {
		t.Fatalf("event = %#v", event)
	}
}

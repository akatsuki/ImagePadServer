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

func TestNicoExportSessionDispatchRequiresValidSessionIDBeforeHandshake(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "missing", args: nil},
		{name: "invalid", args: []string{"--session-id", "bad id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := runNicoExportSession(tc.args, strings.NewReader(""), &stdout, &bytes.Buffer{})
			if err == nil {
				t.Fatal("session dispatch unexpectedly accepted arguments")
			}
			if stdout.Len() != 0 {
				t.Fatalf("session emitted handshake before argument validation: %q", stdout.String())
			}
		})
	}
}

func TestNicoExportSessionDispatchPassesSessionIDToHandshake(t *testing.T) {
	const sessionID = "parent-generation_17"
	var stdout bytes.Buffer
	err := runNicoExportSession([]string{"--session-id", sessionID}, strings.NewReader(""), &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("clean EOF after handshake returned an error: %v", err)
	}
	var messages []nicoexportworker.SessionMessage
	for _, line := range bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte{'\n'}) {
		var message nicoexportworker.SessionMessage
		if decodeErr := json.Unmarshal(line, &message); decodeErr != nil {
			t.Fatalf("decode session message: %v; line=%q", decodeErr, line)
		}
		messages = append(messages, message)
	}
	if len(messages) != 2 || messages[0].Type != "hello" || messages[1].Type != "ready" {
		t.Fatalf("handshake = %#v", messages)
	}
	for _, message := range messages {
		if message.SessionID != sessionID {
			t.Fatalf("%s session_id = %q, want %q", message.Type, message.SessionID, sessionID)
		}
	}
}

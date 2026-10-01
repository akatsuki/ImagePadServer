package nicoexportworker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSessionDecoderReturnsLineBeforePipeEOF(t *testing.T) {
	d := NewSessionDecoder("sess-1")
	line := `{"version":1,"type":"hello","session_id":"sess-1"}` + "\n"
	if err := d.Feed([]byte(line)); err != nil {
		t.Fatal(err)
	}
	m, ok, err := d.Next()
	if err != nil || !ok || m.Type != "hello" {
		t.Fatalf("Next = %#v, %v, %v", m, ok, err)
	}
}

func TestSessionDecoderRejectsIncompleteEOFMalformedAndUnknown(t *testing.T) {
	d := NewSessionDecoder("sess-1")
	if err := d.Feed([]byte(`{"version":1,"type":"hello","session_id":"sess-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err == nil {
		t.Fatal("accepted incomplete line at EOF")
	}
	for _, line := range []string{`{"version":1,"type":"hello","session_id":"sess-1"}x` + "\n", `{"version":1,"type":"wat","session_id":"sess-1"}` + "\n", `{"version":2,"type":"hello","session_id":"sess-1"}` + "\n"} {
		d := NewSessionDecoder("sess-1")
		if err := d.Feed([]byte(line)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.Next(); err == nil {
			t.Fatalf("accepted invalid message %q", line)
		}
	}
}

func TestSessionDecoderLineAndEventBounds(t *testing.T) {
	max := strings.Repeat(" ", MaxSessionControlLineBytes-len(`{"version":1,"type":"hello","session_id":"sess-1"}`)-1) + `{"version":1,"type":"hello","session_id":"sess-1"}` + "\n"
	d := NewSessionDecoder("sess-1")
	if err := d.Feed([]byte(max)); err != nil {
		t.Fatalf("exact outer limit: %v", err)
	}
	if _, ok, err := d.Next(); err != nil || !ok {
		t.Fatalf("exact outer line: ok=%v err=%v", ok, err)
	}
	d = NewSessionDecoder("sess-1")
	if err := d.Feed([]byte(strings.Repeat("x", MaxSessionControlLineBytes))); err == nil {
		t.Fatal("accepted outer line over limit")
	}
	base := Event{Version: 1, Type: "result", RunID: "run-1", MediaID: "media-1"}
	baseJSON, _ := json.Marshal(base)
	base.Error = strings.Repeat("x", maxEventBytes-len(baseJSON)-12)
	boundaryJSON, _ := json.Marshal(base)
	if len(boundaryJSON)+1 != maxEventBytes {
		t.Fatalf("event boundary setup len=%d base=%d error=%d", len(boundaryJSON)+1, len(baseJSON), len(base.Error))
	}
	if err := (SessionMessage{Version: SessionProtocolVersion, Type: "result", SessionID: "sess-1", RunID: "run-1", MediaID: "media-1", Event: &base}).Validate(); err != nil {
		t.Fatalf("exact 64 KiB embedded event: %v", err)
	}
	base.Error += "x"
	err := (SessionMessage{Version: SessionProtocolVersion, Type: "result", SessionID: "sess-1", RunID: "run-1", MediaID: "media-1", Event: &base}).Validate()
	if err == nil {
		t.Fatal("accepted oversized embedded event")
	}
}

func TestSessionDecoderChunkedMultilineBoundaryAccounting(t *testing.T) {
	valid := `{"version":1,"type":"hello","session_id":"sess-1"}` + "\n"
	d := NewSessionDecoder("sess-1")
	if err := d.Feed([]byte(valid[:len(valid)-2])); err != nil {
		t.Fatalf("first message chunk: %v", err)
	}
	rest := append([]byte(valid[len(valid)-2:]), []byte(valid)...)
	if err := d.Feed(rest); err != nil {
		t.Fatalf("complete first line and buffer second: %v", err)
	}
	for i := 0; i < 2; i++ {
		m, ok, err := d.Next()
		if err != nil || !ok || m.Type != "hello" {
			t.Fatalf("message %d = %#v, %v, %v", i, m, ok, err)
		}
	}
}

func TestSessionRunRequiresAndRoundTripsRequest(t *testing.T) {
	request := validTestRequest()
	valid := sessionMessageWire(t, "run", &request, "run-1", "media-1")
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid run request: %v", err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if _, ok := roundTrip["request"]; !ok {
		t.Fatal("run request was not preserved by round trip")
	}
	var decoded SessionMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped run request: %v", err)
	}
	if decoded.Request == nil || !reflect.DeepEqual(*decoded.Request, request) {
		t.Fatalf("round-tripped request = %#v, want %#v", decoded.Request, request)
	}

	missing := sessionMessageWire(t, "run", nil, "run-1", "media-1")
	if err := missing.Validate(); err == nil {
		t.Fatal("accepted run without request")
	}
}

func TestSessionRunRequestIdentityMustMatchEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, runID, mediaID string
	}{
		{name: "run id", runID: "other", mediaID: "media-1"},
		{name: "media id", runID: "run-1", mediaID: "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := validTestRequest()
			m := sessionMessageWire(t, "run", &request, tc.runID, tc.mediaID)
			if err := m.Validate(); err == nil {
				t.Fatalf("accepted request IDs that differ from envelope: %#v", m)
			}
		})
	}
}

func TestSessionRequestForbiddenOnNonRunMessages(t *testing.T) {
	for _, kind := range []string{"hello", "ready", "progress", "result", "cleanup"} {
		t.Run(kind, func(t *testing.T) {
			request := validTestRequest()
			runID, mediaID := "run-1", "media-1"
			if kind == "hello" || kind == "ready" {
				runID, mediaID = "", ""
			}
			m := sessionMessageWire(t, kind, &request, runID, mediaID)
			if err := m.Validate(); err == nil {
				t.Fatalf("accepted request on %s", kind)
			}
		})
	}
}

func TestSessionRunRequestCountsTowardOuterLineLimit(t *testing.T) {
	request := validTestRequest()
	request.SourcePath = strings.Repeat("x", MaxSessionControlLineBytes)
	m := sessionMessageWire(t, "run", &request, "run-1", "media-1")
	if err := m.Validate(); err == nil {
		t.Fatal("accepted run whose request exceeds outer control-line limit")
	}
}

func sessionMessageWire(t *testing.T, kind string, request *Request, runID, mediaID string) SessionMessage {
	t.Helper()
	wire := map[string]any{
		"version":    SessionProtocolVersion,
		"type":       kind,
		"session_id": "sess-1",
	}
	if runID != "" {
		wire["run_id"] = runID
	}
	if mediaID != "" {
		wire["media_id"] = mediaID
	}
	if request != nil {
		wire["request"] = request
	}
	if kind == "progress" || kind == "result" {
		wire["event"] = &Event{Version: ProtocolVersion, Type: kind, RunID: runID, MediaID: mediaID, OK: true}
	}
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var message SessionMessage
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestSessionProtocolIdentityAndTransitions(t *testing.T) {
	newFlow := func() *SessionState {
		s := NewSessionState("sess-1")
		accept(t, s, SessionMessage{Version: 1, Type: "hello", SessionID: "sess-1"})
		accept(t, s, SessionMessage{Version: 1, Type: "ready", SessionID: "sess-1"})
		accept(t, s, runMsg())
		return s
	}
	t.Run("wrong session", func(t *testing.T) {
		if err := NewSessionState("sess-1").Accept(SessionMessage{Version: 1, Type: "hello", SessionID: "other"}); err == nil {
			t.Fatal("accepted wrong session")
		}
	})
	t.Run("wrong run media", func(t *testing.T) {
		s := newFlow()
		m := eventMsg("progress", true)
		m.RunID = "other"
		if err := s.Accept(m); err == nil {
			t.Fatal("accepted wrong run")
		}
		s = newFlow()
		m = eventMsg("progress", true)
		m.MediaID = "other"
		if err := s.Accept(m); err == nil {
			t.Fatal("accepted wrong media")
		}
	})
	t.Run("success result cleanup ready", func(t *testing.T) {
		s := newFlow()
		accept(t, s, eventMsg("result", true))
		accept(t, s, SessionMessage{Version: 1, Type: "cleanup", SessionID: "sess-1", RunID: "run-1", MediaID: "media-1"})
		accept(t, s, SessionMessage{Version: 1, Type: "ready", SessionID: "sess-1"})
		if s.Terminal() {
			t.Fatal("successful flow became terminal")
		}
	})
	t.Run("duplicate result", func(t *testing.T) {
		s := newFlow()
		accept(t, s, eventMsg("result", true))
		if s.Accept(eventMsg("result", true)) == nil || !s.Terminal() {
			t.Fatal("duplicate result was accepted")
		}
	})
	t.Run("progress after result", func(t *testing.T) {
		s := newFlow()
		accept(t, s, eventMsg("result", true))
		if s.Accept(eventMsg("progress", true)) == nil || !s.Terminal() {
			t.Fatal("progress after result was accepted")
		}
	})
	t.Run("failed result terminal", func(t *testing.T) {
		s := newFlow()
		accept(t, s, eventMsg("result", false))
		if !s.Terminal() {
			t.Fatal("failed result was not terminal")
		}
		if s.Accept(SessionMessage{Version: 1, Type: "ready", SessionID: "sess-1"}) == nil {
			t.Fatal("ready after failure was accepted")
		}
	})
	t.Run("extra run", func(t *testing.T) {
		s := newFlow()
		if s.Accept(runMsg()) == nil || !s.Terminal() {
			t.Fatal("second run was accepted")
		}
	})
}

func accept(t *testing.T, s *SessionState, m SessionMessage) {
	t.Helper()
	if err := s.Accept(m); err != nil {
		t.Fatal(err)
	}
}
func runMsg() SessionMessage {
	request := validTestRequest()
	return SessionMessage{Version: 1, Type: "run", SessionID: "sess-1", RunID: "run-1", MediaID: "media-1", Request: &request}
}
func eventMsg(kind string, ok bool) SessionMessage {
	return SessionMessage{Version: 1, Type: kind, SessionID: "sess-1", RunID: "run-1", MediaID: "media-1", Event: &Event{Version: 1, Type: kind, RunID: "run-1", MediaID: "media-1", OK: ok}}
}

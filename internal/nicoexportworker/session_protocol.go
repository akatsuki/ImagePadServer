package nicoexportworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	SessionProtocolVersion     = 1
	MaxSessionControlLineBytes = 128 * 1024
)

// SessionMessage is the versioned control envelope used by the persistent worker protocol.
// The legacy ProtocolVersion=1 Request/Event protocol remains independent.
type SessionMessage struct {
	Version   int      `json:"version"`
	Type      string   `json:"type"`
	SessionID string   `json:"session_id"`
	RunID     string   `json:"run_id,omitempty"`
	MediaID   string   `json:"media_id,omitempty"`
	Request   *Request `json:"request,omitempty"`
	Event     *Event   `json:"event,omitempty"`
}

func (m SessionMessage) Validate() error {
	if m.Version != SessionProtocolVersion {
		return fmt.Errorf("nico export worker: unsupported session protocol version %d", m.Version)
	}
	if !validToken(m.SessionID) {
		return errors.New("nico export worker: invalid session id")
	}
	if m.Type != "run" && m.Request != nil {
		return errors.New("nico export worker: unexpected request outside run message")
	}
	switch m.Type {
	case "hello", "ready":
		if m.RunID != "" || m.MediaID != "" || m.Event != nil {
			return errors.New("nico export worker: unexpected identity or payload on session control")
		}
	case "run", "progress", "result", "cleanup":
		if !validToken(m.RunID) || !validToken(m.MediaID) {
			return errors.New("nico export worker: invalid session run/media id")
		}
		if m.Type == "run" {
			if m.Request == nil {
				return errors.New("nico export worker: run request is required")
			}
			if m.Request.RunID != m.RunID || m.Request.MediaID != m.MediaID {
				return errors.New("nico export worker: run request identity mismatch")
			}
		}
		if (m.Type == "progress" || m.Type == "result") != (m.Event != nil) {
			return errors.New("nico export worker: session event payload mismatch")
		}
		if m.Event != nil {
			if m.Event.Version != ProtocolVersion || m.Event.Type != m.Type || m.Event.RunID != m.RunID || m.Event.MediaID != m.MediaID {
				return errors.New("nico export worker: embedded event identity or type mismatch")
			}
			b, err := json.Marshal(m.Event)
			if err != nil {
				return fmt.Errorf("nico export worker: encode embedded event: %w", err)
			}
			if len(b)+1 > maxEventBytes {
				return errors.New("nico export worker: embedded event exceeds 64 KiB")
			}
		}
	default:
		return fmt.Errorf("nico export worker: unknown session message type %q", m.Type)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("nico export worker: encode session message: %w", err)
	}
	if len(b)+1 > MaxSessionControlLineBytes {
		return errors.New("nico export worker: session control line exceeds 128 KiB")
	}
	return nil
}

// SessionDecoder incrementally extracts complete LF terminated control lines without reading to EOF.
type SessionDecoder struct {
	sessionID string
	pending   []byte
	lineBytes int
	eof       bool
}

func NewSessionDecoder(sessionID string) *SessionDecoder {
	return &SessionDecoder{sessionID: sessionID}
}

func (d *SessionDecoder) Feed(p []byte) error {
	if d == nil || d.eof {
		return errors.New("nico export worker: session decoder is closed")
	}
	for _, b := range p {
		if b == '\n' {
			if d.lineBytes+1 > MaxSessionControlLineBytes {
				return errors.New("nico export worker: session control line exceeds 128 KiB")
			}
			d.pending = append(d.pending, b)
			d.lineBytes = 0
		} else {
			if d.lineBytes+1 >= MaxSessionControlLineBytes {
				return errors.New("nico export worker: session control line exceeds 128 KiB")
			}
			d.pending = append(d.pending, b)
			d.lineBytes++
		}
	}
	return nil
}

func (d *SessionDecoder) Next() (SessionMessage, bool, error) {
	if d == nil {
		return SessionMessage{}, false, errors.New("nico export worker: session decoder is required")
	}
	i := bytes.IndexByte(d.pending, '\n')
	if i < 0 {
		return SessionMessage{}, false, nil
	}
	line := bytes.TrimSuffix(d.pending[:i], []byte{'\r'})
	d.pending = append(d.pending[:0], d.pending[i+1:]...)
	if len(bytes.TrimSpace(line)) == 0 {
		return SessionMessage{}, false, errors.New("nico export worker: empty session control line")
	}
	var m SessionMessage
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return SessionMessage{}, false, fmt.Errorf("nico export worker: decode session message: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return SessionMessage{}, false, errors.New("nico export worker: trailing session JSON")
	}
	if err := m.Validate(); err != nil {
		return SessionMessage{}, false, err
	}
	if d.sessionID == "" || m.SessionID != d.sessionID {
		return SessionMessage{}, false, errors.New("nico export worker: wrong session id")
	}
	return m, true, nil
}

func (d *SessionDecoder) Finish() error {
	if d == nil {
		return errors.New("nico export worker: session decoder is required")
	}
	d.eof = true
	if len(d.pending) != 0 {
		return errors.New("nico export worker: incomplete session control line at EOF")
	}
	return nil
}

// SessionState enforces one job at a time and provisional success until cleanup and ready.
type SessionState struct {
	sessionID, runID, mediaID     string
	phase                         string
	gotResult, resultOK, terminal bool
}

func NewSessionState(sessionID string) *SessionState {
	return &SessionState{sessionID: sessionID, phase: "start"}
}
func (s *SessionState) Terminal() bool { return s == nil || s.terminal }

func (s *SessionState) Accept(m SessionMessage) error {
	if s == nil {
		return errors.New("nico export worker: session state is required")
	}
	violate := func(err error) error { s.terminal = true; return err }
	if s.terminal {
		return errors.New("nico export worker: session is terminal")
	}
	if err := m.Validate(); err != nil {
		return violate(err)
	}
	if m.SessionID != s.sessionID {
		return violate(errors.New("nico export worker: wrong session id"))
	}
	switch s.phase {
	case "start":
		if m.Type != "hello" {
			return violate(errors.New("nico export worker: expected hello"))
		}
		s.phase = "hello"
	case "hello":
		if m.Type != "ready" {
			return violate(errors.New("nico export worker: expected ready after hello"))
		}
		s.phase = "ready"
	case "ready":
		if m.Type != "run" {
			return violate(errors.New("nico export worker: expected one run"))
		}
		s.runID = m.RunID
		s.mediaID = m.MediaID
		s.phase = "job"
	case "job":
		if m.RunID != s.runID || m.MediaID != s.mediaID {
			return violate(errors.New("nico export worker: wrong run or media id"))
		}
		if m.Type == "progress" {
			if s.gotResult {
				return violate(errors.New("nico export worker: progress after result"))
			}
			return nil
		}
		if m.Type != "result" || s.gotResult {
			return violate(errors.New("nico export worker: unexpected or duplicate result"))
		}
		s.gotResult = true
		s.resultOK = m.Event.OK
		if !s.resultOK {
			s.terminal = true
			s.phase = "terminal"
			return nil
		}
		s.phase = "cleanup"
	case "cleanup":
		if m.Type != "cleanup" || m.RunID != s.runID || m.MediaID != s.mediaID {
			return violate(errors.New("nico export worker: expected matching cleanup"))
		}
		s.phase = "await-ready"
	case "await-ready":
		if m.Type != "ready" {
			return violate(errors.New("nico export worker: expected ready after cleanup"))
		}
		s.runID = ""
		s.mediaID = ""
		s.gotResult = false
		s.phase = "ready"
	default:
		return violate(errors.New("nico export worker: invalid session state"))
	}
	return nil
}

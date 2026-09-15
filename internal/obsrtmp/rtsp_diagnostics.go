package obsrtmp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rtspDiagnosticDirection string

const (
	rtspDiagnosticDirectionClientToBackend rtspDiagnosticDirection = "client_to_backend"
	rtspDiagnosticDirectionBackendToClient rtspDiagnosticDirection = "backend_to_client"
)

type rtspDiagnosticsConfig struct {
	Enabled    bool
	MaxEvents  int
	RunID      string
	OutputPath string
}

type rtspDiagnosticEvent struct {
	At                  time.Time               `json:"at"`
	RunID               string                  `json:"runId,omitempty"`
	ConnectionID        string                  `json:"connectionId,omitempty"`
	PublisherGeneration uint64                  `json:"publisherGeneration,omitempty"`
	Backend             string                  `json:"backend,omitempty"`
	Gate                string                  `json:"gate,omitempty"`
	Request             string                  `json:"request,omitempty"`
	Response            string                  `json:"response,omitempty"`
	UserAgent           string                  `json:"userAgent,omitempty"`
	CSeq                string                  `json:"cseq,omitempty"`
	Transport           string                  `json:"transport,omitempty"`
	Authorization       string                  `json:"authorization,omitempty"`
	Headers             map[string]string       `json:"headers,omitempty"`
	CopyDirection       rtspDiagnosticDirection `json:"copyDirection,omitempty"`
	CopyBytes           int64                   `json:"copyBytes,omitempty"`
	ElapsedMS           int64                   `json:"elapsedMs,omitempty"`
	EndReason           string                  `json:"endReason,omitempty"`
	ReadError           string                  `json:"readError,omitempty"`
	WriteError          string                  `json:"writeError,omitempty"`
}

type rtspDiagnostics struct {
	mu            sync.Mutex
	cfg           rtspDiagnosticsConfig
	connectionSeq uint64
	events        []rtspDiagnosticEvent
}

func newRTSPDiagnostics(cfg rtspDiagnosticsConfig) *rtspDiagnostics {
	if cfg.MaxEvents <= 0 {
		cfg.MaxEvents = 256
	}
	if cfg.MaxEvents > 4096 {
		cfg.MaxEvents = 4096
	}
	return &rtspDiagnostics{cfg: cfg}
}

// newRTSPDiagnosticsFromEnvironment keeps the live hook opt-in. It is used by
// the OBS and direct AirPlay gates only when IMAGEPAD_RTSP_DIAGNOSTICS is a
// truthy value; normal sessions retain the zero-cost nil path.
func newRTSPDiagnosticsFromEnvironment() *rtspDiagnostics {
	if !diagnosticEnvTruthy(os.Getenv("IMAGEPAD_RTSP_DIAGNOSTICS")) {
		return nil
	}
	maxEvents := 256
	if raw := strings.TrimSpace(os.Getenv("IMAGEPAD_RTSP_DIAGNOSTICS_MAX_EVENTS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			maxEvents = parsed
		}
	}
	runID := strings.TrimSpace(os.Getenv("IMAGEPAD_RTSP_RUN_ID"))
	if runID == "" {
		runID = time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return newRTSPDiagnostics(rtspDiagnosticsConfig{
		Enabled:    true,
		MaxEvents:  maxEvents,
		RunID:      runID,
		OutputPath: strings.TrimSpace(os.Getenv("IMAGEPAD_RTSP_DIAGNOSTICS_LOG")),
	})
}

func diagnosticEnvTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (d *rtspDiagnostics) nextConnectionID() string {
	if d == nil || !d.cfg.Enabled {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.connectionSeq++
	return "conn-" + strings.TrimSpace(d.cfg.RunID) + "-" + formatUint(d.connectionSeq)
}

func (d *rtspDiagnostics) record(event rtspDiagnosticEvent) {
	if d == nil || !d.cfg.Enabled {
		return
	}
	if event.ConnectionID == "" {
		event.ConnectionID = d.nextConnectionID()
	}
	event.At = time.Now().UTC()
	event.RunID = d.cfg.RunID
	event.Request = redactRTSPDiagnosticValue(event.Request)
	event.Response = redactRTSPDiagnosticValue(event.Response)
	event.UserAgent = redactRTSPDiagnosticValue(event.UserAgent)
	event.CSeq = redactRTSPDiagnosticValue(event.CSeq)
	event.Transport = redactRTSPDiagnosticValue(event.Transport)
	event.Authorization = ""
	if len(event.Headers) > 0 {
		clean := make(map[string]string, len(event.Headers))
		for key, value := range event.Headers {
			if strings.EqualFold(key, "authorization") {
				continue
			}
			clean[key] = redactRTSPDiagnosticValue(value)
		}
		event.Headers = clean
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.events) >= d.cfg.MaxEvents {
		return
	}
	if event.ReadError != "" {
		event.ReadError = redactRTSPDiagnosticValue(event.ReadError)
	}
	if event.WriteError != "" {
		event.WriteError = redactRTSPDiagnosticValue(event.WriteError)
	}
	d.events = append(d.events, event)
}

func (d *rtspDiagnostics) snapshot() []rtspDiagnosticEvent {
	if d == nil || !d.cfg.Enabled {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]rtspDiagnosticEvent(nil), d.events...)
}

// flush appends bounded, already-redacted events as JSONL. An empty output
// path intentionally keeps diagnostics in memory for tests and callers that
// consume diagnosticsSnapshot directly.
func (d *rtspDiagnostics) flush() error {
	if d == nil || !d.cfg.Enabled || strings.TrimSpace(d.cfg.OutputPath) == "" {
		return nil
	}
	events := d.snapshot()
	if len(events) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(d.cfg.OutputPath), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(d.cfg.OutputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return err
		}
	}
	return nil
}

func (d *rtspDiagnostics) copyFinished(connectionID string, generation uint64, direction rtspDiagnosticDirection, bytes, elapsedMS int64, err error) {
	reason := "eof"
	event := rtspDiagnosticEvent{ConnectionID: connectionID, PublisherGeneration: generation, CopyDirection: direction, CopyBytes: bytes, ElapsedMS: elapsedMS, EndReason: reason}
	if err != nil {
		reason = redactRTSPDiagnosticValue(err.Error())
	}
	event.EndReason = reason
	if err != nil {
		if direction == rtspDiagnosticDirectionClientToBackend {
			event.WriteError = reason
		} else {
			event.ReadError = reason
		}
	}
	d.record(event)
}

func rtspDiagnosticsConfigFingerprint(config []byte) string {
	h := sha256.Sum256(config)
	return hex.EncodeToString(h[:])
}

var rtspSecretQueryKey = regexp.MustCompile(`(?i)([?&](?:key|token|secret|password|pass|auth)=)[^&\s]+`)
var rtspUserInfo = regexp.MustCompile(`(?i)(rtsp[s]?://)[^/@\s]+@`)

func redactRTSPDiagnosticValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
		parsed.User = url.User(parsed.User.Username())
		value = parsed.String()
	}
	value = rtspSecretQueryKey.ReplaceAllString(value, `$1<redacted>`)
	value = rtspUserInfo.ReplaceAllString(value, `${1}<redacted>@`)
	for _, marker := range []string{"Authorization:", "authorization:", "Bearer ", "Basic "} {
		if idx := strings.Index(value, marker); idx >= 0 {
			value = value[:idx] + marker[:len(marker)-1] + "<redacted>"
		}
	}
	return value
}

func formatUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = digits[value%10]
		value /= 10
	}
	return string(buf[i:])
}

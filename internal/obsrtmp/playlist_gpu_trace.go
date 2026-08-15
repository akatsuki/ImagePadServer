package obsrtmp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// playlistGPUTraceEvent is an opt-in diagnostic record for the explicit
// playlist GPU process boundary. UnixNano is sampled at the call site so mux,
// sink, and publisher records can be correlated on one clock. It is never
// enabled by the normal CPU music route.
type playlistGPUTraceEvent struct {
	UnixNano int64          `json:"unix_nano"`
	Event    string         `json:"event"`
	Fields   map[string]any `json:"fields,omitempty"`
}

var playlistGPUTraceMu sync.Mutex

func playlistGPUTracePathFromEnv() string {
	return strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_BOUNDARY_TRACE"))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// recordPlaylistGPUTrace appends one JSONL event to an explicitly configured
// diagnostic path. An empty path disables tracing without creating files.
// Callers must redact credentials and URL secrets before passing fields.
func recordPlaylistGPUTrace(path, event string, fields map[string]any) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if strings.TrimSpace(event) == "" {
		return errors.New("playlist GPU trace event is required")
	}
	copiedFields := make(map[string]any, len(fields))
	for key, value := range fields {
		copiedFields[key] = value
	}
	playlistGPUTraceMu.Lock()
	defer playlistGPUTraceMu.Unlock()
	record := playlistGPUTraceEvent{
		UnixNano: time.Now().UnixNano(),
		Event:    event,
		Fields:   copiedFields,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(payload)
	return err
}

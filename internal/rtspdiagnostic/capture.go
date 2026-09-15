package rtspdiagnostic

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CaptureMetadata struct {
	RunID           string            `json:"run_id"`
	CaptureID       string            `json:"capture_id"`
	SourceKind      string            `json:"source_kind"`
	Negotiation     map[string]string `json:"negotiation,omitempty"`
	TCPFraming      string            `json:"tcp_framing,omitempty"`
	StartedUnixNano int64             `json:"started_unix_nano"`
	EndedUnixNano   int64             `json:"ended_unix_nano"`
	ExitCode        int               `json:"exit_code"`
	DropCount       uint64            `json:"drop_count"`
	MissingCount    uint64            `json:"missing_count"`
	RTPPackets      uint64            `json:"rtp_packets"`
	RTPBytes        uint64            `json:"rtp_bytes"`
	TCPReads        uint64            `json:"tcp_reads"`
	AUCount         uint64            `json:"au_count"`
	FrameCount      uint64            `json:"frame_count"`
}

// ParseNegotiation parses repeatable key=value fixture metadata entries.
func ParseNegotiation(entries []string) (map[string]string, error) {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("negotiation entry must be key=value: %q", entry)
		}
		result[strings.TrimSpace(key)] = value
	}
	return result, nil
}

func NewCaptureDir(root, runID, sourceKind string) (string, *CaptureMetadata, error) {
	if runID == "" {
		runID = time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		return "", nil, err
	}
	meta := &CaptureMetadata{RunID: runID, CaptureID: hex.EncodeToString(id), SourceKind: sourceKind, StartedUnixNano: time.Now().UnixNano()}
	dir := filepath.Join(root, fmt.Sprintf("%s-%s", runID, meta.CaptureID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, err
	}
	return dir, meta, nil
}

func (m *CaptureMetadata) Write(dir string) error {
	if m.EndedUnixNano == 0 {
		m.EndedUnixNano = time.Now().UnixNano()
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "metadata.json"), append(b, '\n'), 0600)
}

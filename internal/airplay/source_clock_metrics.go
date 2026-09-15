package airplay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	sourceClockMetricsPrefix       = "IMAGEPAD_METRICS_V1 "
	sourceClockMetricsMaxLineBytes = 4096
)

type ReceiverMetrics struct {
	ReceiverID         string  `json:"receiverId"`
	Sequence           uint64  `json:"sequence"`
	VideoFrames        uint64  `json:"videoFrames"`
	AudioFrames        uint64  `json:"audioFrames"`
	VideoAgeMS         *uint64 `json:"videoAgeMs"`
	AudioAgeMS         *uint64 `json:"audioAgeMs"`
	ConnectedConsumers uint64  `json:"connectedConsumers"`
	SentFrames         uint64  `json:"sentFrames"`
	DroppedFrames      uint64  `json:"droppedFrames"`
	FailureEvents      uint64  `json:"failureEvents"`
	LastSocketError    int     `json:"lastSocketError"`
	LastFailureReason  string  `json:"lastFailureReason"`
	HasFailureEvents   bool    `json:"-"`
}

func parseSourceClockMetricsLine(line string) (ReceiverMetrics, bool, error) {
	var metrics ReceiverMetrics
	if !strings.HasPrefix(line, sourceClockMetricsPrefix) {
		return metrics, false, nil
	}
	if strings.HasSuffix(line, "\r") {
		line = strings.TrimSuffix(line, "\r")
	}
	if len(line) > sourceClockMetricsMaxLineBytes {
		return metrics, true, errors.New("source-clock metrics line is too large")
	}
	payload := strings.TrimPrefix(line, sourceClockMetricsPrefix)
	if payload == "" {
		return metrics, true, errors.New("source-clock metrics payload is empty")
	}

	fields, err := decodeSourceClockMetricsFields(payload)
	if err != nil {
		return metrics, true, err
	}
	required := []string{
		"receiverId", "sequence", "videoFrames", "audioFrames",
		"videoAgeMs", "audioAgeMs", "connectedConsumers", "sentFrames",
		"droppedFrames", "lastSocketError", "lastFailureReason",
	}
	if len(fields) != len(required) && len(fields) != len(required)+1 {
		return metrics, true, errors.New("source-clock metrics field count is invalid")
	}
	allowed := make(map[string]struct{}, len(required)+1)
	for _, name := range required {
		allowed[name] = struct{}{}
		value, ok := fields[name]
		if !ok {
			return metrics, true, fmt.Errorf("source-clock metrics field %q is missing", name)
		}
		if name != "videoAgeMs" && name != "audioAgeMs" &&
			bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return metrics, true, fmt.Errorf("source-clock metrics field %q cannot be null", name)
		}
	}
	allowed["failureEvents"] = struct{}{}
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return metrics, true, fmt.Errorf("source-clock metrics field %q is unknown", name)
		}
	}
	if value, ok := fields["failureEvents"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return metrics, true, errors.New("source-clock metrics field \"failureEvents\" cannot be null")
		}
	}
	if err := json.Unmarshal([]byte(payload), &metrics); err != nil {
		return ReceiverMetrics{}, true, fmt.Errorf("decode source-clock metrics: %w", err)
	}
	if _, ok := fields["failureEvents"]; ok {
		metrics.HasFailureEvents = true
	}
	if !validSourceClockReceiverID(metrics.ReceiverID) {
		return ReceiverMetrics{}, true, errors.New("source-clock metrics receiverId is invalid")
	}
	if metrics.Sequence == 0 {
		return ReceiverMetrics{}, true, errors.New("source-clock metrics sequence must be positive")
	}
	if !validSourceClockFailureReason(metrics.LastFailureReason) {
		return ReceiverMetrics{}, true, errors.New("source-clock metrics failure reason is invalid")
	}
	return metrics, true, nil
}

func decodeSourceClockMetricsFields(payload string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(payload))
	start, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode source-clock metrics object: %w", err)
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("source-clock metrics payload is not an object")
	}
	fields := make(map[string]json.RawMessage, 11)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode source-clock metrics field name: %w", err)
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("source-clock metrics field name is invalid")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("source-clock metrics field %q is duplicated", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode source-clock metrics field %q: %w", name, err)
		}
		fields[name] = value
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode source-clock metrics object end: %w", err)
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return nil, errors.New("source-clock metrics object is incomplete")
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decode source-clock metrics trailing data: %w", err)
		}
		return nil, fmt.Errorf("source-clock metrics has trailing token %v", token)
	}
	return fields, nil
}

func validSourceClockReceiverID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validSourceClockFailureReason(value string) bool {
	switch value {
	case "", "connect", "send", "peer_eof", "queue_drop":
		return true
	default:
		return false
	}
}

package airplaycontract

import (
	"errors"
	"strings"
)

// FixedPaths names the schema-2 publisher notification sidecars used until
// generation descriptors replace the fixed recording-derived paths.
type FixedPaths struct {
	Ready      string
	MediaReady string
	EventLog   string
}

// FixedPathsForRecording derives the fixed schema-2 notification sidecars.
func FixedPathsForRecording(recording string) (FixedPaths, error) {
	if strings.TrimSpace(recording) == "" {
		return FixedPaths{}, errors.New("AirPlay recording path is empty")
	}
	return FixedPaths{
		Ready:      recording + ".publisher-ready",
		MediaReady: recording + ".media-ready",
		EventLog:   recording + ".events.jsonl",
	}, nil
}

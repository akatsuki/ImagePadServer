package airplay

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"imagepadserver/internal/airplaycontract"
)

func WriteVideoViewControl(path string, request VideoViewRequest, sessionID string, generation uint64) error {
	mode, err := airplaycontract.ParseVideoViewMode(string(request.Mode))
	if err != nil {
		return err
	}
	if sessionID == "" || generation == 0 || request.ExpectedRevision == 0 {
		return fmt.Errorf("video view identity is incomplete")
	}
	content := fmt.Sprintf("[video-view]\nschema=1\nsession-id=%s\npublisher-generation=%s\nrevision=%s\nmode=%s\n", sessionID, strconv.FormatUint(generation, 10), strconv.FormatUint(request.ExpectedRevision, 10), mode)
	if len(content) > 4096 || strings.ContainsAny(sessionID, "\r\n") {
		return fmt.Errorf("invalid video view control")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		return err
	}
	return atomicReplaceVideoViewFile(tmp, path)
}

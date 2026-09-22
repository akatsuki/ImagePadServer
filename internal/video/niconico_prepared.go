package video

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreparedNicoHLS is a completed, still-staged HLS tree. The caller may copy
// it into a Store only after all playlist references and files have passed the
// bounded validation below.
type PreparedNicoHLS struct {
	Playlist string
	Segments []string
	MediaID  string
	RunID    string
}

// PrepareNicoHLSForID validates the worker's finite VOD output without
// changing it. Renaming and publication are owned by library.Store so a
// failed worker cannot expose a partial playlist.
func PrepareNicoHLSForID(stagingDir, mediaID, runID string) (PreparedNicoHLS, error) {
	if !validPreparedNicoToken(mediaID) || !validPreparedNicoToken(runID) {
		return PreparedNicoHLS{}, errors.New("niconico: invalid prepared HLS identity")
	}
	if strings.TrimSpace(stagingDir) == "" {
		return PreparedNicoHLS{}, errors.New("niconico: prepared HLS staging directory is required")
	}
	playlist := filepath.Join(stagingDir, "playlist.m3u8")
	file, err := os.Open(playlist)
	if err != nil {
		return PreparedNicoHLS{}, fmt.Errorf("niconico: prepared HLS playlist: %w", err)
	}
	defer file.Close()

	prepared := PreparedNicoHLS{Playlist: playlist, MediaID: mediaID, RunID: runID}
	seen := make(map[string]struct{})
	endList := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "#EXT-X-ENDLIST" {
			endList = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !validPreparedNicoSegmentName(line) {
			return PreparedNicoHLS{}, fmt.Errorf("niconico: playlist segment escapes staging directory: %q", line)
		}
		if _, ok := seen[line]; ok {
			return PreparedNicoHLS{}, fmt.Errorf("niconico: duplicate prepared HLS segment: %q", line)
		}
		seen[line] = struct{}{}
		path := filepath.Join(stagingDir, line)
		info, statErr := os.Stat(path)
		if statErr != nil {
			return PreparedNicoHLS{}, fmt.Errorf("niconico: prepared HLS segment %q: %w", line, statErr)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return PreparedNicoHLS{}, fmt.Errorf("niconico: prepared HLS segment %q is not a non-empty file", line)
		}
		prepared.Segments = append(prepared.Segments, path)
	}
	if err := scanner.Err(); err != nil {
		return PreparedNicoHLS{}, fmt.Errorf("niconico: read prepared HLS playlist: %w", err)
	}
	if !endList || len(prepared.Segments) == 0 {
		return PreparedNicoHLS{}, errors.New("niconico: prepared HLS playlist is incomplete")
	}
	return prepared, nil
}

func validPreparedNicoToken(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func validPreparedNicoSegmentName(value string) bool {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\\:`) {
		return false
	}
	return filepath.Base(value) == value
}

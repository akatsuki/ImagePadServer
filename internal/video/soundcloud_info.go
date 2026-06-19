package video

import (
	"encoding/json"
	"fmt"
)

// soundCloudInfoJSON maps the standard fields in a yt-dlp .info.json
// sidecar file for SoundCloud tracks.
type soundCloudInfoJSON struct {
	Title    string `json:"title"`
	Uploader string `json:"uploader"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Track    string `json:"track"`
}

// ParseSoundCloudInfoJSON parses a SoundCloud .info.json sidecar file
// produced by yt-dlp --write-info-json and returns an AudioMetadata.
//
// Field mapping:
//   - Title: from "title" field (falls back to "track")
//   - Uploader: from "uploader" field
//   - Album: from "album" field (empty when not present in JSON)
//   - Artist: from "artist" field, falling back to uploader when absent
func ParseSoundCloudInfoJSON(data []byte) (AudioMetadata, error) {
	if len(data) == 0 {
		return AudioMetadata{}, fmt.Errorf("empty info JSON data")
	}

	var info soundCloudInfoJSON
	if err := json.Unmarshal(data, &info); err != nil {
		return AudioMetadata{}, fmt.Errorf("parse SoundCloud info JSON: %w", err)
	}

	meta := AudioMetadata{
		Title:    info.Title,
		Uploader: info.Uploader,
		Album:    info.Album,
	}

	if meta.Title == "" {
		meta.Title = info.Track
	}

	// Artist: use artist field if present, otherwise fall back to uploader.
	if info.Artist != "" {
		meta.Artist = info.Artist
	} else {
		meta.Artist = info.Uploader
	}

	return meta, nil
}

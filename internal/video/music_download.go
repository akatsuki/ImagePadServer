package video

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

var musicDownloadSequence uint64

type musicInfoJSON struct {
	Title    string `json:"title"`
	Track    string `json:"track"`
	Artist   string `json:"artist"`
	Uploader string `json:"uploader"`
	Channel  string `json:"channel"`
	Album    string `json:"album"`
}

// ParseMusicInfoJSON maps generic yt-dlp metadata into visualizer metadata.
// Explicit artist data wins, followed by uploader and channel names.
func ParseMusicInfoJSON(data []byte) (AudioMetadata, error) {
	if len(data) == 0 {
		return AudioMetadata{}, fmt.Errorf("empty info JSON data")
	}
	var info musicInfoJSON
	if err := json.Unmarshal(data, &info); err != nil {
		return AudioMetadata{}, fmt.Errorf("parse music info JSON: %w", err)
	}
	title := info.Title
	if title == "" {
		title = info.Track
	}
	artist := info.Artist
	if artist == "" {
		artist = info.Uploader
	}
	if artist == "" {
		artist = info.Channel
	}
	return AudioMetadata{
		Title:    title,
		Artist:   artist,
		Album:    info.Album,
		Uploader: info.Uploader,
	}, nil
}

// DownloadMusic downloads only the best available audio stream and its page
// metadata/artwork. The result is ready for the existing audio visualizer.
func DownloadMusic(ctx context.Context, ytdlp, rawURL, outDir string) (AcquiredAudio, error) {
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return AcquiredAudio{}, fmt.Errorf("create output directory: %w", err)
	}
	seq := atomic.AddUint64(&musicDownloadSequence, 1)
	prefix := "yt-dlp-music-" + queueID() + "-" + strconv.FormatUint(seq, 36)
	manifestPath := filepath.Join(outDir, prefix+".manifest")
	outputTemplate := filepath.Join(outDir, prefix+".%(ext)s")
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--max-filesize", strconv.FormatInt(MaxMediaSourceBytes, 10),
		"-f", "bestaudio/best",
		"-x",
		// Download DASH/HLS fragments in parallel to work around per-connection
		// throttling (notably YouTube), which dominates long-track download time.
		"--concurrent-fragments", "4",
		"--write-thumbnail",
		"--write-info-json",
		"--print-to-file", "after_move:filepath", manifestPath,
		"-o", outputTemplate,
	}
	args = append(args, rawURL)
	if err := runDownloadCmd(ytdlp, args...); err != nil {
		return AcquiredAudio{}, fmt.Errorf("yt-dlp audio download failed: %w", err)
	}
	sourcePath, err := ReadSinglePathManifest(manifestPath, outDir)
	if err != nil {
		return AcquiredAudio{}, fmt.Errorf("read music download manifest: %w", err)
	}

	base := strings.TrimSuffix(outputTemplate, ".%(ext)s")
	artworkPath := firstExistingGlob(base+".jpg", base+".jpeg", base+".png", base+".webp")
	infoPath := firstExistingGlob(base + ".info.json")
	metadata := AudioMetadata{}
	if infoPath != "" {
		if data, readErr := os.ReadFile(infoPath); readErr == nil {
			metadata, _ = ParseMusicInfoJSON(data)
		}
	}

	return AcquiredAudio{
		SourcePath:                sourcePath,
		SourceName:                filepath.Base(sourcePath),
		Kind:                      SourceMusic,
		SoundCloudMetadata:        metadata,
		SoundCloudArtworkPath:     artworkPath,
		SoundCloudInformationPath: infoPath,
	}, nil
}

func firstExistingGlob(patterns ...string) string {
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			return matches[0]
		}
	}
	return ""
}

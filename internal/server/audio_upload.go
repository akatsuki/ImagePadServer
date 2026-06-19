package server

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"imagepadserver/internal/video"
)

// isAudioUpload returns true when the uploaded file is likely audio based on
// its Content-Type or filename extension.  Video and image uploads are handled
// by separate paths and should be excluded by the caller before checking audio.
func isAudioUpload(name, contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(strings.ToLower(mediaType), "audio/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3", ".wav", ".flac", ".ogg", ".opus", ".m4a", ".aac", ".wma":
		return true
	default:
		return false
	}
}

// safeAudioExt returns the file extension for name when it is a recognised
// audio extension; otherwise it returns ".wav" as a safe default.
func safeAudioExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mp3", ".wav", ".flac", ".ogg", ".opus", ".m4a", ".aac", ".wma":
		return ext
	default:
		return ".wav"
	}
}

// ffprobeExeName returns the platform-specific ffprobe executable name.
func ffprobeExeName() string {
	if runtime.GOOS == "windows" {
		return "ffprobe.exe"
	}
	return "ffprobe"
}

// findFFprobe resolves the ffprobe binary path.  It checks the
// IMAGEPAD_FFPROBE environment variable first, then the sibling directory
// of the resolved ffmpeg binary (which is where extractFFmpegZip places it).
func findFFprobe() (string, error) {
	if p := os.Getenv("IMAGEPAD_FFPROBE"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("IMAGEPAD_FFPROBE does not exist: %s", p)
	}
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return "", err
	}
	sibling := filepath.Join(filepath.Dir(ffmpeg), ffprobeExeName())
	if _, err := os.Stat(sibling); err == nil {
		return sibling, nil
	}
	return "", fmt.Errorf("ffprobe not found after installing FFmpeg")
}

// acquireUploadedAudio saves a multipart audio upload to a temporary file,
// probes it with ffprobe, classifies it, and returns an AcquiredAudio with
// embedded metadata and artwork candidates.  The caller takes ownership of
// the temporary file and must remove it on error.
func (s *Server) acquireUploadedAudio(ctx context.Context, reader io.Reader, name string) (video.AcquiredAudio, error) {
	ext := safeAudioExt(name)
	sourcePath := filepath.Join(s.store.Dir(), "source-"+randomSuffix()+ext)

	source, err := os.Create(sourcePath)
	if err != nil {
		return video.AcquiredAudio{}, fmt.Errorf("failed to create temp file: %w", err)
	}

	cleanup := true
	defer func() {
		if cleanup {
			source.Close()
			os.Remove(sourcePath)
		}
	}()

	if _, err := video.CopyMediaWithLimit(source, reader); err != nil {
		return video.AcquiredAudio{}, fmt.Errorf("failed to save upload: %w", err)
	}
	if err := source.Close(); err != nil {
		return video.AcquiredAudio{}, fmt.Errorf("failed to close temp file: %w", err)
	}

	ffprobe, err := findFFprobe()
	if err != nil {
		return video.AcquiredAudio{}, err
	}

	probe, err := video.ProbeMedia(ctx, ffprobe, sourcePath)
	if err != nil {
		return video.AcquiredAudio{}, fmt.Errorf("probe failed: %w", err)
	}

	class := video.ClassifyMediaProbe(probe)
	if class != video.MediaAudio {
		return video.AcquiredAudio{}, fmt.Errorf("media is %s, not audio", class)
	}

	// Extract embedded metadata from probe.
	meta := extractEmbeddedMetadata(probe)

	// Extract embedded artwork (non-fatal on failure).
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return video.AcquiredAudio{}, err
	}
	candidates, err := video.ExtractEmbeddedArtwork(ctx, ffmpeg, sourcePath, s.store.Dir(), probe)
	if err != nil {
		candidates = nil
	}

	cleanup = false // ownership transfers to caller

	return video.AcquiredAudio{
		SourcePath:       sourcePath,
		SourceName:       name,
		Kind:             video.SourceLocalAudio,
		Probe:            probe,
		EmbeddedMetadata: meta,
		EmbeddedArtwork:  candidates,
	}, nil
}

// extractEmbeddedMetadata reads title, artist, and album from the first audio
// stream's tags, falling back to format-level tags when a stream tag is empty.
func extractEmbeddedMetadata(probe video.MediaProbe) video.AudioMetadata {
	meta := video.AudioMetadata{}

	// First audio stream tags take precedence.
	for _, s := range probe.Streams {
		if s.CodecType == "audio" {
			if t, ok := s.Tags["title"]; ok {
				meta.Title = t
			}
			if t, ok := s.Tags["artist"]; ok {
				meta.Artist = t
			}
			if t, ok := s.Tags["album"]; ok {
				meta.Album = t
			}
			// Stop after the first audio stream with any tag.
			if meta.Title != "" || meta.Artist != "" || meta.Album != "" {
				break
			}
		}
	}

	// Fill empty fields from format-level tags.
	if meta.Title == "" {
		if t, ok := probe.FormatTags["title"]; ok {
			meta.Title = t
		}
	}
	if meta.Artist == "" {
		if t, ok := probe.FormatTags["artist"]; ok {
			meta.Artist = t
		}
	}
	if meta.Album == "" {
		if t, ok := probe.FormatTags["album"]; ok {
			meta.Album = t
		}
	}

	return meta
}

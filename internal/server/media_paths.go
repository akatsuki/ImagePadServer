package server

import (
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"imagepadserver/internal/library"
	"imagepadserver/internal/video"
)

func isVideoUpload(name, contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(strings.ToLower(mediaType), "video/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi":
		return true
	default:
		return false
	}
}

func safeVideoExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi":
		return strings.ToLower(filepath.Ext(name))
	default:
		return ".mp4"
	}
}

func videoContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	default:
		return "video/mp4"
	}
}

// audioContentType returns the MIME type for an audio file based on its
// extension. It is a generic version of soundCloudContentType.
func audioContentType(name string) string {
	return soundCloudContentType(name)
}

func imageContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".avif":
		return "image/avif"
	case ".gif":
		return "image/gif"
	case ".jpeg", ".jpg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func soundCloudContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	default:
		return "application/octet-stream"
	}
}

func isHLSSegmentName(name string) bool {
	if !strings.HasPrefix(name, "current") || !strings.HasSuffix(name, ".ts") {
		return false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(name, "current"), ".ts")
	if middle == "" {
		return false
	}
	if !strings.HasPrefix(middle, "-") {
		first := rune(middle[0])
		if first < '0' || first > '9' {
			return false
		}
		for _, r := range middle {
			if (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
		return true
	}
	for _, r := range middle {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func hlsURLPath(id string) string {
	if id == "" {
		return "stream/current.m3u8"
	}
	return "stream/" + url.PathEscape(id) + "/" + video.PlaylistName(id)
}

func publicMediaExtension(info library.CurrentImage) string {
	ext := strings.ToLower(filepath.Ext(info.PublicName))
	switch ext {
	case ".avif", ".avi", ".flac", ".gif", ".jpeg", ".jpg", ".m4a", ".m4v", ".mkv", ".mov", ".mp3", ".mp4", ".ogg", ".opus", ".png", ".wav", ".webm", ".webp":
		return ext
	}

	mediaType, _, _ := mime.ParseMediaType(info.ContentType)
	switch strings.ToLower(mediaType) {
	case "audio/flac":
		return ".flac"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "audio/ogg":
		return ".ogg"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "image/avif":
		return ".avif"
	case "image/gif":
		return ".gif"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	case "video/x-matroska":
		return ".mkv"
	case "video/x-msvideo":
		return ".avi"
	default:
		return ""
	}
}

func publicHistoryPath(item library.HistoryItem) string {
	return "/pub/" + url.PathEscape(item.ID) + publicMediaExtension(item.CurrentImage)
}

func stripPublicMediaExtension(id string) string {
	ext := strings.ToLower(filepath.Ext(id))
	switch ext {
	case ".avif", ".avi", ".flac", ".gif", ".jpeg", ".jpg", ".m4a", ".m4v", ".mkv", ".mov", ".mp3", ".mp4", ".ogg", ".opus", ".png", ".wav", ".webm", ".webp":
		return strings.TrimSuffix(id, id[len(id)-len(ext):])
	default:
		return id
	}
}

// lhlsContentType maps a community-LHLS artifact filename (HLS playlists and
// fMP4 init/media segments produced by the DASH muxer) to its MIME type.
func lhlsContentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(name, ".m4s"), strings.HasSuffix(name, ".mp4"):
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

func streamRequestID(r *http.Request) string {
	if requestedID := r.URL.Query().Get("v"); requestedID != "" {
		return requestedID
	}
	path := strings.TrimPrefix(r.URL.Path, "/stream/")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 && parts[0] != "" {
		if id, err := url.PathUnescape(parts[0]); err == nil {
			return id
		}
		return parts[0]
	}
	return ""
}

func imageURLPath(img *library.CurrentImage) string {
	if img == nil {
		return "image/current"
	}
	switch img.ContentType {
	case "image/png":
		return "image/current.png"
	case "image/jpeg":
		return "image/current.jpg"
	default:
		ext := strings.ToLower(filepath.Ext(img.PublicName))
		if ext == ".png" {
			return "image/current.png"
		}
		if ext == ".jpg" || ext == ".jpeg" {
			return "image/current.jpg"
		}
		return "image/current"
	}
}

func deletedContentType(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".png") {
		return "image/png"
	}
	return "image/jpeg"
}

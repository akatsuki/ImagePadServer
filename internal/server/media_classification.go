package server

import (
	"context"
	"path/filepath"
	"strings"

	"imagepadserver/internal/imageproc"
	"imagepadserver/internal/video"
)

// classifyUploadedMedia determines whether an uploaded file is audio, video,
// or unsupported. It checks for image and camera RAW files first so that
// ffprobe is never invoked for files that are clearly not audio or video.
//
// When enabled is false the file is always classified as unsupported
// regardless of its contents.
func classifyUploadedMedia(
	ctx context.Context,
	enabled bool,
	name, contentType, path string,
	probe func(context.Context, string) (video.MediaProbe, error),
) (video.MediaClass, error) {
	// Without a running video player there is nothing to do with audio or
	// video uploads.
	if !enabled {
		return video.MediaUnsupported, nil
	}

	// Image and camera RAW files are handled by the image path; skip ffprobe.
	if isImageOrRAWName(name) {
		return video.MediaUnsupported, nil
	}

	mp, err := probe(ctx, path)
	if err != nil {
		return video.MediaUnsupported, err
	}

	return video.ClassifyMediaProbe(mp), nil
}

// isImageOrRAWName returns true when name carries an extension that
// corresponds to a known image format or a camera RAW format.
func isImageOrRAWName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}
	if imageproc.IsCameraRAWName(name) {
		return true
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tiff", ".tif", ".svg":
		return true
	}
	return false
}

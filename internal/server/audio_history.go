package server

import (
	"context"
	"fmt"
	"path/filepath"

	"imagepadserver/internal/library"
	"imagepadserver/internal/video"
)

// audioRenderInputForStored runs audio analysis on a stored audio file and
// constructs a fully populated AudioRenderInput for the conversion pipeline.
// Returns an error if EnsureFFmpeg, AnalyzeAudio, or analysis with zero frames fails.
func (s *Server) audioRenderInputForStored(ctx context.Context, path string, item library.CurrentImage) (video.AudioRenderInput, error) {
	ffmpegPath, err := video.EnsureFFmpeg()
	if err != nil {
		return video.AudioRenderInput{}, err
	}

	analysis, err := video.AnalyzeAudio(ctx, ffmpegPath, path)
	if err != nil {
		return video.AudioRenderInput{}, fmt.Errorf("analyze audio: %w", err)
	}

	if len(analysis.Frames) == 0 {
		return video.AudioRenderInput{}, fmt.Errorf("no analysis frames to render")
	}

	kind := video.SourceKind(item.SourceKind)
	meta := video.AudioMetadata{
		Title:  item.Title,
		Artist: item.Artist,
		Album:  item.Album,
	}

	artworkPath := ""
	if item.Thumbnail != "" {
		artworkPath = filepath.Join(s.store.Dir(), item.Thumbnail)
	}

	return video.AudioRenderInput{
		SourcePath:  path,
		Kind:        kind,
		Metadata:    meta,
		ArtworkPath: artworkPath,
		Analysis:    analysis,
	}, nil
}

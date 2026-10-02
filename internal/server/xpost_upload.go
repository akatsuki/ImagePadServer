package server

import (
	"bytes"
	"fmt"
	"net/http"

	"imagepadserver/internal/imageproc"
	"imagepadserver/internal/video"
	"imagepadserver/internal/xpostimage"
)

func shouldUseVideoURLRoute(videoPlayerEnabled bool, intent string) bool {
	return videoPlayerEnabled && intent != "image"
}

func (s *Server) processXPostURL(r *http.Request, rawURL, theme string, opts imageproc.Options, queue bool) (map[string]interface{}, error) {
	fonts, err := video.VisualizerFonts()
	if err != nil {
		return nil, fmt.Errorf("load bundled Noto Sans JP font: %w", err)
	}
	if theme != "dark" {
		theme = "light"
	}
	result, err := xpostimage.Render(r.Context(), rawURL, theme, fonts.Regular400)
	if err != nil {
		return nil, err
	}
	// X layouts are authored at exactly 2048 pixels on their long edge. Keep
	// that canvas size while honoring the user's output format and quality.
	opts.MaxDimension = 2048
	name := fmt.Sprintf("x-post-%s.png", result.TweetID)
	reader := bytes.NewReader(result.PNG)
	if queue {
		return s.processAndQueue(r, reader, name, "image/png", opts)
	}
	return s.processAndPublish(r, reader, name, "image/png", opts)
}

package server

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"imagepadserver/internal/video"
)

// downloadedRemoteMedia holds the result of a successful media download.
type downloadedRemoteMedia struct {
	Path  string
	Name  string
	Class video.MediaClass
}

// downloadRemoteMedia downloads media from rawURL, validates it against SSRF
// rules and size limits, writes it to outDir, probes the result via the
// provided probe function, and returns metadata.
func downloadRemoteMedia(
	ctx context.Context,
	rawURL, outDir string,
	probe func(context.Context, string) (video.MediaProbe, error),
) (downloadedRemoteMedia, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			_, err := validatePublicURL(req.URL.String())
			return err
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return downloadedRemoteMedia{}, err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return downloadedRemoteMedia{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return downloadedRemoteMedia{}, fmt.Errorf("download failed: %s", resp.Status)
	}

	// Reject oversized Content-Length before reading the body.
	if resp.ContentLength > 0 {
		if err := video.ValidateMediaContentLength(resp.ContentLength); err != nil {
			return downloadedRemoteMedia{}, err
		}
	}

	fileName := mediaFileName(resp.Request.URL, resp.Header)
	outPath := filepath.Join(outDir, filepath.Base(fileName))

	outFile, err := os.Create(outPath)
	if err != nil {
		return downloadedRemoteMedia{}, fmt.Errorf("create output file: %w", err)
	}
	defer outFile.Close()

	written, err := video.CopyMediaWithLimit(outFile, resp.Body)
	if err != nil {
		_ = os.Remove(outPath)
		return downloadedRemoteMedia{}, err
	}
	_ = written

	_ = outFile.Close()

	probeResult, err := probe(ctx, outPath)
	if err != nil {
		_ = os.Remove(outPath)
		return downloadedRemoteMedia{}, fmt.Errorf("probe downloaded media: %w", err)
	}

	class := video.ClassifyMediaProbe(probeResult)

	return downloadedRemoteMedia{
		Path:  outPath,
		Name:  fileName,
		Class: class,
	}, nil
}

// mediaFileName extracts a display name for the downloaded media from
// Content-Disposition if present, otherwise from the final URL path.
func mediaFileName(u *url.URL, header http.Header) string {
	if cd := header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if name := params["filename"]; name != "" {
				return filepath.Base(name)
			}
		}
	}
	name := filepath.Base(u.Path)
	if name == "." || name == "/" || name == "" {
		name = "remote-media"
	}
	return name
}



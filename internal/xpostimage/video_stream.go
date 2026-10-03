package xpostimage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Videos are streamed into the private job directory instead of allocating a
// source-sized byte slice while VRChat is running.
func downloadVideoFile(ctx context.Context, rawURL, path string, maxBytes int64) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return errors.New("remote video URL is invalid")
	}
	if err := validatePublicHTTPSURL(ctx, u); err != nil {
		return err
	}
	client := &http.Client{
		Timeout:   10 * time.Minute,
		Transport: &http.Transport{DialContext: safePublicDialContext},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return validatePublicHTTPSURL(req.Context(), req.URL)
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ImagePadServer/XPostVideo")
	req.Header.Set("Accept", "video/mp4,application/octet-stream;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remote server returned %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("remote video exceeds %d MiB", maxBytes>>20)
	}
	return saveVideoStream(resp.Body, path, maxBytes)
}

func saveVideoStream(reader io.Reader, path string, maxBytes int64) error {
	if maxBytes < 12 {
		return errors.New("video byte limit is too small")
	}
	var header [68]byte
	n, err := io.ReadFull(io.LimitReader(reader, maxBytes+1), header[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	if !isMP4Data(header[:n]) {
		return errors.New("remote video is not an MP4")
	}
	if int64(n) > maxBytes {
		return errors.New("remote video exceeds byte limit")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".xpost-video-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(header[:n]); err != nil {
		return err
	}
	written, err := io.Copy(f, io.LimitReader(reader, maxBytes-int64(n)+1))
	if err != nil {
		return err
	}
	if int64(n)+written > maxBytes {
		return fmt.Errorf("remote video exceeds %d MiB", maxBytes>>20)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

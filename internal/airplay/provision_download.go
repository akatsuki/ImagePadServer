package airplay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

func ensureUxPlayArchive(ctx context.Context) (string, error) {
	archivePath, err := uxPlayArchivePath()
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Dir(archivePath)
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return "", fmt.Errorf("create AirPlay download cache: %w", err)
	}
	if err := validateUxPlayArchive(archivePath); err == nil {
		return archivePath, nil
	}
	if err := os.Remove(archivePath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove invalid AirPlay archive: %w", err)
	}

	tmp, err := os.CreateTemp(cacheDir, ".uxplay-download-*")
	if err != nil {
		return "", fmt.Errorf("create temporary AirPlay download: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close temporary AirPlay download: %w", err)
	}
	defer os.Remove(tmpPath)

	if err := downloadUxPlayArchive(ctx, tmpPath); err != nil {
		return "", err
	}
	if err := validateUxPlayArchive(tmpPath); err != nil {
		return "", fmt.Errorf("verify downloaded AirPlay archive: %w", err)
	}
	if err := os.Rename(tmpPath, archivePath); err != nil {
		return "", fmt.Errorf("atomically cache AirPlay archive: %w", err)
	}
	return archivePath, nil
}

func downloadUxPlayArchive(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uxPlayArchiveURL, nil)
	if err != nil {
		return fmt.Errorf("create AirPlay download request: %w", err)
	}
	req.Header.Set("User-Agent", "ImagePadServer-AirPlay-Setup/1")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("download AirPlay archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download AirPlay archive: HTTP %s", resp.Status)
	}
	if resp.ContentLength > int64(uxPlayArchiveLimit) {
		return fmt.Errorf("AirPlay archive is too large: %d bytes", resp.ContentLength)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open temporary AirPlay archive: %w", err)
	}
	n, copyErr := io.Copy(file, io.LimitReader(resp.Body, int64(uxPlayArchiveLimit)+1))
	if copyErr == nil && n > int64(uxPlayArchiveLimit) {
		copyErr = fmt.Errorf("AirPlay archive exceeds %d bytes", uxPlayArchiveLimit)
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write AirPlay archive: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close AirPlay archive: %w", closeErr)
	}
	return nil
}

func validateUxPlayArchive(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("AirPlay archive is not a regular file")
	}
	if uint64(info.Size()) != uxPlayArchiveSize {
		return fmt.Errorf("AirPlay archive size %d does not match pinned size %d", info.Size(), uxPlayArchiveSize)
	}
	size, digest, err := hashFile(path, uxPlayArchiveLimit)
	if err != nil {
		return err
	}
	if uint64(size) != uxPlayArchiveSize || digest != uxPlayArchiveSHA256 {
		return fmt.Errorf("AirPlay archive SHA-256 mismatch: got %s", digest)
	}
	return nil
}

func hashFile(path string, limit uint64) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()

	h := sha256.New()
	var size int64
	buf := make([]byte, 1024*1024)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			size += int64(n)
			if limit > 0 && uint64(size) > limit {
				return size, "", fmt.Errorf("file exceeds %d bytes", limit)
			}
			if _, err := h.Write(buf[:n]); err != nil {
				return size, "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return size, "", readErr
		}
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

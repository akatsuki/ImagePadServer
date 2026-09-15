package airplay

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// RuntimeArtifactDescriptor pins a complete runtime archive. It is separate
// from RuntimeDescriptor because the latter describes an extracted set.
type RuntimeArtifactDescriptor struct {
	RuntimeSetID          string `json:"runtimeSetID"`
	ArchiveURL            string `json:"archiveUrl"`
	ArchiveSHA256         string `json:"archiveSha256"`
	ArchiveSize           int64  `json:"archiveSize"`
	LicenseManifestSHA256 string `json:"licenseManifestSha256"`
	SourceOfferSHA256     string `json:"sourceOfferSha256"`
}

type runtimeLicenseManifest struct {
	Schema       int    `json:"schema"`
	RuntimeSetID string `json:"runtimeSetID"`
}

func (d RuntimeArtifactDescriptor) validate(allowInsecureHTTP bool) error {
	if strings.TrimSpace(d.RuntimeSetID) == "" {
		return errors.New("AirPlay runtime artifact runtimeSetID is empty")
	}
	parsed, err := url.Parse(strings.TrimSpace(d.ArchiveURL))
	if err != nil || parsed.Host == "" ||
		(!strings.EqualFold(parsed.Scheme, "https") && !(allowInsecureHTTP && strings.EqualFold(parsed.Scheme, "http"))) {
		return errors.New("AirPlay runtime artifact URL must use HTTPS")
	}
	if d.ArchiveSize <= 0 {
		return errors.New("AirPlay runtime artifact archive size is invalid")
	}
	for label, value := range map[string]string{
		"archive SHA-256":          d.ArchiveSHA256,
		"license manifest SHA-256": d.LicenseManifestSHA256,
		"source offer SHA-256":     d.SourceOfferSHA256,
	} {
		if len(value) != sha256.Size*2 {
			return fmt.Errorf("AirPlay runtime artifact %s is invalid", label)
		}
		if _, err := hex.DecodeString(value); err != nil {
			return fmt.Errorf("AirPlay runtime artifact %s is invalid: %w", label, err)
		}
	}
	return nil
}

// DownloadAirPlayRuntimeArchive downloads one descriptor-pinned archive to a
// new destination. Production callers must leave allowInsecureHTTP false;
// the opt-in exists only for httptest-based unit tests.
func DownloadAirPlayRuntimeArchive(ctx context.Context, client *http.Client, descriptor RuntimeArtifactDescriptor, destination string, allowInsecureHTTP bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := descriptor.validate(allowInsecureHTTP); err != nil {
		return err
	}
	if strings.TrimSpace(destination) == "" {
		return errors.New("AirPlay runtime archive destination is empty")
	}
	parsedURL, _ := url.Parse(descriptor.ArchiveURL)
	if !allowInsecureHTTP && !strings.EqualFold(parsedURL.Scheme, "https") {
		return errors.New("AirPlay runtime archive refuses non-HTTPS URL")
	}
	if client == nil {
		client = &http.Client{}
	}
	baseClient := *client
	previousRedirect := baseClient.CheckRedirect
	baseClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !allowInsecureHTTP && !strings.EqualFold(request.URL.Scheme, "https") {
			return errors.New("AirPlay runtime archive refuses HTTPS downgrade redirect")
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, descriptor.ArchiveURL, nil)
	if err != nil {
		return fmt.Errorf("create AirPlay runtime download request: %w", err)
	}
	request.Header.Set("User-Agent", "ImagePadServer-AirPlay-Runtime/1")
	response, err := baseClient.Do(request)
	if err != nil {
		return fmt.Errorf("download AirPlay runtime archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download AirPlay runtime archive: HTTP %s", response.Status)
	}
	if response.ContentLength >= 0 && response.ContentLength != descriptor.ArchiveSize {
		return fmt.Errorf("AirPlay runtime archive content length %d does not match pinned size %d", response.ContentLength, descriptor.ArchiveSize)
	}

	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("create AirPlay runtime archive directory: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".airplay-runtime-download-*")
	if err != nil {
		return fmt.Errorf("create temporary AirPlay runtime archive: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	reader := io.LimitReader(response.Body, descriptor.ArchiveSize+1)
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), reader)
	if copyErr == nil && written != descriptor.ArchiveSize {
		copyErr = fmt.Errorf("AirPlay runtime archive size %d does not match pinned size %d", written, descriptor.ArchiveSize)
	}
	if copyErr == nil && written > descriptor.ArchiveSize {
		copyErr = errors.New("AirPlay runtime archive exceeds pinned size")
	}
	if copyErr == nil && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), descriptor.ArchiveSHA256) {
		copyErr = errors.New("AirPlay runtime archive SHA-256 does not match pinned hash")
	}
	if syncErr := temporary.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	closeErr := temporary.Close()
	if copyErr != nil {
		return fmt.Errorf("write AirPlay runtime archive: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close AirPlay runtime archive: %w", closeErr)
	}
	if err := validateRuntimeArchiveMetadata(temporaryPath, descriptor); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("install AirPlay runtime archive: %w", err)
	}
	return nil
}

func validateRuntimeArchiveMetadata(archivePath string, descriptor RuntimeArtifactDescriptor) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open AirPlay runtime archive for metadata validation: %w", err)
	}
	defer reader.Close()
	var licenseData, sourceOffer []byte
	for _, entry := range reader.File {
		name := strings.ReplaceAll(entry.Name, "\\", "/")
		switch name {
		case "license-manifest.json":
			licenseData, err = readRuntimeArchiveEntry(entry)
			if err != nil {
				return err
			}
		case "SOURCE-OFFER.md":
			sourceOffer, err = readRuntimeArchiveEntry(entry)
			if err != nil {
				return err
			}
		}
	}
	if len(licenseData) == 0 || !strings.EqualFold(hashBytes(licenseData), descriptor.LicenseManifestSHA256) {
		return errors.New("AirPlay runtime license-manifest.json is missing or has the wrong hash")
	}
	var license runtimeLicenseManifest
	if err := json.Unmarshal(licenseData, &license); err != nil {
		return fmt.Errorf("parse AirPlay runtime license manifest: %w", err)
	}
	if license.Schema != 1 || license.RuntimeSetID != descriptor.RuntimeSetID {
		return errors.New("AirPlay runtime license manifest does not match runtimeSetID")
	}
	if len(sourceOffer) == 0 || !strings.EqualFold(hashBytes(sourceOffer), descriptor.SourceOfferSHA256) {
		return errors.New("AirPlay runtime SOURCE-OFFER.md is missing or has the wrong hash")
	}
	if !strings.Contains(string(sourceOffer), descriptor.RuntimeSetID) {
		return errors.New("AirPlay runtime SOURCE-OFFER.md does not name runtimeSetID")
	}
	return nil
}

func readRuntimeArchiveEntry(entry *zip.File) ([]byte, error) {
	if entry == nil || entry.FileInfo().IsDir() || entry.UncompressedSize64 > 4*1024*1024 {
		return nil, errors.New("AirPlay runtime metadata entry is invalid")
	}
	file, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(entry.UncompressedSize64)+1))
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) != entry.UncompressedSize64 {
		return nil, errors.New("AirPlay runtime metadata entry size changed")
	}
	return data, nil
}

func hashBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

package airplay

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"imagepadserver/internal/settings"
)

const (
	envAirPlayRuntimeBootstrap = "IMAGEPAD_AIRPLAY_RUNTIME_BOOTSTRAP"
	runtimeBootstrapName       = "airplay-runtime-bootstrap.json"
)

// embeddedRuntimeBootstrapBase64 is populated by the release build with the
// descriptor for the pinned runtime archive. Keeping the descriptor in the
// executable makes the normal distribution a single EXE; the archive itself
// remains in the per-user cache and is downloaded only when needed.
var embeddedRuntimeBootstrapBase64 string

type runtimeBootstrapDescriptor struct {
	Schema int `json:"schema"`
	RuntimeArtifactDescriptor
}

func loadRuntimeBootstrapDescriptor() (RuntimeArtifactDescriptor, error) {
	path := strings.TrimSpace(os.Getenv(envAirPlayRuntimeBootstrap))
	if path == "" && strings.TrimSpace(embeddedRuntimeBootstrapBase64) == "" && embeddedRuntimeAvailable() {
		return loadEmbeddedRuntimeArtifact()
	}
	var data []byte
	if path == "" && strings.TrimSpace(embeddedRuntimeBootstrapBase64) != "" {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(embeddedRuntimeBootstrapBase64))
		if err != nil {
			return RuntimeArtifactDescriptor{}, fmt.Errorf("decode embedded AirPlay runtime bootstrap: %w", err)
		}
		data = decoded
	}
	if path == "" && len(data) == 0 {
		executable, err := os.Executable()
		if err != nil {
			return RuntimeArtifactDescriptor{}, fmt.Errorf("locate AirPlay runtime bootstrap: %w", err)
		}
		path = filepath.Join(filepath.Dir(executable), runtimeBootstrapName)
	}
	if len(data) == 0 {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return RuntimeArtifactDescriptor{}, fmt.Errorf("read AirPlay runtime bootstrap: %w", err)
		}
	}
	var bootstrap runtimeBootstrapDescriptor
	if err := json.Unmarshal(data, &bootstrap); err != nil {
		return RuntimeArtifactDescriptor{}, fmt.Errorf("parse AirPlay runtime bootstrap: %w", err)
	}
	if bootstrap.Schema != 1 {
		return RuntimeArtifactDescriptor{}, errors.New("AirPlay runtime bootstrap schema is unsupported")
	}
	if err := bootstrap.RuntimeArtifactDescriptor.validate(false); err != nil {
		return RuntimeArtifactDescriptor{}, err
	}
	return bootstrap.RuntimeArtifactDescriptor, nil
}

// writeEmbeddedRuntimeArchive materializes the archive only when the
// descriptor-matching cache is absent or invalid. It streams the embedded
// bytes to the per-user cache and applies the same size/hash checks as a
// network download, so a single EXE remains sufficient on first launch.
func writeEmbeddedRuntimeArchive(ctx context.Context, destination string,
	expected RuntimeArtifactDescriptor) error {
	if !embeddedRuntimeAvailable() {
		return errEmbeddedRuntimeUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reader, embedded, err := openEmbeddedRuntimeArchive()
	if err != nil {
		return fmt.Errorf("open embedded AirPlay runtime archive: %w", err)
	}
	defer reader.Close()
	if embedded.RuntimeSetID != expected.RuntimeSetID ||
		!strings.EqualFold(embedded.ArchiveSHA256, expected.ArchiveSHA256) ||
		embedded.ArchiveSize != expected.ArchiveSize {
		return errors.New("embedded AirPlay runtime archive does not match its descriptor")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("create embedded AirPlay runtime cache: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".airplay-runtime-embedded-*")
	if err != nil {
		return fmt.Errorf("create embedded AirPlay runtime cache: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(reader, expected.ArchiveSize+1))
	if copyErr == nil && written != expected.ArchiveSize {
		copyErr = fmt.Errorf("embedded AirPlay runtime archive size %d does not match pinned size %d", written, expected.ArchiveSize)
	}
	if copyErr == nil && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected.ArchiveSHA256) {
		copyErr = errors.New("embedded AirPlay runtime archive SHA-256 does not match pinned hash")
	}
	if syncErr := temporary.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	closeErr := temporary.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := validateRuntimeArchiveMetadata(temporaryPath, expected); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("install embedded AirPlay runtime archive: %w", err)
	}
	return nil
}

func runtimeArchiveCachePath(artifact RuntimeArtifactDescriptor) (string, error) {
	dataDir := settings.Dir()
	if dataDir == "" {
		return "", errors.New("ImagePadServer data directory is empty")
	}
	if strings.TrimSpace(artifact.RuntimeSetID) == "" || strings.ContainsAny(artifact.RuntimeSetID, `/\\:`) {
		return "", errors.New("AirPlay runtimeSetID is invalid")
	}
	return filepath.Join(dataDir, "runtimes", "airplay", "cache", artifact.RuntimeSetID+".zip"), nil
}

func runtimeDescriptorFromArchive(archivePath string) (RuntimeDescriptor, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return RuntimeDescriptor{}, fmt.Errorf("open AirPlay runtime archive descriptor: %w", err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if strings.ReplaceAll(entry.Name, "\\", "/") != runtimeDescriptorName {
			continue
		}
		data, readErr := readRuntimeArchiveEntry(entry)
		if readErr != nil {
			return RuntimeDescriptor{}, readErr
		}
		var descriptor RuntimeDescriptor
		if err := json.Unmarshal(data, &descriptor); err != nil {
			return RuntimeDescriptor{}, fmt.Errorf("parse AirPlay runtime archive descriptor: %w", err)
		}
		return descriptor, nil
	}
	return RuntimeDescriptor{}, errors.New("AirPlay runtime archive descriptor is missing")
}

func validateCachedRuntimeArchive(path string, artifact RuntimeArtifactDescriptor) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.ArchiveSize {
		return errors.New("cached AirPlay runtime archive has the wrong size")
	}
	if _, digest, err := hashFile(path, uint64(artifact.ArchiveSize)); err != nil || !strings.EqualFold(digest, artifact.ArchiveSHA256) {
		if err != nil {
			return err
		}
		return errors.New("cached AirPlay runtime archive has the wrong SHA-256")
	}
	return validateRuntimeArchiveMetadata(path, artifact)
}

func preparePinnedAirPlayRuntime(ctx context.Context) (AirPlayRuntime, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return AirPlayRuntime{}, errAutomaticSetupUnsupported
	}
	// An explicit administrator/developer root remains authoritative and does
	// not require the packaged artifact descriptor.
	if strings.TrimSpace(os.Getenv(envAirPlayRuntimeRoot)) != "" {
		return ResolvePinnedAirPlayRuntime()
	}
	artifact, err := loadRuntimeBootstrapDescriptor()
	if err != nil {
		return AirPlayRuntime{}, err
	}
	// The normal path must not accept an older active set merely because it is
	// complete; it has to match the EXE's pinned runtime artifact.
	if runtimeSet, err := ResolvePinnedAirPlayRuntime(); err == nil &&
		runtimeSet.Descriptor.RuntimeSetID == artifact.RuntimeSetID &&
		runtimeArtifactMatchesInstalled(runtimeSet.Root, artifact) &&
		runtimeInstalledArchiveMatches(runtimeSet.Root, artifact) {
		return runtimeSet, nil
	}
	archivePath, err := runtimeArchiveCachePath(artifact)
	if err != nil {
		return AirPlayRuntime{}, err
	}
	if err := validateCachedRuntimeArchive(archivePath, artifact); err != nil {
		if embeddedRuntimeAvailable() {
			if embeddedErr := writeEmbeddedRuntimeArchive(ctx, archivePath, artifact); embeddedErr != nil {
				return AirPlayRuntime{}, fmt.Errorf("prepare embedded AirPlay runtime archive: %w", embeddedErr)
			}
		} else if downloadErr := DownloadAirPlayRuntimeArchive(ctx, http.DefaultClient, artifact, archivePath, false); downloadErr != nil {
			return AirPlayRuntime{}, fmt.Errorf("prepare AirPlay runtime archive: %w", downloadErr)
		}
	}
	descriptor, err := runtimeDescriptorFromArchive(archivePath)
	if err != nil {
		return AirPlayRuntime{}, err
	}
	if descriptor.RuntimeSetID != artifact.RuntimeSetID {
		return AirPlayRuntime{}, errors.New("AirPlay runtime descriptor runtimeSetID differs from bootstrap")
	}
	installed, err := InstallAirPlayRuntimeArchive(ctx, archivePath, filepath.Dir(filepath.Dir(archivePath)), descriptor)
	if err != nil {
		return AirPlayRuntime{}, err
	}
	if err := writeRuntimeArtifactMarker(installed.Root, artifact); err != nil {
		return AirPlayRuntime{}, err
	}
	return installed, nil
}

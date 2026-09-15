package airplay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const runtimeArtifactMarkerName = ".imagepad-runtime-artifact.json"

const runtimeFileManifestName = "imagepad-airplay-source-clock-manifest.json"

type runtimeFileManifest struct {
	Files []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

func validateRuntimeFileManifest(root string) error {
	manifestPath := filepath.Join(root, runtimeFileManifestName)
	data, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest runtimeFileManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse AirPlay runtime file manifest: %w", err)
	}
	if len(manifest.Files) == 0 {
		return errors.New("AirPlay runtime file manifest is empty")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	for _, entry := range manifest.Files {
		clean, err := safeZipPath(entry.Path)
		if err != nil || clean == runtimeFileManifestName || entry.Size < 0 || len(entry.SHA256) != sha256.Size*2 {
			return fmt.Errorf("invalid AirPlay runtime file manifest entry %q", entry.Path)
		}
		if _, ok := seen[clean]; ok {
			return fmt.Errorf("duplicate AirPlay runtime file manifest entry %q", entry.Path)
		}
		seen[clean] = struct{}{}
		path := filepath.Join(root, filepath.FromSlash(clean))
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			return fmt.Errorf("AirPlay runtime manifest size mismatch for %q", entry.Path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), entry.SHA256) {
			return fmt.Errorf("AirPlay runtime manifest hash mismatch for %q", entry.Path)
		}
	}
	return nil
}

// The marker binds an extracted set to the archive that the current EXE
// verified. An active directory with only a matching set ID is insufficient:
// an older build could have reused that ID with different binaries.
func writeRuntimeArtifactMarker(root string, artifact RuntimeArtifactDescriptor) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("AirPlay runtime artifact marker root is empty")
	}
	if err := artifact.validate(false); err != nil {
		return err
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(root, runtimeArtifactMarkerName), data, 0600)
}

func runtimeArtifactMatchesInstalled(root string, expected RuntimeArtifactDescriptor) bool {
	if validateRuntimeFileManifest(root) != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(root, runtimeArtifactMarkerName))
	if err != nil {
		return false
	}
	var actual RuntimeArtifactDescriptor
	if json.Unmarshal(data, &actual) != nil || actual.validate(false) != nil {
		return false
	}
	return actual.RuntimeSetID == expected.RuntimeSetID &&
		strings.EqualFold(actual.ArchiveSHA256, expected.ArchiveSHA256) &&
		actual.ArchiveSize == expected.ArchiveSize &&
		strings.EqualFold(actual.LicenseManifestSHA256, expected.LicenseManifestSHA256) &&
		strings.EqualFold(actual.SourceOfferSHA256, expected.SourceOfferSHA256)
}

func runtimeInstalledArchiveMatches(root string, expected RuntimeArtifactDescriptor) bool {
	data, err := os.ReadFile(filepath.Join(root, runtimeIdentityFile))
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(data)), expected.ArchiveSHA256)
}

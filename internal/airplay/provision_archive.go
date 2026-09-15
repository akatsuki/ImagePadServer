package airplay

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type bundleManifest struct {
	Version       string       `json:"version"`
	ArchiveURL    string       `json:"archive_url"`
	ArchiveSHA256 string       `json:"archive_sha256"`
	ArchiveSize   uint64       `json:"archive_size"`
	Files         []bundleFile `json:"files"`
}

type bundleFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

var requiredUxPlayFiles = []string{
	"uxplay-windows.exe",
	"uxplay-bluetooth-beacon.exe",
	"mDNSResponder.exe",
	"dnssd.dll",
	"Qt6Core.dll",
	"Qt6Gui.dll",
	"Qt6Network.dll",
	"Qt6Widgets.dll",
	"platforms/qwindows.dll",
	"libgstreamer-1.0-0.dll",
	"libgstapp-1.0-0.dll",
	"libgstbase-1.0-0.dll",
	"libgstvideo-1.0-0.dll",
	"libgstpbutils-1.0-0.dll",
	"lib/gstreamer-1.0/libgstrtp.dll",
	"lib/gstreamer-1.0/libgstudp.dll",
	"libgstrtp-1.0-0.dll",
	"libgstnet-1.0-0.dll",
	"lib/gstreamer-1.0/libgstcoreelements.dll",
	"lib/gstreamer-1.0/libgstapp.dll",
	"lib/gstreamer-1.0/libgstaudioconvert.dll",
	"lib/gstreamer-1.0/libgstaudioresample.dll",
	"lib/gstreamer-1.0/libgstautodetect.dll",
	"lib/gstreamer-1.0/libgstplayback.dll",
	"lib/gstreamer-1.0/libgstvideoconvertscale.dll",
	"lib/gstreamer-1.0/libgstvolume.dll",
	"resources/uxplay_arguments_list.txt",
}

func installUxPlayArchive(archivePath, installDir string) error {
	if err := validateUxPlayArchive(archivePath); err != nil {
		return fmt.Errorf("verify cached AirPlay archive: %w", err)
	}
	parent := filepath.Dir(installDir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("create AirPlay module directory: %w", err)
	}
	if _, err := installedUxPlayPath(installDir); err == nil {
		return nil
	}

	tmpDir, err := os.MkdirTemp(parent, ".uxplay-install-*")
	if err != nil {
		return fmt.Errorf("create temporary AirPlay module directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	manifest, err := extractUxPlayArchive(archivePath, tmpDir)
	if err != nil {
		return err
	}
	if err := installUxPlayRTPSupplement(tmpDir, &manifest); err != nil {
		return fmt.Errorf("install verified RTP dependencies: %w", err)
	}
	if err := validateBundleManifest(tmpDir, manifest); err != nil {
		return fmt.Errorf("validate extracted AirPlay bundle: %w", err)
	}
	if err := writeBundleCompletion(tmpDir, manifest); err != nil {
		return err
	}
	if _, err := installedUxPlayPath(installDir); err == nil {
		return nil
	}
	if err := os.RemoveAll(installDir); err != nil {
		return fmt.Errorf("remove incomplete AirPlay module: %w", err)
	}
	if err := os.Rename(tmpDir, installDir); err != nil {
		return fmt.Errorf("atomically install AirPlay module: %w", err)
	}
	if _, err := installedUxPlayPath(installDir); err != nil {
		return fmt.Errorf("verify installed AirPlay module: %w", err)
	}
	return nil
}

func extractUxPlayArchive(archivePath, destination string) (bundleManifest, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return bundleManifest{}, fmt.Errorf("open AirPlay archive: %w", err)
	}
	defer reader.Close()

	manifest := bundleManifest{
		Version:       uxPlayVersion,
		ArchiveURL:    uxPlayArchiveURL,
		ArchiveSHA256: uxPlayArchiveSHA256,
		ArchiveSize:   uxPlayArchiveSize,
	}
	seen := make(map[string]struct{})
	var expanded uint64
	if len(reader.File) > 10000 {
		return bundleManifest{}, fmt.Errorf("AirPlay archive contains too many entries")
	}
	for _, entry := range reader.File {
		cleanName, err := safeZipPath(entry.Name)
		if err != nil {
			return bundleManifest{}, fmt.Errorf("reject archive entry %q: %w", entry.Name, err)
		}
		if cleanName == uxPlayCompletionFile {
			return bundleManifest{}, fmt.Errorf("archive entry %q is reserved", entry.Name)
		}
		canonicalName := strings.ToLower(cleanName)
		if _, ok := seen[canonicalName]; ok {
			return bundleManifest{}, fmt.Errorf("duplicate archive entry %q", cleanName)
		}
		seen[canonicalName] = struct{}{}

		target := filepath.Join(destination, filepath.FromSlash(cleanName))
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return bundleManifest{}, fmt.Errorf("create extracted directory %q: %w", cleanName, err)
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return bundleManifest{}, fmt.Errorf("symlink archive entry %q is not allowed", cleanName)
		}
		if entry.UncompressedSize64 > uxPlayFileLimit {
			return bundleManifest{}, fmt.Errorf("archive entry %q is too large", cleanName)
		}
		if expanded > uxPlayExpandedLimit-entry.UncompressedSize64 {
			return bundleManifest{}, fmt.Errorf("expanded AirPlay archive exceeds limit")
		}
		expanded += entry.UncompressedSize64
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return bundleManifest{}, fmt.Errorf("create parent for %q: %w", cleanName, err)
		}
		in, err := entry.Open()
		if err != nil {
			return bundleManifest{}, fmt.Errorf("open archive entry %q: %w", cleanName, err)
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			in.Close()
			return bundleManifest{}, fmt.Errorf("create extracted file %q: %w", cleanName, err)
		}
		h := sha256.New()
		limited := io.LimitReader(in, int64(entry.UncompressedSize64)+1)
		n, copyErr := io.Copy(io.MultiWriter(out, h), limited)
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr != nil {
			return bundleManifest{}, fmt.Errorf("extract archive entry %q: %w", cleanName, copyErr)
		}
		if closeInErr != nil || closeOutErr != nil {
			return bundleManifest{}, fmt.Errorf("close extracted file %q", cleanName)
		}
		if uint64(n) != entry.UncompressedSize64 {
			return bundleManifest{}, fmt.Errorf("archive entry %q size changed while extracting", cleanName)
		}
		manifest.Files = append(manifest.Files, bundleFile{
			Path: cleanName, Size: n, SHA256: fmt.Sprintf("%x", h.Sum(nil)),
		})
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	return manifest, nil
}

func safeZipPath(name string) (string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("empty or NUL-containing path")
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "//") {
		return "", fmt.Errorf("absolute path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" {
			continue
		}
		if part == ".." || strings.Contains(part, ":") {
			return "", fmt.Errorf("path traversal")
		}
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return "", fmt.Errorf("Windows path component has a trailing dot or space")
		}
		base := strings.TrimSuffix(part, filepath.Ext(part))
		switch strings.ToUpper(base) {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return "", fmt.Errorf("Windows reserved path component")
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path traversal")
	}
	return clean, nil
}

func writeBundleCompletion(root string, manifest bundleManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode AirPlay completion marker: %w", err)
	}
	data = append(data, byte(10))
	return atomicWriteFile(filepath.Join(root, uxPlayCompletionFile), data, 0600)
}

func installedUxPlayPath(root string) (string, error) {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("AirPlay module path is not a directory")
		}
		return "", err
	}
	markerPath := filepath.Join(root, uxPlayCompletionFile)
	markerData, err := os.ReadFile(markerPath)
	if err != nil {
		return "", fmt.Errorf("read AirPlay completion marker: %w", err)
	}
	if len(markerData) > 1024*1024 {
		return "", fmt.Errorf("AirPlay completion marker is too large")
	}
	var manifest bundleManifest
	if err := json.Unmarshal(markerData, &manifest); err != nil {
		return "", fmt.Errorf("parse AirPlay completion marker: %w", err)
	}
	if err := validateBundleManifest(root, manifest); err != nil {
		return "", err
	}
	return filepath.Join(root, "uxplay-windows.exe"), nil
}

func validateBundleManifest(root string, manifest bundleManifest) error {
	if manifest.Version != uxPlayVersion || manifest.ArchiveURL != uxPlayArchiveURL ||
		manifest.ArchiveSHA256 != uxPlayArchiveSHA256 || manifest.ArchiveSize != uxPlayArchiveSize {
		return fmt.Errorf("AirPlay completion marker does not match pinned bundle")
	}
	if len(manifest.Files) == 0 {
		return fmt.Errorf("AirPlay bundle contains no files")
	}
	seen := make(map[string]bundleFile, len(manifest.Files))
	for _, file := range manifest.Files {
		clean, err := safeZipPath(file.Path)
		if err != nil || clean != file.Path || file.Size < 0 || uint64(file.Size) > uxPlayFileLimit {
			return fmt.Errorf("invalid AirPlay manifest entry %q", file.Path)
		}
		canonicalName := strings.ToLower(file.Path)
		if _, ok := seen[canonicalName]; ok {
			return fmt.Errorf("duplicate AirPlay manifest entry %q", file.Path)
		}
		seen[canonicalName] = file
		actual := filepath.Join(root, filepath.FromSlash(file.Path))
		info, err := os.Stat(actual)
		if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			return fmt.Errorf("AirPlay bundle file %q is missing or has the wrong size", file.Path)
		}
		_, digest, err := hashFile(actual, uxPlayFileLimit)
		if err != nil {
			return fmt.Errorf("hash AirPlay bundle file %q: %w", file.Path, err)
		}
		if digest != file.SHA256 {
			return fmt.Errorf("AirPlay bundle file %q is corrupted", file.Path)
		}
	}
	if err := rejectUnexpectedBundleFiles(root, seen); err != nil {
		return err
	}
	if err := validateRequiredUxPlayFiles(root, seen); err != nil {
		return err
	}
	return nil
}

func rejectUnexpectedBundleFiles(root string, manifest map[string]bundleFile) error {
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == uxPlayCompletionFile {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("AirPlay bundle contains symlink %q", rel)
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := manifest[strings.ToLower(rel)]; !ok {
			return fmt.Errorf("AirPlay bundle contains unexpected file %q", rel)
		}
		return nil
	})
}

func validateRequiredUxPlayFiles(root string, manifest map[string]bundleFile) error {
	for _, required := range requiredUxPlayFiles {
		if _, ok := manifest[strings.ToLower(required)]; !ok {
			return fmt.Errorf("AirPlay bundle is missing required file %q", required)
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(required)))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("AirPlay required file %q is unusable", required)
		}
	}
	for _, name := range []string{"uxplay-windows.exe", "uxplay-bluetooth-beacon.exe", "mDNSResponder.exe"} {
		if err := validatePEFile(filepath.Join(root, name)); err != nil {
			return fmt.Errorf("validate AirPlay executable %q: %w", name, err)
		}
	}
	return nil
}

func validatePEFile(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() < 64 {
		return fmt.Errorf("file is too small to be a PE executable")
	}
	var dos [64]byte
	if _, err := io.ReadFull(file, dos[:]); err != nil {
		return err
	}
	if string(dos[:2]) != "MZ" {
		return fmt.Errorf("missing MZ header")
	}
	peOffset := int64(binary.LittleEndian.Uint32(dos[60:64]))
	if peOffset < 0 || peOffset+24 > info.Size() {
		return fmt.Errorf("invalid PE header offset")
	}
	var pe [24]byte
	if _, err := file.ReadAt(pe[:], peOffset); err != nil {
		return err
	}
	if string(pe[:4]) != "PE\x00\x00" {
		return fmt.Errorf("missing PE signature")
	}
	if binary.LittleEndian.Uint16(pe[4:6]) != 0x8664 {
		return fmt.Errorf("executable is not Windows amd64")
	}
	return nil
}

func atomicWriteFile(filePath string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(filePath)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".atomic-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, filePath)
}

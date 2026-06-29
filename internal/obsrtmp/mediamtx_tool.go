package obsrtmp

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/settings"
)

const (
	// This is the official Windows amd64 release asset. The checksum was
	// independently re-verified before pinning; URL and hash must move together.
	mediaMTXVersion         = "v1.19.2"
	mediaMTXArchiveName     = "mediamtx_v1.19.2_windows_amd64.zip"
	mediaMTXArchiveSHA256   = "53028b551afcc8d9ddbd56eb8406d5b31e395e5505d52e28347f211696be9345"
	mediaMTXArchiveMaxBytes = 64 << 20
	mediaMTXChecksumMarker  = ".archive.sha256"
)

var errMediaMTXNotFound = errors.New("MediaMTX is not installed")

type mediaMTXToolManager struct {
	mu            sync.Mutex
	version       string
	downloadURL   string
	archiveSHA256 string
	maxBytes      int64
	bundledRoot   string
	managedRoot   string
	getenv        func(string) string
	validate      func(string) error
	client        *http.Client
	mkdirAll      func(string, os.FileMode) error
	rename        func(string, string) error
	removeAll     func(string) error
}

var defaultMediaMTXToolManager = newMediaMTXToolManager()

func newMediaMTXToolManager() *mediaMTXToolManager {
	executableDir := "."
	if executable, err := os.Executable(); err == nil {
		executableDir = filepath.Dir(executable)
	}
	return &mediaMTXToolManager{
		version:       mediaMTXVersion,
		downloadURL:   "https://github.com/bluenviron/mediamtx/releases/download/" + mediaMTXVersion + "/" + mediaMTXArchiveName,
		archiveSHA256: mediaMTXArchiveSHA256,
		maxBytes:      mediaMTXArchiveMaxBytes,
		bundledRoot:   filepath.Join(executableDir, "tools", "mediamtx"),
		managedRoot:   filepath.Join(settings.Dir(), "bin", "mediamtx"),
		getenv:        os.Getenv,
		validate:      validateMediaMTXExecutable,
		client:        &http.Client{Timeout: 5 * time.Minute},
		mkdirAll:      os.MkdirAll,
		rename:        os.Rename,
		removeAll:     os.RemoveAll,
	}
}

// ResolveMediaMTX returns an existing trusted MediaMTX executable without
// downloading anything.
func ResolveMediaMTX() (string, error) {
	return defaultMediaMTXToolManager.resolve()
}

// EnsureMediaMTX resolves or atomically installs the pinned MediaMTX release.
func EnsureMediaMTX(ctx context.Context) (string, error) {
	return defaultMediaMTXToolManager.ensure(ctx)
}

func (m *mediaMTXToolManager) resolve() (string, error) {
	if configured := strings.TrimSpace(m.getenv("IMAGEPAD_MEDIAMTX")); configured != "" {
		if err := m.validate(configured); err != nil {
			return "", fmt.Errorf("IMAGEPAD_MEDIAMTX is not a usable executable: %w", err)
		}
		return configured, nil
	}

	if path, found, err := m.resolveDirectory(filepath.Join(m.bundledRoot, m.version), false); found || err != nil {
		return path, err
	}
	if path, found, err := m.resolveDirectory(filepath.Join(m.managedRoot, m.version), true); found || err != nil {
		return path, err
	}
	return "", errMediaMTXNotFound
}

func (m *mediaMTXToolManager) resolveDirectory(dir string, requireMarker bool) (string, bool, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("inspect MediaMTX directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", true, fmt.Errorf("MediaMTX path is not a directory: %s", dir)
	}
	if requireMarker {
		marker, err := os.ReadFile(filepath.Join(dir, mediaMTXChecksumMarker))
		if err != nil {
			return "", true, fmt.Errorf("read MediaMTX checksum marker: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(string(marker)), m.archiveSHA256) {
			return "", true, errors.New("MediaMTX managed install checksum marker does not match the pinned release")
		}
	}
	path := filepath.Join(dir, mediaMTXExecutableName())
	if err := m.validate(path); err != nil {
		return "", true, fmt.Errorf("validate MediaMTX executable %s: %w", path, err)
	}
	return path, true, nil
}

func (m *mediaMTXToolManager) ensure(ctx context.Context) (string, error) {
	if path, err := m.resolve(); err == nil {
		return path, nil
	} else if !errors.Is(err, errMediaMTXNotFound) {
		return "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if path, err := m.resolve(); err == nil {
		return path, nil
	} else if !errors.Is(err, errMediaMTXNotFound) {
		return "", err
	}
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return "", fmt.Errorf("automatic MediaMTX installation is unsupported on %s/%s; set IMAGEPAD_MEDIAMTX", runtime.GOOS, runtime.GOARCH)
	}
	if err := m.install(ctx); err != nil {
		return "", err
	}
	path, err := m.resolve()
	if err != nil {
		return "", fmt.Errorf("validate installed MediaMTX: %w", err)
	}
	return path, nil
}

func (m *mediaMTXToolManager) install(ctx context.Context) error {
	if err := m.mkdirAll(m.managedRoot, 0o755); err != nil {
		return fmt.Errorf("prepare MediaMTX install directory: %w", err)
	}

	archive, err := os.CreateTemp(m.managedRoot, ".mediamtx-download-*.zip")
	if err != nil {
		return fmt.Errorf("create MediaMTX download: %w", err)
	}
	archivePath := archive.Name()
	defer func() {
		_ = archive.Close()
		_ = os.Remove(archivePath)
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create MediaMTX download request: %w", err)
	}
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("download MediaMTX: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download MediaMTX: unexpected HTTP status %s", response.Status)
	}
	if response.ContentLength > m.maxBytes {
		return fmt.Errorf("download MediaMTX: archive exceeds %d bytes", m.maxBytes)
	}

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(response.Body, m.maxBytes+1))
	if err != nil {
		return fmt.Errorf("download MediaMTX: %w", err)
	}
	if written == 0 {
		return errors.New("download MediaMTX: archive is empty")
	}
	if written > m.maxBytes {
		return fmt.Errorf("download MediaMTX: archive exceeds %d bytes", m.maxBytes)
	}
	actualChecksum := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actualChecksum, strings.TrimSpace(m.archiveSHA256)) {
		return fmt.Errorf("download MediaMTX: checksum mismatch: want %s, got %s", m.archiveSHA256, actualChecksum)
	}
	if err := archive.Sync(); err != nil {
		return fmt.Errorf("sync MediaMTX archive: %w", err)
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close MediaMTX archive: %w", err)
	}

	staging, err := os.MkdirTemp(m.managedRoot, ".mediamtx-install-*")
	if err != nil {
		return fmt.Errorf("create MediaMTX staging directory: %w", err)
	}
	defer m.removeAll(staging)
	if err := m.extractArchive(archivePath, staging); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, mediaMTXChecksumMarker), []byte(strings.ToLower(m.archiveSHA256)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write MediaMTX checksum marker: %w", err)
	}
	if err := m.validate(filepath.Join(staging, mediaMTXExecutableName())); err != nil {
		return fmt.Errorf("validate downloaded MediaMTX executable: %w", err)
	}
	if err := m.publishInstall(staging); err != nil {
		return err
	}
	return nil
}

func (m *mediaMTXToolManager) extractArchive(archivePath, staging string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open MediaMTX archive: %w", err)
	}
	defer reader.Close()

	allowed := map[string]os.FileMode{
		mediaMTXExecutableName(): 0o755,
		"mediamtx.yml":           0o644,
		"LICENSE":                0o644,
	}
	extracted := make(map[string]bool, len(allowed))
	var total int64
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		if name != filepath.Base(name) || strings.Contains(name, "..") {
			continue
		}
		mode, ok := allowed[name]
		if !ok {
			continue
		}
		if extracted[name] || file.FileInfo().IsDir() {
			return fmt.Errorf("MediaMTX archive contains invalid duplicate entry %q", name)
		}
		if file.UncompressedSize64 > uint64(m.maxBytes) || total+int64(file.UncompressedSize64) > m.maxBytes {
			return errors.New("MediaMTX archive exceeds the extraction size limit")
		}
		total += int64(file.UncompressedSize64)
		input, err := file.Open()
		if err != nil {
			return fmt.Errorf("open MediaMTX archive entry %q: %w", name, err)
		}
		output, err := os.OpenFile(filepath.Join(staging, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			input.Close()
			return fmt.Errorf("create MediaMTX file %q: %w", name, err)
		}
		copied, copyErr := io.Copy(output, io.LimitReader(input, m.maxBytes+1))
		closeErr := output.Close()
		input.Close()
		if copyErr != nil {
			return fmt.Errorf("extract MediaMTX file %q: %w", name, copyErr)
		}
		if copied != int64(file.UncompressedSize64) || copied > m.maxBytes {
			return fmt.Errorf("extract MediaMTX file %q: invalid size", name)
		}
		if closeErr != nil {
			return fmt.Errorf("close MediaMTX file %q: %w", name, closeErr)
		}
		extracted[name] = true
	}
	for name := range allowed {
		if !extracted[name] {
			return fmt.Errorf("MediaMTX archive is missing required file %q", name)
		}
	}
	return nil
}

func (m *mediaMTXToolManager) publishInstall(staging string) error {
	target := filepath.Join(m.managedRoot, m.version)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		if err := m.rename(staging, target); err != nil {
			return fmt.Errorf("publish MediaMTX install: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect existing MediaMTX install: %w", err)
	}

	backup := staging + "-previous"
	if err := m.rename(target, backup); err != nil {
		return fmt.Errorf("preserve existing MediaMTX install: %w", err)
	}
	if err := m.rename(staging, target); err != nil {
		if restoreErr := m.rename(backup, target); restoreErr != nil {
			return fmt.Errorf("publish MediaMTX install: %w (restore failed: %v)", err, restoreErr)
		}
		return fmt.Errorf("publish MediaMTX install: %w", err)
	}
	if err := m.removeAll(backup); err != nil {
		return fmt.Errorf("remove previous MediaMTX install: %w", err)
	}
	return nil
}

func validateMediaMTXExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	command := exec.Command(path, "--version")
	hideWindow(command)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run --version: %w", err)
	}
	if !isPinnedMediaMTXVersion(string(output)) {
		return fmt.Errorf("unexpected version output %q", strings.TrimSpace(string(output)))
	}
	return nil
}

func isPinnedMediaMTXVersion(output string) bool {
	fields := strings.Fields(output)
	return len(fields) > 0 && strings.EqualFold(fields[len(fields)-1], mediaMTXVersion)
}

func mediaMTXExecutableName() string {
	if runtime.GOOS == "windows" {
		return "mediamtx.exe"
	}
	return "mediamtx"
}

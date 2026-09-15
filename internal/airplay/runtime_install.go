package airplay

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	runtimeActiveFile   = "active-runtime-set"
	runtimeLockFile     = ".airplay-runtime.lock"
	runtimeIdentityFile = ".imagepad-runtime-archive-identity"
	runtimeFileLimit    = uint64(128 * 1024 * 1024)
	runtimeTotalLimit   = uint64(2 * 1024 * 1024 * 1024)
)

type runtimeFileLock struct {
	filePath string
	file     *os.File
}

func acquireRuntimeFileLock(ctx context.Context, lockPath string, staleAfter time.Duration) (*runtimeFileLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(lockPath) == "" {
		return nil, errors.New("AirPlay runtime lock path is empty")
	}
	if staleAfter <= 0 {
		staleAfter = 10 * time.Minute
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("create AirPlay runtime lock directory: %w", err)
	}
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if _, writeErr := fmt.Fprintf(file, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339)); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, writeErr
			}
			return &runtimeFileLock{filePath: lockPath, file: file}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create AirPlay runtime lock: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > staleAfter {
			// This is the exact lock path; never sweep a directory or kill a
			// process based on a stale filename.
			_ = os.Remove(lockPath)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for AirPlay runtime lock: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (lock *runtimeFileLock) Close() error {
	if lock == nil {
		return nil
	}
	var err error
	if lock.file != nil {
		err = lock.file.Close()
	}
	if removeErr := os.Remove(lock.filePath); err == nil && !os.IsNotExist(removeErr) {
		err = removeErr
	}
	return err
}

// InstallAirPlayRuntimeArchive installs a verified descriptor-backed archive
// into one immutable runtimeSetID directory and atomically updates the small
// active marker. Existing sets are never removed or overwritten.
func InstallAirPlayRuntimeArchive(ctx context.Context, archivePath, runtimeRoot string, descriptor RuntimeDescriptor) (AirPlayRuntime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := descriptor.validate(); err != nil {
		return AirPlayRuntime{}, err
	}
	if strings.TrimSpace(archivePath) == "" || strings.TrimSpace(runtimeRoot) == "" {
		return AirPlayRuntime{}, errors.New("AirPlay runtime archive path and root are required")
	}
	root, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return AirPlayRuntime{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return AirPlayRuntime{}, fmt.Errorf("create AirPlay runtime root: %w", err)
	}
	lock, err := acquireRuntimeFileLock(ctx, filepath.Join(root, runtimeLockFile), 10*time.Minute)
	if err != nil {
		return AirPlayRuntime{}, err
	}
	defer lock.Close()
	archiveIdentity, err := runtimeArchiveIdentity(archivePath)
	if err != nil {
		return AirPlayRuntime{}, err
	}
	setDir := filepath.Join(root, descriptor.RuntimeSetID)
	activeSetID := descriptor.RuntimeSetID
	if existing, existingErr := resolveAirPlayRuntime(setDir); existingErr == nil {
		if installedIdentity, readErr := os.ReadFile(filepath.Join(setDir, runtimeIdentityFile)); readErr == nil && strings.TrimSpace(string(installedIdentity)) == archiveIdentity && validateRuntimeFileManifest(setDir) == nil {
			if err := writeActiveRuntimeSet(root, descriptor.RuntimeSetID); err != nil {
				return AirPlayRuntime{}, err
			}
			return existing, nil
		}
		setDir = filepath.Join(root, descriptor.RuntimeSetID+"-"+archiveIdentity[:16])
		activeSetID = filepath.Base(setDir)
		if replacement, replacementErr := resolveAirPlayRuntime(setDir); replacementErr == nil {
			if installedIdentity, readErr := os.ReadFile(filepath.Join(setDir, runtimeIdentityFile)); readErr == nil && strings.TrimSpace(string(installedIdentity)) == archiveIdentity && validateRuntimeFileManifest(setDir) == nil {
				if err := writeActiveRuntimeSet(root, filepath.Base(setDir)); err != nil {
					return AirPlayRuntime{}, err
				}
				return replacement, nil
			}
			return AirPlayRuntime{}, errors.New("AirPlay runtime archive identity conflicts with an existing generation")
		}
		if _, statErr := os.Stat(setDir); statErr == nil {
			return AirPlayRuntime{}, errors.New("AirPlay runtime generation exists but is not valid; it was retained")
		}
		// Keep the last-good set and install the new archive under its own
		// immutable generation directory.
	}
	if _, statErr := os.Stat(setDir); statErr == nil {
		return AirPlayRuntime{}, errors.New("AirPlay runtime set directory exists but is not valid; it was retained")
	}

	staging, err := os.MkdirTemp(root, ".airplay-runtime-staging-*")
	if err != nil {
		return AirPlayRuntime{}, fmt.Errorf("create AirPlay runtime staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := extractRuntimeArchive(archivePath, staging); err != nil {
		return AirPlayRuntime{}, err
	}
	if err := validateRuntimeFileManifest(staging); err != nil {
		return AirPlayRuntime{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, runtimeIdentityFile), []byte(archiveIdentity+"\n"), 0600); err != nil {
		return AirPlayRuntime{}, fmt.Errorf("write AirPlay runtime archive identity: %w", err)
	}
	if _, err := resolveAirPlayRuntime(staging); err != nil {
		return AirPlayRuntime{}, fmt.Errorf("validate staged AirPlay runtime: %w", err)
	}
	if err := os.Rename(staging, setDir); err != nil {
		return AirPlayRuntime{}, fmt.Errorf("activate AirPlay runtime set: %w", err)
	}
	installed, err := resolveAirPlayRuntime(setDir)
	if err != nil {
		return AirPlayRuntime{}, fmt.Errorf("validate activated AirPlay runtime: %w", err)
	}
	if err := writeActiveRuntimeSet(root, activeSetID); err != nil {
		return AirPlayRuntime{}, err
	}
	return installed, nil
}

func runtimeArchiveIdentity(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open AirPlay runtime archive for identity: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash AirPlay runtime archive: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractRuntimeArchive(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open AirPlay runtime archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > 10000 {
		return errors.New("AirPlay runtime archive entry count is invalid")
	}
	seen := make(map[string]struct{}, len(reader.File))
	var total uint64
	for _, entry := range reader.File {
		clean, err := safeZipPath(entry.Name)
		if err != nil {
			return fmt.Errorf("reject AirPlay runtime archive entry %q: %w", entry.Name, err)
		}
		key := strings.ToLower(clean)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate AirPlay runtime archive entry %q", entry.Name)
		}
		seen[key] = struct{}{}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(filepath.Join(destination, filepath.FromSlash(clean)), 0700); err != nil {
				return err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 || entry.UncompressedSize64 > runtimeFileLimit || total > runtimeTotalLimit-entry.UncompressedSize64 {
			return fmt.Errorf("AirPlay runtime archive entry %q exceeds safe limits", entry.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			_ = input.Close()
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil || inputCloseErr != nil || outputCloseErr != nil || uint64(written) != entry.UncompressedSize64 {
			return fmt.Errorf("extract AirPlay runtime archive entry %q failed", entry.Name)
		}
		total += uint64(written)
	}
	return nil
}

func writeActiveRuntimeSet(root, setID string) error {
	if strings.TrimSpace(setID) == "" || strings.ContainsAny(setID, `/\\:`) || setID == "." || setID == ".." {
		return errors.New("AirPlay runtime set ID is invalid")
	}
	return atomicWriteFile(filepath.Join(root, runtimeActiveFile), []byte(setID+"\n"), 0600)
}

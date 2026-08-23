package airplay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/settings"
)

const (
	uxPlayVersion        = "2.0.0.1736"
	uxPlayArchiveURL     = "https://github.com/leapbtw/uxplay-windows/releases/download/2.0.0.1736/uxplay-windows.zip"
	uxPlayArchiveSHA256  = "9d3a51c15fc9db857351195e7eb7bbb21700d9ae25d936a54bcf8536b62cca18"
	uxPlayArchiveSize    = uint64(113529789)
	uxPlayCompletionFile = ".complete.json"
	uxPlayArchiveLimit   = uint64(256 << 20)
	uxPlayExpandedLimit  = uint64(512 << 20)
	uxPlayFileLimit      = uint64(128 << 20)
	uxPlaySetupTimeout   = 15 * time.Minute
)

var provisionMu sync.Mutex

var errAutomaticSetupUnsupported = errors.New("automatic AirPlay setup is supported only on Windows amd64")

// EnsureUxPlay downloads and installs the complete pinned Windows receiver
// bundle when it is not already present. The bundle contains UxPlay,
// GStreamer, Bonjour, Qt, FFmpeg codecs, plugins, and their DLLs.
func EnsureUxPlay(ctx context.Context) (string, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return "", errAutomaticSetupUnsupported
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, uxPlaySetupTimeout)
		defer cancel()
	}

	provisionMu.Lock()
	defer provisionMu.Unlock()

	installDir, err := uxPlayInstallDir()
	if err != nil {
		return "", err
	}
	if path, err := installedUxPlayPath(installDir); err == nil {
		if err := ensureBonjour(ctx, installDir); err != nil {
			return "", err
		}
		return path, nil
	}

	archivePath, err := ensureUxPlayArchive(ctx)
	if err != nil {
		return "", err
	}
	if err := installUxPlayArchive(archivePath, installDir); err != nil {
		return "", err
	}
	if err := ensureBonjour(ctx, installDir); err != nil {
		return "", err
	}
	return filepath.Join(installDir, "uxplay-windows.exe"), nil
}

// InstalledUxPlayPath returns a verified cached receiver without downloading
// or changing the Bonjour service.
func InstalledUxPlayPath() (string, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return "", errAutomaticSetupUnsupported
	}
	dir, err := uxPlayInstallDir()
	if err != nil {
		return "", err
	}
	return installedUxPlayPath(dir)
}

func uxPlayInstallDir() (string, error) {
	dataDir := settings.Dir()
	if dataDir == "" {
		return "", errors.New("ImagePadServer data directory is empty")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve ImagePadServer data directory: %w", err)
	}
	return filepath.Join(absolute, "modules", "uxplay", uxPlayVersion), nil
}

func uxPlayArchivePath() (string, error) {
	dir, err := uxPlayInstallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dir), "cache", "uxplay-windows-"+uxPlayVersion+".zip"), nil
}

func hasExplicitReceiverPath() bool {
	return strings.TrimSpace(os.Getenv("IMAGEPAD_UXPLAY")) != "" ||
		strings.TrimSpace(os.Getenv("IMAGEPAD_AIRPLAY_RECEIVER")) != ""
}

// PrepareOnStartup warms the verified Windows receiver bundle when AirPlay is
// explicitly enabled and no receiver path was supplied by the user. It is a
// no-op for disabled AirPlay, an explicit receiver, or an already available
// receiver on PATH/cache.
func PrepareOnStartup(ctx context.Context) (string, error) {
	if !FeatureEnabled() || hasExplicitReceiverPath() {
		return "", nil
	}
	if path, err := resolveReceiverOnPATH(); err == nil {
		return path, nil
	}
	return EnsureUxPlay(ctx)
}

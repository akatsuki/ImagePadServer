package toolchain

import (
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
	"time"

	"imagepadserver/internal/settings"
)

func downloadFFmpeg() (string, error) {
	if runtime.GOOS == "darwin" {
		return downloadDarwinFFmpeg()
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("automatic FFmpeg download is currently supported on Windows and macOS only")
	}

	target := localFFmpegPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare FFmpeg folder: %w", err)
	}

	installBegin("ffmpeg")
	// Avoid re-downloading on every app update: if a previous version's bundle
	// (or the legacy flat layout) already holds working ffmpeg + ffprobe, copy
	// them into this version's directory instead of fetching ~100 MB again.
	// If migration cannot complete — including when a copy is blocked by a lock —
	// it returns false and we fall through to a fresh download below.
	if migrateFFmpegToolsInto(filepath.Dir(target)) {
		installDone()
		return target, nil
	}
	zipPath := filepath.Join(filepath.Dir(target), "ffmpeg-download.zip")
	envChecksum := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_SHA256"))
	if envChecksum == "" {
		envChecksum = ffmpegDownloadSHA256
	}
	attempt := func(src toolSource) error {
		// Precedence: env override (tests) > inline per-source checksum >
		// sidecar checksumURL fetched at download time.
		checksum := envChecksum
		if checksum == "" {
			checksum = src.checksum
		}
		if checksum == "" && src.checksumURL != "" {
			c, err := remoteTextSHA256(src.checksumURL)
			if err != nil {
				return fmt.Errorf("failed to resolve FFmpeg checksum: %w", err)
			}
			checksum = c
		}
		if checksum != "" {
			if err := downloadFile(zipPath, src.url, 160<<20, checksum); err != nil {
				return fmt.Errorf("failed to download FFmpeg: %w", err)
			}
		} else {
			if err := downloadFileAllowMissingChecksum(zipPath, src.url, 160<<20, ""); err != nil {
				return fmt.Errorf("failed to download FFmpeg: %w", err)
			}
		}
		defer os.Remove(zipPath)
		installPhase("extract")
		if err := extractFFmpegZip(zipPath, target); err != nil {
			return fmt.Errorf("failed to install FFmpeg: %w", err)
		}
		installPhase("validate")
		if err := validateExecutable(target, "-version"); err != nil {
			_ = os.Remove(target)
			return err
		}
		return nil
	}
	if err := acquireFromSources("ffmpeg", ffmpegWindowsSources(), 2, attempt); err != nil {
		installFail(err.Error())
		return "", err
	}
	installDone()
	return target, nil
}

func downloadYTDLP() (string, error) {
	return downloadYTDLPWithChecksum("")
}

func downloadYTDLPWithChecksum(checksum string) (string, error) {
	if runtime.GOOS == "darwin" {
		return downloadDarwinYTDLPWithChecksum(checksum)
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("automatic yt-dlp download is currently supported on Windows and macOS only")
	}
	target := localYTDLPPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare yt-dlp folder: %w", err)
	}

	// Reuse an existing yt-dlp from an older version dir (or the legacy flat
	// layout) instead of re-downloading — but only when it already matches the
	// wanted checksum, so the "update to latest" path still fetches a new build.
	if migrateYTDLPInto(filepath.Dir(target), strings.TrimSpace(checksum)) {
		return target, nil
	}

	installBegin("yt-dlp")
	envChecksum := strings.TrimSpace(checksum)
	if envChecksum == "" {
		envChecksum = strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP_SHA256"))
	}
	if envChecksum == "" {
		envChecksum = ytdlpDownloadSHA256
	}
	attempt := func(src toolSource) error {
		sum := envChecksum
		if sum == "" {
			if c, err := remoteSHA256For("yt-dlp.exe"); err == nil {
				sum = c
			}
		}
		if sum != "" {
			if err := downloadFile(target, src.url, 50<<20, sum); err != nil {
				return fmt.Errorf("failed to download yt-dlp: %w", err)
			}
		} else {
			if err := downloadFileAllowMissingChecksum(target, src.url, 50<<20, ""); err != nil {
				return fmt.Errorf("failed to download yt-dlp: %w", err)
			}
		}
		installPhase("validate")
		if err := validateExecutable(target, "--version"); err != nil {
			_ = os.Remove(target)
			return err
		}
		return nil
	}
	if err := acquireFromSources("yt-dlp", ytdlpSources(), 2, attempt); err != nil {
		installFail(err.Error())
		return "", err
	}
	installDone()
	return target, nil
}

func downloadDarwinFFmpeg() (string, error) {
	target := localFFmpegPath()
	probeTarget := localFFprobePath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare FFmpeg folder: %w", err)
	}
	rawURL, err := darwinToolDownloadURL(runtime.GOARCH, "ffmpeg")
	if err != nil {
		return "", err
	}
	zipPath := filepath.Join(settings.Dir(), "bin", "ffmpeg-macos.zip")
	checksum := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_SHA256"))
	if err := downloadFileAllowMissingChecksum(zipPath, rawURL, 180<<20, checksum); err != nil {
		return "", fmt.Errorf("failed to download FFmpeg: %w", err)
	}
	defer os.Remove(zipPath)
	if err := extractNamedBinaryFromZip(zipPath, target, "ffmpeg"); err != nil {
		return "", fmt.Errorf("failed to install FFmpeg: %w", err)
	}
	if err := validateExecutable(target, "-version"); err != nil {
		_ = os.Remove(target)
		return "", err
	}

	probeURL, err := darwinToolDownloadURL(runtime.GOARCH, "ffprobe")
	if err != nil {
		_ = os.Remove(target)
		return "", err
	}
	probeZipPath := filepath.Join(settings.Dir(), "bin", "ffprobe-macos.zip")
	if err := downloadFileAllowMissingChecksum(probeZipPath, probeURL, 80<<20, ""); err != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("failed to download ffprobe: %w", err)
	}
	defer os.Remove(probeZipPath)
	if err := extractNamedBinaryFromZip(probeZipPath, probeTarget, "ffprobe"); err != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("failed to install ffprobe: %w", err)
	}
	if err := validateExecutable(probeTarget, "-version"); err != nil {
		_ = os.Remove(target)
		_ = os.Remove(probeTarget)
		return "", err
	}
	return target, nil
}

func darwinToolDownloadURL(arch, tool string) (string, error) {
	if arch != "arm64" && arch != "amd64" {
		return "", fmt.Errorf("automatic FFmpeg install is not available for darwin/%s", arch)
	}
	if tool != "ffmpeg" && tool != "ffprobe" {
		return "", fmt.Errorf("unsupported Darwin FFmpeg tool %q", tool)
	}
	return fmt.Sprintf("https://ffmpeg.martin-riedl.de/redirect/latest/macos/%s/release/%s.zip", arch, tool), nil
}

func downloadDarwinYTDLPWithChecksum(checksum string) (string, error) {
	target := localYTDLPPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare yt-dlp folder: %w", err)
	}
	checksum = strings.TrimSpace(checksum)
	if checksum == "" {
		checksum = strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP_SHA256"))
	}
	if checksum == "" {
		var err error
		checksum, err = remoteSHA256For("yt-dlp_macos")
		if err != nil {
			return "", fmt.Errorf("failed to resolve yt-dlp checksum: %w", err)
		}
	}
	if err := downloadExecutable(target, ytdlpMacOSURL, 80<<20, checksum); err != nil {
		return "", fmt.Errorf("failed to download yt-dlp: %w", err)
	}
	if err := validateExecutable(target, "--version"); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}

// ---------------------------------------------------------------------------
// Download helpers
// ---------------------------------------------------------------------------

func downloadFile(path, rawURL string, maxBytes int64, expectedSHA256 string) error {
	if strings.TrimSpace(expectedSHA256) == "" {
		return errors.New("missing SHA256 checksum for trusted download")
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("download exceeds size limit")
	}

	tempPath := path + ".tmp"
	out, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	installPhase("download")
	pw := &progressWriter{total: resp.ContentLength, onProgress: installPercent}
	written, copyErr := io.Copy(out, io.TeeReader(io.LimitReader(resp.Body, maxBytes+1), pw))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return closeErr
	}
	if written > maxBytes {
		_ = os.Remove(tempPath)
		return fmt.Errorf("download exceeds size limit")
	}

	if err := verifySHA256(tempPath, expectedSHA256); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return replaceFile(path, tempPath)
}

func downloadFileAllowMissingChecksum(path, rawURL string, maxBytes int64, expectedSHA256 string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("download exceeds size limit")
	}
	tempPath := path + ".tmp"
	out, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	installPhase("download")
	pw := &progressWriter{total: resp.ContentLength, onProgress: installPercent}
	written, copyErr := io.Copy(out, io.TeeReader(io.LimitReader(resp.Body, maxBytes+1), pw))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return closeErr
	}
	if written == 0 || written > maxBytes {
		_ = os.Remove(tempPath)
		return fmt.Errorf("download has invalid size")
	}
	if strings.TrimSpace(expectedSHA256) != "" {
		if err := verifySHA256(tempPath, expectedSHA256); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
	}
	return replaceFile(path, tempPath)
}

func downloadExecutable(path, rawURL string, maxBytes int64, expectedSHA256 string) error {
	if err := downloadFile(path, rawURL, maxBytes, expectedSHA256); err != nil {
		return err
	}
	return os.Chmod(path, 0755)
}

func remoteSHA256For(fileName string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, ytdlpSHA256SumsURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("checksum download returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == fileName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s was not found", fileName)
}

func remoteTextSHA256(rawURL string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("checksum download returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", errors.New("checksum response was empty")
	}
	return fields[0], nil
}

func verifySHA256(path, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return errors.New("expected SHA256 checksum is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	actual := hex.EncodeToString(hash[:])
	if actual != expected {
		return fmt.Errorf("download checksum mismatch: want %s, got %s", expected, actual)
	}
	return nil
}

func validateExecutable(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("installed executable validation failed: %w: %s", err, trimOutput(output))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Zip extraction — Windows FFmpeg archive
// ---------------------------------------------------------------------------

// extractFFmpegZip extracts both ffmpeg.exe and ffprobe.exe from the
// downloaded FFmpeg Essentials zip archive.  Returns an error wrapping
// "ffprobe not found after FFmpeg installation" when ffprobe.exe is
// absent from the archive.

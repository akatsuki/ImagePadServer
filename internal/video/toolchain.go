package video

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

	"imagepadserver/internal/about"
	"imagepadserver/internal/settings"
)

// Download URLs and checksum placeholders for external tools.
const (
	ffmpegDownloadURL = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
	ffmpegSHA256URL   = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip.sha256"
	// ffmpegGitHubURL is the same gyan "essentials" build mirrored on the
	// publisher's own GitHub repo. GitHub's CDN is much faster/steadier than
	// gyan.dev from many regions, so this is the primary source. GitHub has no
	// .sha256 sidecar, so the hash is pinned inline alongside the version and
	// must be bumped together with the URL. (Verified byte-identical to the
	// gyan.dev release-essentials.zip of the same version.)
	ffmpegGitHubURL    = "https://github.com/GyanD/codexffmpeg/releases/download/8.1.1/ffmpeg-8.1.1-essentials_build.zip"
	ffmpegGitHubSHA256 = "6f58ce889f59c311410f7d2b18895b33c03456463486f3b1ebc93d97a0f54541"
	// ffmpegPinnedVersion is the FFmpeg version this build expects (matches the
	// pinned download URL above). A previous app version's bundle is migrated
	// forward only when its ffmpeg reports this exact version; otherwise the
	// newer pinned build is downloaded instead.
	ffmpegPinnedVersion  = "8.1.1"
	ytdlpDownloadURL     = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"
	ytdlpMacOSURL        = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_macos"
	ytdlpSHA256SumsURL   = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/SHA2-256SUMS"
	ffmpegDownloadSHA256 = ""
	ytdlpDownloadSHA256  = ""
)

// executableName returns base with the OS-specific executable extension.
func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// ---------------------------------------------------------------------------
// ffprobePath and verifyVisualizerFFmpeg (AV-100 new API)
// ---------------------------------------------------------------------------

// ffprobePath resolves the ffprobe binary with the following priority:
//
//  1. IMAGEPAD_FFPROBE environment variable
//  2. Sibling directory of the resolved FFmpeg binary
//  3. App bin directory (settings.Dir()/bin/)
//  4. PATH
func ffprobePath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_FFPROBE")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		return "", fmt.Errorf("IMAGEPAD_FFPROBE does not exist: %s", configured)
	}
	if ffmpeg, err := ffmpegPath(); err == nil {
		sibling := filepath.Join(filepath.Dir(ffmpeg), executableName("ffprobe"))
		if fileExists(sibling) {
			return sibling, nil
		}
	}
	if local := localFFprobePath(); fileExists(local) {
		return local, nil
	}
	return "", fmt.Errorf("ffprobe not found in bundle; %s; you can also set IMAGEPAD_FFPROBE", toolInstallHint("ffmpeg"))
}

func localFFprobePath() string {
	return filepath.Join(toolVersionDir(), executableName("ffprobe"))
}

var (
	ffmpegBundleMu             sync.Mutex
	ytdlpBundleMu              sync.Mutex
	ffprobeBundleInstaller     = downloadFFmpeg
	ffmpegBundleInstaller      = downloadFFmpeg
	validateToolExecutable     = validateExecutable
	ffprobeExecutableValidator = func(path string) error {
		return validateExecutable(path, "-version")
	}
)

func usableFFprobePath() string {
	candidates := make([]string, 0, 4)
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_FFPROBE")); configured != "" {
		candidates = append(candidates, configured)
	}
	if ffmpeg, err := ffmpegPath(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(ffmpeg), executableName("ffprobe")))
	}
	candidates = append(candidates, localFFprobePath())

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		clean := filepath.Clean(candidate)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		if !fileExists(clean) {
			continue
		}
		if err := ffprobeExecutableValidator(clean); err == nil {
			return clean
		}
	}
	return ""
}

// EnsureFFprobe returns a validated ffprobe path. Missing and stale candidates
// are repaired from the existing FFmpeg bundle instead of becoming an
// immediate "ffprobe not found" error.
func EnsureFFprobe() (string, error) {
	if path := usableFFprobePath(); path != "" {
		return path, nil
	}

	ffmpegBundleMu.Lock()
	defer ffmpegBundleMu.Unlock()

	// Another request may have completed installation while this one waited.
	if path := usableFFprobePath(); path != "" {
		return path, nil
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return "", fmt.Errorf("ffprobe not found; %s; you can also set IMAGEPAD_FFPROBE", toolInstallHint("ffmpeg"))
	}
	if _, err := ffprobeBundleInstaller(); err != nil {
		return "", fmt.Errorf("failed to acquire ffprobe: %w", err)
	}
	path := localFFprobePath()
	if !fileExists(path) {
		return "", fmt.Errorf("failed to acquire ffprobe: installer did not create %s", path)
	}
	if err := ffprobeExecutableValidator(path); err != nil {
		return "", fmt.Errorf("failed to validate installed ffprobe: %w", err)
	}
	return path, nil
}

// verifyVisualizerFFmpeg runs ffmpeg -hide_banner -filters and checks that the
// required filters for the audio visualizer pipeline are present: subtitles,
// drawtext, showwaves, gblur, and ebur128.  Returns a descriptive error
// naming any missing filter.
func verifyVisualizerFFmpeg(ffmpeg string) error {
	cmd := exec.Command(ffmpeg, "-hide_banner", "-filters")
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("failed to list FFmpeg filters: %w", err)
	}
	required := []string{"subtitles", "drawtext", "showwaves", "gblur", "ebur128"}
	var missing []string
	for _, name := range required {
		if !strings.Contains(string(output), name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("visualizer FFmpeg is missing required filters: %s", strings.Join(missing, ", "))
	}
	return nil
}

// hasFFmpegAssFilter checks whether the given ffmpeg binary supports the
// "ass" filter (provided by libass).  Returns false when the filter is not
// found or when the filter listing command itself fails.
func hasFFmpegAssFilter(ffmpeg string) bool {
	cmd := exec.Command(ffmpeg, "-hide_banner", "-filters")
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return false
	}
	return strings.Contains(string(output), " ass ")
}

// ---------------------------------------------------------------------------
// ffmpeg / yt-dlp path resolution
// ---------------------------------------------------------------------------

func ffmpegPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		return "", fmt.Errorf("IMAGEPAD_FFMPEG does not exist: %s", configured)
	}
	if local := localFFmpegPath(); fileExists(local) {
		return local, nil
	}
	// A newer app version's bundle may already be installed (e.g. after a
	// downgrade). Run that one in place — never copy a higher version down.
	if higher := higherVersionFFmpegPath(); higher != "" {
		return higher, nil
	}
	return "", fmt.Errorf("ffmpeg not found in bundle; %s; you can also set IMAGEPAD_FFMPEG", toolInstallHint("ffmpeg"))
}

func ytdlpPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		return "", fmt.Errorf("IMAGEPAD_YTDLP does not exist: %s", configured)
	}
	if local := localYTDLPPath(); fileExists(local) {
		return local, nil
	}
	// Run a newer app version's yt-dlp in place rather than copying it down.
	if higher := higherVersionToolPath("yt-dlp"); higher != "" {
		return higher, nil
	}
	return "", fmt.Errorf("yt-dlp not found in bundle; %s; you can also set IMAGEPAD_YTDLP", toolInstallHint("yt-dlp"))
}

// binDir is the root tools directory (settings.Dir()/bin).
func binDir() string {
	return filepath.Join(settings.Dir(), "bin")
}

// toolVersionDir is the per-app-version directory that holds ffmpeg/ffprobe.
// Keying these tools by the ImagePadServer version means a newer build never
// has to overwrite the (possibly running, therefore locked) binaries of the
// version that is currently executing — which previously failed mid-install
// with "Access is denied" on Windows.
func toolVersionDir() string {
	return filepath.Join(binDir(), about.Version)
}

func localFFmpegPath() string {
	return filepath.Join(toolVersionDir(), executableName("ffmpeg"))
}

func localYTDLPPath() string {
	return filepath.Join(toolVersionDir(), executableName("yt-dlp"))
}

func ytdlpAssetName() string {
	if runtime.GOOS == "darwin" {
		return "yt-dlp_macos"
	}
	return "yt-dlp.exe"
}

func toolInstallHint(name string) string {
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("install it with Homebrew (`brew install %s`) or add it to PATH", name)
	case "linux":
		return fmt.Sprintf("install %s with your package manager or add it to PATH", name)
	default:
		return fmt.Sprintf("add %s to PATH", name)
	}
}

// ---------------------------------------------------------------------------
// Ensure functions
// ---------------------------------------------------------------------------

func EnsureFFmpeg() (string, error) {
	if ffmpeg, err := ffmpegPath(); err == nil && validateToolExecutable(ffmpeg, "-version") == nil {
		return ffmpeg, nil
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return "", fmt.Errorf("ffmpeg not found; %s; you can also set IMAGEPAD_FFMPEG", toolInstallHint("ffmpeg"))
	}
	ffmpegBundleMu.Lock()
	defer ffmpegBundleMu.Unlock()
	if ffmpeg, err := ffmpegPath(); err == nil && validateToolExecutable(ffmpeg, "-version") == nil {
		return ffmpeg, nil
	}
	return ffmpegBundleInstaller()
}

// ToolsReady reports whether ffmpeg and ffprobe both resolve to a bundled (or
// IMAGEPAD_*) binary that passes -version, without downloading anything.
func ToolsReady() bool {
	ffmpeg, err := ffmpegPath()
	if err != nil || validateToolExecutable(ffmpeg, "-version") != nil {
		return false
	}
	return usableFFprobePath() != ""
}

// ValidateInstalledTools checks the bundled binaries at startup and re-acquires
// any that are missing or fail validation. It is best-effort: errors are
// surfaced only through the install tracker, never returned, so startup never
// blocks on a tool problem it cannot fix.
func ValidateInstalledTools() {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return
	}
	// Reap tool directories from older app versions (best-effort; skips any that
	// are still locked by another running instance).
	defer CleanupOldToolVersions()
	if ToolsReady() {
		return
	}
	if _, err := EnsureFFmpeg(); err != nil {
		installFail(err.Error())
		return
	}
	if _, err := EnsureFFprobe(); err != nil {
		installFail(err.Error())
	}
}

func EnsureYTDLP() (string, error) {
	if exe, err := ytdlpPath(); err == nil && validateToolExecutable(exe, "--version") == nil {
		return exe, nil
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return "", fmt.Errorf("yt-dlp not found; %s; you can also set IMAGEPAD_YTDLP", toolInstallHint("yt-dlp"))
	}
	// Serialize with EnsureLatestYTDLP: both write the same target path via
	// downloadYTDLPWithChecksum, and the startup update goroutine can race a
	// user-triggered download. Without this lock two concurrent downloads
	// share the same "<target>.tmp" and corrupt each other's bytes, or one
	// execs the binary mid-replaceFile.
	ytdlpBundleMu.Lock()
	defer ytdlpBundleMu.Unlock()
	if exe, err := ytdlpPath(); err == nil && validateToolExecutable(exe, "--version") == nil {
		return exe, nil
	}
	return downloadYTDLP()
}

func EnsureLatestYTDLP() (string, bool, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, false, nil
		}
		return "", false, fmt.Errorf("IMAGEPAD_YTDLP does not exist: %s", configured)
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return "", false, nil
	}

	checksum, err := remoteSHA256For(ytdlpAssetName())
	if err != nil {
		return "", false, err
	}
	// Hold the yt-dlp mutex only across the check + download + replace, not
	// the checksum fetch above, so a slow SHA2-256SUMS request does not block
	// a concurrent EnsureYTDLP.
	ytdlpBundleMu.Lock()
	defer ytdlpBundleMu.Unlock()
	target := localYTDLPPath()
	if fileExists(target) {
		if err := verifySHA256(target, checksum); err == nil {
			return target, false, nil
		}
	}
	path, err := downloadYTDLPWithChecksum(checksum)
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

// ---------------------------------------------------------------------------
// Download functions
// ---------------------------------------------------------------------------

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
func extractFFmpegZip(zipPath, ffmpegTarget string) error {
	if err := extractNamedBinaryFromZip(zipPath, ffmpegTarget, executableName("ffmpeg")); err != nil {
		return err
	}
	ffprobeTarget := filepath.Join(filepath.Dir(ffmpegTarget), executableName("ffprobe"))
	if err := extractNamedBinaryFromZip(zipPath, ffprobeTarget, executableName("ffprobe")); err != nil {
		// Partial installation — clean up ffmpeg so we don't leave a broken state.
		os.Remove(ffmpegTarget)
		return fmt.Errorf("ffprobe not found after FFmpeg installation: %w", err)
	}
	return nil
}

func extractNamedBinaryFromZip(zipPath, target, binaryName string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		if !strings.EqualFold(filepath.Base(name), binaryName) {
			continue
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		defer src.Close()

		tempTarget := target + ".tmp"
		dst, err := os.OpenFile(tempTarget, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		if copyErr != nil {
			_ = os.Remove(tempTarget)
			return copyErr
		}
		if closeErr != nil {
			_ = os.Remove(tempTarget)
			return closeErr
		}
		return replaceFile(target, tempTarget)
	}
	return fmt.Errorf("%s was not found in the downloaded archive", binaryName)
}

// replaceFile moves tempTarget onto target, tolerating a target that is locked
// because it is currently executing or being scanned by antivirus (a common
// Windows failure that surfaced as "rename ... Access is denied"). It first
// tries a direct rename, then moves the existing file aside (Windows allows
// renaming a running executable even when it cannot be overwritten), and
// finally retries a few times for transient locks.
func replaceFile(target, tempTarget string) error {
	if err := os.Rename(tempTarget, target); err == nil {
		return nil
	}
	aside := fmt.Sprintf("%s.old-%d", target, time.Now().UnixNano())
	if err := os.Rename(target, aside); err == nil {
		if err := os.Rename(tempTarget, target); err == nil {
			_ = os.Remove(aside) // best effort; may still be locked
			return nil
		}
		_ = os.Rename(aside, target) // restore on failure
	}
	var lastErr error
	for i := 0; i < 5; i++ {
		time.Sleep(time.Duration(150*(i+1)) * time.Millisecond)
		_ = os.Remove(target)
		if err := os.Rename(tempTarget, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	_ = os.Remove(tempTarget)
	if lastErr == nil {
		lastErr = fmt.Errorf("could not replace %s", target)
	}
	return lastErr
}

// migrateFFmpegToolsInto copies a working ffmpeg + ffprobe pair from an older
// version directory (or the legacy flat bin/ layout) into dstDir, so an app
// update does not have to re-download the archive. Higher versions are never
// migrated down (they are run in place by ffmpegPath). It only migrates when
// the candidate ffmpeg reports the pinned version and ffprobe is present and
// valid; otherwise it returns false and the caller downloads the pinned build.
func migrateFFmpegToolsInto(dstDir string) bool {
	ffName := executableName("ffmpeg")
	fpName := executableName("ffprobe")
	root := binDir()

	// Legacy flat layout first, then strictly older version directories.
	candidates := []string{root}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && looksLikeVersionDir(e.Name()) && compareAppVersions(e.Name(), about.Version) < 0 {
				candidates = append(candidates, filepath.Join(root, e.Name()))
			}
		}
	}

	for _, dir := range candidates {
		if filepath.Clean(dir) == filepath.Clean(dstDir) {
			continue
		}
		ff := filepath.Join(dir, ffName)
		fp := filepath.Join(dir, fpName)
		if !fileExists(ff) || !fileExists(fp) {
			continue
		}
		if !ffmpegReportsVersion(ff, ffmpegPinnedVersion) {
			continue
		}
		if validateToolExecutable(fp, "-version") != nil {
			continue
		}
		// If a copy cannot complete (e.g. a locked file), drop any partial
		// result and move on; exhausting all candidates returns false so the
		// caller falls back to a fresh download.
		ffDst := filepath.Join(dstDir, ffName)
		fpDst := filepath.Join(dstDir, fpName)
		if err := copyFileTo(ffDst, ff); err != nil {
			_ = os.Remove(ffDst)
			continue
		}
		if err := copyFileTo(fpDst, fp); err != nil {
			_ = os.Remove(ffDst)
			_ = os.Remove(fpDst)
			continue
		}
		return true
	}
	return false
}

// migrateYTDLPInto copies an existing yt-dlp from an older version dir (or the
// legacy flat layout) into dstDir to avoid a re-download. When wantSHA is set
// the candidate must match it (so the update-to-latest path is not satisfied by
// a stale build); otherwise any binary that passes --version is accepted.
// Higher versions are never copied down — they are run in place by ytdlpPath.
func migrateYTDLPInto(dstDir, wantSHA string) bool {
	exe := executableName("yt-dlp")
	root := binDir()

	candidates := []string{root}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && looksLikeVersionDir(e.Name()) && compareAppVersions(e.Name(), about.Version) < 0 {
				candidates = append(candidates, filepath.Join(root, e.Name()))
			}
		}
	}

	for _, dir := range candidates {
		if filepath.Clean(dir) == filepath.Clean(dstDir) {
			continue
		}
		src := filepath.Join(dir, exe)
		if !fileExists(src) {
			continue
		}
		if wantSHA != "" {
			if verifySHA256(src, wantSHA) != nil {
				continue
			}
		} else if validateToolExecutable(src, "--version") != nil {
			continue
		}
		dst := filepath.Join(dstDir, exe)
		if err := copyFileTo(dst, src); err != nil {
			_ = os.Remove(dst)
			continue
		}
		return true
	}
	return false
}

// ffmpegReportsVersion runs "<path> -version" and reports whether the output
// identifies the wanted FFmpeg version. It is a var so tests can stub it.
var ffmpegReportsVersion = func(path, want string) bool {
	cmd := exec.Command(path, "-version")
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "ffmpeg version "+want)
}

// copyFileTo copies src to dst via a temp file and atomic rename, preserving an
// executable mode. It is a var so tests can stub it (e.g. to simulate a locked
// destination). When it fails during migration the caller falls back to a fresh
// download.
var copyFileTo = func(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".copy.tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return replaceFile(dst, tmp)
}

// CleanupOldToolVersions removes per-version tool directories that do not match
// the running app version, plus any leftover legacy flat ffmpeg/ffprobe once a
// versioned copy exists. It is best-effort: a directory still locked by another
// running instance is left for a later run.
func CleanupOldToolVersions() {
	root := binDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	current := about.Version
	for _, e := range entries {
		// Only reap strictly older versions; a higher version's bundle is kept
		// (ffmpegPath runs it in place after a downgrade).
		if e.IsDir() && looksLikeVersionDir(e.Name()) && compareAppVersions(e.Name(), current) < 0 {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
	for _, base := range []string{"ffmpeg", "ffprobe", "yt-dlp"} {
		flat := filepath.Join(root, executableName(base))
		versioned := filepath.Join(root, current, executableName(base))
		if fileExists(flat) && fileExists(versioned) {
			_ = os.Remove(flat)
		}
	}
}

// looksLikeVersionDir reports whether name is an app-version directory such as
// "v1.4.2" (so cleanup never touches unrelated entries).
func looksLikeVersionDir(name string) bool {
	return len(name) >= 2 && name[0] == 'v' && name[1] >= '0' && name[1] <= '9'
}

// higherVersionFFmpegPath returns the ffmpeg path inside the highest app-version
// directory that is newer than the running version and actually contains an
// ffmpeg binary, or "" if none. Such a bundle is run in place rather than
// copied down.
func higherVersionFFmpegPath() string {
	return higherVersionToolPath("ffmpeg")
}

// higherVersionToolPath returns the path to the named tool inside the highest
// app-version directory newer than the running version that contains it, or ""
// if none. Such a binary is run in place rather than copied down.
func higherVersionToolPath(base string) string {
	root := binDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	exe := executableName(base)
	bestName := ""
	for _, e := range entries {
		if !e.IsDir() || !looksLikeVersionDir(e.Name()) {
			continue
		}
		if compareAppVersions(e.Name(), about.Version) <= 0 {
			continue
		}
		if !fileExists(filepath.Join(root, e.Name(), exe)) {
			continue
		}
		if bestName == "" || compareAppVersions(e.Name(), bestName) > 0 {
			bestName = e.Name()
		}
	}
	if bestName == "" {
		return ""
	}
	return filepath.Join(root, bestName, exe)
}

// compareAppVersions compares two app version strings like "v1.4.2" or
// "v1.4.2-dev3". Returns -1 if a<b, 0 if equal, 1 if a>b. A plain release ranks
// above a same-numbered pre-release (…-dev) build.
func compareAppVersions(a, b string) int {
	abase, apre := splitAppVersion(a)
	bbase, bpre := splitAppVersion(b)
	for i := 0; i < 3; i++ {
		if abase[i] != bbase[i] {
			if abase[i] < bbase[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "": // release > pre-release
		return 1
	case bpre == "":
		return -1
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

// splitAppVersion parses "v1.4.2-dev3" into ([1,4,2], "dev3"). Missing numeric
// components default to 0; unparseable components are treated as 0.
func splitAppVersion(v string) ([3]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	base := v
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		base = v[:i]
		pre = v[i+1:]
	}
	var nums [3]int
	for i, part := range strings.SplitN(base, ".", 3) {
		if i > 2 {
			break
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				n = 0
				break
			}
			n = n*10 + int(r-'0')
		}
		nums[i] = n
	}
	return nums, pre
}

// ---------------------------------------------------------------------------
// Execution helpers
// ---------------------------------------------------------------------------

func run(ffmpeg string, args ...string) error {
	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

func runInDir(dir, ffmpeg string, args ...string) error {
	cmd := exec.Command(ffmpeg, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

func runInDirContext(ctx context.Context, dir, ffmpeg string, args ...string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func trimOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 700 {
		return text[len(text)-700:]
	}
	if text == "" {
		return "no output"
	}
	return text
}

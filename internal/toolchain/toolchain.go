package toolchain

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

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

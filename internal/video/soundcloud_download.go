package video

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

// runDownloadCmd is the function used to execute yt-dlp.
// It can be overridden by tests to avoid calling the real tool.
var runDownloadCmd = run

// ffmpegLocationArgs returns yt-dlp's --ffmpeg-location flag pointing at the
// bundled ffmpeg/ffprobe directory, so yt-dlp postprocessing (audio extraction
// with -x and DASH/HLS muxing) works even when ffmpeg is not on PATH — which is
// the normal case here because the tools are bundled, not installed system-wide.
// Returns nil when no bundled ffmpeg resolves, letting yt-dlp fall back to PATH.
func ffmpegLocationArgs() []string {
	p, err := ffmpegPath()
	if err != nil {
		return nil
	}
	return []string{"--ffmpeg-location", filepath.Dir(p)}
}

// youtubeImpersonateTargets is the ordered list of curl_cffi browser targets to
// try for YouTube. Generic aliases (no version) auto-resolve to the latest
// supported fingerprint, so they survive yt-dlp/curl_cffi upgrades. Safari is
// first because it is the combination VRChat uses successfully; chrome and
// firefox are robust fallbacks. (edge is intentionally omitted — curl_cffi only
// ships stale edge99/edge101 fingerprints.)
var youtubeImpersonateTargets = []string{"safari", "chrome", "firefox"}

// isYouTubeURL reports whether rawURL points at YouTube.
func isYouTubeURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range []string{"youtube.com", "youtu.be", "youtube-nocookie.com"} {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// ytdlpDownloadAttempts returns the ordered sets of extra yt-dlp args to try for
// rawURL. For YouTube it forces the web player client and impersonates a browser
// (the default android_vr client returns formats that 403 on download); each
// target is a separate attempt, so the caller stops at the first success. Other
// sites get a single attempt with no extra args to avoid impersonation side
// effects on their extractors.
func ytdlpDownloadAttempts(rawURL string) [][]string {
	if !isYouTubeURL(rawURL) {
		return [][]string{nil}
	}
	sets := make([][]string, 0, len(youtubeImpersonateTargets))
	for _, target := range youtubeImpersonateTargets {
		sets = append(sets, []string{
			"--impersonate", target,
			"--extractor-args", "youtube:player_client=web",
		})
	}
	return sets
}

// runYTDLPDownload runs yt-dlp for rawURL, retrying YouTube downloads across the
// browser impersonation targets until one succeeds. baseArgs must not include
// the URL (it is appended last). Non-YouTube URLs run exactly once.
func runYTDLPDownload(exe, rawURL string, baseArgs []string) error {
	var lastErr error
	for _, extra := range ytdlpDownloadAttempts(rawURL) {
		args := make([]string, 0, len(baseArgs)+len(extra)+1)
		args = append(args, baseArgs...)
		args = append(args, extra...)
		args = append(args, rawURL)
		if err := runDownloadCmd(exe, args...); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

// DownloadSoundCloud downloads a SoundCloud track using yt-dlp and returns
// an AcquiredAudio with the manifest-selected audio, thumbnail, info JSON
// sidecar, and parsed SoundCloud metadata.
//
// The function uses unique job prefixes to avoid collisions between
// concurrent downloads and writes a manifest file (via yt-dlp's
// --print-to-file after_move:filepath) so that file selection is driven
// by the tool output rather than glob heuristics.
func DownloadSoundCloud(ctx context.Context, ytdlp, rawURL, outDir string) (AcquiredAudio, error) {
	if !isSoundCloudURL(rawURL) {
		return AcquiredAudio{}, fmt.Errorf("not a SoundCloud URL: %s", rawURL)
	}

	if err := os.MkdirAll(outDir, 0700); err != nil {
		return AcquiredAudio{}, fmt.Errorf("create output directory: %w", err)
	}

	// Unique job prefix — spec section 4.7 sidecar isolation.
	seq := atomic.AddUint64(&soundCloudDownloadSequence, 1)
	prefix := "yt-dlp-sc-" + queueID() + "-" + strconv.FormatUint(seq, 36)

	// Manifest path — yt-dlp writes the final audio path here after move.
	manifestPath := filepath.Join(outDir, prefix+".manifest")

	outputTemplate := filepath.Join(outDir, prefix+".%(ext)s")

	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--max-filesize", strconv.FormatInt(int64(MaxMediaSourceBytes), 10),
		"--write-thumbnail",
		"--write-info-json",
		"--print-to-file", "after_move:filepath", manifestPath,
		"-o", outputTemplate,
	}
	args = append(args, ffmpegLocationArgs()...)
	args = append(args, rawURL)

	if err := runDownloadCmd(ytdlp, args...); err != nil {
		return AcquiredAudio{}, fmt.Errorf("yt-dlp download failed: %w", err)
	}

	// Read manifest to get the audio source path.
	sourcePath, err := ReadSinglePathManifest(manifestPath, outDir)
	if err != nil {
		return AcquiredAudio{}, fmt.Errorf("read download manifest: %w", err)
	}

	// Locate thumbnail and .info.json by globbing the output prefix.
	base := strings.TrimSuffix(outputTemplate, ".%(ext)s")

	// Find thumbnail — first image file matching the prefix wins.
	var artworkPath string
	imgMatches, _ := filepath.Glob(base + ".jpg")
	if len(imgMatches) == 0 {
		imgMatches, _ = filepath.Glob(base + ".jpeg")
	}
	if len(imgMatches) == 0 {
		imgMatches, _ = filepath.Glob(base + ".png")
	}
	if len(imgMatches) == 0 {
		imgMatches, _ = filepath.Glob(base + ".webp")
	}
	if len(imgMatches) > 0 {
		artworkPath = imgMatches[0]
	}

	// Find .info.json sidecar.
	var infoPath string
	infoMatches, _ := filepath.Glob(base + ".info.json")
	if len(infoMatches) > 0 {
		infoPath = infoMatches[0]
	}

	// Parse SoundCloud metadata from .info.json if available.
	var scMeta AudioMetadata
	if infoPath != "" {
		data, readErr := os.ReadFile(infoPath)
		if readErr == nil {
			if parsed, parseErr := ParseSoundCloudInfoJSON(data); parseErr == nil {
				scMeta = parsed
			}
		}
	}

	return AcquiredAudio{
		SourcePath:                sourcePath,
		SourceName:                filepath.Base(sourcePath),
		Kind:                      SourceSoundCloud,
		SoundCloudMetadata:        scMeta,
		SoundCloudArtworkPath:     artworkPath,
		SoundCloudInformationPath: infoPath,
	}, nil
}

// ReadSinglePathManifest reads a manifest file produced by yt-dlp's
// --print-to-file after_move:filepath. The manifest contains exactly one
// non-empty line (the path to the downloaded audio file).  It validates
// that the path exists and is within the given root directory.
func ReadSinglePathManifest(manifest, root string) (string, error) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return "", fmt.Errorf("read manifest %s: %w", manifest, err)
	}

	// Normalise line endings and split into non-empty lines.
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", fmt.Errorf("manifest %s is empty", manifest)
	}

	lines := strings.Split(text, "\n")
	// Filter empty lines.
	var nonEmpty []string
	for _, line := range lines {
		// Also strip \r for CRLF handling.
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}

	if len(nonEmpty) == 0 {
		return "", fmt.Errorf("manifest %s contains no paths", manifest)
	}
	if len(nonEmpty) > 1 {
		return "", fmt.Errorf("manifest %s contains %d paths, expected 1", manifest, len(nonEmpty))
	}

	path := nonEmpty[0]

	// Clean the path for validation.
	cleanedPath := filepath.Clean(path)

	// Verify path exists.
	if _, err := os.Stat(cleanedPath); err != nil {
		return "", fmt.Errorf("manifest path %q does not exist: %w", cleanedPath, err)
	}

	// Verify path is within root.
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root %s: %w", root, err)
	}
	pathAbs, err := filepath.Abs(cleanedPath)
	if err != nil {
		return "", fmt.Errorf("resolve path %s: %w", cleanedPath, err)
	}
	if pathAbs != rootAbs && !strings.HasPrefix(pathAbs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("manifest path %q is outside root %q", pathAbs, rootAbs)
	}

	return cleanedPath, nil
}

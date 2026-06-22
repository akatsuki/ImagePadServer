package video

import (
	"fmt"
	"runtime"
	"time"
)

// toolSource is one place to fetch a tool. The checksum is resolved in this
// priority order: an inline checksum, then a checksumURL fetched at download
// time, then (if both are empty) trust only after a successful -version
// validation. Use checksum for sources that have no sidecar (e.g. GitHub
// release assets); use checksumURL for sources that publish a .sha256 sidecar.
type toolSource struct {
	url         string
	checksum    string
	checksumURL string
}

// ffmpegWindowsSources lists the Windows FFmpeg archive download locations in
// priority order. Both entries are the same gyan "essentials" build; the
// primary is served from GitHub's CDN (fast) and the fallback from gyan.dev's
// origin. The zips store binaries under bin/, but extractNamedBinaryFromZip
// matches by basename so the layout does not matter.
func ffmpegWindowsSources() []toolSource {
	return []toolSource{
		// Primary: gyan essentials mirrored on GitHub (fast CDN, pinned hash).
		{url: ffmpegGitHubURL, checksum: ffmpegGitHubSHA256},
		// Fallback: gyan.dev origin with its .sha256 sidecar.
		{url: ffmpegDownloadURL, checksumURL: ffmpegSHA256URL},
	}
}

// ytdlpSources lists yt-dlp executable download locations in priority order.
func ytdlpSources() []toolSource {
	if runtime.GOOS == "darwin" {
		return []toolSource{
			{url: ytdlpMacOSURL},
			{url: "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/yt-dlp_macos"},
		}
	}
	return []toolSource{
		{url: ytdlpDownloadURL},
		{url: "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/yt-dlp.exe"},
	}
}

// acquireFromSources tries each source in order; each source is retried up to
// retries times with exponential backoff before advancing to the next. The
// current attempt number is reported to the install tracker.
func acquireFromSources(tool string, sources []toolSource, retries int, attempt func(toolSource) error) error {
	if retries < 1 {
		retries = 1
	}
	var lastErr error
	n := 0
	for _, src := range sources {
		for try := 0; try < retries; try++ {
			n++
			installAttempt(n)
			if err := attempt(src); err != nil {
				lastErr = err
				time.Sleep(backoffDelay(try))
				continue
			}
			return nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no download sources configured for %s", tool)
	}
	return fmt.Errorf("failed to acquire %s after exhausting %d source(s): %w", tool, len(sources), lastErr)
}

func backoffDelay(try int) time.Duration {
	d := time.Second << uint(try) // 1s, 2s, 4s, ...
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

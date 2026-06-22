package video

import (
	"fmt"
	"runtime"
	"time"
)

// toolSource is one place to fetch a tool. checksumURL may be empty, in which
// case the binary is trusted only after a successful -version validation.
type toolSource struct {
	url         string
	checksumURL string
}

// ffmpegWindowsSources lists the Windows FFmpeg archive download locations in
// priority order.
func ffmpegWindowsSources() []toolSource {
	return []toolSource{
		{url: ffmpegDownloadURL, checksumURL: ffmpegSHA256URL},
		// Mirror: BtbN nightly win64 build. No sidecar checksum; the archive
		// is validated by running ffmpeg -version after extraction. The zip
		// stores binaries under bin/, but extractNamedBinaryFromZip matches by
		// basename so the layout does not matter.
		{url: "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip"},
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

package server

import "fmt"

// combineURLErrors merges the yt-dlp and direct-download failures so the user
// sees why both routes failed for a URL that is neither a supported video page
// nor a direct media file.
func combineURLErrors(ytdlpErr, directErr error) error {
	if ytdlpErr == nil {
		return directErr
	}
	if directErr == nil {
		return ytdlpErr
	}
	return fmt.Errorf("%v (direct download: %v)", ytdlpErr, directErr)
}

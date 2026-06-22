package video

import (
	"strings"
	"testing"
)

func TestIsYouTubeURL(t *testing.T) {
	cases := map[string]bool{
		"https://www.youtube.com/watch?v=x":        true,
		"https://youtu.be/x":                       true,
		"https://music.youtube.com/watch?v=x":      true,
		"https://www.youtube-nocookie.com/embed/x": true,
		"https://x.com/u/status/1/video/1":         false,
		"https://soundcloud.com/a/b":               false,
		"https://example.com/clip.mp4":             false,
	}
	for url, want := range cases {
		if got := isYouTubeURL(url); got != want {
			t.Errorf("isYouTubeURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestRunYTDLPDownloadNonYouTubeRunsOnce(t *testing.T) {
	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	var calls [][]string
	runDownloadCmd = func(_ string, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	if err := runYTDLPDownload("yt-dlp", "https://x.com/u/status/1/video/1", []string{"-o", "out"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("non-YouTube made %d calls, want 1", len(calls))
	}
	joined := strings.Join(calls[0], " ")
	if strings.Contains(joined, "--impersonate") {
		t.Errorf("non-YouTube call must not impersonate: %q", joined)
	}
	if last := calls[0][len(calls[0])-1]; last != "https://x.com/u/status/1/video/1" {
		t.Errorf("URL not appended last: %q", last)
	}
}

func TestRunYTDLPDownloadYouTubeFallsBackThroughTargets(t *testing.T) {
	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	var targets []string
	runDownloadCmd = func(_ string, args ...string) error {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--impersonate" {
				targets = append(targets, args[i+1])
			}
		}
		// Fail safari, succeed on chrome (the second target).
		if targets[len(targets)-1] == "safari" {
			return errStub
		}
		return nil
	}
	if err := runYTDLPDownload("yt-dlp", "https://www.youtube.com/watch?v=x", []string{"-o", "out"}); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0] != "safari" || targets[1] != "chrome" {
		t.Fatalf("impersonation order = %v, want [safari chrome] (stop at first success)", targets)
	}
}

func TestYouTubeAttemptsForceMultiClient(t *testing.T) {
	for _, set := range ytdlpDownloadAttempts("https://youtu.be/x") {
		joined := strings.Join(set, " ")
		if !strings.Contains(joined, "youtube:player_client=") {
			t.Errorf("attempt %q does not pin player clients", joined)
		}
		// web alone is insufficient (some videos return an empty format list);
		// android_vr alone 403s for some users. All three must be present so
		// yt-dlp can fall back through them within a single attempt.
		for _, client := range []string{"web", "web_safari", "android_vr"} {
			if !strings.Contains(joined, client) {
				t.Errorf("attempt %q missing player client %q", joined, client)
			}
		}
	}
}

func TestIsPageMediaURL(t *testing.T) {
	cases := map[string]bool{
		"https://www.youtube.com/watch?v=x":        true,
		"https://youtu.be/x":                       true,
		"https://music.youtube.com/watch?v=x":      true,
		"https://soundcloud.com/a/b":               true,
		"https://on.soundcloud.com/abc":            true,
		"https://x.com/u/status/1/video/1":         true,
		"https://twitter.com/u/status/1":           true,
		"https://example.com/clip.mp4":             false,
		"https://cdn.example.com/video.mp4":        false,
		"https://example.com/some-page":            false,
	}
	for url, want := range cases {
		if got := IsPageMediaURL(url); got != want {
			t.Errorf("IsPageMediaURL(%q) = %v, want %v", url, got, want)
		}
	}
}

var errStub = stubError("stub failure")

type stubError string

func (e stubError) Error() string { return string(e) }

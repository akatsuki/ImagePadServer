package video

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	var attempts int
	runDownloadCmd = func(_ string, args ...string) error {
		attempts++
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--impersonate" {
				targets = append(targets, args[i+1])
			}
		}
		// Fail the default attempt and safari, succeed on chrome (the third
		// attempt: default -> safari -> chrome).
		if attempts < 3 {
			return errStub
		}
		return nil
	}
	if err := runYTDLPDownload("yt-dlp", "https://www.youtube.com/watch?v=x", []string{"-o", "out"}); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (default, safari, chrome)", attempts)
	}
	if len(targets) != 2 || targets[0] != "safari" || targets[1] != "chrome" {
		t.Fatalf("impersonation order = %v, want [safari chrome] (default attempt has no impersonation)", targets)
	}
}

func TestRunYTDLPDownloadAddsSavedCookies(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	cookiePath := filepath.Join(os.Getenv("IMAGEPAD_DATA_DIR"), "cookies", "yt-dlp-cookies.txt")
	if err := os.MkdirAll(filepath.Dir(cookiePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookiePath, []byte("# Netscape HTTP Cookie File\n"), 0600); err != nil {
		t.Fatal(err)
	}

	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	var calls [][]string
	runDownloadCmd = func(_ string, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	if err := runYTDLPDownload("yt-dlp", "https://www.youtube.com/watch?v=x", []string{"-o", "out"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("expected yt-dlp call")
	}
	joined := strings.Join(calls[0], " ")
	if !strings.Contains(joined, "--cookies "+cookiePath) {
		t.Fatalf("yt-dlp args %q do not include saved cookies %q", joined, cookiePath)
	}
}

func TestRunYTDLPDownloadStopsAfterYouTubeBotCheck(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dataDir)

	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	oldResolver := nightlyYTDLPResolver
	defer func() { nightlyYTDLPResolver = oldResolver }()
	nightlyYTDLPResolver = func() (string, error) { return "", errors.New("nightly unavailable") }
	var calls int
	runDownloadCmd = func(_ string, args ...string) error {
		calls++
		return errors.New("ERROR: [youtube] id: Sign in to confirm you're not a bot")
	}

	err := runYTDLPDownload("yt-dlp", "https://www.youtube.com/watch?v=x", []string{"-o", "out"})
	if err == nil {
		t.Fatal("expected yt-dlp failure")
	}
	if calls != 1 {
		t.Fatalf("yt-dlp calls = %d, want 1 after bot check", calls)
	}
	matches, globErr := filepath.Glob(filepath.Join(dataDir, "diagnostics", "ytdlp", "*.json"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("diagnostic files = %v, want exactly one", matches)
	}
	var report ytdlpFailureDiagnostic
	data, readErr := os.ReadFile(matches[0])
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.URL != "https://www.youtube.com/watch?v=x" {
		t.Fatalf("URL = %q", report.URL)
	}
	if report.Executable != "yt-dlp" {
		t.Fatalf("Executable = %q", report.Executable)
	}
	if report.Class != "youtube_bot_check" {
		t.Fatalf("Class = %q", report.Class)
	}
	if len(report.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 after bot check", len(report.Attempts))
	}
	if report.Attempts[0].ImpersonateTarget != "" {
		t.Fatalf("first attempt should be the default client selection (no impersonation), got %+v", report.Attempts[0])
	}
	if strings.Contains(strings.Join(report.Attempts[0].Args, " "), "youtube:player_client=") {
		t.Fatalf("first attempt should not pin player clients, got %+v", report.Attempts[0])
	}
	if report.Attempts[0].Class != "youtube_bot_check" {
		t.Fatalf("attempt class = %q", report.Attempts[0].Class)
	}
}

func TestRunYTDLPDownloadDoesNotWriteDiagnosticForNonYouTubeFailure(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dataDir)

	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	runDownloadCmd = func(_ string, args ...string) error {
		return errors.New("network failed")
	}

	if err := runYTDLPDownload("yt-dlp", "https://x.com/u/status/1/video/1", []string{"-o", "out"}); err == nil {
		t.Fatal("expected yt-dlp failure")
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, "diagnostics", "ytdlp", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("diagnostic files = %v, want none", matches)
	}
}

func TestYouTubeAttemptsForceMultiClient(t *testing.T) {
	sets := ytdlpDownloadAttempts("https://youtu.be/x")
	if len(sets) != len(youtubeImpersonateTargets)+1 {
		t.Fatalf("attempts = %d, want %d (default + one per impersonation target)", len(sets), len(youtubeImpersonateTargets)+1)
	}
	if joined := strings.Join(sets[0], " "); joined != "" {
		t.Errorf("first attempt should be the default client selection (no extra args), got %q", joined)
	}
	for _, set := range sets[1:] {
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
		"https://www.youtube.com/watch?v=x":   true,
		"https://youtu.be/x":                  true,
		"https://music.youtube.com/watch?v=x": true,
		"https://soundcloud.com/a/b":          true,
		"https://on.soundcloud.com/abc":       true,
		"https://x.com/u/status/1/video/1":    true,
		"https://twitter.com/u/status/1":      true,
		"https://example.com/clip.mp4":        false,
		"https://cdn.example.com/video.mp4":   false,
		"https://example.com/some-page":       false,
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

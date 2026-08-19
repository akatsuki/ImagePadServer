package video

import (
	"errors"
	"testing"

	"imagepadserver/internal/settings"
)

func TestRunYTDLPDownloadNightlyFallbackOnBotCheck(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	ResetYTDLPAutoNightlyForTest()

	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	oldResolver := nightlyYTDLPResolver
	defer func() { nightlyYTDLPResolver = oldResolver }()

	nightlyYTDLPResolver = func() (string, error) { return "yt-dlp-nightly", nil }

	var exes []string
	runDownloadCmd = func(exe string, args ...string) error {
		exes = append(exes, exe)
		if exe == "yt-dlp-nightly" {
			return nil
		}
		return errors.New("ERROR: [youtube] id: Sign in to confirm you're not a bot")
	}

	if err := runYTDLPDownload("yt-dlp", "https://www.youtube.com/watch?v=x", []string{"-o", "out"}); err != nil {
		t.Fatalf("expected nightly fallback success, got %v", err)
	}
	if len(exes) != 2 || exes[0] != "yt-dlp" || exes[1] != "yt-dlp-nightly" {
		t.Fatalf("exes = %v, want [yt-dlp yt-dlp-nightly]", exes)
	}
	if !ytdlpAutoNightly.Load() {
		t.Fatal("expected auto-nightly flag set after successful fallback")
	}
}

func TestRunYTDLPDownloadNightlyFallbackOnHTTP403(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	ResetYTDLPAutoNightlyForTest()

	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	oldResolver := nightlyYTDLPResolver
	defer func() { nightlyYTDLPResolver = oldResolver }()

	var resolverCalls int
	nightlyYTDLPResolver = func() (string, error) {
		resolverCalls++
		return "yt-dlp-nightly", nil
	}

	var lastExe string
	runDownloadCmd = func(exe string, args ...string) error {
		lastExe = exe
		if exe == "yt-dlp-nightly" {
			return nil
		}
		return errors.New("ERROR: [youtube] HTTP Error 403: Forbidden")
	}

	if err := runYTDLPDownload("yt-dlp", "https://www.youtube.com/watch?v=x", []string{"-o", "out"}); err != nil {
		t.Fatalf("expected nightly fallback success, got %v", err)
	}
	if resolverCalls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolverCalls)
	}
	if lastExe != "yt-dlp-nightly" {
		t.Fatalf("last exe = %q, want yt-dlp-nightly", lastExe)
	}
	if !ytdlpAutoNightly.Load() {
		t.Fatal("expected auto-nightly flag set")
	}
}

func TestRunYTDLPDownloadNoNightlyFallbackForNonYouTube(t *testing.T) {
	ResetYTDLPAutoNightlyForTest()

	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	oldResolver := nightlyYTDLPResolver
	defer func() { nightlyYTDLPResolver = oldResolver }()

	var resolverCalls int
	nightlyYTDLPResolver = func() (string, error) {
		resolverCalls++
		return "yt-dlp-nightly", nil
	}
	runDownloadCmd = func(_ string, args ...string) error {
		return errors.New("Unable to download webpage")
	}

	if err := runYTDLPDownload("yt-dlp", "https://x.com/u/status/1/video/1", []string{"-o", "out"}); err == nil {
		t.Fatal("expected failure")
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver called %d times for non-YouTube, want 0", resolverCalls)
	}
}

func TestRunYTDLPDownloadNoNightlyFallbackForNonRetryableClass(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	ResetYTDLPAutoNightlyForTest()

	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	oldResolver := nightlyYTDLPResolver
	defer func() { nightlyYTDLPResolver = oldResolver }()

	var resolverCalls int
	nightlyYTDLPResolver = func() (string, error) {
		resolverCalls++
		return "yt-dlp-nightly", nil
	}
	runDownloadCmd = func(_ string, args ...string) error {
		return errors.New("ERROR: [youtube] HTTP Error 429: Too Many Requests")
	}

	if err := runYTDLPDownload("yt-dlp", "https://www.youtube.com/watch?v=x", []string{"-o", "out"}); err == nil {
		t.Fatal("expected failure")
	}
	if resolverCalls != 0 {
		t.Fatalf("resolver called %d times for rate_limited, want 0", resolverCalls)
	}
	if ytdlpAutoNightly.Load() {
		t.Fatal("auto-nightly flag must not be set")
	}
}

func TestEffectiveYTDLPChannelPrecedence(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_YTDLP_CHANNEL", "")
	ResetYTDLPAutoNightlyForTest()

	if got := EffectiveYTDLPChannel(); got != "stable" {
		t.Fatalf("default channel = %q, want stable", got)
	}

	if err := settings.Update(func(s *settings.Settings) error {
		s.YTDLPChannel = "nightly"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := EffectiveYTDLPChannel(); got != "nightly" {
		t.Fatalf("settings nightly => %q, want nightly", got)
	}

	t.Setenv("IMAGEPAD_YTDLP_CHANNEL", "stable")
	if got := EffectiveYTDLPChannel(); got != "stable" {
		t.Fatalf("env stable over settings => %q, want stable", got)
	}
	t.Setenv("IMAGEPAD_YTDLP_CHANNEL", "")

	if err := settings.Update(func(s *settings.Settings) error {
		s.YTDLPChannel = "auto"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	SetYTDLPAutoNightly()
	if got := EffectiveYTDLPChannel(); got != "nightly" {
		t.Fatalf("auto flag => %q, want nightly", got)
	}

	if err := settings.Update(func(s *settings.Settings) error {
		s.YTDLPChannel = "stable"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := EffectiveYTDLPChannel(); got != "stable" {
		t.Fatalf("settings stable over flag => %q, want stable", got)
	}
	ResetYTDLPAutoNightlyForTest()
}

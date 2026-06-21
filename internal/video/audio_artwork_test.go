package video

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ---------------------------------------------------------------------------
// SelectArtwork tests (pure function, no external tools)
// ---------------------------------------------------------------------------

func TestSelectArtworkFrontCoverWins(t *testing.T) {
	candidates := []ArtworkCandidate{
		{Path: "noncover.png", FrontCover: false, Width: 200, Height: 200, Bytes: 5000},
		{Path: "cover.png", FrontCover: true, Width: 100, Height: 100, Bytes: 1000},
	}
	selected, err := SelectArtwork(candidates, "", SourceLocalAudio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "cover.png" {
		t.Fatalf("expected cover.png, got %q", selected)
	}
}

func TestSelectArtworkLargestArea(t *testing.T) {
	candidates := []ArtworkCandidate{
		{Path: "small.png", FrontCover: true, Width: 50, Height: 50, Bytes: 1000},
		{Path: "medium.png", FrontCover: true, Width: 100, Height: 100, Bytes: 2000},
		{Path: "large.png", FrontCover: true, Width: 200, Height: 200, Bytes: 5000},
	}
	selected, err := SelectArtwork(candidates, "", SourceLocalAudio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "large.png" {
		t.Fatalf("expected large.png, got %q", selected)
	}
}

func TestSelectArtworkNonCoverLargestArea(t *testing.T) {
	candidates := []ArtworkCandidate{
		{Path: "small.png", FrontCover: false, Width: 50, Height: 50, Bytes: 1000},
		{Path: "large.png", FrontCover: false, Width: 200, Height: 200, Bytes: 5000},
	}
	selected, err := SelectArtwork(candidates, "", SourceLocalAudio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "large.png" {
		t.Fatalf("expected large.png, got %q", selected)
	}
}

func TestSelectArtworkLargestBytesTiebreaker(t *testing.T) {
	candidates := []ArtworkCandidate{
		{Path: "small_bytes.png", FrontCover: true, Width: 100, Height: 100, Bytes: 1000},
		{Path: "large_bytes.png", FrontCover: true, Width: 100, Height: 100, Bytes: 2000},
	}
	selected, err := SelectArtwork(candidates, "", SourceLocalAudio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "large_bytes.png" {
		t.Fatalf("expected large_bytes.png, got %q", selected)
	}
}

func TestSelectArtworkNonCoverBytesTiebreaker(t *testing.T) {
	candidates := []ArtworkCandidate{
		{Path: "small_bytes.png", FrontCover: false, Width: 100, Height: 100, Bytes: 1000},
		{Path: "large_bytes.png", FrontCover: false, Width: 100, Height: 100, Bytes: 2000},
	}
	selected, err := SelectArtwork(candidates, "", SourceLocalAudio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "large_bytes.png" {
		t.Fatalf("expected large_bytes.png, got %q", selected)
	}
}

func TestSelectArtworkSoundCloudOnlyForSoundCloud(t *testing.T) {
	// For SourceLocalAudio, SoundCloud path should never be selected.
	selected, err := SelectArtwork(nil, "/path/to/sc_art.jpg", SourceLocalAudio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "" {
		t.Fatalf("expected empty for local audio, got %q", selected)
	}

	// For SourceRemoteAudio, SoundCloud path should never be selected.
	selected, err = SelectArtwork(nil, "/path/to/sc_art.jpg", SourceRemoteAudio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "" {
		t.Fatalf("expected empty for remote audio, got %q", selected)
	}

	// For SourceSoundCloud with no embedded, SoundCloud path should be selected.
	selected, err = SelectArtwork(nil, "/path/to/sc_art.jpg", SourceSoundCloud)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "/path/to/sc_art.jpg" {
		t.Fatalf("expected /path/to/sc_art.jpg, got %q", selected)
	}
}

func TestSelectArtworkEmbeddedPreferredOverSoundCloud(t *testing.T) {
	candidates := []ArtworkCandidate{
		{Path: "embedded.png", FrontCover: true, Width: 100, Height: 100, Bytes: 1000},
	}
	selected, err := SelectArtwork(candidates, "/path/to/sc_art.jpg", SourceSoundCloud)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected != "embedded.png" {
		t.Fatalf("expected embedded.png, got %q", selected)
	}
}

func TestSelectArtworkEmptyEmbedded(t *testing.T) {
	// Empty embedded should return empty (not error) for all kinds.
	for _, kind := range []SourceKind{SourceLocalAudio, SourceRemoteAudio, SourceSoundCloud} {
		selected, err := SelectArtwork(nil, "", kind)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", kind, err)
		}
		if selected != "" {
			t.Fatalf("expected empty for %s, got %q", kind, selected)
		}
	}
}

// ---------------------------------------------------------------------------
// ExtractEmbeddedArtwork tests (use fake ffmpeg)
// ---------------------------------------------------------------------------

func TestExtractEmbeddedArtworkValid(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := mustWriteFakeFFmpegValid(t, dir)

	probe := MediaProbe{
		Streams: []MediaStream{
			{
				Index:       1,
				CodecType:   "video",
				CodecName:   "mjpeg",
				AttachedPic: true,
				Width:       1,
				Height:      1,
				Tags:        map[string]string{"title": "Front Cover"},
			},
		},
	}

	ctx := context.Background()
	candidates, err := ExtractEmbeddedArtwork(ctx, ffmpegPath, filepath.Join(dir, "source.mp3"), dir, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	c := candidates[0]
	if !c.FrontCover {
		t.Fatal("expected FrontCover=true")
	}
	if c.Width != 1 || c.Height != 1 {
		t.Fatalf("expected 1x1, got %dx%d", c.Width, c.Height)
	}
	if c.Bytes <= 0 {
		t.Fatalf("expected positive file size, got %d", c.Bytes)
	}
	if c.Path == "" {
		t.Fatal("expected non-empty Path")
	}
}

func TestExtractEmbeddedArtworkCorruptSkipped(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := mustWriteFakeFFmpegCorrupt(t, dir)

	probe := MediaProbe{
		Streams: []MediaStream{
			{
				Index:       1,
				CodecType:   "video",
				CodecName:   "mjpeg",
				AttachedPic: true,
				Width:       100,
				Height:      100,
				Tags:        map[string]string{"title": "Front Cover"},
			},
		},
	}

	ctx := context.Background()
	candidates, err := ExtractEmbeddedArtwork(ctx, ffmpegPath, filepath.Join(dir, "source.mp3"), dir, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates for corrupt image, got %d", len(candidates))
	}
}

func TestExtractEmbeddedArtworkNonAttachedStreamsIgnored(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := mustWriteFakeFFmpegValid(t, dir)

	probe := MediaProbe{
		Streams: []MediaStream{
			{Index: 0, CodecType: "audio", CodecName: "aac"},
			{
				Index:       1,
				CodecType:   "video",
				CodecName:   "png",
				AttachedPic: true,
				Width:       100,
				Height:      100,
			},
		},
	}

	ctx := context.Background()
	candidates, err := ExtractEmbeddedArtwork(ctx, ffmpegPath, filepath.Join(dir, "source.mp3"), dir, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].FrontCover {
		t.Fatal("expected FrontCover=false when no title tag")
	}
}

// ---------------------------------------------------------------------------
// Test helpers: fake ffmpeg binaries
// ---------------------------------------------------------------------------

// mustWriteFakeFFmpegValid creates a fake ffmpeg that writes a valid 1×1 red
// PNG to the output path (last command argument).  Returns the script path.
func mustWriteFakeFFmpegValid(t *testing.T, dir string) string {
	t.Helper()
	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

	var path string
	var script string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "ffmpeg_test.bat")
		script = "@echo off\r\n" +
			"set outPath=\r\n" +
			":loop\r\n" +
			`if "%~1"=="" goto done` + "\r\n" +
			"set outPath=%~1\r\n" +
			"shift\r\n" +
			"goto loop\r\n" +
			":done\r\n" +
			`if "%outPath%"=="" exit /b 1` + "\r\n" +
			`powershell -NoProfile -Command "[System.IO.File]::WriteAllBytes('%outPath:\=\\%', [System.Convert]::FromBase64String('` + pngBase64 + `'))" >nul 2>&1` + "\r\n" +
			"exit /b 0\r\n"
	} else {
		path = filepath.Join(dir, "ffmpeg_test")
		script = "#!/bin/sh\n" +
			"for outPath in \"$@\"; do :; done\n" +
			"[ -n \"$outPath\" ] || exit 1\n" +
			"echo '" + pngBase64 + "' | base64 -d > \"$outPath\" 2>/dev/null\n"
	}
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// mustWriteFakeFFmpegCorrupt creates a fake ffmpeg that writes invalid image
// data to the output path (last command argument).  Returns the script path.
func mustWriteFakeFFmpegCorrupt(t *testing.T, dir string) string {
	t.Helper()
	var path string
	var script string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "ffmpeg_corrupt.bat")
		script = "@echo off\r\n" +
			"set outPath=\r\n" +
			":loop\r\n" +
			`if "%~1"=="" goto done` + "\r\n" +
			"set outPath=%~1\r\n" +
			"shift\r\n" +
			"goto loop\r\n" +
			":done\r\n" +
			`if "%outPath%"=="" exit /b 1` + "\r\n" +
			`echo notanimage > "%outPath%"` + "\r\n" +
			"exit /b 0\r\n"
	} else {
		path = filepath.Join(dir, "ffmpeg_corrupt")
		script = "#!/bin/sh\n" +
			"for outPath in \"$@\"; do :; done\n" +
			"[ -n \"$outPath\" ] || exit 1\n" +
			"echo \"notanimage\" > \"$outPath\"\n"
	}
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

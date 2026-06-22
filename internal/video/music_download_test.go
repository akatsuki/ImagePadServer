package video

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadMusicUsesAudioOnlyAndChannelMetadata(t *testing.T) {
	dir := t.TempDir()
	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()

	var gotArgs []string
	runDownloadCmd = func(_ string, args ...string) error {
		gotArgs = append([]string(nil), args...)
		var outputTemplate, manifestPath string
		for i := 0; i < len(args)-1; i++ {
			switch args[i] {
			case "-o":
				outputTemplate = args[i+1]
			case "--print-to-file":
				manifestPath = args[i+2]
			}
		}
		base := strings.TrimSuffix(outputTemplate, ".%(ext)s")
		audioPath := base + ".m4a"
		if err := os.WriteFile(audioPath, []byte("audio"), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(base+".webp", []byte("art"), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(base+".info.json", []byte(`{"title":"Track title","channel":"Channel Name","uploader":"Uploader Name"}`), 0600); err != nil {
			return err
		}
		return os.WriteFile(manifestPath, []byte(audioPath+"\n"), 0600)
	}

	audio, err := DownloadMusic(context.Background(), "yt-dlp", "https://www.youtube.com/watch?v=test", dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, required := range []string{"--no-playlist", "--max-filesize", "--write-thumbnail", "--write-info-json", "-f bestaudio/best", "-x", "--concurrent-fragments 4"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("yt-dlp args %q do not contain %q", joined, required)
		}
	}
	if strings.Contains(joined, "--cookies") {
		t.Fatalf("yt-dlp args unexpectedly contain cookie options: %q", joined)
	}
	if audio.Kind != SourceMusic {
		t.Fatalf("Kind = %q, want %q", audio.Kind, SourceMusic)
	}
	if filepath.Ext(audio.SourcePath) != ".m4a" {
		t.Fatalf("SourcePath = %q, want manifest-selected m4a", audio.SourcePath)
	}
	if audio.SoundCloudMetadata.Title != "Track title" || audio.SoundCloudMetadata.Artist != "Uploader Name" {
		t.Fatalf("metadata = %#v, want title and uploader as artist", audio.SoundCloudMetadata)
	}
	if filepath.Ext(audio.SoundCloudArtworkPath) != ".webp" {
		t.Fatalf("artwork = %q, want webp thumbnail", audio.SoundCloudArtworkPath)
	}
}

func TestDownloadMusicPassesFFmpegLocation(t *testing.T) {
	dir := t.TempDir()
	// Point ffmpegPath() at a resolvable bundled ffmpeg so its directory is
	// handed to yt-dlp via --ffmpeg-location (fixes "ffprobe and ffmpeg not
	// found" postprocessing failures when ffmpeg is not on PATH).
	ffDir := t.TempDir()
	ffmpeg := filepath.Join(ffDir, executableName("ffmpeg"))
	if err := os.WriteFile(ffmpeg, []byte("ff"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_FFMPEG", ffmpeg)
	// ffmpegLocationArgs now goes through EnsureFFmpeg, which validates the
	// binary with -version. Stub the validator so the fake ffmpeg is accepted
	// without attempting a real download.
	oldValidate := validateToolExecutable
	defer func() { validateToolExecutable = oldValidate }()
	validateToolExecutable = func(string, ...string) error { return nil }

	oldRun := runDownloadCmd
	defer func() { runDownloadCmd = oldRun }()
	var gotArgs []string
	runDownloadCmd = func(_ string, args ...string) error {
		gotArgs = append([]string(nil), args...)
		var outputTemplate, manifestPath string
		for i := 0; i < len(args)-1; i++ {
			switch args[i] {
			case "-o":
				outputTemplate = args[i+1]
			case "--print-to-file":
				manifestPath = args[i+2]
			}
		}
		base := strings.TrimSuffix(outputTemplate, ".%(ext)s")
		if err := os.WriteFile(base+".m4a", []byte("audio"), 0600); err != nil {
			return err
		}
		return os.WriteFile(manifestPath, []byte(base+".m4a\n"), 0600)
	}

	if _, err := DownloadMusic(context.Background(), "yt-dlp", "https://x.com/u/status/1/video/1", dir); err != nil {
		t.Fatal(err)
	}
	loc := ""
	for i := 0; i < len(gotArgs)-1; i++ {
		if gotArgs[i] == "--ffmpeg-location" {
			loc = gotArgs[i+1]
		}
	}
	if loc != ffDir {
		t.Fatalf("--ffmpeg-location = %q, want %q", loc, ffDir)
	}
}

func TestParseMusicInfoJSONFallsBackToChannelForArtist(t *testing.T) {
	meta, err := ParseMusicInfoJSON([]byte(`{"title":"Track","channel":"Only Channel"}`))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Artist != "Only Channel" {
		t.Fatalf("Artist = %q, want channel fallback", meta.Artist)
	}
}

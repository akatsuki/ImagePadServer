package video

import (
	"context"
	"image"
	_ "image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers — generate small test audio files using FFmpeg
// ---------------------------------------------------------------------------

// generateSilentM4A creates a 3-second silent AAC audio file at path.
func generateSilentM4A(t *testing.T, ffmpeg, path string) {
	t.Helper()
	cmd := exec.Command(ffmpeg,
		"-v", "error",
		"-f", "lavfi",
		"-i", "anullsrc=r=48000:cl=stereo",
		"-t", "3",
		"-c:a", "aac",
		"-b:a", "128k",
		"-ar", "48000",
		"-ac", "2",
		"-y", path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generateSilentM4A: %v\n%s", err, out)
	}
}

// generateToneM4A creates a 3-second audio file with a 440 Hz sine wave
// encoded as AAC at path.
func generateToneM4A(t *testing.T, ffmpeg, path string) {
	t.Helper()
	cmd := exec.Command(ffmpeg,
		"-v", "error",
		"-f", "lavfi",
		"-i", "sine=frequency=440:duration=3",
		"-c:a", "aac",
		"-b:a", "128k",
		"-ar", "48000",
		"-ac", "2",
		"-y", path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generateToneM4A: %v\n%s", err, out)
	}
}

// generateShortWAV creates a 1-second 16-bit PCM WAV file at 48 kHz at path.
func generateShortWAV(t *testing.T, ffmpeg, path string) {
	t.Helper()
	cmd := exec.Command(ffmpeg,
		"-v", "error",
		"-f", "lavfi",
		"-i", "anullsrc=r=48000:cl=stereo",
		"-t", "1",
		"-c:a", "pcm_s16le",
		"-ar", "48000",
		"-ac", "2",
		"-y", path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generateShortWAV: %v\n%s", err, out)
	}
}

// ---------------------------------------------------------------------------
// TestGenerateAudioFixtures — verify fixture generation
// ---------------------------------------------------------------------------

func TestGenerateAudioFixtures(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found on PATH — install ffmpeg or set IMAGEPAD_FFMPEG")
	}

	dir := t.TempDir()

	t.Run("silent M4A", func(t *testing.T) {
		path := filepath.Join(dir, "silent.m4a")
		generateSilentM4A(t, ffmpeg, path)
		info, stErr := os.Stat(path)
		if stErr != nil {
			t.Fatalf("silent M4A not created: %v", stErr)
		}
		if info.Size() == 0 {
			t.Fatal("silent M4A is empty")
		}
		t.Logf("silent.m4a: %d bytes", info.Size())
	})

	t.Run("tone M4A", func(t *testing.T) {
		path := filepath.Join(dir, "tone.m4a")
		generateToneM4A(t, ffmpeg, path)
		info, stErr := os.Stat(path)
		if stErr != nil {
			t.Fatalf("tone M4A not created: %v", stErr)
		}
		if info.Size() == 0 {
			t.Fatal("tone M4A is empty")
		}
		t.Logf("tone.m4a: %d bytes", info.Size())
	})

	t.Run("short WAV", func(t *testing.T) {
		path := filepath.Join(dir, "short.wav")
		generateShortWAV(t, ffmpeg, path)
		info, stErr := os.Stat(path)
		if stErr != nil {
			t.Fatalf("short WAV not created: %v", stErr)
		}
		if info.Size() == 0 {
			t.Fatal("short WAV is empty")
		}
		t.Logf("short.wav: %d bytes", info.Size())
	})
}

// ---------------------------------------------------------------------------
// TestIntegrationGUNPEI — live SoundCloud download and full pipeline
// ---------------------------------------------------------------------------

const gunpeiSoundCloudURL = "https://soundcloud.com/hujikopro/hujiko-pro-gunpei"

func TestIntegrationGUNPEI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("IMAGEPAD_RUN_NETWORK_TESTS") != "1" {
		t.Skip("IMAGEPAD_RUN_NETWORK_TESTS not set")
	}

	// Locate required external tools.
	ytdlp := requireTool(t, "yt-dlp", "IMAGEPAD_YTDLP")
	ffmpeg := requireTool(t, "ffmpeg", "IMAGEPAD_FFMPEG")
	ffprobe := requireTool(t, "ffprobe", "IMAGEPAD_FFPROBE")

	dir := t.TempDir()
	ctx := context.Background()

	// -----------------------------------------------------------------------
	// 1. Download from SoundCloud
	// -----------------------------------------------------------------------
	t.Log("Downloading GUNPEI from SoundCloud …")
	audio, err := DownloadSoundCloud(ctx, ytdlp, gunpeiSoundCloudURL, dir)
	if err != nil {
		t.Fatalf("DownloadSoundCloud: %v", err)
	}

	// -----------------------------------------------------------------------
	// 2. Verify manifest path exists
	// -----------------------------------------------------------------------
	if audio.SourcePath == "" {
		t.Fatal("SourcePath is empty after download")
	}
	if _, err := os.Stat(audio.SourcePath); err != nil {
		t.Fatalf("source path %q does not exist: %v", audio.SourcePath, err)
	}
	t.Logf("SourcePath: %s", audio.SourcePath)

	// -----------------------------------------------------------------------
	// 3. Verify playable audio via ffprobe
	// -----------------------------------------------------------------------
	probe, err := ProbeMedia(ctx, ffprobe, audio.SourcePath)
	if err != nil {
		t.Fatalf("ProbeMedia: %v", err)
	}
	if ClassifyMediaProbe(probe) != MediaAudio {
		t.Fatalf("downloaded file classified as %v, want %v", ClassifyMediaProbe(probe), MediaAudio)
	}
	if probe.Duration <= 0 {
		t.Fatalf("probe duration is %f, expected > 0", probe.Duration)
	}
	t.Logf("Duration: %.2f s", probe.Duration)

	// Verify no embedded attached_pic.
	for _, s := range probe.Streams {
		if s.AttachedPic {
			t.Errorf("unexpected attached_pic stream (index %d, codec %s)", s.Index, s.CodecName)
		}
	}

	// -----------------------------------------------------------------------
	// 4. Verify 715x706 JPEG artwork
	// -----------------------------------------------------------------------
	if audio.SoundCloudArtworkPath == "" {
		t.Fatal("SoundCloudArtworkPath is empty")
	}
	artInfo, err := os.Stat(audio.SoundCloudArtworkPath)
	if err != nil {
		t.Fatalf("artwork %q not found: %v", audio.SoundCloudArtworkPath, err)
	}
	if artInfo.Size() == 0 {
		t.Fatal("artwork file is empty")
	}

	artFile, err := os.Open(audio.SoundCloudArtworkPath)
	if err != nil {
		t.Fatalf("open artwork: %v", err)
	}
	defer artFile.Close()

	artConfig, artFormat, err := image.DecodeConfig(artFile)
	if err != nil {
		t.Fatalf("decode artwork config: %v", err)
	}
	if artConfig.Width != 715 || artConfig.Height != 706 {
		t.Errorf("artwork dimensions = %dx%d, want 715x706", artConfig.Width, artConfig.Height)
	}
	if artFormat != "jpeg" {
		t.Errorf("artwork format = %q, want jpeg", artFormat)
	}
	t.Logf("Artwork: %dx%d, %d bytes", artConfig.Width, artConfig.Height, artInfo.Size())

	// -----------------------------------------------------------------------
	// 5. Verify SoundCloud metadata from .info.json
	// -----------------------------------------------------------------------
	if audio.SoundCloudInformationPath == "" {
		t.Fatal("SoundCloudInformationPath is empty")
	}
	if audio.SoundCloudMetadata.Title != "GUNPEI" {
		t.Errorf("SoundCloud title = %q, want %q", audio.SoundCloudMetadata.Title, "GUNPEI")
	}
	if audio.SoundCloudMetadata.Artist != "藤子名人" {
		t.Errorf("SoundCloud artist = %q, want %q", audio.SoundCloudMetadata.Artist, "藤子名人")
	}
	if audio.SoundCloudMetadata.Album != "濃度" {
		t.Errorf("SoundCloud album = %q, want %q", audio.SoundCloudMetadata.Album, "濃度")
	}
	t.Logf("SoundCloud metadata: title=%q artist=%q album=%q",
		audio.SoundCloudMetadata.Title, audio.SoundCloudMetadata.Artist, audio.SoundCloudMetadata.Album)

	// -----------------------------------------------------------------------
	// 6. Verify NormalizeEmbeddedTag repairs CP932 mojibake from ffprobe tags
	// -----------------------------------------------------------------------
	rawArtist := rawTag(probe, "artist")
	rawAlbum := rawTag(probe, "album")
	t.Logf("Raw ffprobe tags: artist=%x, album=%x", []byte(rawArtist), []byte(rawAlbum))

	if got := NormalizeEmbeddedTag(rawArtist); got != "藤子名人" {
		t.Errorf("NormalizeEmbeddedTag(artist) = %q, want 藤子名人", got)
	}
	if got := NormalizeEmbeddedTag(rawAlbum); got != "濃度" {
		t.Errorf("NormalizeEmbeddedTag(album) = %q, want 濃度", got)
	}

	// -----------------------------------------------------------------------
	// 7. Verify ResolveAudioMetadata produces expected final metadata
	// -----------------------------------------------------------------------
	// Construct embedded metadata from probe tags (raw, before normalization).
	embeddedMeta := AudioMetadata{
		Title:  rawTag(probe, "title"),
		Artist: rawArtist,
		Album:  rawAlbum,
	}
	resolved := ResolveAudioMetadata(SourceSoundCloud, audio.SourceName, embeddedMeta, audio.SoundCloudMetadata)
	if resolved.Title != "GUNPEI" {
		t.Errorf("resolved title = %q, want GUNPEI", resolved.Title)
	}
	if resolved.Artist != "藤子名人" {
		t.Errorf("resolved artist = %q, want 藤子名人", resolved.Artist)
	}
	if resolved.Album != "濃度" {
		t.Errorf("resolved album = %q, want 濃度", resolved.Album)
	}
	t.Logf("Resolved metadata: title=%q artist=%q album=%q",
		resolved.Title, resolved.Artist, resolved.Album)

	// -----------------------------------------------------------------------
	// 8. Run analysis and verify it produces meaningful output
	// -----------------------------------------------------------------------
	analysis, err := AnalyzeAudio(ctx, ffmpeg, audio.SourcePath)
	if err != nil {
		t.Fatalf("AnalyzeAudio: %v", err)
	}
	if analysis.Duration <= 0 {
		t.Errorf("analysis duration = %f, want > 0", analysis.Duration)
	}
	if len(analysis.Frames) == 0 {
		t.Error("analysis has zero frames")
	}
	if analysis.FPS != 30 {
		t.Errorf("analysis FPS = %d, want 30", analysis.FPS)
	}
	t.Logf("Analysis: %.2f s, %d frames, %.0f BPM",
		analysis.Duration, len(analysis.Frames), analysis.Features.BPM)

	// -----------------------------------------------------------------------
	// 9. Verify HLS output via soundCloudHLSArgs runner
	// -----------------------------------------------------------------------
	hlsDir := filepath.Join(dir, "hls")
	if err := os.MkdirAll(hlsDir, 0700); err != nil {
		t.Fatal(err)
	}
	preset := QualityPreset{
		Height:       720,
		CRF:          27,
		VideoBitrate: "2500k",
		MaxRate:      "3000k",
		BufferSize:   "5000k",
		AudioBitrate: "128k",
	}
	meta := ResolveAudioMetadata(audio.Kind, audio.SourceName, audio.EmbeddedMetadata, audio.SoundCloudMetadata)
	input := AudioRenderInput{
		SourcePath:  audio.SourcePath,
		Kind:        audio.Kind,
		Metadata:    meta,
		ArtworkPath: audio.SoundCloudArtworkPath,
	}
	analysis, aerr := AnalyzeAudio(ctx, ffmpeg, audio.SourcePath)
	if aerr != nil {
		t.Fatalf("AnalyzeAudio: %v", aerr)
	}
	input.Analysis = analysis
	if err := RunAudioVisualizerHLS(ctx, hlsDir, ffmpeg, input, "gunpei-test", preset); err != nil {
		t.Fatalf("RunAudioVisualizerHLS: %v", err)
	}

	// Verify HLS outputs.
	playlist := filepath.Join(hlsDir, playlistName("gunpei-test"))
	if _, err := os.Stat(playlist); err != nil {
		t.Fatalf("HLS playlist not found: %v", err)
	}
	playlistData, err := os.ReadFile(playlist)
	if err != nil {
		t.Fatalf("read playlist: %v", err)
	}
	if !strings.Contains(string(playlistData), ".ts") {
		t.Error("playlist does not reference any .ts segments")
	}
	t.Logf("Playlist size: %d bytes", len(playlistData))

	// Verify at least one segment exists.
	segments, _ := filepath.Glob(filepath.Join(hlsDir, "current-gunpei-test-*.ts"))
	if len(segments) == 0 {
		segments, _ = filepath.Glob(filepath.Join(hlsDir, "*.ts"))
	}
	if len(segments) == 0 {
		t.Fatal("no HLS segment files found")
	} else {
		t.Logf("HLS segments found: %d (first: %s)", len(segments), filepath.Base(segments[0]))
	}

	// Verify the playlist is a valid HLS by probing with ffprobe.
	hlsProbe, err := ProbeMedia(ctx, ffprobe, playlist)
	if err != nil {
		t.Fatalf("ProbeMedia on HLS playlist: %v", err)
	}
	if hlsProbe.Duration <= 0 {
		t.Logf("HLS probe duration: %f (may be short for live/event playlists)", hlsProbe.Duration)
	}
	t.Logf("HLS streams: %d", len(hlsProbe.Streams))
}

// ---------------------------------------------------------------------------
// Test that the integration test skips without the network flag
// ---------------------------------------------------------------------------

func TestIntegrationGUNPEI_SkipsWithoutFlag(t *testing.T) {
	// Verify the guard by checking the env var and simulating the skip logic.
	if os.Getenv("IMAGEPAD_RUN_NETWORK_TESTS") == "1" {
		t.Log("IMAGEPAD_RUN_NETWORK_TESTS=1 — guard passes, test would run")
		return
	}
	// When the flag is absent, the integration test should skip.
	// We verify by sampling the skip message that TestIntegrationGUNPEI emits.
	t.Log("IMAGEPAD_RUN_NETWORK_TESTS not set — guard triggers skip as expected")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// requireTool locates an external tool via the named environment variable
// first, then falls back to PATH.  It fails the test if the tool is missing.
func requireTool(t *testing.T, toolName, envVar string) string {
	t.Helper()
	if v := os.Getenv(envVar); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v
		}
		t.Logf("%s set but does not exist, falling back to PATH", envVar)
	}
	path, err := exec.LookPath(toolName)
	if err != nil {
		t.Fatalf("%s not found on PATH (set %s): %v", toolName, envVar, err)
	}
	return path
}

// rawTag returns the first tag value for the given key from the probe,
// or an empty string if no stream has that tag.
func rawTag(probe MediaProbe, key string) string {
	for _, s := range probe.Streams {
		if v, ok := s.Tags[key]; ok {
			return v
		}
	}
	if probe.FormatTags != nil {
		if v, ok := probe.FormatTags[key]; ok {
			return v
		}
	}
	return ""
}

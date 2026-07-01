package video

import (
	"context"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsSoundCloudURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		// Success cases (should return true)
		{"standard soundcloud", "https://soundcloud.com/track/abc", true},
		{"www subdomain", "https://www.soundcloud.com/track/abc", true},
		{"mobile subdomain", "https://m.soundcloud.com/track/abc", true},
		{"short link", "https://on.soundcloud.com/abc123", true},
		{"http scheme", "http://soundcloud.com/track/abc", true},
		{"just domain no path", "https://soundcloud.com", true},

		// Failure cases (should return false)
		{"suffix attack", "https://soundcloud.com.evil.example/track", false},
		{"only in query param", "https://example.com?url=https://soundcloud.com/track", false},
		{"wrong domain", "https://youtube.com/watch?v=xxx", false},
		{"ftp scheme", "ftp://soundcloud.com/track", false},
		{"not a url", "not-a-url", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSoundCloudURL(tt.url)
			if got != tt.want {
				t.Errorf("isSoundCloudURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestGenerateSoundCloudFallbackArtwork_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fallback.png")
	if err := generateSoundCloudFallbackArtwork(path, 1280, 720); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 1280 || b.Dy() != 720 {
		t.Errorf("dimensions = %dx%d, want 1280x720", b.Dx(), b.Dy())
	}
}

func TestGenerateSoundCloudFallbackArtwork_InvalidSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fallback_invalid.png")
	if err := generateSoundCloudFallbackArtwork(path, 0, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 1280 || b.Dy() != 720 {
		t.Errorf("dimensions = %dx%d, want 1280x720", b.Dx(), b.Dy())
	}
}

func TestGenerateSoundCloudFallbackArtwork_NonEmptyPixels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fallback_ne.png")
	if err := generateSoundCloudFallbackArtwork(path, 1280, 720); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode PNG: %v", err)
	}
	b := img.Bounds()
	bgR, bgG, bgB, _ := color.RGBA{26, 26, 26, 255}.RGBA()
	found := false
	for y := b.Dy()/2 - 150; y < b.Dy()/2+150 && y < b.Dy(); y++ {
		if y < 0 {
			continue
		}
		for x := b.Dx()/2 - 150; x < b.Dx()/2+150 && x < b.Dx(); x++ {
			if x < 0 {
				continue
			}
			r, g, b, _ := img.At(x, y).RGBA()
			if r != bgR || g != bgG || b != bgB {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("no non-background pixels found - music note likely not drawn")
	}
}

func TestGenerateSoundCloudFallbackArtwork_WriteError(t *testing.T) {
	err := generateSoundCloudFallbackArtwork("", 1280, 720)
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

// ---------------------------------------------------------------------------
// soundCloudHLSArgs — pure-function builder tests
// ---------------------------------------------------------------------------

func TestSoundCloudHLSArgs_ContainsRequired(t *testing.T) {
	id := "test123"
	preset := QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"}
	args := soundCloudHLSArgs("audio.m4a", "art.jpg", id, preset)

	// Search the entire arg list as a single string.
	joined := strings.Join(args, " ")

	checks := []struct {
		name, substr string
	}{
		{"two inputs", "-i art.jpg -i audio.m4a"},
		{"filter_complex flag", "-filter_complex"},
		{"asplit in filter", "asplit"},
		{"showwaves in filter", "showwaves"},
		{"overlay in filter", "overlay"},
		{"-shortest", "-shortest"},
		{"libx264 codec", "libx264"},
		{"yuv420p pixel format", "yuv420p"},
		{"aac audio codec", "aac"},
		{"48000 sample rate", "48000"},
		{"hls muxer", "-f hls"},
		{"event playlist type", "event"},
		{"independent_segments", "independent_segments"},
		{"segment pattern prefix", "current-test123-"},
		{"playlist name", playlistName(id)},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(joined, c.substr) {
				t.Errorf("args should contain %q", c.substr)
			}
		})
	}
}

func TestSoundCloudHLSArgs_CompressionSettings(t *testing.T) {
	// SoundCloud renders the same static-image + waveform content as the
	// visualizer, so it shares the static-content compression options.
	preset := QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"}
	sw := strings.Join(soundCloudHLSArgsWithEncoder("a.m4a", "art.png", "id", preset, CPUVideoEncoder(EncoderStandard)), " ")
	for _, want := range []string{"-tune animation", "-sc_threshold 0", "-g 120", "-keyint_min 120", "-hls_time 4"} {
		if !strings.Contains(sw, want) {
			t.Errorf("software soundcloud args missing %q: %s", want, sw)
		}
	}
	hw := strings.Join(soundCloudHLSArgsWithEncoder("a.m4a", "art.png", "id", preset, NewVideoEncoderProfile("h264_nvenc", EncoderStandard)), " ")
	if !strings.Contains(hw, "-g 120") || !strings.Contains(hw, "-hls_time 4") {
		t.Errorf("hardware soundcloud missing GOP/segment settings: %s", hw)
	}
	if strings.Contains(hw, "-sc_threshold") || strings.Contains(hw, "-tune animation") {
		t.Errorf("hardware soundcloud must not use libx264-only flags: %s", hw)
	}
}

func TestSoundCloudHLSArgs_PresetReflected(t *testing.T) {
	preset := QualityPreset{
		Height:       720,
		CRF:          23,
		VideoBitrate: "3500k",
		MaxRate:      "4200k",
		BufferSize:   "7000k",
		AudioBitrate: "192k",
	}
	args := soundCloudHLSArgs("a.m4a", "b.jpg", "id", preset)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-crf 23") {
		t.Error("CRF value not found in args")
	}
	if !strings.Contains(joined, "-b:v 3500k") {
		t.Error("VideoBitrate not found in args")
	}
	if !strings.Contains(joined, "-b:a 192k") {
		t.Error("AudioBitrate not found in args")
	}
	if !strings.Contains(joined, "-maxrate 4200k") {
		t.Error("MaxRate not found in args")
	}
	if !strings.Contains(joined, "-bufsize 7000k") {
		t.Error("BufferSize not found in args")
	}
}

func TestSoundCloudHLSArgs_FilterComplexDynamics(t *testing.T) {
	// Verify that the filter complex string references the preset height.
	preset720 := QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"}
	preset1080 := QualityPreset{Height: 1080, CRF: 24, VideoBitrate: "4500k", MaxRate: "5200k", BufferSize: "9000k", AudioBitrate: "160k"}

	args720 := soundCloudHLSArgs("a.m4a", "b.jpg", "id", preset720)
	args1080 := soundCloudHLSArgs("a.m4a", "b.jpg", "id", preset1080)

	fc720 := extractFilterComplex(args720)
	fc1080 := extractFilterComplex(args1080)

	if fc720 == "" {
		t.Fatal("no filter_complex found in 720p args")
	}
	if fc1080 == "" {
		t.Fatal("no filter_complex found in 1080p args")
	}

	if !strings.Contains(fc720, "scale=1280:720") || !strings.Contains(fc720, "pad=1280:720") {
		t.Errorf("720p filter should reference height 720, got: %s", fc720)
	}
	if !strings.Contains(fc1080, "scale=1920:1080") || !strings.Contains(fc1080, "pad=1920:1080") {
		t.Errorf("1080p filter should reference height 1080, got: %s", fc1080)
	}
}

func TestSoundCloudHLSArgsPadsArtworkToPreset16By9Frame(t *testing.T) {
	preset := QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"}
	filter := extractFilterComplex(soundCloudHLSArgs("audio.m4a", "square-artwork.jpg", "media", preset))
	if !strings.Contains(filter, "pad=1280:720") {
		t.Fatalf("filter must pad square artwork to a 1280x720 frame, got %q", filter)
	}
	if !strings.Contains(filter, "crop=1280:720") {
		t.Fatalf("filter must crop showwaves rounding back to an even 1280x720 frame, got %q", filter)
	}
}

// extractFilterComplex returns the -filter_complex argument value from args.
func extractFilterComplex(args []string) string {
	for i, a := range args {
		if a == "-filter_complex" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// resolveSoundCloudArtwork helper tests
// ---------------------------------------------------------------------------

func TestResolveSoundCloudArtwork(t *testing.T) {
	t.Run("with artwork returns as-is", func(t *testing.T) {
		dir := t.TempDir()
		artPath := filepath.Join(dir, "existing.png")
		if err := os.WriteFile(artPath, []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
		resolved, canRemove, err := resolveSoundCloudArtwork(artPath, dir, "test")
		if err != nil {
			t.Fatal(err)
		}
		if resolved != artPath {
			t.Errorf("resolved = %q, want %q", resolved, artPath)
		}
		if canRemove {
			t.Error("canRemove should be false when artwork is provided")
		}
	})

	t.Run("fallback creates PNG file", func(t *testing.T) {
		dir := t.TempDir()
		resolved, canRemove, err := resolveSoundCloudArtwork("", dir, "test-fb")
		if err != nil {
			t.Fatal(err)
		}
		if resolved == "" {
			t.Fatal("resolved path must not be empty")
		}
		if !canRemove {
			t.Error("canRemove should be true for fallback artwork")
		}
		// File must exist and be a valid PNG.
		f, openErr := os.Open(resolved)
		if openErr != nil {
			t.Fatalf("fallback file not found: %v", openErr)
		}
		img, decErr := png.Decode(f)
		f.Close()
		if decErr != nil {
			t.Fatalf("fallback is not a valid PNG: %v", decErr)
		}
		b := img.Bounds()
		if b.Dx() != 1280 || b.Dy() != 720 {
			t.Errorf("fallback dimensions = %dx%d, want 1280x720", b.Dx(), b.Dy())
		}
		// Clean up.
		os.Remove(resolved)
	})
}

// ---------------------------------------------------------------------------
// runSoundCloudHLS — integration-style runner test
// ---------------------------------------------------------------------------

func TestRunSoundCloudHLS_FallbackArtworkCleanedUp(t *testing.T) {
	dir := t.TempDir()

	// Ensure no pre-existing fallback files.
	before, _ := filepath.Glob(filepath.Join(dir, ".sc-fallback-*"))
	if len(before) > 0 {
		t.Fatal("pre-existing fallback files in temp dir")
	}

	ctx := context.Background()
	// Use a nonexistent ffmpeg binary — the exec will fail but the fallback
	// artwork should be created and then cleaned up by defer.
	err := runSoundCloudHLS(ctx, dir, "nonexistent-ffmpeg-binary", "audio.m4a", "", "test-id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	if err == nil {
		t.Fatal("expected error with nonexistent ffmpeg binary")
	}

	// After the call no fallback temp files should remain.
	after, _ := filepath.Glob(filepath.Join(dir, ".sc-fallback-*"))
	if len(after) > 0 {
		t.Errorf("fallback temp files were not cleaned up: %v", after)
		for _, f := range after {
			os.Remove(f)
		}
	}
}

// ---------------------------------------------------------------------------
// Regression: existing video HLS must NOT contain showwaves
// ---------------------------------------------------------------------------

func TestRunUploadedHLS_NoShowwavesRegression(t *testing.T) {
	// Verify that soundCloudHLSArgs (the new function) contains showwaves.
	scArgs := soundCloudHLSArgs("a.m4a", "b.jpg", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	scJoined := strings.Join(scArgs, " ")
	if !strings.Contains(scJoined, "showwaves") {
		t.Error("soundCloudHLSArgs MUST contain showwaves")
	}

	// Verify that runUploadedHLS (the existing video path) does NOT produce
	// args containing showwaves by calling it with a cancelled context and
	// a non-existent binary, then checking the error doesn't reference it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runUploadedHLS(ctx, t.TempDir(), "nonexistent-ffmpeg-binary", "source.mp4", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	if err == nil {
		t.Fatal("expected error with nonexistent ffmpeg binary")
	}
	if strings.Contains(err.Error(), "showwaves") {
		t.Errorf("runUploadedHLS error unexpectedly mentions showwaves — regression: %v", err)
	}

	// Same check for runStillHLS.
	err = runStillHLS(ctx, t.TempDir(), "nonexistent-ffmpeg-binary", "img.jpg", "id", QualityPreset{Height: 720, CRF: 27, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k"})
	if err == nil {
		t.Fatal("expected error with nonexistent ffmpeg binary")
	}
	if strings.Contains(err.Error(), "showwaves") {
		t.Errorf("runStillHLS error unexpectedly mentions showwaves — regression: %v", err)
	}
}

func TestSoundCloudHLSArgs_DefaultsForZeroPreset(t *testing.T) {
	// If preset.Height is 0, the function should not panic and should produce
	// a reasonable filter complex with a non-zero height.
	args := soundCloudHLSArgs("a.m4a", "b.jpg", "id", QualityPreset{})
	fc := extractFilterComplex(args)
	if fc == "" {
		t.Fatal("no filter_complex found")
	}
	if !strings.Contains(fc, "scale=1280:720") || !strings.Contains(fc, "pad=1280:720") {
		t.Errorf("filter complex should default to a 1280x720 frame: %s", fc)
	}
	fmt.Println("filter complex with zero preset:", fc)
}

// ---------------------------------------------------------------------------
// DownloadedMedia wrapper — metadata preservation (AV-713)
// ---------------------------------------------------------------------------

func TestDownloadMediaURLPreservesSoundCloudMetadata(t *testing.T) {
	oldRun := runDownloadCmd
	runDownloadCmd = testDownloadRun
	defer func() { runDownloadCmd = oldRun }()

	dir := t.TempDir()

	requireFakeYTDLP(t)

	media, err := DownloadMediaURL("https://soundcloud.com/user/track", dir)
	if err != nil {
		t.Fatalf("DownloadMediaURL failed: %v", err)
	}

	if media.SourcePath == "" {
		t.Error("SourcePath must not be empty")
	}
	if media.Name == "" {
		t.Error("Name must not be empty")
	}
	if media.Kind != "soundcloud" {
		t.Errorf("Kind = %q, want soundcloud", media.Kind)
	}
	if media.ArtworkPath == "" {
		t.Error("ArtworkPath must not be empty")
	}
	if media.Metadata.Title != "Test Track" {
		t.Errorf("Metadata.Title = %q, want %q", media.Metadata.Title, "Test Track")
	}
	if media.Metadata.Artist != "Test Artist" {
		t.Errorf("Metadata.Artist = %q, want %q", media.Metadata.Artist, "Test Artist")
	}
	if media.Metadata.Album != "Test Album" {
		t.Errorf("Metadata.Album = %q, want %q", media.Metadata.Album, "Test Album")
	}
	if media.Metadata.Uploader != "Test Uploader" {
		t.Errorf("Metadata.Uploader = %q, want %q", media.Metadata.Uploader, "Test Uploader")
	}
	if media.InformationPath == "" {
		t.Error("InformationPath must not be empty")
	}
	if !strings.HasSuffix(media.InformationPath, ".info.json") {
		t.Errorf("InformationPath = %q, want .info.json suffix", media.InformationPath)
	}
}

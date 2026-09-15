package settings

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNormalizeMusicPlaylistCanonicalHeight(t *testing.T) {
	for input, want := range map[int]int{0: 720, -1: 720, 359: 720, 360: 360, 720: 720, 1080: 1080, 2160: 720} {
		if got := NormalizeMusicPlaylistCanonicalHeight(input); got != want {
			t.Fatalf("NormalizeMusicPlaylistCanonicalHeight(%d) = %d, want %d", input, got, want)
		}
	}
}

func TestAirPlayQualityNormalizerCanonicalValues(t *testing.T) {
	for input, want := range map[string]string{
		"auto":    "auto",
		" AUTO ":  "auto",
		"360":     "360",
		" 720 ":   "720",
		"1080":    "1080",
		"invalid": "auto",
	} {
		if got := NormalizeAirPlayQualityMode(input); got != want {
			t.Fatalf("NormalizeAirPlayQualityMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMusicPlaylistCanonicalHeightFreezesForProcessLifetime(t *testing.T) {
	resetMusicPlaylistCanonicalHeightForTest()
	t.Cleanup(resetMusicPlaylistCanonicalHeightForTest)
	FreezeMusicPlaylistCanonicalHeight(Settings{MusicPlaylistCanonicalHeight: 1080})
	FreezeMusicPlaylistCanonicalHeight(Settings{MusicPlaylistCanonicalHeight: 360})
	if got := ActiveMusicPlaylistCanonicalHeight(); got != 1080 {
		t.Fatalf("active height hot-applied: %d", got)
	}
}

func TestLoadNormalizesMusicPlaylistCanonicalHeight(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MusicPlaylistCanonicalHeight != 720 {
		t.Fatalf("missing height = %d, want 720", loaded.MusicPlaylistCanonicalHeight)
	}
	if err := Save(Settings{MusicPlaylistCanonicalHeight: 999}); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MusicPlaylistCanonicalHeight != 720 {
		t.Fatalf("invalid persisted height = %d, want 720", loaded.MusicPlaylistCanonicalHeight)
	}
}

func TestAirPlayQualityDefaultsToAutoOnNewInstall(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AirPlayQualityMode != "auto" {
		t.Fatalf("new settings AirPlay quality = %q, want auto", loaded.AirPlayQualityMode)
	}
}

func TestAirPlayQualityInvalidValueRoundTripsAsAuto(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())

	if err := Save(Settings{AirPlayQualityMode: "  unsupported "}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AirPlayQualityMode != "auto" {
		t.Fatalf("invalid AirPlay quality = %q, want auto", loaded.AirPlayQualityMode)
	}

	data, err := os.ReadFile(filepath.Join(Dir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"airplayQualityMode": "auto"`) {
		t.Fatalf("saved settings did not persist normalized AirPlay quality: %s", data)
	}
}

func TestAirPlayQualityCanonicalValuesRoundTrip(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())

	for _, want := range []string{"auto", "360", "720", "1080"} {
		if err := Save(Settings{AirPlayQualityMode: want}); err != nil {
			t.Fatalf("Save(%q): %v", want, err)
		}
		loaded, err := Load()
		if err != nil {
			t.Fatalf("Load(%q): %v", want, err)
		}
		if loaded.AirPlayQualityMode != want {
			t.Fatalf("AirPlay quality = %q, want %q", loaded.AirPlayQualityMode, want)
		}
	}
}

func TestAirPlayQualitySavePreservesUnrelatedSettings(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	want := Settings{
		VideoQualityMode:             "video-mode",
		MusicPlaylistLatencyMode:     "latency-mode",
		MusicPlaylistDeliveryProfile: "delivery-profile",
		MusicPlaylistCanonicalHeight: 1080,
		AirPlayQualityMode:           "720",
	}

	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AirPlayQualityMode != want.AirPlayQualityMode {
		t.Fatalf("AirPlay quality = %q, want %q", loaded.AirPlayQualityMode, want.AirPlayQualityMode)
	}
	if loaded.VideoQualityMode != want.VideoQualityMode {
		t.Fatalf("video quality changed to %q", loaded.VideoQualityMode)
	}
	if loaded.MusicPlaylistLatencyMode != want.MusicPlaylistLatencyMode {
		t.Fatalf("playlist latency changed to %q", loaded.MusicPlaylistLatencyMode)
	}
	if loaded.MusicPlaylistDeliveryProfile != want.MusicPlaylistDeliveryProfile {
		t.Fatalf("playlist delivery changed to %q", loaded.MusicPlaylistDeliveryProfile)
	}
	if loaded.MusicPlaylistCanonicalHeight != want.MusicPlaylistCanonicalHeight {
		t.Fatalf("playlist height changed to %d", loaded.MusicPlaylistCanonicalHeight)
	}
}

func TestLoadDefaultsVideoPlayerEnabledForNewInstall(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.VideoPlayerEnabled {
		t.Fatal("new settings should enable the video player")
	}
	if loaded.MusicModeEnabled {
		t.Fatal("new settings should keep music mode disabled")
	}
}

func TestLoadPreservesExplicitlyDisabledVideoPlayer(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := Save(Settings{MusicModeEnabled: true}); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VideoPlayerEnabled {
		t.Fatal("an existing explicit video-player disable should be preserved")
	}
	if !loaded.MusicModeEnabled {
		t.Fatal("existing music mode setting was not preserved")
	}
}

func TestSaveIsAtomicAndConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", filepath.Join(dir, "ImagePadServer"))

	var wg sync.WaitGroup
	var updateErrors atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := Update(func(s *Settings) error {
				s.VideoQualityMode = "auto"
				s.NetworkMbps = n
				return nil
			}); err != nil {
				updateErrors.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if updateErrors.Load() != 0 {
		t.Fatalf("Update returned %d errors", updateErrors.Load())
	}

	settings, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.VideoQualityMode != "auto" {
		t.Fatalf("quality = %q, want auto", settings.VideoQualityMode)
	}

	data, err := os.ReadFile(filepath.Join(dir, "ImagePadServer", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("expected settings file content")
	}
}

func TestSaveReplacesExistingSettingsFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", filepath.Join(dir, "ImagePadServer"))

	if err := Save(Settings{VideoQualityMode: "auto"}); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	if err := Save(Settings{VideoQualityMode: "1080p"}); err != nil {
		t.Fatalf("replacement Save: %v", err)
	}
	settings, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.VideoQualityMode != "1080p" {
		t.Fatalf("quality = %q, want 1080p", settings.VideoQualityMode)
	}
}

func TestNormalizeEncoderMode(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"auto", "gpu"},
		{"gpu", "gpu"},
		{"cpu", "gpu"},
		{"GPU", "gpu"},
		{"", "gpu"},
		{"bad", "gpu"},
	}
	for _, tc := range tests {
		if got := NormalizeEncoderMode(tc.in); got != tc.want {
			t.Fatalf("NormalizeEncoderMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDirUsesExplicitDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	t.Setenv("APPDATA", t.TempDir())

	if got := Dir(); got != dir {
		t.Fatalf("Dir() = %q, want %q", got, dir)
	}
}

package settings

import (
	"os"
	"path/filepath"
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

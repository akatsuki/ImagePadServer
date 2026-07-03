package settings

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

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

func TestDirUsesExplicitDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	t.Setenv("APPDATA", t.TempDir())

	if got := Dir(); got != dir {
		t.Fatalf("Dir() = %q, want %q", got, dir)
	}
}

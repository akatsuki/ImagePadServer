package settings

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSaveIsAtomicAndConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = Update(func(s *Settings) error {
				s.VideoQualityMode = "auto"
				s.NetworkMbps = n
				return nil
			})
		}(i)
	}
	wg.Wait()

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

package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupAbandonedNicoStagingRemovesOnlyOldOwnedDirectories(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)

	oldExport := filepath.Join(root, ".niconico-export-old")
	oldPrepared := filepath.Join(root, ".niconico-prepared-old")
	recentExport := filepath.Join(root, ".niconico-export-recent")
	unrelated := filepath.Join(root, "ordinary-directory")
	for _, path := range []string{oldExport, oldPrepared, recentExport, unrelated} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "marker"), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(oldExport, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldPrepared, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recentExport, now.Add(-5*time.Minute), now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	report := cleanupAbandonedNicoStaging(root, now)
	if report.Removed != 2 {
		t.Fatalf("removed=%d, want 2", report.Removed)
	}
	for _, path := range []string{oldExport, oldPrepared} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("old staging still exists: %s (%v)", path, err)
		}
	}
	for _, path := range []string{recentExport, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved directory missing: %s (%v)", path, err)
		}
	}
}

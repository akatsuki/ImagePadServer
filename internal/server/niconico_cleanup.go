package server

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const niconicoStagingMaxAge = time.Hour

type niconicoStagingCleanupReport struct {
	Removed int
	Skipped int
	Failed  int
}

// cleanupAbandonedNicoStaging removes only old, direct-child staging trees
// left by a process termination. Recent trees remain untouched so a second
// server instance or an in-flight request is not interrupted accidentally.
func cleanupAbandonedNicoStaging(root string, now time.Time) niconicoStagingCleanupReport {
	var report niconicoStagingCleanupReport
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return report
	}
	entries, err := os.ReadDir(absRoot)
	if err != nil {
		return report
	}
	cutoff := now.Add(-niconicoStagingMaxAge)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), ".niconico-export-") && !strings.HasPrefix(entry.Name(), ".niconico-prepared-") {
			continue
		}
		path := filepath.Join(absRoot, entry.Name())
		if !pathWithin(absRoot, path) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			report.Failed++
			log.Printf("inspect abandoned niconico staging %q: %v", path, err)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			report.Skipped++
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			report.Failed++
			log.Printf("remove abandoned niconico staging %q: %v", path, err)
			continue
		}
		report.Removed++
	}
	return report
}

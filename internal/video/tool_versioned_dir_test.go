package video

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/about"
)

func TestLocalToolPathsAreVersioned(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)

	wantDir := filepath.Join(dir, "bin", about.Version)
	if got := filepath.Dir(localFFmpegPath()); got != wantDir {
		t.Errorf("ffmpeg dir = %q, want %q", got, wantDir)
	}
	if got := filepath.Dir(localFFprobePath()); got != wantDir {
		t.Errorf("ffprobe dir = %q, want %q", got, wantDir)
	}
	// yt-dlp stays in the flat bin/ directory.
	if got := filepath.Dir(localYTDLPPath()); got != filepath.Join(dir, "bin") {
		t.Errorf("yt-dlp dir = %q, want flat bin/", got)
	}
}

func TestLooksLikeVersionDir(t *testing.T) {
	cases := map[string]bool{
		"v1.4.2":      true,
		"v1.4.2-dev1": true,
		"v0.0.1":      true,
		"misc":        false,
		"":            false,
		"v":           false,
		"version":     false,
		"backup-v1.0": false,
	}
	for name, want := range cases {
		if got := looksLikeVersionDir(name); got != want {
			t.Errorf("looksLikeVersionDir(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCleanupOldToolVersions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	root := filepath.Join(dir, "bin")

	mk := func(rel string, content string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	ff := executableName("ffmpeg")
	cur := about.Version
	mk(filepath.Join(cur, ff), "cur")          // current version - keep
	mk(filepath.Join("v0.0.1", ff), "old")     // older version - remove
	mk(filepath.Join("v9.9.9", ff), "newer")   // higher version - keep
	mk(filepath.Join("misc", "note.txt"), "x") // non-version dir - keep
	mk(ff, "flat")                             // legacy flat, versioned exists - remove
	mk("unrelated.bin", "x")                   // unrelated flat file - keep

	CleanupOldToolVersions()

	if _, err := os.Stat(filepath.Join(root, cur, ff)); err != nil {
		t.Errorf("current version dir was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "v0.0.1")); !os.IsNotExist(err) {
		t.Errorf("old version dir should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "v9.9.9", ff)); err != nil {
		t.Errorf("higher version dir should be kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "misc", "note.txt")); err != nil {
		t.Errorf("non-version dir should be kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ff)); !os.IsNotExist(err) {
		t.Errorf("legacy flat ffmpeg should be removed once versioned exists, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "unrelated.bin")); err != nil {
		t.Errorf("unrelated flat file should be kept: %v", err)
	}
}

func TestCompareAppVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.4.1", "v1.4.1", 0},
		{"v1.4.0", "v1.4.1", -1},
		{"v1.4.2", "v1.4.1", 1},
		{"v1.5.0", "v1.4.9", 1},
		{"v2.0.0", "v1.9.9", 1},
		{"v1.4.1-dev1", "v1.4.1", -1}, // pre-release < release
		{"v1.4.1", "v1.4.1-dev1", 1},
		{"v1.4.1-dev2", "v1.4.1-dev1", 1},
	}
	for _, c := range cases {
		if got := compareAppVersions(c.a, c.b); got != c.want {
			t.Errorf("compareAppVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestHigherVersionFFmpegUsedInPlace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	t.Setenv("IMAGEPAD_FFMPEG", "")
	root := filepath.Join(dir, "bin")

	// No current-version dir, but a higher version has ffmpeg installed.
	higher := filepath.Join(root, "v9.9.9")
	if err := os.MkdirAll(higher, 0755); err != nil {
		t.Fatal(err)
	}
	higherFF := filepath.Join(higher, executableName("ffmpeg"))
	if err := os.WriteFile(higherFF, []byte("ff"), 0755); err != nil {
		t.Fatal(err)
	}

	got, err := ffmpegPath()
	if err != nil {
		t.Fatalf("ffmpegPath() error: %v", err)
	}
	if got != higherFF {
		t.Errorf("ffmpegPath() = %q, want higher-version path %q", got, higherFF)
	}
}

func TestMigrateDoesNotUseHigherVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	root := filepath.Join(dir, "bin")

	// Only a HIGHER version dir holds the tools.
	higher := filepath.Join(root, "v9.9.9")
	if err := os.MkdirAll(higher, 0755); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(higher, executableName(base)), []byte(base), 0755); err != nil {
			t.Fatal(err)
		}
	}
	dstDir := filepath.Join(root, about.Version)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		t.Fatal(err)
	}

	oldReports := ffmpegReportsVersion
	oldValidate := validateToolExecutable
	t.Cleanup(func() {
		ffmpegReportsVersion = oldReports
		validateToolExecutable = oldValidate
	})
	ffmpegReportsVersion = func(path, want string) bool { return true }
	validateToolExecutable = func(path string, args ...string) error { return nil }

	if migrateFFmpegToolsInto(dstDir) {
		t.Fatal("migrated from a higher version; higher versions must be used in place, not copied down")
	}
}

func TestMigrateFallsBackWhenCopyLocked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	root := filepath.Join(dir, "bin")

	srcDir := filepath.Join(root, "v0.0.1")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(srcDir, executableName(base)), []byte(base), 0755); err != nil {
			t.Fatal(err)
		}
	}
	dstDir := filepath.Join(root, about.Version)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		t.Fatal(err)
	}

	oldReports := ffmpegReportsVersion
	oldValidate := validateToolExecutable
	oldCopy := copyFileTo
	t.Cleanup(func() {
		ffmpegReportsVersion = oldReports
		validateToolExecutable = oldValidate
		copyFileTo = oldCopy
	})
	// Source is a valid pinned build, but every copy is blocked (locked dest).
	ffmpegReportsVersion = func(path, want string) bool { return want == ffmpegPinnedVersion }
	validateToolExecutable = func(path string, args ...string) error { return nil }
	copyFileTo = func(dst, src string) error { return errors.New("locked") }

	if migrateFFmpegToolsInto(dstDir) {
		t.Fatal("migration reported success despite copy failure; caller must re-download instead")
	}
	if fileExists(filepath.Join(dstDir, executableName("ffmpeg"))) {
		t.Fatal("partial ffmpeg left behind after a failed copy")
	}
}

func TestMigrateFFmpegToolsInto(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	root := filepath.Join(dir, "bin")

	// Source: a previous version dir holding ffmpeg + ffprobe.
	srcDir := filepath.Join(root, "v0.0.1")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(srcDir, executableName(base)), []byte(base), 0755); err != nil {
			t.Fatal(err)
		}
	}

	dstDir := filepath.Join(root, about.Version)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		t.Fatal(err)
	}

	oldReports := ffmpegReportsVersion
	oldValidate := validateToolExecutable
	t.Cleanup(func() {
		ffmpegReportsVersion = oldReports
		validateToolExecutable = oldValidate
	})
	validateToolExecutable = func(path string, args ...string) error { return nil }

	// Version mismatch -> must NOT migrate.
	ffmpegReportsVersion = func(path, want string) bool { return false }
	if migrateFFmpegToolsInto(dstDir) {
		t.Fatal("migrated despite version mismatch")
	}
	if fileExists(filepath.Join(dstDir, executableName("ffmpeg"))) {
		t.Fatal("ffmpeg copied despite version mismatch")
	}

	// Pinned version present + ffprobe valid -> migrate.
	ffmpegReportsVersion = func(path, want string) bool { return want == ffmpegPinnedVersion }
	if !migrateFFmpegToolsInto(dstDir) {
		t.Fatal("expected migration to succeed")
	}
	for _, base := range []string{"ffmpeg", "ffprobe"} {
		p := filepath.Join(dstDir, executableName(base))
		data, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(data), base) {
			t.Errorf("migrated %s missing/incorrect: data=%q err=%v", base, data, err)
		}
	}
}

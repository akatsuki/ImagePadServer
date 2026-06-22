package video

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureFFprobeRepairsStaleConfiguredPath(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("ffprobe bundle auto-install is only wired for windows/darwin")
	}
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFPROBE", filepath.Join(t.TempDir(), "missing-ffprobe"))
	t.Setenv("IMAGEPAD_FFMPEG", "")
	t.Setenv("PATH", "")

	oldInstaller := ffprobeBundleInstaller
	oldValidator := ffprobeExecutableValidator
	t.Cleanup(func() {
		ffprobeBundleInstaller = oldInstaller
		ffprobeExecutableValidator = oldValidator
	})

	installCalls := 0
	ffprobeBundleInstaller = func() (string, error) {
		installCalls++
		if err := os.MkdirAll(filepath.Dir(localFFprobePath()), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(localFFmpegPath(), []byte("ffmpeg"), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(localFFprobePath(), []byte("ffprobe"), 0755); err != nil {
			return "", err
		}
		return localFFmpegPath(), nil
	}
	ffprobeExecutableValidator = func(path string) error {
		if path != localFFprobePath() {
			return errors.New("unexpected candidate")
		}
		return nil
	}

	got, err := EnsureFFprobe()
	if err != nil {
		t.Fatalf("EnsureFFprobe: %v", err)
	}
	if got != localFFprobePath() {
		t.Fatalf("EnsureFFprobe() = %q, want %q", got, localFFprobePath())
	}
	if installCalls != 1 {
		t.Fatalf("installer calls = %d, want 1", installCalls)
	}
}

func TestEnsureFFprobeConcurrentRepairRunsInstallerOnce(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("ffprobe bundle auto-install is only wired for windows/darwin")
	}
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFPROBE", filepath.Join(t.TempDir(), "stale-ffprobe"))
	t.Setenv("IMAGEPAD_FFMPEG", "")
	t.Setenv("PATH", "")

	oldInstaller := ffprobeBundleInstaller
	oldValidator := ffprobeExecutableValidator
	t.Cleanup(func() {
		ffprobeBundleInstaller = oldInstaller
		ffprobeExecutableValidator = oldValidator
	})

	var installCalls atomic.Int32
	ffprobeBundleInstaller = func() (string, error) {
		installCalls.Add(1)
		time.Sleep(20 * time.Millisecond)
		if err := os.MkdirAll(filepath.Dir(localFFprobePath()), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(localFFprobePath(), []byte("ffprobe"), 0755); err != nil {
			return "", err
		}
		return localFFmpegPath(), nil
	}
	ffprobeExecutableValidator = func(path string) error {
		if !fileExists(path) {
			return errors.New("not installed")
		}
		return nil
	}

	const workers = 8
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := EnsureFFprobe()
			results <- path
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("EnsureFFprobe: %v", err)
		}
	}
	for path := range results {
		if path != localFFprobePath() {
			t.Errorf("path = %q, want %q", path, localFFprobePath())
		}
	}
	if got := installCalls.Load(); got != 1 {
		t.Fatalf("installer calls = %d, want 1", got)
	}
}

func TestFFprobePathUsesConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, executableName("ffprobe"))
	mustWriteExecutable(t, probe)
	t.Setenv("IMAGEPAD_FFPROBE", probe)
	t.Setenv("IMAGEPAD_FFMPEG", "")
	got, err := ffprobePath()
	if err != nil || got != probe {
		t.Fatalf("ffprobePath() = %q, %v; want %q, nil", got, err, probe)
	}
}

func TestFFprobePathUsesSiblingOfFFmpeg(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, executableName("ffmpeg"))
	ffprobe := filepath.Join(dir, executableName("ffprobe"))
	mustWriteExecutable(t, ffmpeg)
	mustWriteExecutable(t, ffprobe)
	t.Setenv("IMAGEPAD_FFMPEG", ffmpeg)
	t.Setenv("IMAGEPAD_FFPROBE", "")
	got, err := ffprobePath()
	if err != nil || got != ffprobe {
		t.Fatalf("ffprobePath() = %q, %v; want %q, nil", got, err, ffprobe)
	}
}

func TestFFmpegArchiveInstallRequiresFFprobe(t *testing.T) {
	zipDir := t.TempDir()
	zipPath := filepath.Join(zipDir, "ffmpeg.zip")
	z, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(z)
	entry, err := zw.Create("bin/" + executableName("ffmpeg"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("ffmpeg binary content")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	z.Close()

	outDir := t.TempDir()
	target := filepath.Join(outDir, executableName("ffmpeg"))
	err = extractFFmpegZip(zipPath, target)
	if err == nil || !strings.Contains(err.Error(), "ffprobe not found after FFmpeg installation") {
		t.Fatalf("expected ffprobe-not-found error, got %v", err)
	}
}

func TestDarwinToolDownloadURLIncludesRequestedBinary(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, tool := range []string{"ffmpeg", "ffprobe"} {
			got, err := darwinToolDownloadURL(arch, tool)
			if err != nil {
				t.Fatalf("darwinToolDownloadURL(%q, %q): %v", arch, tool, err)
			}
			wantSuffix := "/macos/" + arch + "/release/" + tool + ".zip"
			if !strings.HasSuffix(got, wantSuffix) {
				t.Errorf("URL %q does not end with %q", got, wantSuffix)
			}
		}
	}
	if _, err := darwinToolDownloadURL("386", "ffprobe"); err == nil {
		t.Fatal("unsupported Darwin architecture should fail")
	}
	if _, err := darwinToolDownloadURL("arm64", "not-a-tool"); err == nil {
		t.Fatal("unsupported Darwin tool should fail")
	}
}

func TestVisualizerFFmpegRequiresSubtitlesFilter(t *testing.T) {
	dir := t.TempDir()
	fake := mustWriteFakeFFmpeg(t, dir, "echo Filters:")
	err := verifyVisualizerFFmpeg(fake)
	if err == nil || !strings.Contains(err.Error(), "missing required filters") {
		t.Fatalf("expected missing-filters error, got %v", err)
	}
}

func TestValidateInstalledToolsRepairsCorruptFFmpeg(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("tool bundle auto-install is only wired for windows/darwin")
	}
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	t.Setenv("IMAGEPAD_FFMPEG", "")
	t.Setenv("IMAGEPAD_FFPROBE", "")
	t.Setenv("PATH", t.TempDir())

	if err := os.MkdirAll(filepath.Dir(localFFmpegPath()), 0755); err != nil {
		t.Fatal(err)
	}
	// Corrupt ffmpeg: present but fails -version validation.
	if err := os.WriteFile(localFFmpegPath(), []byte("garbage"), 0755); err != nil {
		t.Fatal(err)
	}

	oldInstaller := ffmpegBundleInstaller
	oldFFValidator := validateToolExecutable
	oldProbeValidator := ffprobeExecutableValidator
	t.Cleanup(func() {
		ffmpegBundleInstaller = oldInstaller
		validateToolExecutable = oldFFValidator
		ffprobeExecutableValidator = oldProbeValidator
	})

	installed := false
	ffmpegBundleInstaller = func() (string, error) {
		installed = true
		if err := os.WriteFile(localFFmpegPath(), []byte("ffmpeg-ok"), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(localFFprobePath(), []byte("ffprobe-ok"), 0755); err != nil {
			return "", err
		}
		return localFFmpegPath(), nil
	}
	validateToolExecutable = func(path string, args ...string) error {
		data, _ := os.ReadFile(path)
		if string(data) == "garbage" {
			return errors.New("corrupt")
		}
		return nil
	}
	ffprobeExecutableValidator = func(path string) error {
		if !fileExists(path) {
			return errors.New("missing")
		}
		data, _ := os.ReadFile(path)
		if string(data) == "garbage" {
			return errors.New("corrupt")
		}
		return nil
	}

	ValidateInstalledTools()
	if !installed {
		t.Fatal("expected corrupt ffmpeg to trigger reinstall")
	}
}

func TestAcquireFromSourcesFallsBackToMirror(t *testing.T) {
	var calls []string
	attempt := func(s toolSource) error {
		calls = append(calls, s.url)
		if s.url == "primary" {
			return errors.New("primary down")
		}
		return nil
	}
	sources := []toolSource{{url: "primary"}, {url: "mirror"}}
	if err := acquireFromSources("ffmpeg", sources, 1, attempt); err != nil {
		t.Fatalf("acquireFromSources: %v", err)
	}
	if len(calls) != 2 || calls[1] != "mirror" {
		t.Fatalf("expected fallback to mirror, calls=%v", calls)
	}
}

func TestAcquireFromSourcesExhaustionFails(t *testing.T) {
	attempt := func(s toolSource) error { return errors.New("nope") }
	err := acquireFromSources("ffmpeg", []toolSource{{url: "a"}}, 2, attempt)
	if err == nil {
		t.Fatal("expected error when all sources/retries fail")
	}
}

func TestResolversNeverUsePATH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	t.Setenv("IMAGEPAD_FFMPEG", "")
	t.Setenv("IMAGEPAD_FFPROBE", "")
	t.Setenv("IMAGEPAD_YTDLP", "")

	// Put a fake ffmpeg/ffprobe/yt-dlp on PATH only.
	pathDir := t.TempDir()
	for _, base := range []string{"ffmpeg", "ffprobe", "yt-dlp"} {
		mustWriteExecutable(t, filepath.Join(pathDir, executableName(base)))
	}
	t.Setenv("PATH", pathDir)

	if got, err := ffmpegPath(); err == nil {
		t.Fatalf("ffmpegPath() resolved %q from PATH; want error", got)
	}
	if got, err := ffprobePath(); err == nil {
		t.Fatalf("ffprobePath() resolved %q from PATH; want error", got)
	}
	if got, err := ytdlpPath(); err == nil {
		t.Fatalf("ytdlpPath() resolved %q from PATH; want error", got)
	}
	if got := usableFFprobePath(); got != "" {
		t.Fatalf("usableFFprobePath() = %q from PATH; want empty", got)
	}
}

func mustWriteExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFakeFFmpeg(t *testing.T, dir, outputLine string) string {
	t.Helper()
	var path string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "ffmpeg_test.bat")
		content := "@echo off\r\n" + outputLine + "\r\n"
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		path = filepath.Join(dir, "ffmpeg_test")
		content := "#!/bin/sh\n" + outputLine + "\n"
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

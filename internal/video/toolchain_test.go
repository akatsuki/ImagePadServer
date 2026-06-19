package video

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
	entry, err := zw.Create("bin/ffmpeg.exe")
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

func TestVisualizerFFmpegRequiresSubtitlesFilter(t *testing.T) {
	dir := t.TempDir()
	fake := mustWriteFakeFFmpeg(t, dir, "echo Filters:")
	err := verifyVisualizerFFmpeg(fake)
	if err == nil || !strings.Contains(err.Error(), "missing required filters") {
		t.Fatalf("expected missing-filters error, got %v", err)
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

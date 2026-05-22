package video

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/settings"
)

type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	MP4     bool   `json:"mp4"`
	HLS     bool   `json:"hls"`
	Active  bool   `json:"active"`
}

const (
	MP4File      = "current.mp4"
	HLSPlaylist  = "current.m3u8"
	HLSSegment   = "current0.ts"
	FrameRate    = "10"
	ClipDuration = "8"

	ffmpegDownloadURL = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
)

var activeHLS sync.Map

func PublishStillImage(imagePath, outDir string) Result {
	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		removeGenerated(outDir)
		return Result{Message: err.Error()}
	}

	removeGenerated(outDir)

	mp4Err := run(ffmpeg,
		"-y",
		"-loop", "1",
		"-t", ClipDuration,
		"-i", imagePath,
		"-f", "lavfi",
		"-t", ClipDuration,
		"-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "24",
		"-pix_fmt", "yuv420p",
		"-vf", "fps="+FrameRate+",pad=ceil(iw/2)*2:ceil(ih/2)*2",
		"-r", FrameRate,
		"-c:a", "aac",
		"-b:a", "32k",
		"-shortest",
		"-movflags", "+faststart",
		filepath.Join(outDir, MP4File),
	)

	hlsErr := runInDir(outDir, ffmpeg,
		"-y",
		"-loop", "1",
		"-t", ClipDuration,
		"-i", imagePath,
		"-f", "lavfi",
		"-t", ClipDuration,
		"-i", "anullsrc=channel_layout=stereo:sample_rate=48000",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "24",
		"-pix_fmt", "yuv420p",
		"-vf", "fps="+FrameRate+",pad=ceil(iw/2)*2:ceil(ih/2)*2",
		"-r", FrameRate,
		"-g", "20",
		"-keyint_min", "20",
		"-sc_threshold", "0",
		"-c:a", "aac",
		"-b:a", "32k",
		"-shortest",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", "current%d.ts",
		HLSPlaylist,
	)

	result := Result{
		MP4: fileExists(filepath.Join(outDir, MP4File)),
		HLS: fileExists(filepath.Join(outDir, HLSPlaylist)) && fileExists(filepath.Join(outDir, HLSSegment)),
	}
	result.OK = result.MP4 || result.HLS
	switch {
	case mp4Err == nil && hlsErr == nil:
		result.Message = "VRChat video outputs generated."
	case result.OK:
		result.Message = fmt.Sprintf("Some VRChat video outputs generated. MP4: %v, HLS: %v", errorText(mp4Err), errorText(hlsErr))
	default:
		result.Message = fmt.Sprintf("FFmpeg failed. MP4: %v, HLS: %v", errorText(mp4Err), errorText(hlsErr))
	}
	return result
}

func CurrentStatus(outDir string) Result {
	mp4 := fileExists(filepath.Join(outDir, MP4File))
	hls := fileExists(filepath.Join(outDir, HLSPlaylist)) && fileExists(filepath.Join(outDir, HLSSegment))
	active := isActive(outDir)
	result := Result{
		OK:     mp4 || hls,
		MP4:    mp4,
		HLS:    hls,
		Active: active,
	}
	if active && hls {
		result.Message = "HLS conversion is streaming."
		return result
	}
	if active {
		result.Message = "HLS conversion is starting."
		return result
	}
	if result.OK {
		result.Message = "VRChat video outputs are available."
		return result
	}
	if _, err := ffmpegPath(); err != nil {
		result.Message = "FFmpeg not found. Turn on video player support to download it, set IMAGEPAD_FFMPEG, or add ffmpeg to PATH."
		return result
	}
	result.Message = "VRChat video outputs have not been generated yet."
	return result
}

func PublishUploadedVideoAsync(sourcePath, outDir string) {
	go func() {
		activeHLS.Store(outDir, true)
		defer activeHLS.Delete(outDir)

		ffmpeg, err := EnsureFFmpeg()
		if err != nil {
			return
		}
		removeGenerated(outDir)
		_ = runInDir(outDir, ffmpeg,
			"-y",
			"-re",
			"-i", sourcePath,
			"-map", "0:v:0",
			"-map", "0:a:0?",
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-crf", "28",
			"-pix_fmt", "yuv420p",
			"-vf", "scale=w='min(1920,iw)':h='min(1080,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
			"-g", "60",
			"-keyint_min", "60",
			"-sc_threshold", "0",
			"-c:a", "aac",
			"-b:a", "128k",
			"-ac", "2",
			"-ar", "48000",
			"-f", "hls",
			"-hls_time", "2",
			"-hls_list_size", "0",
			"-hls_playlist_type", "event",
			"-hls_flags", "temp_file",
			"-hls_segment_filename", "current%d.ts",
			HLSPlaylist,
		)
	}()
}

func EnsureFFmpeg() (string, error) {
	if ffmpeg, err := ffmpegPath(); err == nil {
		return ffmpeg, nil
	}
	if runtime.GOOS != "windows" {
		return "", errors.New("FFmpeg not found. Automatic download is currently supported on Windows only. Set IMAGEPAD_FFMPEG or add ffmpeg to PATH.")
	}
	return downloadFFmpeg()
}

func RemoveGenerated(outDir string) {
	removeGenerated(outDir)
}

func isActive(outDir string) bool {
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return false
	}
	value, _ := active.(bool)
	return value
}

func ffmpegPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		return "", fmt.Errorf("IMAGEPAD_FFMPEG does not exist: %s", configured)
	}
	if local := localFFmpegPath(); fileExists(local) {
		return local, nil
	}
	return exec.LookPath("ffmpeg")
}

func localFFmpegPath() string {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(settings.Dir(), "bin", name)
}

func downloadFFmpeg() (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("automatic FFmpeg download is currently supported on Windows only")
	}

	target := localFFmpegPath()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", fmt.Errorf("failed to prepare FFmpeg folder: %w", err)
	}

	zipPath := filepath.Join(settings.Dir(), "bin", "ffmpeg-release-essentials.zip")
	if err := downloadFile(zipPath, ffmpegDownloadURL, 160<<20); err != nil {
		return "", fmt.Errorf("failed to download FFmpeg: %w", err)
	}
	defer os.Remove(zipPath)

	if err := extractFFmpegExe(zipPath, target); err != nil {
		return "", fmt.Errorf("failed to install FFmpeg: %w", err)
	}
	return target, nil
}

func downloadFile(path, rawURL string, maxBytes int64) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download returned %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("download exceeds size limit")
	}

	tempPath := path + ".tmp"
	out, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return closeErr
	}
	if written > maxBytes {
		_ = os.Remove(tempPath)
		return fmt.Errorf("download exceeds size limit")
	}
	_ = os.Remove(path)
	return os.Rename(tempPath, path)
}

func extractFFmpegExe(zipPath, target string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		if !strings.HasSuffix(strings.ToLower(name), "/bin/ffmpeg.exe") {
			continue
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		defer src.Close()

		tempTarget := target + ".tmp"
		dst, err := os.OpenFile(tempTarget, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		if copyErr != nil {
			_ = os.Remove(tempTarget)
			return copyErr
		}
		if closeErr != nil {
			_ = os.Remove(tempTarget)
			return closeErr
		}
		_ = os.Remove(target)
		return os.Rename(tempTarget, target)
	}
	return errors.New("ffmpeg.exe was not found in the downloaded archive")
}

func run(ffmpeg string, args ...string) error {
	cmd := exec.Command(ffmpeg, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

func runInDir(dir, ffmpeg string, args ...string) error {
	cmd := exec.Command(ffmpeg, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

func removeGenerated(outDir string) {
	_ = os.Remove(filepath.Join(outDir, MP4File))
	_ = os.Remove(filepath.Join(outDir, HLSPlaylist))
	matches, _ := filepath.Glob(filepath.Join(outDir, "current*.ts"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func trimOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 700 {
		return text[len(text)-700:]
	}
	if text == "" {
		return "no output"
	}
	return text
}

func errorText(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

package video

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func downloadVideoURL(rawURL, outDir string) (sourcePath, name, thumbnailPath string, err error) {
	exe, err := EnsureYTDLP()
	if err != nil {
		return "", "", "", err
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return "", "", "", err
	}
	removeYTDLPFiles(outDir)
	target := filepath.Join(outDir, "yt-dlp-source.%(ext)s")
	infoPath := filepath.Join(outDir, "yt-dlp-source.info.json")
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--max-filesize", "2G",
		"-f", "bv*[height<=1080]+ba/b[height<=1080]/best[height<=1080]/best",
		"--merge-output-format", "mp4",
		// Download DASH/HLS fragments in parallel to work around per-connection
		// throttling (notably YouTube), the same speedup music mode uses.
		"--concurrent-fragments", "4",
		"--write-info-json",
		"--write-thumbnail",
		"-o", target,
	}
	args = append(args, ffmpegLocationArgs()...)
	if err := runYTDLPDownload(exe, rawURL, args); err != nil {
		return "", "", "", err
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "yt-dlp-source.*"))
	var thumbPaths []string
	for _, match := range matches {
		ext := strings.ToLower(filepath.Ext(match))
		if ext == ".json" {
			continue
		}
		if info, statErr := os.Stat(match); statErr != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp", ".bmp", ".gif":
			thumbPaths = append(thumbPaths, match)
		default:
			if sourcePath == "" {
				sourcePath = match
			}
		}
	}
	if sourcePath == "" {
		return "", "", "", errors.New("yt-dlp did not produce a video file")
	}
	name = "yt-dlp-source" + filepath.Ext(sourcePath)
	if data, readErr := os.ReadFile(infoPath); readErr == nil {
		if meta, parseErr := ParseMusicInfoJSON(data); parseErr == nil && strings.TrimSpace(meta.Title) != "" {
			name = meta.Title
		}
	}
	if len(thumbPaths) > 0 {
		thumbnailPath = thumbPaths[0]
	}
	return sourcePath, name, thumbnailPath, nil
}

func GenerateThumbnail(sourcePath, outPath string) error {
	ffmpeg, err := EnsureFFmpeg()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0700); err != nil {
		return err
	}
	var lastErr error
	for _, seek := range []string{"00:00:01", "00:00:00"} {
		args := []string{"-y"}
		if seek != "" {
			args = append(args, "-ss", seek)
		}
		args = append(args,
			"-i", sourcePath,
			"-frames:v", "1",
			"-vf", "scale='min(480,iw)':-2",
			"-q:v", "4",
			outPath,
		)
		if err := run(ffmpeg, args...); err == nil {
			if stat, statErr := os.Stat(outPath); statErr == nil && stat.Size() > 0 {
				return nil
			}
			lastErr = errors.New("thumbnail output was empty")
			continue
		} else {
			lastErr = err
		}
	}
	return lastErr
}

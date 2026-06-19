package video

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

var soundCloudDownloadSequence uint64

// DownloadedMedia represents a media file that has been downloaded for
// publishing. Kind is "video" or "soundcloud".
type DownloadedMedia struct {
	SourcePath  string
	Name        string
	Kind        string // "video" or "soundcloud"
	ArtworkPath string
}

// isSoundCloudURL reports whether rawURL is a valid http/https URL with
// a SoundCloud host: soundcloud.com, www.soundcloud.com, m.soundcloud.com,
// or on.soundcloud.com.  It only inspects the scheme and host; query
// parameters, fragments, userinfo, and path are ignored.
func isSoundCloudURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	host := u.Hostname()
	if host == "" {
		return false
	}

	scheme := u.Scheme
	if scheme != "http" && scheme != "https" {
		return false
	}

	host = strings.ToLower(host)

	switch host {
	case "soundcloud.com", "www.soundcloud.com", "m.soundcloud.com", "on.soundcloud.com":
		return true
	default:
		return false
	}
}

// soundCloudDownloadArgs returns the yt-dlp arguments for downloading
// a SoundCloud track including thumbnail. The rawURL is placed at the end.
func soundCloudDownloadArgs(outDir, rawURL string) []string {
	prefix := "yt-dlp-sc-" + queueID() + "-" + strconv.FormatUint(atomic.AddUint64(&soundCloudDownloadSequence, 1), 36)
	return []string{
		"--no-playlist",
		"--no-warnings",
		"--max-filesize", "2G",
		"--write-thumbnail",
		"--no-download-archive",
		"-o", filepath.Join(outDir, prefix+".%(ext)s"),
		rawURL,
	}
}

func selectDownloadedFilesForTemplate(outputTemplate string) (sourcePath, artworkPath string, err error) {
	pattern := strings.ReplaceAll(outputTemplate, "%(ext)s", "*")
	return selectDownloadedFilesMatching(pattern)
}

// selectDownloadedFiles examines outDir for files matching the SoundCloud
// download prefix (yt-dlp-sc.*). It returns the largest non-directory,
// non-.part file as the source, and the first image file as the artwork.
// Artwork is optional — if none is found the artworkPath is empty.
func selectDownloadedFiles(outDir string) (sourcePath, artworkPath string, err error) {
	return selectDownloadedFilesMatching(filepath.Join(outDir, "yt-dlp-sc.*"))
}

func selectDownloadedFilesMatching(pattern string) (sourcePath, artworkPath string, err error) {
	matches, _ := filepath.Glob(pattern)

	var largestFile string
	var largestSize int64
	for _, match := range matches {
		ext := strings.ToLower(filepath.Ext(match))
		if ext == ".part" {
			continue
		}
		info, statErr := os.Stat(match)
		if statErr != nil || info.IsDir() {
			continue
		}
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp":
			if artworkPath == "" {
				artworkPath = match
			}
		default:
			if info.Size() > largestSize {
				largestSize = info.Size()
				largestFile = match
			}
		}
	}

	if largestFile == "" {
		return "", "", errors.New("no SoundCloud audio file found after download")
	}

	return largestFile, artworkPath, nil
}

// DownloadMediaURL downloads media from rawURL. For SoundCloud URLs it
// downloads audio + thumbnail using yt-dlp. For other URLs it delegates
// to downloadVideoURL (the standard video path in publisher.go).
func DownloadMediaURL(rawURL, outDir string) (DownloadedMedia, error) {
	if !isSoundCloudURL(rawURL) {
		sourcePath, name, err := downloadVideoURL(rawURL, outDir)
		if err != nil {
			return DownloadedMedia{}, err
		}
		return DownloadedMedia{
			SourcePath:  sourcePath,
			Name:        name,
			Kind:        "video",
			ArtworkPath: "",
		}, nil
	}

	exe, err := EnsureYTDLP()
	if err != nil {
		return DownloadedMedia{}, err
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return DownloadedMedia{}, err
	}

	args := soundCloudDownloadArgs(outDir, rawURL)
	if err := run(exe, args...); err != nil {
		return DownloadedMedia{}, err
	}

	outputTemplate := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" {
			outputTemplate = args[i+1]
			break
		}
	}
	sourcePath, artworkPath, err := selectDownloadedFilesForTemplate(outputTemplate)
	if err != nil {
		return DownloadedMedia{}, err
	}

	name := filepath.Base(sourcePath)
	return DownloadedMedia{
		SourcePath:  sourcePath,
		Name:        name,
		Kind:        "soundcloud",
		ArtworkPath: artworkPath,
	}, nil
}

// fillEllipse fills an elliptical region in img centred at (cx, cy) with
// horizontal radius rx, vertical radius ry, using color c.
func fillEllipse(img *image.RGBA, cx, cy, rx, ry int, c color.Color) {
	clip := img.Bounds()
	minX := cx - rx
	if minX < clip.Min.X {
		minX = clip.Min.X
	}
	maxX := cx + rx
	if maxX > clip.Max.X {
		maxX = clip.Max.X
	}
	minY := cy - ry
	if minY < clip.Min.Y {
		minY = clip.Min.Y
	}
	maxY := cy + ry
	if maxY > clip.Max.Y {
		maxY = clip.Max.Y
	}
	for y := minY; y < maxY; y++ {
		dy := float64(y-cy) / float64(ry)
		dy2 := dy * dy
		for x := minX; x < maxX; x++ {
			dx := float64(x-cx) / float64(rx)
			if dx*dx+dy2 <= 1.0 {
				img.Set(x, y, c)
			}
		}
	}
}

// generateSoundCloudFallbackArtwork creates a simple fallback PNG artwork at
// path showing a music note on a dark background.  width and height are
// normalised to 16:9; if either dimension is <= 0, 1280×720 is used.
func generateSoundCloudFallbackArtwork(path string, width, height int) error {
	if width <= 0 || height <= 0 {
		width, height = 1280, 720
	}
	// Normalise to 16:9 aspect ratio.
	height = width * 9 / 16
	if height < 1 {
		height = 1
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Dark background (#1a1a1a).
	draw.Draw(img, img.Bounds(),
		&image.Uniform{color.RGBA{26, 26, 26, 255}},
		image.Point{}, draw.Src)

	noteColor := color.RGBA{220, 220, 220, 255}

	// Scale factor relative to the 1280×720 design.
	s := float64(width) / 1280.0

	cx, cy := width/2, height/2+int(30.0*s) // slightly below centre

	// ---- Note head (filled ellipse) ----
	headRx := int(40.0 * s)
	headRy := int(28.0 * s)
	if headRx < 3 {
		headRx = 3
	}
	if headRy < 2 {
		headRy = 2
	}
	fillEllipse(img, cx, cy, headRx, headRy, noteColor)

	// ---- Stem (vertical rectangle) ----
	stemW := int(8.0 * s)
	if stemW < 2 {
		stemW = 2
	}
	stemX := cx + headRx - stemW/2
	stemTop := cy - int(160.0*s)
	stemBottom := cy + headRy*3/4
	if stemBottom > height {
		stemBottom = height
	}
	draw.Draw(img,
		image.Rect(stemX, stemTop, stemX+stemW, stemBottom),
		&image.Uniform{noteColor}, image.Point{}, draw.Src)

	// ---- Flag (approximate curve with a filled ellipse) ----
	flagRx := int(28.0 * s)
	flagRy := int(18.0 * s)
	if flagRx < 2 {
		flagRx = 2
	}
	if flagRy < 2 {
		flagRy = 2
	}
	flagCx := stemX + stemW + flagRx*2/3
	flagCy := stemTop + flagRy
	fillEllipse(img, flagCx, flagCy, flagRx, flagRy, noteColor)

	// Create parent directories.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return png.Encode(f, img)
}

// soundCloudHLSArgs returns the complete ffmpeg argument list for converting
// a SoundCloud audio track + artwork into an HLS video with waveform overlay.
// It is a pure function and does NOT invoke ffmpeg.
func soundCloudHLSArgs(audioPath, artworkPath, id string, preset QualityPreset) []string {
	height := preset.Height
	if height <= 0 {
		height = 720
	}
	// 16:9 video width from the preset height.
	waveWidth := height * 16 / 9
	if waveWidth%2 != 0 {
		waveWidth++
	}
	waveHeight := 150

	// Build the filter complex:
	//   1. Scale artwork to fit a 16:9 frame, padded with black.
	//   2. Split audio into two streams: one for encoding, one for waveform.
	//   3. Generate waveform visualization from the second audio copy.
	//   4. Overlay the waveform at the bottom of the video frame.
	filterComplex := fmt.Sprintf(
		"[0:v]scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black[bg];"+
			"[1:a]asplit=2[audio][waves];"+
			"[waves]showwaves=s=%dx%d:rate=30:colors=#00ccff|#00ffff[waveform];"+
			"[bg][waveform]overlay=0:H-h[composite];"+
			"[composite]crop=%d:%d,format=yuv420p[out]",
		waveWidth, height, waveWidth, height, waveWidth, waveHeight, waveWidth, height,
	)

	return []string{
		"-y",
		"-i", artworkPath,
		"-i", audioPath,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", strconv.Itoa(preset.CRF),
		"-b:v", preset.VideoBitrate,
		"-maxrate", preset.MaxRate,
		"-bufsize", preset.BufferSize,
		"-pix_fmt", "yuv420p",
		"-filter_complex", filterComplex,
		"-map", "[out]",
		"-map", "[audio]",
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ac", "2",
		"-ar", "48000",
		"-shortest",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segmentPattern(id),
		playlistName(id),
	}
}

// resolveSoundCloudArtwork returns the artwork path to use for HLS generation.
// If artworkPath is empty it creates a fallback PNG in outDir.  The caller
// must call os.Remove(resolved) when canRemove is true.
func resolveSoundCloudArtwork(artworkPath, outDir, id string) (resolved string, canRemove bool, err error) {
	if artworkPath != "" {
		return artworkPath, false, nil
	}
	tempPath := filepath.Join(outDir, ".sc-fallback-"+safeID(id)+".png")
	if err := generateSoundCloudFallbackArtwork(tempPath, 0, 0); err != nil {
		return "", false, err
	}
	return tempPath, true, nil
}

// runSoundCloudHLS runs ffmpeg to convert a SoundCloud audio track and artwork
// into an HLS video with a waveform overlay.  If artworkPath is empty a
// fallback PNG is generated automatically and cleaned up after conversion.
func runSoundCloudHLS(ctx context.Context, outDir, ffmpeg, audioPath, artworkPath, id string, preset QualityPreset) error {
	actualArtwork, canRemove, err := resolveSoundCloudArtwork(artworkPath, outDir, id)
	if err != nil {
		return err
	}
	if canRemove {
		defer os.Remove(actualArtwork)
	}
	return runInDirContext(ctx, outDir, ffmpeg, soundCloudHLSArgs(audioPath, actualArtwork, id, preset)...)
}

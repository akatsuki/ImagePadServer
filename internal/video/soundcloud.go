package video

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var soundCloudDownloadSequence uint64

// DownloadedMedia represents a media file that has been downloaded for
// publishing. Kind is "video" or "soundcloud".
type DownloadedMedia struct {
	SourcePath      string
	Name            string
	Kind            string // "video" or "soundcloud"
	ArtworkPath     string
	Metadata        AudioMetadata
	InformationPath string
	ThumbnailPath   string // external thumbnail for link-downloaded videos
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

// IsSoundCloudURL reports whether rawURL points at a supported SoundCloud host.
func IsSoundCloudURL(rawURL string) bool {
	return isSoundCloudURL(rawURL)
}

// isTwitterURL reports whether rawURL points at X/Twitter.
func isTwitterURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range []string{"x.com", "twitter.com"} {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// IsPageMediaURL reports whether rawURL is a media page URL (YouTube, Twitter/X,
// SoundCloud) that requires yt-dlp to fetch and cannot be retrieved with a
// plain HTTP GET. The server uses this to skip the direct-download fallback
// for such URLs: a plain GET on a YouTube /watch, Twitter /status, or
// SoundCloud page only returns HTML, which ffprobe rejects as "Invalid data
// found", masking the real yt-dlp error. Unknown hosts are left to the
// fallback so direct media links without an extension still work.
func IsPageMediaURL(rawURL string) bool {
	return isYouTubeURL(rawURL) || isTwitterURL(rawURL) || isSoundCloudURL(rawURL)
}

// DownloadMediaURL downloads media from rawURL. For SoundCloud URLs it
// downloads audio + thumbnail using yt-dlp via DownloadSoundCloud. For
// other URLs it delegates to downloadVideoURL (the standard video path
// in publisher.go).
func DownloadMediaURL(rawURL, outDir string) (DownloadedMedia, error) {
	if !isSoundCloudURL(rawURL) {
		sourcePath, name, thumbnailPath, err := downloadVideoURL(rawURL, outDir)
		if err != nil {
			return DownloadedMedia{}, err
		}
		return DownloadedMedia{
			SourcePath:    sourcePath,
			Name:          name,
			Kind:          "video",
			ThumbnailPath: thumbnailPath,
		}, nil
	}

	exe, err := EnsureYTDLP()
	if err != nil {
		return DownloadedMedia{}, err
	}

	audio, err := DownloadSoundCloud(context.Background(), exe, rawURL, outDir)
	if err != nil {
		return DownloadedMedia{}, err
	}

	return DownloadedMedia{
		SourcePath:      audio.SourcePath,
		Name:            audio.SourceName,
		Kind:            "soundcloud",
		ArtworkPath:     audio.SoundCloudArtworkPath,
		Metadata:        audio.SoundCloudMetadata,
		InformationPath: audio.SoundCloudInformationPath,
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
	return soundCloudHLSArgsWithEncoder(audioPath, artworkPath, id, preset, CPUVideoEncoder(EncoderStandard))
}

func soundCloudHLSArgsWithEncoder(audioPath, artworkPath, id string, preset QualityPreset, encoder VideoEncoderProfile) []string {
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

	args := []string{
		"-y",
		"-i", artworkPath,
		"-i", audioPath,
	}
	args = append(args, encoder.FFmpegArgs(preset, "veryfast")...)
	if !encoder.Hardware {
		args = append(args,
			"-b:v", preset.VideoBitrate,
			"-maxrate", preset.MaxRate,
			"-bufsize", preset.BufferSize,
		)
	}
	args = append(args, staticContentEncodeOptions(encoder)...)
	return append(args,
		"-filter_complex", filterComplex,
		"-map", "[out]",
		"-map", "[audio]",
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ac", "2",
		"-ar", "48000",
		"-shortest",
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "0",
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segmentPattern(id),
		playlistName(id),
	)
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
	selected := SelectVideoEncoder(ctx, ffmpeg, EncoderStandard)
	return runVideoEncodeWithFallback(ctx, selected, func() { removeHLSForID(outDir, id) }, func(encoder VideoEncoderProfile) error {
		return runInDirContext(ctx, outDir, ffmpeg, soundCloudHLSArgsWithEncoder(audioPath, actualArtwork, id, preset, encoder)...)
	})
}

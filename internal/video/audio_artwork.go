package video

import (
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// ExtractEmbeddedArtwork
// ---------------------------------------------------------------------------

// ExtractEmbeddedArtwork runs ffmpeg to extract every attached_pic stream from
// sourcePath into a unique PNG/JPEG file in outDir, validates each image, and
// returns the list of viable ArtworkCandidates.  A stream whose image fails to
// decode is silently skipped.
func ExtractEmbeddedArtwork(ctx context.Context, ffmpeg, sourcePath, outDir string, probe MediaProbe) ([]ArtworkCandidate, error) {
	var candidates []ArtworkCandidate

	for _, stream := range probe.Streams {
		if !stream.AttachedPic {
			continue
		}

		ext := ".png"
		switch stream.CodecName {
		case "mjpeg", "jpeg":
			ext = ".jpg"
		case "png":
			ext = ".png"
		}

		outPath := filepath.Join(outDir, fmt.Sprintf("attached_pic_%d%s", stream.Index, ext))

		args := []string{
			"-y",
			"-i", sourcePath,
			"-map", "0:" + strconv.Itoa(stream.Index),
			"-vcodec", "copy",
			"-f", "image2",
			outPath,
		}

		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		hideWindow(cmd)
		if _, err := CombinedOutputTrackedFFmpeg(cmd); err != nil {
			continue // ffmpeg failed; skip this stream
		}

		// Validate the extracted image.
		f, err := os.Open(outPath)
		if err != nil {
			continue
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			os.Remove(outPath)
			continue // corrupt image; skip
		}

		info, err := os.Stat(outPath)
		if err != nil {
			continue
		}

		isFrontCover := false
		if title, ok := stream.Tags["title"]; ok {
			isFrontCover = strings.EqualFold(title, "Front Cover")
		}

		candidates = append(candidates, ArtworkCandidate{
			Path:       outPath,
			FrontCover: isFrontCover,
			Width:      cfg.Width,
			Height:     cfg.Height,
			Bytes:      info.Size(),
		})
	}

	return candidates, nil
}

// ---------------------------------------------------------------------------
// SelectArtwork
// ---------------------------------------------------------------------------

// SelectArtwork picks the best artwork from embedded candidates.  The rules
// are:
//
//  1. Front cover beats non-cover.
//  2. Larger pixel area (Width × Height) wins.
//  3. Larger file size (Bytes) breaks the tie.
//
// When there are no embedded candidates, the SoundCloud artwork path is
// returned only when kind is SourceSoundCloud; for local/remote audio the
// SoundCloud path is never consulted.
func SelectArtwork(embedded []ArtworkCandidate, soundCloudPath string, kind SourceKind) (string, error) {
	if len(embedded) > 0 {
		var best *ArtworkCandidate
		for i := range embedded {
			c := &embedded[i]
			if best == nil {
				best = c
				continue
			}

			// 1. Front cover wins over non-cover.
			if c.FrontCover != best.FrontCover {
				if c.FrontCover {
					best = c
				}
				continue
			}

			// 2. Larger area wins.
			areaC := c.Width * c.Height
			areaBest := best.Width * best.Height
			if areaC != areaBest {
				if areaC > areaBest {
					best = c
				}
				continue
			}

			// 3. Larger bytes wins.
			if c.Bytes > best.Bytes {
				best = c
			}
		}
		return best.Path, nil
	}

	if (kind == SourceSoundCloud || kind == SourceMusic) && soundCloudPath != "" {
		return soundCloudPath, nil
	}

	return "", nil
}

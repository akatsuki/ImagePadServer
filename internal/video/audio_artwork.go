package video

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ---------------------------------------------------------------------------
// ExtractEmbeddedArtwork
// ---------------------------------------------------------------------------

// attachedPicExt returns a file extension for an attached picture stream based
// on its codec name. Unrecognised codecs default to ".png" so that ffmpeg's
// image2 muxer can still write the raw bytes; the actual content is always
// detected by magic bytes downstream.
func attachedPicExt(codecName string) string {
	switch codecName {
	case "mjpeg", "jpeg":
		return ".jpg"
	case "webp":
		return ".webp"
	case "gif":
		return ".gif"
	case "bmp":
		return ".bmp"
	case "tiff":
		return ".tiff"
	default:
		return ".png"
	}
}

// ExtractEmbeddedArtwork runs ffmpeg to extract every attached_pic stream from
// sourcePath into outDir, and returns the list of ArtworkCandidates.
// Streams that ffmpeg cannot extract are skipped; dimension detection falls
// back to 0×0 for image formats not supported by Go's image decoders.
func ExtractEmbeddedArtwork(ctx context.Context, ffmpeg, sourcePath, outDir string, probe MediaProbe) ([]ArtworkCandidate, error) {
	var candidates []ArtworkCandidate

	for _, stream := range probe.Streams {
		if !stream.AttachedPic {
			continue
		}

		ext := attachedPicExt(stream.CodecName)

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

		// Validate the extracted image. With all common decoders registered,
		// only genuinely corrupt data fails here.
		f, err := os.Open(outPath)
		if err != nil {
			continue
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			os.Remove(outPath)
			continue
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

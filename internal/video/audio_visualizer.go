package video

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	spectrumBarWidth  = 28
	spectrumBarGap    = 3
	spectrumBarX      = 464
	spectrumBarY      = 336
	spectrumBarStartH = 4
	spectrumBarMaxH   = 128
)

func AudioVisualizerFFmpegArgs(audioPath, assPath, fontDir, id string, preset QualityPreset) []string {
	return []string{
		"-v", "error",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", 1280, 720),
		"-r", "30",
		"-i", "pipe:0",
		"-i", audioPath,
		"-filter_complex",
		fmt.Sprintf(
			"[1:a]showwaves=s=752x168:rate=30:mode=line:colors=white@0.55[wave];[0:v]format=yuv420p[vis];[vis][wave]overlay=432:320[vid];[vid]ass=%s:fontsdir=%s[out]",
			escapeFilterPath(assPath),
			escapeFilterPath(fontDir),
		),
		"-map", "[out]",
		"-map", "1:a",
		"-c:v", "libx264",
		"-preset", "medium",
		"-crf", fmt.Sprintf("%d", preset.CRF),
		"-c:a", "aac",
		"-b:a", preset.AudioBitrate,
		"-ar", "48000",
		"-ac", "2",
		"-pix_fmt", "yuv420p",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_segment_filename", "%s/seg-%%05d.ts",
		"-hls_flags", "event+omit_endlist",
		"%s/playlist.m3u8",
	}
}

func escapeFilterPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.ReplaceAll(p, ":", "\\:")
	return p
}

func WriteVisualizerRGBAFrames(ctx context.Context, dst io.Writer, input AudioRenderInput, width, height int) error {
	if len(input.Analysis.Frames) == 0 {
		return fmt.Errorf("no analysis frames to render")
	}

	frameW, frameH := width, height
	barW := spectrumBarWidth
	barGap := spectrumBarGap
	barX := spectrumBarX - 432
	barY := spectrumBarY - 320
	barH := spectrumBarMaxH
	barStart := spectrumBarStartH

	canvas := image.NewRGBA(image.Rect(0, 0, frameW, frameH))

	for fi, frame := range input.Analysis.Frames {
		draw.Draw(canvas, canvas.Bounds(), image.Transparent, image.Point{}, draw.Src)

		bgColor := color.RGBA{20, 20, 30, 255}
		draw.Draw(canvas, canvas.Bounds(), &image.Uniform{bgColor}, image.Point{}, draw.Src)

		for b := 0; b < 24 && b < len(frame.Spectrum24); b++ {
			val := frame.Spectrum24[b]
			h := int(val * float64(barH))
			if h < barStart {
				h = barStart
			}
			x := barX + b*(barW+barGap) - barW/2
			y := barY + barH - h

			alpha := 128 + int(127*(1-float64(b)/24.0))
			if alpha > 255 {
				alpha = 255
			}
			if alpha < 0 {
				alpha = 0
			}
			barColor := color.RGBA{100, 180, 255, uint8(alpha)}

			for dx := 0; dx < barW; dx++ {
				for dy := 0; dy < h; dy++ {
					cx, cy := x+dx, y+dy
					if cx >= 0 && cx < frameW && cy >= 0 && cy < frameH {
						fade := 1.0 - float64(dy)/float64(h)*0.5
						bc := barColor
						bc.R = uint8(float64(bc.R) * fade)
						bc.G = uint8(float64(bc.G) * fade)
						bc.B = uint8(float64(bc.B) * fade)
						canvas.Set(cx, cy, bc)
					}
				}
			}
		}

		progress := float64(fi) / float64(len(input.Analysis.Frames))
		progX := barX
		progY := barY + barH + 10
		progW := 24*barW + 23*barGap
		markerX := progX + int(progress*float64(progW))

		for dx := 0; dx < progW; dx++ {
			for dy := 0; dy < 4; dy++ {
				cx, cy := progX+dx, progY+dy
				if cx >= 0 && cx < frameW && cy >= 0 && cy < frameH {
					canvas.Set(cx, cy, color.RGBA{60, 60, 80, 255})
				}
			}
		}
		for dx := -2; dx <= 2; dx++ {
			for dy := -3; dy <= 3; dy++ {
				cx, cy := markerX+dx, progY+dy
				if cx >= 0 && cx < frameW && cy >= 0 && cy < frameH {
					canvas.Set(cx, cy, color.RGBA{255, 200, 100, 255})
				}
			}
		}

		if err := binary.Write(dst, binary.LittleEndian, canvas.Pix); err != nil {
			return fmt.Errorf("frame %d: %w", fi, err)
		}
	}
	return nil
}

func RunAudioVisualizerHLS(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, id string, preset QualityPreset) error {
	assPath := filepath.Join(outDir, id+".ass")
	fonts, err := VisualizerFonts()
	if err != nil {
		return fmt.Errorf("fonts: %w", err)
	}

	layout, err := LayoutForSize(1280, 720)
	if err != nil {
		return fmt.Errorf("layout: %w", err)
	}

	metrics := map[string]TextMetrics{}
	titleMetrics, _ := MeasureTextWithFFmpeg(ctx, ffmpeg, fonts.SemiBold600, input.Metadata.Title, 28)
	artistMetrics, _ := MeasureTextWithFFmpeg(ctx, ffmpeg, fonts.Medium500, input.Metadata.Artist, 20)
	metrics["title"] = titleMetrics
	metrics["artist"] = artistMetrics
	if input.Metadata.Album != "" {
		albumMetrics, _ := MeasureTextWithFFmpeg(ctx, ffmpeg, fonts.Regular400, input.Metadata.Album, 16)
		metrics["album"] = albumMetrics
	}

	ass := BuildVisualizerASS(input.Metadata, input.Analysis.Duration, layout, fonts, metrics)
	if err := os.WriteFile(assPath, []byte(ass), 0644); err != nil {
		return fmt.Errorf("write ass: %w", err)
	}

	args := AudioVisualizerFFmpegArgs(input.SourcePath, assPath, filepath.Dir(fonts.Regular400), id, preset)
	outArg := fmt.Sprintf(args[len(args)-2], outDir)
	playlistArg := fmt.Sprintf(args[len(args)-1], outDir)
	args[len(args)-2] = outArg
	args[len(args)-1] = playlistArg

	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	frameReader, frameWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	cmd.Stdin = frameReader

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	frameErrCh := make(chan error, 1)
	go func() {
		defer frameWriter.Close()
		frameErrCh <- WriteVisualizerRGBAFrames(ctx, frameWriter, input, 1280, 720)
	}()

	if err := cmd.Run(); err != nil {
		frameWriter.Close()
		return fmt.Errorf("ffmpeg: %w\n%s", err, stderr.String())
	}

	if err := <-frameErrCh; err != nil {
		return fmt.Errorf("frame writer: %w", err)
	}

	return nil
}

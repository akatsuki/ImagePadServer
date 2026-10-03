package xpostcodec

import (
	"context"
	"encoding/json"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xpostvideo"
)

// Run against already generated production cards and local real-source media.
// This creates review artifacts only; it never publishes or changes settings.
func TestEncodePostCardVisualPreview(t *testing.T) {
	root := os.Getenv("XPOST_STYLE_PREVIEW_DIR")
	if root == "" {
		t.Skip("set XPOST_STYLE_PREVIEW_DIR, XPOST_STYLE_SOURCE and XPOST_STYLE_SPEECH for native visual previews")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	compositor := os.Getenv("IMAGEPAD_XPOST_COMPOSITOR")
	if compositor == "" {
		t.Fatal("IMAGEPAD_XPOST_COMPOSITOR is required")
	}
	encoder := os.Getenv("XPOST_STYLE_PREVIEW_ENCODER")
	if encoder == "" {
		encoder = "libx264"
	}
	clip := filepath.Join(root, "real-source-first-3s.mp4")
	if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-i", os.Getenv("XPOST_STYLE_SOURCE"), "-t", "3", "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", clip}, nil); err != nil {
		t.Fatal(err)
	}
	speechPath := os.Getenv("XPOST_STYLE_SPEECH")
	info, err := probe(ctx, ffprobe, speechPath)
	if err != nil {
		t.Fatal(err)
	}
	duration, err := strconv.ParseFloat(info.Format.Duration, 64)
	if err != nil || duration <= 0 {
		t.Fatalf("invalid real narration duration %q: %v", info.Format.Duration, err)
	}
	results := make(map[string]Result)
	for _, theme := range []string{"light", "dark"} {
		for _, kind := range []string{"real", "quote", "flow"} {
			name := kind + "-" + theme
			t.Run(name, func(t *testing.T) {
				dir, err := os.MkdirTemp(root, "native-"+name+"-")
				if err != nil {
					t.Fatal(err)
				}
				cardSet := name
				if kind == "flow" {
					cardSet = "real-" + theme
				}
				pages := []xpostmodel.Page{{CardPath: filepath.Join(root, cardSet, "post-card-001.png"), PanelPath: filepath.Join(root, cardSet, "post-panel-001.png")}}
				speeches := []xpostmodel.Speech{{}}
				var media []xpostmodel.Media
				if kind == "real" || kind == "flow" {
					var err error
					media, err = PrepareMediaWithTheme(ctx, ffmpeg, ffprobe, []xpostmodel.Media{{Kind: "video", Path: clip}}, dir, 1920, 1080, 30, theme)
					if err != nil {
						t.Fatal(err)
					}
					if kind == "real" {
						speeches[0] = xpostmodel.Speech{Path: speechPath, Duration: duration}
					} else {
						media = append([]xpostmodel.Media{{Kind: "image", Path: media[0].FirstPath, Width: media[0].Width, Height: media[0].Height}}, media...)
					}
				} else {
					pages = append(pages, xpostmodel.Page{CardPath: filepath.Join(root, name, "post-card-002.png"), PanelPath: filepath.Join(root, name, "post-panel-002.png"), Quoted: true})
					speeches = append(speeches, xpostmodel.Speech{})
				}
				options := xpostvideo.DefaultOptions()
				options.Theme = theme
				if kind == "flow" {
					options.PhotoSeconds = .3
				}
				plan, err := xpostvideo.Compile(pages, speeches, media, options)
				if err != nil {
					t.Fatal(err)
				}
				result, err := Encode(ctx, Options{FFmpeg: ffmpeg, FFprobe: ffprobe, Compositor: compositor, OutDir: dir, Encoder: encoder, CRF: 26, AudioBitrate: "160k"}, plan)
				if err != nil {
					t.Fatal(err)
				}
				if result.Frames != plan.TotalFrames || math.Abs(result.Duration-float64(plan.TotalFrames)/30) > .001 || result.Adapter == "" || result.Backend == "" {
					t.Fatalf("native output differs from the planned cards/clock: %+v", result)
				}
				frameTimes := []string{"0.1", "2.1"}
				if kind == "flow" {
					frameTimes = []string{"1.6", "2.25", "2.65", "3.1", "3.6", "4.5"}
				}
				for _, at := range frameTimes {
					framePath := filepath.Join(dir, "frame-"+at+".png")
					if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-ss", at, "-i", result.OutputPath, "-frames:v", "1", framePath}, nil); err != nil {
						t.Fatal(err)
					}
					sample := image.Pt(0, 540)
					if kind == "flow" && at == "3.1" {
						// The exiting card crosses the left edge here. Sample the
						// visible gap between the two cards, not the source video.
						sample = image.Pt(960, 0)
					}
					assertPreviewThemeBackground(t, framePath, theme, sample)
					if kind == "real" && at == "0.1" || kind == "quote" && at == "2.1" {
						pixels, err := os.ReadFile(framePath)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(root, name+"-preview.png"), pixels, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				results[name] = result
				t.Logf("%s: %d frames, %.3fs, %s / %s / %s", name, result.Frames, result.Duration, result.Adapter, result.Backend, encoder)
			})
		}
	}
	if t.Failed() {
		return
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "native-results.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertPreviewThemeBackground(t *testing.T, path, theme string, sample image.Point) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(sample.X, sample.Y).RGBA()
	if theme == "light" && (r < 240*257 || g < 240*257 || b < 240*257) || theme == "dark" && (r > 15*257 || g > 15*257 || b > 15*257) {
		t.Fatalf("%s background RGB=%d,%d,%d does not match %s mode", path, r/257, g/257, b/257, theme)
	}
}

package xpostcodec

import (
	"context"
	"encoding/json"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"imagepadserver/internal/xpostimage"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xpostvideo"
)

// Opt-in, local synthetic fixtures. The real-post/VOICEVOX check is recorded
// separately; these clips do not publish media or modify voice settings.
func TestEncodeScrollingCardPreview(t *testing.T) {
	root := os.Getenv("XPOST_SCROLL_PREVIEW_DIR")
	if root == "" {
		t.Skip("set XPOST_SCROLL_PREVIEW_DIR for native scroll fixtures")
	}
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
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	portrait := filepath.Join(root, "portrait-fixture.mp4")
	if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=0x274A6E:s=360x640:r=30:d=1", "-an", "-c:v", "libx264", "-threads", "2", "-x264-params", "sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0", portrait}, nil); err != nil {
		t.Fatal(err)
	}
	results := map[string]Result{}
	for _, theme := range []string{"light", "dark"} {
		for _, kind := range []string{"short", "quote"} {
			name := kind + "-" + theme
			t.Run(name, func(t *testing.T) {
				dir, err := os.MkdirTemp(root, "native-"+name+"-")
				if err != nil {
					t.Fatal(err)
				}
				post := xpostmodel.Post{Name: "固定枠の検証", Handle: "fixture", CreatedAt: "2026-10-03T00:00:00Z", RawText: "短い投稿はそのまま表示します。"}
				if kind == "quote" {
					post.RawText = strings.Repeat("元投稿の本文です。読み上げに合わせて本文だけが動きます。\n", 8)
					post.Quoted = &xpostmodel.Post{Name: "引用元の検証", Handle: "quoted_fixture", RawText: strings.Repeat("引用の本文です。引用ボックスと作者の位置を保って読み進めます。\n", 10)}
				}
				pages, err := xpostimage.RenderVideoCards(post, theme, filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf"), dir, "")
				if err != nil {
					t.Fatal(err)
				}
				speech := make([]xpostmodel.Speech, len(pages))
				for i, page := range pages {
					runes := utf8.RuneCountInString(page.SpeechText)
					speech[i] = xpostmodel.Speech{Duration: 2, Cues: []xpostmodel.SpeechCue{{StartRune: 0, EndRune: runes, StartSeconds: .1, EndSeconds: 1.8}}}
					if kind == "quote" {
						speech[i] = xpostmodel.Speech{Duration: 6, Cues: []xpostmodel.SpeechCue{{StartRune: 0, EndRune: runes / 2, StartSeconds: .1, EndSeconds: 2.2}, {StartRune: runes / 2, EndRune: runes, StartSeconds: 3.5, EndSeconds: 5.6}}}
					}
				}
				var media []xpostmodel.Media
				if kind == "quote" {
					media, err = PrepareMediaWithTheme(ctx, ffmpeg, ffprobe, []xpostmodel.Media{{Kind: "video", Path: portrait}}, dir, 1920, 1080, 30, theme)
					if err != nil {
						t.Fatal(err)
					}
				}
				o := xpostvideo.DefaultOptions()
				o.Theme = theme
				plan, err := xpostvideo.Compile(pages, speech, media, o)
				if err != nil {
					t.Fatal(err)
				}
				result, err := Encode(ctx, Options{FFmpeg: ffmpeg, FFprobe: ffprobe, Compositor: compositor, OutDir: dir, Encoder: encoder, CRF: 26, AudioBitrate: "160k"}, plan)
				if err != nil {
					t.Fatal(err)
				}
				if result.Frames != plan.TotalFrames || result.Adapter == "" || result.Backend == "" {
					t.Fatalf("native result differs from plan: %+v", result)
				}
				if kind == "short" && result.RendererCalls != 1 {
					t.Fatalf("short static card redrawn %d times", result.RendererCalls)
				}
				times := []string{"0.2", "1.8"}
				if kind == "quote" {
					times = []string{"0.2", "5.9", "6.1", "6.4", "12.6", "13.0"}
				}
				for _, at := range times {
					frame := filepath.Join(dir, "frame-"+at+".png")
					if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-ss", at, "-i", result.OutputPath, "-frames:v", "1", frame}, nil); err != nil {
						t.Fatal(err)
					}
					assertPreviewThemeBackground(t, frame, theme, image.Pt(0, 540))
				}
				results[name] = result
				t.Logf("%s: %d frames; %d renderer calls; %s / %s", name, result.Frames, result.RendererCalls, result.Adapter, result.Backend)
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
	if err := os.WriteFile(filepath.Join(root, "scroll-native-results.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

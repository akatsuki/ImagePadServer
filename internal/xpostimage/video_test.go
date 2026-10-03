package xpostimage

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
)

func TestSelectPostMediaUsesQuoteOnlyWhenOriginalMediaIsAbsent(t *testing.T) {
	quoted := &xpostmodel.Post{Media: []xpostmodel.Media{{Kind: "video", URL: "https://cdn.test/quoted.mp4"}}}
	main := xpostmodel.Post{Quoted: quoted}
	if got := selectPostMedia(main); len(got) != 1 || got[0].URL != "https://cdn.test/quoted.mp4" {
		t.Fatalf("quote fallback = %#v", got)
	}

	main.Media = []xpostmodel.Media{{Kind: "video", URL: "https://cdn.test/original.mp4"}}
	if got := selectPostMedia(main); len(got) != 1 || got[0].URL != "https://cdn.test/original.mp4" {
		t.Fatalf("original media should take precedence: %#v", got)
	}
}

func TestSelectPostMediaPreservesSourceOrderAndCapsAtFour(t *testing.T) {
	post := xpostmodel.Post{Media: make([]xpostmodel.Media, 6)}
	for i := range post.Media {
		post.Media[i] = xpostmodel.Media{Kind: "video", URL: string(rune('a' + i))}
	}
	got := selectPostMedia(post)
	if len(got) != 4 || got[0].URL != "a" || got[3].URL != "d" {
		t.Fatalf("selected media = %#v", got)
	}
}

func TestDownloadMediaFilesValidatesAndBoundsActualAssets(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "fixture.png")
	imageFile, err := os.Create(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(imageFile, img); err != nil {
		t.Fatal(err)
	}
	if err := imageFile.Close(); err != nil {
		t.Fatal(err)
	}
	pngBytes, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	var seenLimit int64
	fetch := func(_ context.Context, rawURL string, limit int64) ([]byte, string, string, error) {
		seenLimit = limit
		if rawURL != "https://cdn.test/photo.png" {
			return nil, "", "", errors.New("unexpected URL")
		}
		return pngBytes, "image/png", rawURL, nil
	}
	media, err := downloadMediaFiles(context.Background(), []xpostmodel.Media{{
		Kind: "image", URL: "https://cdn.test/photo.png",
	}}, filepath.Join(dir, "out"), fetch)
	if err != nil {
		t.Fatal(err)
	}
	if seenLimit != maxImageBytes {
		t.Fatalf("image byte limit = %d, want %d", seenLimit, maxImageBytes)
	}
	if len(media) != 1 || media[0].Width != 3 || media[0].Height != 2 {
		t.Fatalf("downloaded image metadata = %#v", media)
	}
	if data, err := os.ReadFile(media[0].Path); err != nil || len(data) == 0 {
		t.Fatalf("saved image path %q, read error %v", media[0].Path, err)
	}
}

func TestDownloadMediaFilesRejectsMissingVideoAndNonMP4Bytes(t *testing.T) {
	dir := t.TempDir()
	media := []xpostmodel.Media{{Kind: "video", URL: "https://cdn.test/video.mp4"}}
	if _, err := downloadMediaFiles(context.Background(), media, dir, func(context.Context, string, int64) ([]byte, string, string, error) {
		return nil, "", "", errors.New("missing")
	}); err == nil {
		t.Fatal("missing video should fail")
	}
	if _, err := downloadMediaFiles(context.Background(), media, dir, func(context.Context, string, int64) ([]byte, string, string, error) {
		return []byte("not an mp4"), "video/mp4", media[0].URL, nil
	}); err == nil {
		t.Fatal("non-MP4 bytes should fail")
	}
}

func TestRenderVideoCardsScrollsPostThenQuoteAtFixedSizes(t *testing.T) {
	fontPath := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	post := xpostmodel.Post{
		ID: "123", Name: "Main author", Handle: "main", RawText: "本文 ", Text: "本文 ",
		Quoted: &xpostmodel.Post{ID: "456", Name: "Quoted author", Handle: "quoted", Text: "引用本文", RawText: "引用本文"},
	}
	post.RawText = "本文 " + strings.Repeat("あ", 1800)
	post.Text = post.RawText
	pages, err := RenderVideoCards(post, "dark", fontPath, t.TempDir(), "Credit")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Quoted || !pages[1].Quoted || pages[0].CardScroll == nil || pages[0].PanelScroll == nil {
		t.Fatalf("expected one scrolling post followed by one quote: %#v", pages)
	}
	want, err := xposttts.BuildSpeechText(post)
	if err != nil || pages[0].SpeechText != want || pages[0].PanelScroll.ContentHeight <= pages[0].PanelScroll.Height {
		t.Fatal("long scrolling post lost its narrated body or scroll area")
	}
	if pages[len(pages)-1].SpeechText != "引用本文" {
		t.Fatalf("quote page narration = %q", pages[len(pages)-1].SpeechText)
	}
	for _, page := range pages {
		for path, want := range map[string]image.Point{page.CardPath: image.Pt(1920, 1080), page.PanelPath: image.Pt(960, 1080)} {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			bounds, _, err := image.DecodeConfig(f)
			_ = f.Close()
			if err != nil || bounds.Width != want.X || bounds.Height != want.Y {
				t.Fatalf("%s dimensions = %v, error %v; want %v", path, bounds, err, want)
			}
		}
	}
}

func TestSplitVideoTextPreservesEntityRangesAcrossPages(t *testing.T) {
	prefix := strings.Repeat("あ", 355) + " "
	urlText := "https://t.co/abc"
	mentionText := " @alice"
	raw := prefix + urlText + mentionText + " tail"
	urlStart := len(utf16.Encode([]rune(prefix)))
	mentionStart := urlStart + len(utf16.Encode([]rune(urlText)))
	entities := []xpostmodel.Entity{
		{Start: urlStart, End: mentionStart, Kind: "url"},
		{Start: mentionStart + 1, End: mentionStart + len(utf16.Encode([]rune(mentionText))), Kind: "mention"},
	}
	chunks := splitVideoText(raw, entities, 360)
	var rebuilt strings.Builder
	for _, chunk := range chunks {
		rebuilt.WriteString(chunk.text)
		post := xpostmodel.Post{RawText: chunk.text, Text: chunk.text, Entities: chunk.entities}
		speech, err := xposttts.BuildSpeechText(post)
		if err != nil {
			t.Fatalf("BuildSpeechText() error = %v", err)
		}
		if strings.Contains(speech, "t.co") || strings.Contains(speech, "@alice") {
			t.Fatalf("entity leaked into page speech: %q", speech)
		}
	}
	if rebuilt.String() != raw {
		t.Fatalf("reassembled card text differs from source")
	}
}

func TestSplitVideoTextDoesNotSplitUTF16SurrogatePairs(t *testing.T) {
	raw := strings.Repeat("a", 359) + "😀" + "tail"
	chunks := splitVideoText(raw, nil, 360)
	var rebuilt strings.Builder
	for _, chunk := range chunks {
		rebuilt.WriteString(chunk.text)
	}
	if rebuilt.String() != raw {
		t.Fatalf("split and rejoined text differs from UTF-8 source")
	}
}

func TestEmptyVideoCardDoesNotNarrateVisualPlaceholder(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	pages, err := RenderVideoCards(xpostmodel.Post{ID: "123", Name: "author"}, "light", font, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].SpeechText != "" {
		t.Fatalf("empty body became speech: %+v", pages)
	}
}

func TestPrepareTextOnlyPostHasNoMediaAndStillRenders(t *testing.T) {
	font := filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf")
	assets, err := PrepareVideoPost(t.Context(), xpostmodel.Post{ID: "123", Name: "author", RawText: "本文"}, "light", font, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets.Media) != 0 || len(assets.Pages) != 1 || assets.Pages[0].SpeechText != "本文" {
		t.Fatalf("text-only export: %+v", assets)
	}
}
